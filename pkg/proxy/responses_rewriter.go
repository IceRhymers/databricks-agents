package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ResponsesRewriteSettings bundles the optional Responses-API SSE rewriter
// knobs. Embedded in Config; when Enabled is false the rewriter is fully
// bypassed and the proxy forwards bytes verbatim. OpenAI-shaped consumers
// (databricks-opencode) set Enabled=true; Claude Code leaves it false.
type ResponsesRewriteSettings struct {
	Enabled bool
}

// responsesIndexCacheLimit bounds the per-stream output_index -> item.id cache.
// Far above any real Responses-API turn (which has a handful of output items)
// but cheap insurance against an upstream that emits many distinct indices
// without ever sending output_item.done. On overflow new ids are not cached and
// affected downstream events degrade to passthrough.
const responsesIndexCacheLimit = 1024

// responsesRewriteState holds per-stream mutable state for the Responses-API
// SSE rewriter. It caches the canonical item id (from response.output_item.added)
// keyed by output_index, so downstream events that carry a mismatched item_id
// can be corrected.
type responsesRewriteState struct {
	// canonicalByIndex maps output_index -> the item.id reported in the
	// response.output_item.added event for that index (message items only).
	// Entries are deleted on response.output_item.done so a reused index
	// never carries a stale id.
	canonicalByIndex map[int]string
	// warned tracks (output_index, originalId) pairs already logged so a
	// long stream emits at most one warning per distinct mismatch.
	warned map[string]struct{}
}

// responsesEnvelope is the lightweight shape we probe on every frame. Only the
// fields needed for id reconciliation are decoded; everything else in the JSON
// payload is preserved by re-marshalling the full object on the rare rewrite
// path (conforming streams are emitted verbatim, byte-for-byte).
type responsesEnvelope struct {
	Type        string `json:"type"`
	OutputIndex *int   `json:"output_index"`
	ItemID      string `json:"item_id"`
	Item        *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"item"`
}

// pumpResponsesSSE reads OpenAI Responses-API SSE frames from src and writes
// them to w, rewriting a downstream event's item_id to the canonical item.id
// announced by the matching response.output_item.added event when the two
// disagree. This works around OpenAI-compatible proxies (notably Databricks AI
// Gateway) that re-encode the stream and emit divergent ids, which otherwise
// breaks @ai-sdk/openai's parser with "text part <id> not found".
//
// Behaviour:
//   - Caches output_index -> item.id from output_item.added (message items only;
//     reasoning / function_call items are intentionally ignored).
//   - Deletes the cache entry on output_item.done so reused indices don't carry
//     a stale id.
//   - Rewrites item_id on downstream events only when it disagrees with the
//     cached canonical id; logs once per (output_index, originalId) pair.
//   - Passes through verbatim (byte-identical) for: comment/empty frames,
//     [DONE], parse failures, unknown event types, reordered events
//     (delta before added -> cache miss), and any event that already agrees.
//
// Returns nil on clean EOF, otherwise the underlying scanner error. Caller owns
// src.Close.
func pumpResponsesSSE(ctx context.Context, w http.ResponseWriter, src io.Reader, verbose bool) error {
	flusher, _ := w.(http.Flusher)
	state := &responsesRewriteState{
		canonicalByIndex: map[int]string{},
		warned:           map[string]struct{}{},
	}

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
		// Honour context cancellation between frames.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		raw := scanner.Bytes()
		// Copy — bufio.Scanner reuses its buffer on the next Scan.
		frame := append([]byte(nil), raw...)

		ev := parseSSEFrame(frame)
		if len(ev.Data) == 0 {
			// Comment-only or data-less frame; pass through verbatim.
			if err := emit(frame); err != nil {
				return err
			}
			continue
		}

		out := maybeRewriteResponsesFrame(frame, ev, state, verbose)
		if err := emit(out); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if verbose {
			log.Printf("databricks-claude: responses: SSE scanner error: %v", err)
		}
		return err
	}
	return nil
}

// maybeRewriteResponsesFrame inspects a single SSE frame and returns either the
// original bytes (the overwhelmingly common case) or a re-serialized frame with
// a corrected item_id. State (the index->canonical-id cache) is updated as a
// side effect on output_item.added / output_item.done events.
func maybeRewriteResponsesFrame(frame []byte, ev sseFrame, state *responsesRewriteState, verbose bool) []byte {
	// [DONE] sentinel and anything that isn't a JSON object: pass through.
	trimmed := bytes.TrimSpace(ev.Data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return frame
	}

	var env responsesEnvelope
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		// Unparseable payload: pass through verbatim.
		return frame
	}

	switch env.Type {
	case "response.output_item.added":
		// Cache the canonical id for message items only.
		if env.OutputIndex != nil && env.Item != nil && env.Item.Type == "message" && env.Item.ID != "" {
			// Bound the cache against a hostile/buggy upstream that emits many
			// distinct output_index values without ever sending
			// output_item.done. Mirrors the websearch rewriter's
			// fulfilledMemoryLimit discipline. On overflow we stop caching new
			// ids (downstream mismatches for uncached indices degrade to
			// passthrough — never a panic or unbounded growth).
			if len(state.canonicalByIndex) < responsesIndexCacheLimit {
				state.canonicalByIndex[*env.OutputIndex] = env.Item.ID
			} else if verbose {
				log.Printf("databricks-claude: responses: index cache at cap (%d); not caching output_index=%d", responsesIndexCacheLimit, *env.OutputIndex)
			}
		}
		return frame

	case "response.output_item.done":
		// Index is being retired; drop the cached entry so a reused index
		// cannot carry a stale id.
		if env.OutputIndex != nil {
			delete(state.canonicalByIndex, *env.OutputIndex)
		}
		return frame
	}

	// Downstream events: only those carrying both an output_index and an
	// item_id are candidates for rewrite.
	if env.OutputIndex == nil || env.ItemID == "" {
		return frame
	}
	canonical, ok := state.canonicalByIndex[*env.OutputIndex]
	if !ok || canonical == env.ItemID {
		// Cache miss (e.g. reordered delta-before-added, or non-message
		// item) or already-correct id: pass through verbatim.
		return frame
	}

	// Mismatch — rewrite item_id to the canonical id. Decode into a generic
	// map to preserve all other fields, then re-emit. This path is only taken
	// for non-conforming streams, so byte-identity of conforming streams is
	// unaffected.
	var obj map[string]any
	if err := json.Unmarshal(ev.Data, &obj); err != nil {
		return frame
	}
	obj["item_id"] = canonical
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return frame
	}

	if verbose {
		key := env.ItemID + "@" + strconv.Itoa(*env.OutputIndex)
		if _, seen := state.warned[key]; !seen {
			state.warned[key] = struct{}{}
			log.Printf("databricks-claude: responses: rewrote item_id %q -> %q (output_index=%d, type=%s)",
				env.ItemID, canonical, *env.OutputIndex, env.Type)
		}
	}

	return buildSSEFrame(ev.EventType, rewritten)
}

// buildSSEFrame reconstructs an SSE frame from an optional event type and a
// single-line JSON data payload, terminated by the "\n\n" boundary. Used only
// on the rewrite path; conforming frames are forwarded verbatim.
func buildSSEFrame(eventType string, data []byte) []byte {
	var b bytes.Buffer
	if eventType != "" {
		b.WriteString("event: ")
		b.WriteString(eventType)
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes()
}

// isResponsesPath reports whether the request path targets the OpenAI
// Responses API (".../responses"). The Databricks serving-endpoint shape is
// "/serving-endpoints/{...}/openai/v1/responses".
func isResponsesPath(p string) bool {
	return strings.Contains(p, "/responses")
}
