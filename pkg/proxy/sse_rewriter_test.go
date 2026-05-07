package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-claude/pkg/websearch"
)

// erroringBackend is a Backend that returns a fixed error from Search.
type erroringBackend struct{}

func (erroringBackend) Name() string { return "erroring" }
func (erroringBackend) Search(_ context.Context, _ string, _ int) ([]websearch.Result, error) {
	return nil, errors.New("simulated backend failure")
}

// frame builds a single SSE frame from event type and JSON data.
func frame(event string, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// runPump runs pumpSSE on the given input string and returns the output as
// a string for assertions. fakeRW provides http.ResponseWriter +
// http.Flusher so the rewriter exercises the flushing path.
func runPump(t *testing.T, input string, ws WebSearchSettings, rt rewrittenTools) string {
	t.Helper()
	rec := newFlushRecorder()
	if err := pumpSSE(context.Background(), rec, strings.NewReader(input), ws, rt, false); err != nil {
		t.Fatalf("pumpSSE: %v", err)
	}
	return rec.body.String()
}

// flushRecorder is an http.ResponseWriter that satisfies http.Flusher.
type flushRecorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newFlushRecorder() *flushRecorder { return &flushRecorder{header: http.Header{}, status: 200} }
func (f *flushRecorder) Header() http.Header { return f.header }
func (f *flushRecorder) Write(b []byte) (int, error) {
	return f.body.Write(b)
}
func (f *flushRecorder) WriteHeader(s int) { f.status = s }
func (f *flushRecorder) Flush()            {}

// extractDataPayloads returns the JSON payload bytes of each `data:` line
// in an SSE byte stream, in order. Useful for asserting on parsed events.
func extractDataPayloads(t *testing.T, sse string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, frame := range strings.Split(sse, "\n\n") {
		if frame == "" {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			var m map[string]any
			if err := json.Unmarshal([]byte(payload), &m); err != nil {
				t.Fatalf("data not JSON: %q (err=%v)", payload, err)
			}
			out = append(out, m)
		}
	}
	return out
}

// TestSSERewriter_StreamingWebSearch_HappyPath: tool_use(web_search) with
// split input_json_delta fragments → rewritten to server_tool_use +
// synthesized web_search_tool_result.
func TestSSERewriter_StreamingWebSearch_HappyPath(t *testing.T) {
	input := strings.Join([]string{
		frame("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`),
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_abc","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"que"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"ry\":\"go"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"lang\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`),
		frame("message_stop", `{"type":"message_stop"}`),
	}, "")

	ws := WebSearchSettings{
		Enabled: true,
		Backend: &fakeBackend{results: []websearch.Result{
			{Title: "Go", URL: "https://go.dev", Snippet: "An open source language"},
		}},
	}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	// Original tool_use must have been rewritten on the wire to server_tool_use.
	if !strings.Contains(out, `"type":"server_tool_use"`) {
		t.Errorf("missing rewritten server_tool_use in output:\n%s", out)
	}
	if strings.Contains(out, `"type":"tool_use"`) {
		t.Errorf("output still contains client tool_use type:\n%s", out)
	}
	// Synthetic result must be present with the URL from the fake backend.
	if !strings.Contains(out, "https://go.dev") {
		t.Errorf("missing search result URL in output:\n%s", out)
	}
	if !strings.Contains(out, `"type":"web_search_tool_result"`) {
		t.Errorf("missing web_search_tool_result block:\n%s", out)
	}
	// Snippet should be folded into title.
	if !strings.Contains(out, "An open source language") {
		t.Errorf("snippet not folded into title:\n%s", out)
	}
	// Server tool id minted from original toolu_abc.
	if !strings.Contains(out, `srvtoolu_toolu_abc`) {
		t.Errorf("server tool id not minted from toolu_abc:\n%s", out)
	}
}

// TestSSERewriter_StreamingWebSearch_BackendError: backend returns error →
// emit error variant block.
func TestSSERewriter_StreamingWebSearch_BackendError(t *testing.T) {
	input := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"x\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_stop", `{"type":"message_stop"}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: erroringBackend{}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	if !strings.Contains(out, `"type":"web_search_tool_result_error"`) {
		t.Errorf("expected error variant in output:\n%s", out)
	}
	if !strings.Contains(out, `"error_code":"unavailable"`) {
		t.Errorf("expected unavailable error_code:\n%s", out)
	}
}

// TestSSERewriter_StreamingWebSearch_InputOverflow: simulated huge
// partial_json → overflow → no synthetic block injected.
func TestSSERewriter_StreamingWebSearch_InputOverflow(t *testing.T) {
	bigChunk := strings.Repeat("a", 32*1024) // 32 KiB per delta; two will overflow 64 KiB cap
	jsonDelta := func(s string) string {
		// Embed the chunk safely — escape would be heavy; we only need to
		// trigger the byte counter, content can be invalid JSON.
		return fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, s)
	}
	input := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_y","name":"web_search"}}`),
		frame("content_block_delta", jsonDelta(bigChunk)),
		frame("content_block_delta", jsonDelta(bigChunk)),
		frame("content_block_delta", jsonDelta(bigChunk)),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: &fakeBackend{results: nil}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	if strings.Contains(out, `"type":"web_search_tool_result"`) {
		t.Errorf("expected no synthetic block on overflow:\n%s", out[:min(500, len(out))])
	}
}

// TestSSERewriter_StreamingWebSearch_InvalidJSON: malformed accumulated
// input → emit invalid_input error block.
func TestSSERewriter_StreamingWebSearch_InvalidJSON(t *testing.T) {
	input := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_z","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"not-json"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: &fakeBackend{}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	if !strings.Contains(out, `"error_code":"invalid_input"`) {
		t.Errorf("expected invalid_input error code on malformed JSON:\n%s", out)
	}
}

// TestSSERewriter_StreamingNoToolUse_Passthrough: text-only stream is
// effectively unchanged (events forwarded; no synthesis fires).
func TestSSERewriter_StreamingNoToolUse_Passthrough(t *testing.T) {
	input := strings.Join([]string{
		frame("message_start", `{"type":"message_start","message":{"id":"msg_x"}}`),
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`),
		frame("message_stop", `{"type":"message_stop"}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: &fakeBackend{}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	// All content preserved; no injected blocks.
	if strings.Contains(out, "web_search_tool_result") {
		t.Errorf("unexpected synthetic block injected:\n%s", out)
	}
	if !strings.Contains(out, `"text":"hello"`) {
		t.Errorf("text delta missing from output:\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("end_turn stop_reason should be preserved unchanged:\n%s", out)
	}
}

// TestSSERewriter_NonStreaming_BodyJSON_Rewrite: the non-streaming JSON
// branch correctly rewrites tool_use → server_tool_use + injects
// web_search_tool_result.
func TestSSERewriter_NonStreaming_BodyJSON_Rewrite(t *testing.T) {
	body := []byte(`{
	  "id": "msg_a",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-opus-4-7",
	  "content": [
	    {"type": "text", "text": "Searching..."},
	    {"type": "tool_use", "id": "toolu_q", "name": "web_search", "input": {"query": "golang"}}
	  ],
	  "stop_reason": "tool_use",
	  "usage": {"input_tokens": 10, "output_tokens": 20}
	}`)

	ws := WebSearchSettings{
		Enabled: true,
		Backend: &fakeBackend{results: []websearch.Result{{Title: "Go", URL: "https://go.dev"}}},
	}
	out, err := rewriteJSONResponse(context.Background(), body, ws, false)
	if err != nil {
		t.Fatalf("rewriteJSONResponse: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"type":"server_tool_use"`) {
		t.Errorf("missing server_tool_use:\n%s", s)
	}
	if !strings.Contains(s, `"type":"web_search_tool_result"`) {
		t.Errorf("missing web_search_tool_result:\n%s", s)
	}
	if !strings.Contains(s, "https://go.dev") {
		t.Errorf("missing URL from results:\n%s", s)
	}
	if !strings.Contains(s, `"stop_reason":"end_turn"`) {
		t.Errorf("stop_reason should have been rewritten to end_turn:\n%s", s)
	}
	if !strings.Contains(s, `"input_tokens":10`) {
		t.Errorf("usage.input_tokens should be preserved verbatim:\n%s", s)
	}
}

// TestSSERewriter_IndexRenumbering: tool_use at index 1 followed by text
// at index 2; injected block goes at index 2; original text renumbered to 3.
func TestSSERewriter_IndexRenumbering(t *testing.T) {
	input := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`),
		frame("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"hello"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":2}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: &fakeBackend{results: []websearch.Result{{URL: "https://x", Title: "X"}}}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	payloads := extractDataPayloads(t, out)
	// Expected sequence (events on the wire):
	//   block_start(idx=0,text), block_stop(idx=0),
	//   block_start(idx=1,server_tool_use), block_delta(idx=1), block_stop(idx=1),
	//   block_start(idx=2,web_search_tool_result) [INJECTED],
	//   block_stop(idx=2) [INJECTED],
	//   block_start(idx=3,text) [shifted +1], block_delta(idx=3), block_stop(idx=3).
	wantIndices := []int{0, 0, 1, 1, 1, 2, 2, 3, 3, 3}
	if len(payloads) != len(wantIndices) {
		t.Fatalf("payload count = %d, want %d:\n%s", len(payloads), len(wantIndices), out)
	}
	for i, p := range payloads {
		idx, ok := p["index"].(float64)
		if !ok {
			continue // some events lack index (e.g. message_start) — none expected here
		}
		if int(idx) != wantIndices[i] {
			t.Errorf("payload[%d] index=%d want %d (type=%v):\n%s", i, int(idx), wantIndices[i], p["type"], out)
		}
	}
	// And the injected block at index 5 (0-indexed payload position) is the
	// web_search_tool_result with index=2.
	injected := payloads[5]
	if cb, ok := injected["content_block"].(map[string]any); !ok {
		t.Errorf("payload[5] missing content_block: %v", injected)
	} else if cb["type"] != "web_search_tool_result" {
		t.Errorf("injected block type=%v want web_search_tool_result; payload[5]=%v", cb["type"], injected)
	}
	if int(injected["index"].(float64)) != 2 {
		t.Errorf("injected block at wrong index: %v want 2", injected["index"])
	}
}

// TestSSERewriter_TwoSearches: two tool_use(web_search) blocks in one
// response → two synthetic results injected.
func TestSSERewriter_TwoSearches(t *testing.T) {
	input := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"a\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_b","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"b\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`),
		frame("message_stop", `{"type":"message_stop"}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: &fakeBackend{results: []websearch.Result{{URL: "https://r", Title: "R"}}}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	// Two injected web_search_tool_result blocks expected.
	count := strings.Count(out, `"type":"web_search_tool_result"`)
	if count != 2 {
		t.Errorf("expected 2 injected blocks, got %d:\n%s", count, out)
	}
	// Two server_tool_use blocks expected on the wire.
	count = strings.Count(out, `"type":"server_tool_use"`)
	if count != 2 {
		t.Errorf("expected 2 server_tool_use blocks, got %d:\n%s", count, out)
	}
}

// TestSSERewriter_StopReasonRewrite: stop_reason "tool_use" rewritten to
// "end_turn" after injection; usage preserved verbatim.
func TestSSERewriter_StopReasonRewrite(t *testing.T) {
	input := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_q","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":12,"output_tokens":34}}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: &fakeBackend{results: []websearch.Result{{URL: "https://r", Title: "R"}}}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("expected stop_reason rewritten to end_turn:\n%s", out)
	}
	if strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Errorf("original stop_reason still present:\n%s", out)
	}
	if !strings.Contains(out, `"input_tokens":12`) || !strings.Contains(out, `"output_tokens":34`) {
		t.Errorf("usage fields should be preserved verbatim:\n%s", out)
	}
}

// TestSSERewriter_NoToolUse_StopReasonUntouched: stop_reason untouched
// when no synthetic blocks were injected.
func TestSSERewriter_NoToolUse_StopReasonUntouched(t *testing.T) {
	input := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`),
	}, "")

	ws := WebSearchSettings{Enabled: true, Backend: &fakeBackend{}}
	out := runPump(t, input, ws, rewrittenTools{HasWebSearch: true})

	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("end_turn stop_reason should be preserved verbatim:\n%s", out)
	}
}

// TestSSERewriter_ContentLengthStripped: integration test against a fake
// upstream that sets Content-Length on the SSE response. Assert the
// outbound response to the proxy client does NOT carry Content-Length.
func TestSSERewriter_ContentLengthStripped(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_q","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_stop", `{"type":"message_stop"}`),
	}, "")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(upstreamSSE)))
		w.WriteHeader(200)
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		WebSearch: WebSearchSettings{
			Enabled: true,
			Backend: &fakeBackend{results: []websearch.Result{{URL: "https://x", Title: "X"}}},
		},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	body := `{"model":"claude-opus-4-7","messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}],"max_tokens":1024,"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Length") != "" {
		t.Errorf("Content-Length should be stripped on SSE rewrite path; got %q", rec.Header().Get("Content-Length"))
	}
	if !strings.Contains(rec.Body.String(), "web_search_tool_result") {
		t.Errorf("integration test: expected injected web_search_tool_result in response:\n%s", rec.Body.String())
	}
}

// TestProxy_WebSearchEnabled_StreamingResponse_RewritesToWebSearchToolResult:
// integration test verifying a full request → upstream SSE → rewritten
// response flow.
func TestProxy_WebSearchEnabled_StreamingResponse_RewritesToWebSearchToolResult(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		frame("message_start", `{"type":"message_start","message":{"id":"msg_int"}}`),
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_int","name":"web_search"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"golang\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		frame("message_stop", `{"type":"message_stop"}`),
	}, "")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		WebSearch: WebSearchSettings{
			Enabled: true,
			Backend: &fakeBackend{results: []websearch.Result{
				{URL: "https://go.dev", Title: "Go", Snippet: "An open source language"},
			}},
		},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	body := `{"model":"claude-opus-4-7","messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}],"max_tokens":1024,"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	out := rec.Body.String()
	if !strings.Contains(out, `"type":"web_search_tool_result"`) {
		t.Errorf("integration: missing web_search_tool_result block:\n%s", out)
	}
	if !strings.Contains(out, "https://go.dev") {
		t.Errorf("integration: missing URL:\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("integration: stop_reason should have been rewritten to end_turn:\n%s", out)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("integration: Content-Type should be preserved as text/event-stream, got %q", rec.Header().Get("Content-Type"))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
