package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/proxy/anthropic"
	"github.com/IceRhymers/databricks-claude/pkg/websearch"
)

// rewrittenTools records which tool kinds the request-side rewrite touched.
// Used as the per-request signal that flows from tryRewriteRequest into the
// response forwarding path so the response handler knows whether to inspect
// the SSE stream / JSON body.
type rewrittenTools struct {
	HasWebSearch bool
	HasWebFetch  bool
}

// Any reports whether any web_search_*/web_fetch_* server tool was rewritten.
// Hot-path gate: when false, the response handler skips all SSE/JSON
// inspection and falls through to the byte-identical passthrough.
func (rt rewrittenTools) Any() bool { return rt.HasWebSearch || rt.HasWebFetch }

// maxInputJSONBytes caps the total accumulated partial_json bytes per
// content block. 64 KiB is far above any reasonable web_search query or
// web_fetch URL; on overflow we degrade to passthrough for the affected
// block.
const maxInputJSONBytes = 64 * 1024

// sseFrame is a parsed Server-Sent-Events frame (one event boundary).
type sseFrame struct {
	EventType string // from "event: TYPE" line; "" if absent
	Data      []byte // from "data: JSON" line; nil if absent
}

// rewriteState holds per-stream mutable state for the SSE rewriter.
type rewriteState struct {
	indexShift   int                // # of synthetic blocks injected so far; added to every subsequent block index
	activeBlocks map[int]*blockInfo // index → info for blocks of interest (only annotated tool_uses)
	injectedAny  bool               // true once at least one synthetic web_search_tool_result was emitted
}

// blockInfo tracks an in-flight tool_use content block we may rewrite.
type blockInfo struct {
	toolName     string // "web_search" or "web_fetch"
	toolUseID    string // original id from content_block_start
	inputJSON    bytes.Buffer
	overflowed   bool
	originalIdx  int // upstream's original index for this block
	rewrittenIdx int // our outbound index = originalIdx + indexShift at the time the block opened
}

// pumpSSE reads SSE frames from src, rewrites web_search/web_fetch
// tool_use blocks into server_tool_use + web_search_tool_result pairs,
// renumbers indices, rewrites stop_reason, and writes the transformed
// stream to w with flushing. Caller owns src.Close.
//
// Returns nil on clean EOF, otherwise the underlying error.
func pumpSSE(ctx context.Context, w http.ResponseWriter, src io.Reader, ws WebSearchSettings, rt rewrittenTools, verbose bool) error {
	flusher, _ := w.(http.Flusher)
	state := &rewriteState{activeBlocks: map[int]*blockInfo{}}

	scanner := bufio.NewScanner(src)
	// SSE frames are delimited by "\n\n". Allow up to 1 MiB per frame.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	scanner.Split(splitSSEFrames)

	emit := func(b []byte) error {
		if _, err := w.Write(b); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	for scanner.Scan() {
		raw := scanner.Bytes()
		// Make a copy — bufio.Scanner reuses its buffer on the next Scan.
		frame := append([]byte(nil), raw...)

		ev := parseSSEFrame(frame)
		if len(ev.Data) == 0 {
			// Comment-only or malformed frame; pass through verbatim.
			if err := emit(frame); err != nil {
				return err
			}
			continue
		}

		// Branch on event type. We only care about content_block_* and
		// message_delta; everything else passes through (with index bump
		// where applicable).
		switch ev.EventType {
		case "content_block_start":
			if err := handleContentBlockStart(emit, frame, ev, state, ws, rt, verbose); err != nil {
				return err
			}
		case "content_block_delta":
			if err := handleContentBlockDelta(emit, frame, ev, state, verbose); err != nil {
				return err
			}
		case "content_block_stop":
			if err := handleContentBlockStop(ctx, emit, frame, ev, state, ws, verbose); err != nil {
				return err
			}
		case "message_delta":
			if err := handleMessageDelta(emit, frame, ev, state, verbose); err != nil {
				return err
			}
		default:
			// message_start, message_stop, ping, error, unknown: forward
			// verbatim (no index field to rewrite at top level).
			if err := emit(frame); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if verbose {
			log.Printf("databricks-claude: websearch: SSE scanner error: %v", err)
		}
		return err
	}
	return nil
}

// splitSSEFrames is a bufio.SplitFunc that yields one "\n\n"-terminated
// frame at a time. Includes the trailing "\n\n" in the returned token so
// the rewriter can pass through verbatim when desired.
func splitSSEFrames(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		end := i + 2
		return end, data[:end], nil
	}
	if atEOF {
		// Trailing partial frame at EOF — emit as-is.
		return len(data), data, nil
	}
	return 0, nil, nil // need more data
}

// parseSSEFrame extracts the event type and data payload from a frame.
// Frame format: "event: TYPE\ndata: JSON\n\n" with either line optional.
// Comment lines starting with ":" are skipped.
func parseSSEFrame(frame []byte) sseFrame {
	var out sseFrame
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if line[0] == ':' {
			continue // SSE comment
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			out.EventType = strings.TrimSpace(string(line[len("event:"):]))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			val := bytes.TrimSpace(line[len("data:"):])
			if out.Data == nil {
				out.Data = append([]byte(nil), val...)
			} else {
				// Multi-line data: concatenate with newline per SSE spec.
				out.Data = append(out.Data, '\n')
				out.Data = append(out.Data, val...)
			}
		}
	}
	return out
}

// handleContentBlockStart inspects a content_block_start data payload.
// If the block is a tool_use targeting our annotated client tools
// (web_search/web_fetch), we rewrite its content_block.type to
// server_tool_use on the wire (preserves Anthropic shape) and remember
// the block so we can synthesize a web_search_tool_result on stop.
// Other block types pass through verbatim with their index bumped.
func handleContentBlockStart(emit func([]byte) error, frame []byte, ev sseFrame, state *rewriteState, ws WebSearchSettings, rt rewrittenTools, verbose bool) error {
	var probe struct {
		Type         string          `json:"type"`
		Index        int             `json:"index"`
		ContentBlock json.RawMessage `json:"content_block"`
	}
	if err := json.Unmarshal(ev.Data, &probe); err != nil {
		return emit(frame)
	}

	var cb struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(probe.ContentBlock, &cb)

	// Is this an annotated tool_use we want to rewrite?
	annotated := cb.Type == "tool_use" && (cb.Name == anthropic.ClientToolName_WebSearch || cb.Name == anthropic.ClientToolName_WebFetch)

	if !annotated {
		// Not interesting — forward with possible index shift.
		out, err := bumpIndexInData(ev.Data, state.indexShift)
		if err != nil {
			return emit(frame)
		}
		return emitFrame(emit, "content_block_start", out)
	}

	// Track this block for input accumulation + later synthesis.
	bi := &blockInfo{
		toolName:     cb.Name,
		toolUseID:    cb.ID,
		originalIdx:  probe.Index,
		rewrittenIdx: probe.Index + state.indexShift,
	}
	state.activeBlocks[probe.Index] = bi

	if verbose {
		log.Printf("databricks-claude: websearch: SSE intercept tool_use(name=%s, id=%s, index=%d→%d)", cb.Name, cb.ID, bi.originalIdx, bi.rewrittenIdx)
	}

	// Rewrite content_block.type from "tool_use" to "server_tool_use" so
	// downstream Anthropic SDK consumers correlate the result block we'll
	// inject. Mint a server-tool id by prefixing with "srvtoolu_" if the
	// upstream id doesn't already match that prefix.
	serverID := cb.ID
	if !strings.HasPrefix(serverID, "srvtoolu_") {
		serverID = "srvtoolu_" + cb.ID
	}
	bi.toolUseID = serverID

	rewrittenCB := map[string]string{
		"type": "server_tool_use",
		"id":   serverID,
		"name": cb.Name,
	}
	rewrittenCBBytes, _ := json.Marshal(rewrittenCB)

	// Re-emit the start event with the rewritten content_block and shifted index.
	outData, err := rewriteContentBlockStartData(ev.Data, bi.rewrittenIdx, rewrittenCBBytes)
	if err != nil {
		return emit(frame)
	}
	return emitFrame(emit, "content_block_start", outData)
}

// handleContentBlockDelta forwards delta events with bumped index.
// If the delta belongs to one of our tracked tool_use blocks, we also
// accumulate its partial_json fragment for later input parsing.
func handleContentBlockDelta(emit func([]byte) error, frame []byte, ev sseFrame, state *rewriteState, verbose bool) error {
	var probe struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(ev.Data, &probe); err != nil {
		return emit(frame)
	}

	if bi, ok := state.activeBlocks[probe.Index]; ok && probe.Delta.Type == "input_json_delta" && !bi.overflowed {
		if bi.inputJSON.Len()+len(probe.Delta.PartialJSON) > maxInputJSONBytes {
			bi.overflowed = true
			if verbose {
				log.Printf("databricks-claude: websearch: SSE input_json overflow on block index=%d (cap=%d); degrading to passthrough", probe.Index, maxInputJSONBytes)
			}
		} else {
			bi.inputJSON.WriteString(probe.Delta.PartialJSON)
		}
	}

	out, err := bumpIndexInData(ev.Data, state.indexShift)
	if err != nil {
		return emit(frame)
	}
	return emitFrame(emit, "content_block_delta", out)
}

// handleContentBlockStop forwards the stop event for the original block,
// then — if the block was one of ours — synthesizes a new
// content_block_start (web_search_tool_result, full content baked in)
// and content_block_stop pair, and increments indexShift.
func handleContentBlockStop(ctx context.Context, emit func([]byte) error, frame []byte, ev sseFrame, state *rewriteState, ws WebSearchSettings, verbose bool) error {
	var probe struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}
	if err := json.Unmarshal(ev.Data, &probe); err != nil {
		return emit(frame)
	}

	// Forward the stop for the original block (with index shift).
	stopOut, err := bumpIndexInData(ev.Data, state.indexShift)
	if err != nil {
		stopOut = ev.Data
	}
	if err := emitFrame(emit, "content_block_stop", stopOut); err != nil {
		return err
	}

	bi, tracked := state.activeBlocks[probe.Index]
	if !tracked {
		return nil
	}
	delete(state.activeBlocks, probe.Index)

	if bi.overflowed {
		// Skip injection; degraded to passthrough.
		return nil
	}

	// Execute local fulfillment with a 15s budget.
	fulfillCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resultBlock, fulErr := fulfillBlock(fulfillCtx, bi, ws)
	if fulErr != nil {
		// fulfillBlock already returns an error-variant block on backend
		// failure; only nil-block + non-nil err is a true bug.
		if verbose {
			log.Printf("databricks-claude: websearch: SSE fulfillment internal error for tool_use_id=%s: %v", bi.toolUseID, fulErr)
		}
		return nil
	}

	// Inject content_block_start for the synthetic web_search_tool_result.
	injectIdx := bi.rewrittenIdx + 1
	startEvent := map[string]any{
		"type":          "content_block_start",
		"index":         injectIdx,
		"content_block": json.RawMessage(resultBlock),
	}
	startBytes, _ := json.Marshal(startEvent)
	if err := emitFrame(emit, "content_block_start", startBytes); err != nil {
		return err
	}

	stopEvent := map[string]any{
		"type":  "content_block_stop",
		"index": injectIdx,
	}
	stopBytes, _ := json.Marshal(stopEvent)
	if err := emitFrame(emit, "content_block_stop", stopBytes); err != nil {
		return err
	}

	state.indexShift++
	state.injectedAny = true

	if verbose {
		log.Printf("databricks-claude: websearch: SSE injected web_search_tool_result for tool_use_id=%s at index=%d (shift now %d)", bi.toolUseID, injectIdx, state.indexShift)
	}
	return nil
}

// handleMessageDelta forwards message_delta with stop_reason rewriting:
// if we injected at least one synthetic block AND the upstream said
// stop_reason="tool_use", rewrite to "end_turn". usage fields preserved.
func handleMessageDelta(emit func([]byte) error, frame []byte, ev sseFrame, state *rewriteState, verbose bool) error {
	if !state.injectedAny {
		return emitFrame(emit, "message_delta", ev.Data)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &raw); err != nil {
		return emit(frame)
	}
	delta, ok := raw["delta"]
	if !ok {
		return emitFrame(emit, "message_delta", ev.Data)
	}
	var deltaMap map[string]json.RawMessage
	if err := json.Unmarshal(delta, &deltaMap); err != nil {
		return emitFrame(emit, "message_delta", ev.Data)
	}
	if sr, ok := deltaMap["stop_reason"]; ok {
		var s string
		_ = json.Unmarshal(sr, &s)
		if s == "tool_use" {
			deltaMap["stop_reason"] = json.RawMessage(`"end_turn"`)
			newDelta, _ := json.Marshal(deltaMap)
			raw["delta"] = newDelta
			out, _ := json.Marshal(raw)
			if verbose {
				log.Printf("databricks-claude: websearch: SSE rewrote message_delta.stop_reason tool_use→end_turn")
			}
			return emitFrame(emit, "message_delta", out)
		}
	}
	return emitFrame(emit, "message_delta", ev.Data)
}

// fulfillBlock executes the local search/fetch and returns the JSON-encoded
// web_search_tool_result content block.
func fulfillBlock(ctx context.Context, bi *blockInfo, ws WebSearchSettings) ([]byte, error) {
	switch bi.toolName {
	case anthropic.ClientToolName_WebSearch:
		query, err := anthropic.ParseWebSearchInput(bi.inputJSON.Bytes())
		if err != nil {
			return anthropic.BuildWebSearchErrorBlock(bi.toolUseID, "invalid_input")
		}
		if ws.Backend == nil {
			return anthropic.BuildWebSearchErrorBlock(bi.toolUseID, "unavailable")
		}
		results, err := ws.Backend.Search(ctx, query, 5)
		if err != nil {
			return anthropic.BuildWebSearchErrorBlock(bi.toolUseID, "unavailable")
		}
		out := make([]anthropic.WebSearchResult, 0, len(results))
		for _, r := range results {
			title := r.Title
			if r.Snippet != "" {
				title = r.Title + " — " + r.Snippet
			}
			out = append(out, anthropic.WebSearchResult{
				URL:              r.URL,
				Title:            title,
				EncryptedContent: "",
			})
		}
		return anthropic.BuildWebSearchSuccessBlock(bi.toolUseID, out)

	case anthropic.ClientToolName_WebFetch:
		fetchURL, err := anthropic.ParseWebFetchInput(bi.inputJSON.Bytes())
		if err != nil {
			return anthropic.BuildWebSearchErrorBlock(bi.toolUseID, "invalid_input")
		}
		fr, err := websearch.Fetch(ctx, fetchURL, ws.FetchBudget, ws.Robots)
		if err != nil {
			return anthropic.BuildWebSearchErrorBlock(bi.toolUseID, "unavailable")
		}
		return anthropic.BuildWebFetchSuccessBlock(bi.toolUseID, fr.URL, fr.Text, fr.Truncated)
	}
	return anthropic.BuildWebSearchErrorBlock(bi.toolUseID, "unavailable")
}

// emitFrame serializes an event as `event: TYPE\ndata: <JSON>\n\n` and
// writes it via the emit callback.
func emitFrame(emit func([]byte) error, eventType string, data []byte) error {
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(eventType)
	buf.WriteByte('\n')
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return emit(buf.Bytes())
}

// bumpIndexInData rewrites the top-level "index" field in a content_block_*
// data payload, adding shift to its value. If shift == 0 returns data as-is.
func bumpIndexInData(data []byte, shift int) ([]byte, error) {
	if shift == 0 {
		return data, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data, err
	}
	idxRaw, ok := raw["index"]
	if !ok {
		return data, nil
	}
	var idx int
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		return data, err
	}
	newIdx, _ := json.Marshal(idx + shift)
	raw["index"] = newIdx
	return json.Marshal(raw)
}

// rewriteContentBlockStartData replaces the index and content_block fields
// in a content_block_start data payload while preserving any other fields.
func rewriteContentBlockStartData(data []byte, newIndex int, newContentBlock []byte) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data, err
	}
	idxBytes, _ := json.Marshal(newIndex)
	raw["index"] = idxBytes
	raw["content_block"] = newContentBlock
	return json.Marshal(raw)
}

// rewriteJSONResponse handles the non-streaming /v1/messages JSON body
// path. It walks content[] looking for tool_use(web_search|web_fetch),
// rewrites each to server_tool_use + a synthetic web_search_tool_result
// pair, and rewrites stop_reason "tool_use" → "end_turn" if any injection
// happened.
func rewriteJSONResponse(ctx context.Context, body []byte, ws WebSearchSettings, verbose bool) ([]byte, error) {
	var resp anthropic.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return body, err
	}
	if len(resp.Content) == 0 {
		return body, nil
	}

	newContent := make([]json.RawMessage, 0, len(resp.Content)+2)
	injected := false
	for _, block := range resp.Content {
		var probe struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(block, &probe); err != nil {
			newContent = append(newContent, block)
			continue
		}
		if probe.Type != "tool_use" || (probe.Name != anthropic.ClientToolName_WebSearch && probe.Name != anthropic.ClientToolName_WebFetch) {
			newContent = append(newContent, block)
			continue
		}

		// Mint server tool id and rewrite the block to server_tool_use.
		serverID := probe.ID
		if !strings.HasPrefix(serverID, "srvtoolu_") {
			serverID = "srvtoolu_" + probe.ID
		}
		var inputMap map[string]json.RawMessage
		_ = json.Unmarshal(probe.Input, &inputMap)
		stuRewritten, _ := json.Marshal(map[string]any{
			"type":  "server_tool_use",
			"id":    serverID,
			"name":  probe.Name,
			"input": inputMap,
		})
		newContent = append(newContent, stuRewritten)

		bi := &blockInfo{toolName: probe.Name, toolUseID: serverID}
		if probe.Input != nil {
			bi.inputJSON.Write(probe.Input)
		}
		fulfillCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		resultBlock, err := fulfillBlock(fulfillCtx, bi, ws)
		cancel()
		if err != nil {
			if verbose {
				log.Printf("databricks-claude: websearch: JSON fulfillment error for tool_use_id=%s: %v", serverID, err)
			}
			continue
		}
		newContent = append(newContent, resultBlock)
		injected = true
	}
	resp.Content = newContent

	if injected && resp.StopReason == "tool_use" {
		resp.StopReason = "end_turn"
	}

	out, err := json.Marshal(&resp)
	if err != nil {
		return body, err
	}
	if verbose && injected {
		log.Printf("databricks-claude: websearch: JSON-rewrote response with injected web_search_tool_result blocks")
	}
	return out, nil
}

// isSSEResponse reports whether the response Content-Type indicates SSE.
func isSSEResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])), "text/event-stream")
}

// isJSONResponse reports whether the response Content-Type indicates JSON.
func isJSONResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	first := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	return first == "application/json" || strings.HasSuffix(first, "+json")
}

