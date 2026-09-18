package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// segmentEnd classifies how one upstream SSE segment terminated, which
// decides what the interleave state machine does next.
type segmentEnd int

const (
	// segAdvisorPause: at least one advisor tool_use was seen and no client
	// tool_use. The proxy runs the sub-inference(s) and continues the turn.
	segAdvisorPause segmentEnd = iota
	// segClientToolUse: a client tool_use was seen. Stop — replay the
	// captured message_delta/message_stop verbatim and end the stream.
	segClientToolUse
	// segNaturalEnd: no tool_use at all. Same passthrough ending.
	segNaturalEnd
	// segError: the upstream stream failed or was unparseable. The caller
	// synthesizes a terminal message_delta + message_stop.
	segError
)

// streamSegmentResult is what one executor segment yields.
type streamSegmentResult struct {
	end segmentEnd
	// advisorCalls are the suppressed advisor tool_use blocks, in order,
	// with their accumulated input (always {}) and upstream ids.
	advisorCalls []streamAdvisorCall
	// textBlocks are the text content blocks seen in this segment, used to
	// build the continuation's assistant turn.
	textBlocks []json.RawMessage
	// clientToolUses are the non-advisor tool_use blocks passed through to
	// the client; needed to reconstruct the continuation assistant turn.
	clientToolUses []json.RawMessage
	// messageDelta / messageStop are the captured terminal frames, replayed
	// verbatim on segClientToolUse / segNaturalEnd.
	messageDelta []byte
	messageStop  []byte
	// nextIndex is the next free downstream block index after this segment.
	nextIndex int
	err       error
}

// streamAdvisorCall records one suppressed advisor invocation from the stream.
type streamAdvisorCall struct {
	upstreamID string
	toolName   string
}

// handleAdvisorStreamingInterleave keeps ONE SSE connection to the client
// across as many continuation segments as needed (bounded by
// maxAdvisorTurns). Upstream events pass through verbatim except advisor
// tool_use blocks, which are suppressed; downstream indices are rewritten
// through an index map so client-facing indices stay contiguous. Advisor
// blocks are synthesized at fresh monotonically increasing indices.
//
// Graceful degradation: on any error, cap, or forward failure the proxy
// synthesizes message_delta (stop_reason end_turn) + message_stop so the
// client never sees a dangling stream.
func handleAdvisorStreamingInterleave(
	ctx context.Context,
	w http.ResponseWriter,
	firstUpstream io.ReadCloser,
	lc *advisorLoopContext,
	transport http.RoundTripper,
) error {
	lg := logger{prefix: lc.toolName, verbose: lc.verbose}
	flusher, _ := w.(http.Flusher)

	em := &sseEmitter{w: w, flusher: flusher}
	emitEvent := em.Event

	// nextIndex is the monotonically increasing downstream block index. It
	// never resets across segments — continuation segments' blocks continue
	// where the previous segment's left off.
	nextIndex := 0
	// messageStartEmitted guards against re-emitting message_start on
	// continuation segments (the client expects exactly one per stream).
	messageStartEmitted := false

	upstreamBody := firstUpstream
	defer func() {
		if upstreamBody != nil {
			upstreamBody.Close()
		}
	}()

	for turn := 0; turn < maxAdvisorTurns; turn++ {
		seg := streamExecutorSegment(upstreamBody, lc, em, nextIndex, messageStartEmitted, lg)
		nextIndex = seg.nextIndex
		messageStartEmitted = true
		upstreamBody.Close()
		upstreamBody = nil

		switch seg.end {
		case segClientToolUse, segNaturalEnd:
			// Replay the captured terminal frames verbatim and end.
			if len(seg.messageDelta) > 0 {
				if err := emitEvent("message_delta", seg.messageDelta); err != nil {
					return err
				}
			}
			if len(seg.messageStop) > 0 {
				if err := emitEvent("message_stop", seg.messageStop); err != nil {
					return err
				}
			} else {
				emitAdvisorTerminal(emitEvent, "end_turn")
			}
			lg.Vlogf("advisor: stream segment %d ended (end=%v)", turn, seg.end)
			return nil

		case segError:
			lg.Logf("advisor: stream segment %d error: %v; synthesizing terminal", turn, seg.err)
			emitAdvisorTerminal(emitEvent, "end_turn")
			return nil

		case segAdvisorPause:
			// Run the advisor sub-inference(s), emitting pings while blocked,
			// then synthesize the advisor blocks and continue the turn.
			toolResults := []json.RawMessage{}
			for _, call := range seg.advisorCalls {
				serverID := mintAdvisorServerID(call.upstreamID)
				constraints := lc.constraints[call.toolName]

				// max_uses enforced pre-dispatch, counters cross-segment.
				if constraints.MaxUses > 0 && lc.usesByTool[call.toolName] >= constraints.MaxUses {
					lg.Logf("advisor: stream max_uses_exceeded tool=%s max=%d", call.toolName, constraints.MaxUses)
					idx, err := emitAdvisorBlocks(emitEvent, nextIndex, serverID, "", "max_uses_exceeded")
					if err != nil {
						return err
					}
					nextIndex = idx
					toolResults = append(toolResults, buildToolResultBlock(call.upstreamID, advisorUnavailableText("max_uses_exceeded")))
					continue
				}
				lc.usesByTool[call.toolName]++

				resolved := resolveAdvisorCall(constraints, lc.settings, lc.executorReq.Model)
				inProgress := append([]json.RawMessage{}, seg.textBlocks...)

				sub, err := runAdvisorWithPings(ctx, lc, inProgress, resolved, emitEvent, lg)
				if err != nil {
					code := advisorErrorCode(err)
					lg.Logf("advisor: stream sub-inference failed tool=%s code=%s err=%v", call.toolName, code, err)
					idx, eerr := emitAdvisorBlocks(emitEvent, nextIndex, serverID, "", code)
					if eerr != nil {
						return eerr
					}
					nextIndex = idx
					toolResults = append(toolResults, buildToolResultBlock(call.upstreamID, advisorUnavailableText(code)))
					continue
				}

				stopReason := ""
				if resolved.MaxTokensSet {
					stopReason = sub.StopReason
				}
				idx, eerr := emitAdvisorBlocksSuccess(emitEvent, nextIndex, serverID, sub.Text, stopReason)
				if eerr != nil {
					return eerr
				}
				nextIndex = idx
				toolResults = append(toolResults, buildToolResultBlock(call.upstreamID, sub.Text))
			}

			// Build and forward the streaming continuation.
			assistantContent := append([]json.RawMessage{}, seg.textBlocks...)
			assistantContent = append(assistantContent, seg.clientToolUses...)
			for _, call := range seg.advisorCalls {
				normalized := map[string]any{
					"type":  "tool_use",
					"id":    call.upstreamID,
					"name":  call.toolName,
					"input": map[string]any{},
				}
				if b, err := json.Marshal(normalized); err == nil {
					assistantContent = append(assistantContent, b)
				}
			}
			contBody, err := buildAdvisorStreamingContinuation(lc.executorReq, assistantContent, toolResults)
			if err != nil {
				lg.Logf("advisor: stream continuation build failed: %v", err)
				emitAdvisorTerminal(emitEvent, "end_turn")
				return nil
			}
			nextResp, err := forwardAdvisorStreamingContinuation(ctx, lc, contBody, transport)
			if err != nil {
				lg.Logf("advisor: stream continuation forward failed: %v", err)
				emitAdvisorTerminal(emitEvent, "end_turn")
				return nil
			}
			upstreamBody = nextResp
		}
	}

	lg.Logf("advisor: stream maxAdvisorTurns=%d reached; synthesizing terminal", maxAdvisorTurns)
	emitAdvisorTerminal(emitEvent, "end_turn")
	return nil
}

// streamExecutorSegment pumps one upstream SSE segment to the client,
// suppressing advisor tool_use blocks and remapping downstream indices so
// they stay contiguous. Returns how the segment ended plus the material
// needed to run the advisor and build the continuation.
func streamExecutorSegment(
	src io.Reader,
	lc *advisorLoopContext,
	em *sseEmitter,
	startIndex int,
	skipMessageStart bool,
	lg logger,
) streamSegmentResult {
	emitEvent := em.Event
	res := streamSegmentResult{nextIndex: startIndex}

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	scanner.Split(splitSSEFrames)

	// indexMap maps upstream block index → downstream index. Suppressed
	// advisor blocks get no downstream index (absent from the map).
	indexMap := map[int]int{}
	// suppressed tracks upstream indices belonging to advisor tool_use
	// blocks so their delta/stop frames are dropped too.
	suppressed := map[int]streamAdvisorCall{}
	// blockTypes records each upstream block's type so we can collect text
	// and client tool_use content for the continuation.
	pendingText := map[int]*strings.Builder{}
	pendingToolUse := map[int]*json.RawMessage{}
	pendingToolInput := map[int]*strings.Builder{}

	for scanner.Scan() {
		raw := scanner.Bytes()
		frame := append([]byte(nil), raw...)
		ev := parseSSEFrame(frame)
		if len(ev.Data) == 0 {
			// SSE comment (": keepalive") or any data-less frame. Forward
			// the ORIGINAL bytes — reconstructing it as "event: \ndata: \n\n"
			// would emit a malformed frame for a comment, whose EventType and
			// Data are both empty.
			if err := em.Raw(frame); err != nil {
				res.end, res.err = segError, err
				return res
			}
			continue
		}

		switch ev.EventType {
		case "message_start":
			if skipMessageStart {
				continue
			}
			if err := emitEvent("message_start", ev.Data); err != nil {
				res.end, res.err = segError, err
				return res
			}

		case "content_block_start":
			var probe struct {
				Index        int             `json:"index"`
				ContentBlock json.RawMessage `json:"content_block"`
			}
			if err := json.Unmarshal(ev.Data, &probe); err != nil {
				_ = emitEvent("content_block_start", ev.Data)
				continue
			}
			var cb struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(probe.ContentBlock, &cb)

			if cb.Type == "tool_use" && lc.constraints.isAdvisorToolUseName(cb.Name) {
				// SUPPRESS: the client must not see the raw advisor tool_use.
				suppressed[probe.Index] = streamAdvisorCall{upstreamID: cb.ID, toolName: cb.Name}
				lg.Vlogf("advisor: stream suppressing advisor tool_use upstream_index=%d id=%s", probe.Index, cb.ID)
				continue
			}

			// Pass through at the next contiguous downstream index.
			downIdx := res.nextIndex
			indexMap[probe.Index] = downIdx
			res.nextIndex++

			if cb.Type == "text" {
				sb := &strings.Builder{}
				sb.WriteString(cb.Text)
				pendingText[probe.Index] = sb
			}
			if cb.Type == "tool_use" {
				block := probe.ContentBlock
				pendingToolUse[probe.Index] = &block
				pendingToolInput[probe.Index] = &strings.Builder{}
			}

			out, err := rewriteContentBlockStartData(ev.Data, downIdx, probe.ContentBlock)
			if err != nil {
				out = ev.Data
			}
			if err := emitEvent("content_block_start", out); err != nil {
				res.end, res.err = segError, err
				return res
			}

		case "content_block_delta":
			var probe struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal(ev.Data, &probe); err != nil {
				_ = emitEvent("content_block_delta", ev.Data)
				continue
			}
			if _, isSuppressed := suppressed[probe.Index]; isSuppressed {
				// Drop the advisor block's input_json_delta frames.
				continue
			}
			if sb, ok := pendingText[probe.Index]; ok && probe.Delta.Type == "text_delta" {
				sb.WriteString(probe.Delta.Text)
			}
			if sb, ok := pendingToolInput[probe.Index]; ok && probe.Delta.Type == "input_json_delta" {
				sb.WriteString(probe.Delta.PartialJSON)
			}
			downIdx, ok := indexMap[probe.Index]
			if !ok {
				continue
			}
			out, err := setIndexInData(ev.Data, downIdx)
			if err != nil {
				out = ev.Data
			}
			if err := emitEvent("content_block_delta", out); err != nil {
				res.end, res.err = segError, err
				return res
			}

		case "content_block_stop":
			var probe struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal(ev.Data, &probe); err != nil {
				_ = emitEvent("content_block_stop", ev.Data)
				continue
			}
			if call, isSuppressed := suppressed[probe.Index]; isSuppressed {
				res.advisorCalls = append(res.advisorCalls, call)
				delete(suppressed, probe.Index)
				continue
			}
			// Finalize collected content for the continuation.
			if sb, ok := pendingText[probe.Index]; ok {
				if sb.Len() > 0 {
					block := map[string]string{"type": "text", "text": sb.String()}
					if b, err := json.Marshal(block); err == nil {
						res.textBlocks = append(res.textBlocks, b)
					}
				}
				delete(pendingText, probe.Index)
			}
			if blockPtr, ok := pendingToolUse[probe.Index]; ok {
				inputJSON := "{}"
				if sb, ok := pendingToolInput[probe.Index]; ok && sb.Len() > 0 {
					inputJSON = sb.String()
				}
				res.clientToolUses = append(res.clientToolUses, reassembleToolUseBlock(*blockPtr, inputJSON))
				delete(pendingToolUse, probe.Index)
				delete(pendingToolInput, probe.Index)
			}
			downIdx, ok := indexMap[probe.Index]
			if !ok {
				continue
			}
			out, err := setIndexInData(ev.Data, downIdx)
			if err != nil {
				out = ev.Data
			}
			if err := emitEvent("content_block_stop", out); err != nil {
				res.end, res.err = segError, err
				return res
			}

		case "message_delta":
			// Capture rather than emit — the interleave decides whether this
			// is the terminal delta (client tool use / natural end) or an
			// intermediate one (advisor pause, continuation follows).
			res.messageDelta = append([]byte(nil), ev.Data...)

		case "message_stop":
			res.messageStop = append([]byte(nil), ev.Data...)

		default:
			// ping, error, unknown: forward verbatim.
			if err := emitEvent(ev.EventType, ev.Data); err != nil {
				res.end, res.err = segError, err
				return res
			}
		}
	}
	if err := scanner.Err(); err != nil {
		res.end, res.err = segError, err
		return res
	}

	// Classify the segment.
	switch {
	case len(res.clientToolUses) > 0:
		res.end = segClientToolUse
	case len(res.advisorCalls) > 0:
		res.end = segAdvisorPause
	default:
		res.end = segNaturalEnd
	}
	return res
}

// reassembleToolUseBlock rebuilds a complete tool_use content block from its
// content_block_start payload plus the accumulated input_json_delta text.
func reassembleToolUseBlock(startBlock json.RawMessage, inputJSON string) json.RawMessage {
	var probe struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(startBlock, &probe)
	var input json.RawMessage
	if json.Valid([]byte(inputJSON)) {
		input = json.RawMessage(inputJSON)
	} else {
		input = json.RawMessage("{}")
	}
	out := map[string]any{
		"type":  "tool_use",
		"id":    probe.ID,
		"name":  probe.Name,
		"input": input,
	}
	b, _ := json.Marshal(out)
	return b
}

// runAdvisorWithPings executes the (blocking, non-streaming) advisor
// sub-inference while emitting `event: ping` frames every
// sseAdvisorPingInterval so the client connection is not dropped.
func runAdvisorWithPings(
	ctx context.Context,
	lc *advisorLoopContext,
	inProgressTurn []json.RawMessage,
	call resolvedAdvisorCall,
	emitEvent func(string, []byte) error,
	lg logger,
) (*advisorSubResponse, error) {
	type result struct {
		sub *advisorSubResponse
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sub, err := executeAdvisorCall(ctx, lc.upstream, lc.tokenSource, lc.toolName, lc.verbose, lc.executorReq, inProgressTurn, call)
		ch <- result{sub, err}
	}()

	ticker := time.NewTicker(sseAdvisorPingInterval)
	defer ticker.Stop()
	for {
		select {
		case r := <-ch:
			return r.sub, r.err
		case <-ticker.C:
			lg.Vlogf("advisor: stream ping (advisor sub-inference in flight)")
			_ = emitEvent("ping", []byte(`{"type":"ping"}`))
		case <-ctx.Done():
			return nil, ErrExecutionTimeExceeded
		}
	}
}

// emitAdvisorBlocksSuccess synthesizes the server_tool_use + success
// advisor_tool_result block pair at fresh indices. Returns the next free
// index.
func emitAdvisorBlocksSuccess(emitEvent func(string, []byte) error, startIndex int, serverID, text, stopReason string) (int, error) {
	resultBlock, err := anthropic.BuildAdvisorSuccessBlock(serverID, text, stopReason)
	if err != nil {
		return startIndex, err
	}
	return emitAdvisorBlockPair(emitEvent, startIndex, serverID, resultBlock)
}

// emitAdvisorBlocks synthesizes the server_tool_use + error
// advisor_tool_result pair. errorCode must be non-empty.
func emitAdvisorBlocks(emitEvent func(string, []byte) error, startIndex int, serverID, _ string, errorCode string) (int, error) {
	resultBlock, err := anthropic.BuildAdvisorErrorBlock(serverID, errorCode)
	if err != nil {
		return startIndex, err
	}
	return emitAdvisorBlockPair(emitEvent, startIndex, serverID, resultBlock)
}

// emitAdvisorBlockPair emits the call block (start → input_json_delta →
// stop) followed by the result block (start with full payload → stop; the
// result is delivered whole in the start event, no delta). Returns the next
// free downstream index.
func emitAdvisorBlockPair(emitEvent func(string, []byte) error, startIndex int, serverID string, resultBlock []byte) (int, error) {
	callIdx := startIndex
	stuBlock, err := anthropic.BuildAdvisorServerToolUseBlock(serverID)
	if err != nil {
		return startIndex, err
	}
	startEvent, _ := json.Marshal(map[string]any{
		"type":          "content_block_start",
		"index":         callIdx,
		"content_block": json.RawMessage(stuBlock),
	})
	if err := emitEvent("content_block_start", startEvent); err != nil {
		return startIndex, err
	}
	deltaEvent, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": callIdx,
		"delta": map[string]string{"type": "input_json_delta", "partial_json": "{}"},
	})
	if err := emitEvent("content_block_delta", deltaEvent); err != nil {
		return startIndex, err
	}
	stopEvent, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": callIdx})
	if err := emitEvent("content_block_stop", stopEvent); err != nil {
		return startIndex, err
	}

	resultIdx := callIdx + 1
	resStart, _ := json.Marshal(map[string]any{
		"type":          "content_block_start",
		"index":         resultIdx,
		"content_block": json.RawMessage(resultBlock),
	})
	if err := emitEvent("content_block_start", resStart); err != nil {
		return startIndex, err
	}
	resStop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": resultIdx})
	if err := emitEvent("content_block_stop", resStop); err != nil {
		return startIndex, err
	}
	return resultIdx + 1, nil
}

// emitAdvisorTerminal synthesizes message_delta (with the supplied
// stop_reason) + message_stop so the client never sees a dangling stream.
func emitAdvisorTerminal(emitEvent func(string, []byte) error, stopReason string) {
	delta, _ := json.Marshal(map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
	})
	_ = emitEvent("message_delta", delta)
	stop, _ := json.Marshal(map[string]any{"type": "message_stop"})
	_ = emitEvent("message_stop", stop)
}

// buildAdvisorStreamingContinuation assembles the streaming continuation
// request body: original messages + the reconstructed assistant turn +
// a user message with the tool_result blocks. stream:true.
func buildAdvisorStreamingContinuation(
	executorReq *anthropic.Request,
	assistantContent []json.RawMessage,
	toolResults []json.RawMessage,
) ([]byte, error) {
	messages := make([]json.RawMessage, 0, len(executorReq.Messages)+2)
	messages = append(messages, executorReq.Messages...)

	assistantMsg := map[string]any{"role": "assistant", "content": assistantContent}
	ab, err := json.Marshal(assistantMsg)
	if err != nil {
		return nil, err
	}
	messages = append(messages, ab)

	userMsg := map[string]any{"role": "user", "content": toolResults}
	ub, err := json.Marshal(userMsg)
	if err != nil {
		return nil, err
	}
	messages = append(messages, ub)

	out := map[string]json.RawMessage{}
	for k, v := range executorReq.Extras {
		if k == "thinking" {
			continue
		}
		out[k] = v
	}
	if executorReq.Model != "" {
		b, _ := json.Marshal(executorReq.Model)
		out["model"] = b
	}
	if len(executorReq.Tools) > 0 {
		b, _ := json.Marshal(executorReq.Tools)
		out["tools"] = b
	}
	if len(executorReq.System) > 0 {
		out["system"] = executorReq.System
	}
	mb, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	out["messages"] = mb
	out["stream"] = json.RawMessage("true")
	return json.Marshal(out)
}

// forwardAdvisorStreamingContinuation POSTs the continuation and returns the
// upstream response body for the next segment to stream.
func forwardAdvisorStreamingContinuation(
	ctx context.Context,
	lc *advisorLoopContext,
	body []byte,
	transport http.RoundTripper,
) (io.ReadCloser, error) {
	if transport == nil {
		transport = http.DefaultTransport
	}
	token, err := lc.tokenSource.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	basePath := strings.TrimRight(lc.upstream.Path, "/")
	outURL := *lc.upstream
	outURL.Path = basePath + "/v1/messages"
	outURL.RawPath = ""
	outURL.RawQuery = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, outURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("x-api-key", token)
	}
	req.Header.Set("x-databricks-use-coding-agent-mode", "true")
	req.Host = lc.upstream.Host

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, sanitizeLogOutput(string(snippet)))
	}
	return resp.Body, nil
}

// setIndexInData rewrites the top-level "index" field to an absolute value
// (as opposed to bumpIndexInData, which adds a shift).
func setIndexInData(data []byte, index int) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data, err
	}
	b, err := json.Marshal(index)
	if err != nil {
		return data, err
	}
	raw["index"] = b
	return json.Marshal(raw)
}

// sseEmitter serializes SSE output to the client. Raw writes a pre-formatted
// frame verbatim (used for comments / unknown data-less frames, where
// reconstruction would corrupt the frame); Event formats and writes an
// `event: TYPE\ndata: JSON\n\n` frame. Both flush so chunks reach the client
// as they are produced.
//
// The mutex guards against concurrent writers. Today emission is
// single-goroutine (the advisor sub-inference runs in its own goroutine but
// does not emit — runAdvisorWithPings emits from the select loop), so the
// lock is uncontended; it is kept so that adding a background emitter later
// cannot silently corrupt the stream.
type sseEmitter struct {
	mu      sync.Mutex
	w       io.Writer
	flusher http.Flusher
}

func (e *sseEmitter) Raw(b []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(b); err != nil {
		return err
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	return nil
}

func (e *sseEmitter) Event(eventType string, data []byte) error {
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(eventType)
	buf.WriteByte('\n')
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return e.Raw(buf.Bytes())
}
