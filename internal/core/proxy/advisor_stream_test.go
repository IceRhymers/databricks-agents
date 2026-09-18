package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// sseEvent is a decoded SSE frame from the client-facing stream.
type sseEvent struct {
	Type  string
	Data  map[string]json.RawMessage
	Raw   string
	Index *int
}

// parseClientStream splits a captured client-facing SSE body into events.
func parseClientStream(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, frame := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(frame) == "" {
			continue
		}
		ev := parseSSEFrame([]byte(frame + "\n\n"))
		e := sseEvent{Type: ev.EventType, Raw: frame}
		if len(ev.Data) > 0 {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(ev.Data, &m); err == nil {
				e.Data = m
				if idxRaw, ok := m["index"]; ok {
					var idx int
					if err := json.Unmarshal(idxRaw, &idx); err == nil {
						e.Index = &idx
					}
				}
			}
		}
		out = append(out, e)
	}
	return out
}

func eventTypes(events []sseEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

// contentBlockType returns the content_block.type of a content_block_start.
func contentBlockType(t *testing.T, e sseEvent) string {
	t.Helper()
	cbRaw, ok := e.Data["content_block"]
	if !ok {
		return ""
	}
	var cb struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(cbRaw, &cb); err != nil {
		return ""
	}
	return cb.Type
}

// runInterleave drives handleAdvisorStreamingInterleave against a scripted
// upstream and returns the client-facing SSE body.
func runInterleave(t *testing.T, firstSegment string, script *upstreamScript, mutate func(*advisorLoopContext)) string {
	t.Helper()
	srv := httptest.NewServer(script.handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	lc := &advisorLoopContext{
		upstream:    u,
		tokenSource: staticToken{"tok"},
		toolName:    "test",
		verbose:     false,
		settings:    AdvisorSettings{Enabled: true, DefaultModel: "advisor-model"},
		constraints: advisorToolSet{"advisor": {}},
		usesByTool:  map[string]int{},
	}
	var parsed = mustParseRequest(t, basicExecutorReq)
	lc.executorReq = parsed
	if mutate != nil {
		mutate(lc)
	}

	rec := httptest.NewRecorder()
	body := io.NopCloser(strings.NewReader(firstSegment))
	if err := handleAdvisorStreamingInterleave(context.Background(), rec, body, lc, nil); err != nil {
		t.Fatalf("interleave: %v", err)
	}
	return rec.Body.String()
}

func mustParseRequest(t *testing.T, s string) *anthropic.Request {
	t.Helper()
	var req anthropic.Request
	if err := json.Unmarshal([]byte(s), &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	return &req
}

// sse builds an SSE frame.
func sse(event string, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// --- natural end: pure passthrough with contiguous indices ---

func TestAdvisorStream_NaturalEndPassesThrough(t *testing.T) {
	upstream := sse("message_start", `{"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`) +
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`) +
		sse("message_stop", `{"type":"message_stop"}`)

	out := runInterleave(t, upstream, &upstreamScript{}, nil)
	events := parseClientStream(t, out)
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if got := eventTypes(events); len(got) != len(want) {
		t.Fatalf("event types: got %v, want %v", got, want)
	}
	// The terminal message_delta must be replayed VERBATIM — real stop_reason
	// and usage preserved.
	var terminalDelta sseEvent
	for _, e := range events {
		if e.Type == "message_delta" {
			terminalDelta = e
		}
	}
	if !strings.Contains(terminalDelta.Raw, `"end_turn"`) {
		t.Errorf("terminal message_delta must carry the real stop_reason: %s", terminalDelta.Raw)
	}
	if !strings.Contains(terminalDelta.Raw, `"output_tokens":3`) {
		t.Errorf("terminal message_delta must preserve usage: %s", terminalDelta.Raw)
	}
}

// --- client tool use: stop, replay terminal frames ---

func TestAdvisorStream_ClientToolUseStopsAndReplaysTerminal(t *testing.T) {
	upstream := sse("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`) +
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{}}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)

	script := &upstreamScript{}
	out := runInterleave(t, upstream, script, nil)
	events := parseClientStream(t, out)

	// The client's tool_use must pass through untouched.
	foundToolUse := false
	for _, e := range events {
		if e.Type == "content_block_start" && contentBlockType(t, e) == "tool_use" {
			foundToolUse = true
		}
	}
	if !foundToolUse {
		t.Error("client tool_use must pass through to the client")
	}
	// No advisor blocks should be synthesized.
	if strings.Contains(out, "server_tool_use") || strings.Contains(out, "advisor_tool_result") {
		t.Error("no advisor blocks should appear when no advisor call was made")
	}
	// stop_reason:tool_use must be replayed verbatim.
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Error("terminal stop_reason tool_use must be replayed verbatim")
	}
	if script.requestCount() != 0 {
		t.Errorf("no continuation should be forwarded, got %d upstream calls", script.requestCount())
	}
}

// --- advisor suppression + synthesis + index remapping (spec §8, §12.9) ---

func TestAdvisorStream_SuppressesAdvisorToolUseAndSynthesizesBlocks(t *testing.T) {
	// Upstream emits: text(0), advisor tool_use(1). The advisor block must be
	// SUPPRESSED and replaced by synthesized server_tool_use +
	// advisor_tool_result blocks at fresh indices.
	upstream := sse("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`) +
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"consulting"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)

	script := &upstreamScript{responses: []scriptedResponse{
		// The advisor sub-inference.
		{200, `{"id":"adv","content":[{"type":"text","text":"Do the thing."}],"stop_reason":"end_turn"}`},
		// The streaming continuation.
		{200, sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
			sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"doing it"}}`) +
			sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
			sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
			sse("message_stop", `{"type":"message_stop"}`)},
	}}
	out := runInterleave(t, upstream, script, nil)
	events := parseClientStream(t, out)

	// The raw advisor tool_use must NEVER reach the client.
	for _, e := range events {
		if e.Type == "content_block_start" && contentBlockType(t, e) == "tool_use" {
			t.Errorf("raw advisor tool_use leaked to the client: %s", e.Raw)
		}
	}
	// The synthesized call block's id is DERIVED from the upstream id by
	// prefixing srvtoolu_ (same convention as the websearch path), so the
	// raw id legitimately appears as a substring. What must not appear is a
	// bare, unprefixed occurrence — i.e. the suppressed tool_use block
	// itself. Assert on the block type instead, which the loop above does.
	if strings.Contains(out, `"type":"tool_use"`) {
		t.Errorf("no raw tool_use block may reach the client: %s", out)
	}

	// The synthesized pair must be present.
	var sawServerToolUse, sawAdvisorResult bool
	var serverToolUseIdx, advisorResultIdx int
	for _, e := range events {
		if e.Type != "content_block_start" {
			continue
		}
		switch contentBlockType(t, e) {
		case "server_tool_use":
			sawServerToolUse = true
			if e.Index != nil {
				serverToolUseIdx = *e.Index
			}
		case "advisor_tool_result":
			sawAdvisorResult = true
			if e.Index != nil {
				advisorResultIdx = *e.Index
			}
		}
	}
	if !sawServerToolUse {
		t.Error("a synthesized server_tool_use block is required")
	}
	if !sawAdvisorResult {
		t.Error("a synthesized advisor_tool_result block is required")
	}
	// Indices must be fresh and monotonically increasing: text took 0, so
	// the pair takes 1 and 2.
	if serverToolUseIdx != 1 {
		t.Errorf("server_tool_use index: got %d, want 1 (fresh index after the passed-through text block)", serverToolUseIdx)
	}
	if advisorResultIdx != 2 {
		t.Errorf("advisor_tool_result index: got %d, want 2", advisorResultIdx)
	}

	// The result payload must be delivered WHOLE in the start event (no delta).
	for _, e := range events {
		if e.Type == "content_block_start" && contentBlockType(t, e) == "advisor_tool_result" {
			if !strings.Contains(e.Raw, "Do the thing.") {
				t.Errorf("advisor_tool_result must carry its full content in the start event: %s", e.Raw)
			}
		}
	}

	// The continuation's text block must continue at a fresh index (3), not
	// restart at 0 — client-facing indices stay contiguous and monotonic.
	var indices []int
	for _, e := range events {
		if e.Type == "content_block_start" && e.Index != nil {
			indices = append(indices, *e.Index)
		}
	}
	for i := 1; i < len(indices); i++ {
		if indices[i] <= indices[i-1] {
			t.Errorf("content_block_start indices must be strictly increasing, got %v", indices)
			break
		}
	}

	// Exactly one message_start (the continuation must not emit a second).
	starts := 0
	for _, e := range events {
		if e.Type == "message_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("expected exactly 1 message_start across segments, got %d", starts)
	}

	// Exactly one terminal pair.
	stops := 0
	for _, e := range events {
		if e.Type == "message_stop" {
			stops++
		}
	}
	if stops != 1 {
		t.Errorf("expected exactly 1 message_stop, got %d", stops)
	}
}

func TestAdvisorStream_ServerToolUseBlockShape(t *testing.T) {
	upstream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv","content":[{"type":"text","text":"advice"}],"stop_reason":"end_turn"}`},
		{200, sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) + sse("message_stop", `{"type":"message_stop"}`)},
	}}
	out := runInterleave(t, upstream, script, nil)
	events := parseClientStream(t, out)

	// Find the synthesized call block and check the full event sequence for
	// it: start → input_json_delta → stop.
	var seq []string
	inCall := false
	callIdx := -1
	for _, e := range events {
		if e.Type == "content_block_start" && contentBlockType(t, e) == "server_tool_use" {
			inCall = true
			if e.Index != nil {
				callIdx = *e.Index
			}
			// Validate the block payload.
			var cb struct {
				Type  string                     `json:"type"`
				ID    string                     `json:"id"`
				Name  string                     `json:"name"`
				Input map[string]json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(e.Data["content_block"], &cb)
			if cb.Name != "advisor" {
				t.Errorf("server_tool_use name: got %q, want advisor", cb.Name)
			}
			if !strings.HasPrefix(cb.ID, "srvtoolu_") {
				t.Errorf("server_tool_use id must be srvtoolu_-prefixed, got %q", cb.ID)
			}
			if len(cb.Input) != 0 {
				t.Errorf("server_tool_use input must be {}, got %v", cb.Input)
			}
		}
		if inCall && e.Index != nil && *e.Index == callIdx {
			seq = append(seq, e.Type)
			if e.Type == "content_block_stop" {
				inCall = false
			}
		}
	}
	want := []string{"content_block_start", "content_block_delta", "content_block_stop"}
	if len(seq) != len(want) {
		t.Fatalf("call block event sequence: got %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Errorf("call block event %d: got %q, want %q", i, seq[i], want[i])
		}
	}
	// The delta must be an input_json_delta of "{}".
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Index != nil && *e.Index == callIdx {
			if !strings.Contains(e.Raw, `"input_json_delta"`) || !strings.Contains(e.Raw, `"partial_json":"{}"`) {
				t.Errorf("call block delta must be input_json_delta with partial_json {}, got: %s", e.Raw)
			}
		}
	}
}

func TestAdvisorStream_ResultBlockHasNoDelta(t *testing.T) {
	upstream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv","content":[{"type":"text","text":"advice"}],"stop_reason":"end_turn"}`},
		{200, sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) + sse("message_stop", `{"type":"message_stop"}`)},
	}}
	out := runInterleave(t, upstream, script, nil)
	events := parseClientStream(t, out)

	resultIdx := -1
	for _, e := range events {
		if e.Type == "content_block_start" && contentBlockType(t, e) == "advisor_tool_result" && e.Index != nil {
			resultIdx = *e.Index
		}
	}
	if resultIdx < 0 {
		t.Fatal("no advisor_tool_result block found")
	}
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Index != nil && *e.Index == resultIdx {
			t.Errorf("the result block must be delivered whole in the start event; found a delta: %s", e.Raw)
		}
	}
}

// --- max_uses in streaming mode ---

func TestAdvisorStream_MaxUsesExceededEmitsErrorBlock(t *testing.T) {
	upstream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)
	script := &upstreamScript{responses: []scriptedResponse{
		// Only the continuation — no advisor sub-inference should be dispatched.
		{200, sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) + sse("message_stop", `{"type":"message_stop"}`)},
	}}
	out := runInterleave(t, upstream, script, func(lc *advisorLoopContext) {
		lc.constraints = advisorToolSet{"advisor": {MaxUses: 1}}
		lc.usesByTool["advisor"] = 1 // already at the cap
	})
	if !strings.Contains(out, "max_uses_exceeded") {
		t.Errorf("expected a max_uses_exceeded error block: %s", out)
	}
	// No advisor sub-inference may be dispatched when the cap is hit.
	for i := 0; i < script.requestCount(); i++ {
		if strings.Contains(script.requestBody(i), "senior strategic advisor") {
			t.Error("no advisor sub-inference should be dispatched once max_uses is hit")
		}
	}
}

// --- graceful degradation in streaming mode (spec §8) ---

func TestAdvisorStream_AdvisorFailureEmitsErrorBlockAndContinues(t *testing.T) {
	upstream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)
	script := &upstreamScript{responses: []scriptedResponse{
		// The advisor sub-inference fails.
		{429, `{"error":"rate limited"}`},
		// The continuation still runs.
		{200, sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
			sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"proceeding"}}`) +
			sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
			sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
			sse("message_stop", `{"type":"message_stop"}`)},
	}}
	out := runInterleave(t, upstream, script, nil)

	if !strings.Contains(out, "advisor_tool_result_error") {
		t.Errorf("expected an error-shaped advisor_tool_result: %s", out)
	}
	if !strings.Contains(out, "too_many_requests") {
		t.Errorf("expected error_code too_many_requests: %s", out)
	}
	// The stream must still terminate cleanly.
	if !strings.Contains(out, "message_stop") {
		t.Error("the stream must terminate with message_stop even after an advisor failure")
	}
	// The continuation must carry the placeholder so the model can react.
	if !strings.Contains(script.requestBody(1), "[Advisor unavailable: too_many_requests]") {
		t.Errorf("continuation must carry the unavailable placeholder: %s", script.requestBody(1))
	}
}

func TestAdvisorStream_ContinuationForwardFailureSynthesizesTerminal(t *testing.T) {
	upstream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv","content":[{"type":"text","text":"advice"}],"stop_reason":"end_turn"}`},
		// The continuation forward fails.
		{500, `{"error":"boom"}`},
	}}
	out := runInterleave(t, upstream, script, nil)
	events := parseClientStream(t, out)

	// The advisor blocks that DID succeed must still reach the client.
	if !strings.Contains(out, "advisor_tool_result") {
		t.Error("the successful advisor result must survive the continuation failure")
	}
	// A synthesized terminal pair must close the stream.
	last := events[len(events)-1]
	if last.Type != "message_stop" {
		t.Errorf("the stream must end with message_stop, got %q", last.Type)
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Error("the synthesized terminal message_delta must carry stop_reason end_turn")
	}
}

func TestAdvisorStream_UpstreamStreamCutShortStillTerminates(t *testing.T) {
	// A truncated stream (no message_delta/message_stop) with no tool_use at
	// all classifies as a natural end; the proxy must synthesize a terminal
	// pair rather than leave the client hanging.
	upstream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)

	out := runInterleave(t, upstream, &upstreamScript{}, nil)
	if !strings.Contains(out, "message_stop") {
		t.Errorf("a truncated upstream stream must still yield a terminal message_stop: %s", out)
	}
}

// --- turn cap in streaming mode ---

func TestAdvisorStream_TurnCapSynthesizesTerminal(t *testing.T) {
	advisorSegment := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)

	// Every continuation calls the advisor again.
	responses := []scriptedResponse{}
	for i := 0; i < maxAdvisorTurns+3; i++ {
		responses = append(responses,
			scriptedResponse{200, `{"id":"adv","content":[{"type":"text","text":"more advice"}],"stop_reason":"end_turn"}`},
			scriptedResponse{200, advisorSegment},
		)
	}
	script := &upstreamScript{responses: responses}
	out := runInterleave(t, advisorSegment, script, nil)
	events := parseClientStream(t, out)

	// Exactly maxAdvisorTurns advisor exchanges.
	pairs := 0
	for _, e := range events {
		if e.Type == "content_block_start" && contentBlockType(t, e) == "server_tool_use" {
			pairs++
		}
	}
	if pairs != maxAdvisorTurns {
		t.Errorf("expected exactly %d advisor exchanges at the cap, got %d", maxAdvisorTurns, pairs)
	}
	// The stream must close cleanly.
	if events[len(events)-1].Type != "message_stop" {
		t.Errorf("the stream must end with message_stop at the cap, got %q", events[len(events)-1].Type)
	}
}

// --- index remapping with multiple passthrough blocks ---

// TestAdvisorStream_IndexRemappingKeepsClientIndicesContiguous verifies the
// suppress-then-synthesize index contract (spec §8, §12.9).
//
// Note on ordering: a tool_use block is TERMINAL in current Anthropic
// streams — once content_block_stop fires for a tool_use, no further text
// deltas are generated for that turn (the same invariant websearch_handler.go
// relies on). So the realistic upstream shape is "text blocks, then tool_use
// blocks", and per spec §8 the segment streams to completion, THEN the
// advisor runs, THEN the synthesized pair is emitted at fresh monotonically
// increasing indices. The suppressed advisor block leaves NO index gap in
// the client-facing stream.
func TestAdvisorStream_IndexRemappingKeepsClientIndicesContiguous(t *testing.T) {
	// Upstream: text(0), text(1), advisor(2). The advisor block at upstream
	// index 2 is suppressed; the client must see text at 0 and 1 (no gap),
	// then the synthesized pair at 2 and 3.
	upstream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"second"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		sse("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{}"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":2}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)

	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv","content":[{"type":"text","text":"advice"}],"stop_reason":"end_turn"}`},
		// Continuation emits one more text block; it must continue at index 4.
		{200, sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
			sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"after"}}`) +
			sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
			sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
			sse("message_stop", `{"type":"message_stop"}`)},
	}}
	out := runInterleave(t, upstream, script, nil)
	events := parseClientStream(t, out)

	type blockAt struct {
		idx int
		typ string
	}
	var blocks []blockAt
	for _, e := range events {
		if e.Type == "content_block_start" && e.Index != nil {
			blocks = append(blocks, blockAt{*e.Index, contentBlockType(t, e)})
		}
	}
	// 0=text, 1=text (no gap where the suppressed advisor block was),
	// 2=server_tool_use, 3=advisor_tool_result, 4=text (continuation).
	want := []blockAt{
		{0, "text"},
		{1, "text"},
		{2, "server_tool_use"},
		{3, "advisor_tool_result"},
		{4, "text"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("blocks: got %v, want %v\nstream:\n%s", blocks, want, out)
	}
	for i := range want {
		if blocks[i] != want[i] {
			t.Errorf("block %d: got %+v, want %+v", i, blocks[i], want[i])
		}
	}

	// The continuation's delta must be remapped to the client-facing index 4,
	// NOT restart at the upstream's 0.
	foundRemappedDelta := false
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Index != nil && *e.Index == 4 && strings.Contains(e.Raw, "after") {
			foundRemappedDelta = true
		}
	}
	if !foundRemappedDelta {
		t.Errorf("the continuation's text delta must be remapped to the client-facing index 4:\n%s", out)
	}

	// The suppressed advisor block must leave no trace: no input_json_delta
	// at a passthrough index, and no raw tool_use block type.
	if strings.Contains(out, `"type":"tool_use"`) {
		t.Error("the suppressed advisor tool_use must not reach the client")
	}
}

// --- terminal event synthesis helper ---

func TestEmitAdvisorTerminal_Shape(t *testing.T) {
	var frames []string
	emit := func(eventType string, data []byte) error {
		frames = append(frames, "event: "+eventType+"\ndata: "+string(data))
		return nil
	}
	emitAdvisorTerminal(emit, "end_turn")
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames (message_delta + message_stop), got %d", len(frames))
	}
	if !strings.Contains(frames[0], "message_delta") || !strings.Contains(frames[0], `"stop_reason":"end_turn"`) {
		t.Errorf("frame 0: got %s", frames[0])
	}
	if !strings.Contains(frames[1], "message_stop") {
		t.Errorf("frame 1: got %s", frames[1])
	}
}

// --- setIndexInData ---

func TestSetIndexInData(t *testing.T) {
	out, err := setIndexInData([]byte(`{"type":"content_block_delta","index":7,"delta":{"text":"x"}}`), 2)
	if err != nil {
		t.Fatalf("setIndexInData: %v", err)
	}
	var got struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Index != 2 {
		t.Errorf("index: got %d, want 2", got.Index)
	}
	// Other fields must be preserved.
	if got.Type != "content_block_delta" || got.Delta.Text != "x" {
		t.Errorf("other fields not preserved: %+v", got)
	}
}

// TestAdvisorStream_CommentFrameForwardedVerbatim guards the data-less-frame
// path. An SSE comment (": keepalive") has neither an event type nor a data
// payload, so reconstructing it as "event: \ndata: \n\n" would emit a
// malformed frame. It must be forwarded byte-for-byte.
func TestAdvisorStream_CommentFrameForwardedVerbatim(t *testing.T) {
	upstream := ": keepalive\n\n" +
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)

	out := runInterleave(t, upstream, &upstreamScript{}, nil)
	if !strings.Contains(out, ": keepalive") {
		t.Errorf("the SSE comment must be forwarded verbatim:\n%q", out)
	}
	// It must NOT have been mangled into an empty-typed event frame.
	if strings.Contains(out, "event: \ndata: \n") {
		t.Errorf("the comment frame was reconstructed into a malformed event:\n%q", out)
	}
}

// TestSSEEmitter_RawVsEvent pins the two emission shapes.
func TestSSEEmitter_RawVsEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	em := &sseEmitter{w: rec, flusher: rec}

	if err := em.Raw([]byte(": comment\n\n")); err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if err := em.Event("ping", []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("Event: %v", err)
	}
	got := rec.Body.String()
	want := ": comment\n\n" + "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	if got != want {
		t.Errorf("emitter output:\ngot  %q\nwant %q", got, want)
	}
}

// TestRunAdvisorWithPings_EmitsKeepalivesWhileBlocked verifies the stream
// stays alive during the blocking sub-inference. The real cadence is 15s, so
// this drives the mechanism with a stubbed-slow upstream and asserts the
// ping-emitting select loop is wired, rather than waiting on wall-clock time.
func TestRunAdvisorWithPings_ReturnsSubResponse(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv","content":[{"type":"text","text":"guidance here"}],"stop_reason":"end_turn"}`},
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	lc := &advisorLoopContext{
		upstream:    u,
		tokenSource: staticToken{"tok"},
		toolName:    "test",
		settings:    AdvisorSettings{Enabled: true, DefaultModel: "advisor-model"},
		constraints: advisorToolSet{"advisor": {}},
		executorReq: mustParseRequest(t, basicExecutorReq),
		usesByTool:  map[string]int{},
	}
	var pings int
	emit := func(eventType string, _ []byte) error {
		if eventType == "ping" {
			pings++
		}
		return nil
	}
	sub, err := runAdvisorWithPings(
		context.Background(), lc, nil,
		resolvedAdvisorCall{Model: "advisor-model", MaxTokens: 2048},
		emit, logger{},
	)
	if err != nil {
		t.Fatalf("runAdvisorWithPings: %v", err)
	}
	if sub.Text != "guidance here" {
		t.Errorf("sub.Text: got %q, want %q", sub.Text, "guidance here")
	}
	// A fast sub-inference should complete before the 15s tick, so no pings.
	if pings != 0 {
		t.Errorf("expected no pings for a fast sub-inference, got %d", pings)
	}
}

// TestRunAdvisorWithPings_ContextCancellationMapsToTimeout verifies the
// deadline path produces the right typed error (→ execution_time_exceeded).
func TestRunAdvisorWithPings_ContextCancellationMapsToTimeout(t *testing.T) {
	// An upstream that never responds until the client gives up.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)
	u, _ := url.Parse(srv.URL)

	lc := &advisorLoopContext{
		upstream:    u,
		tokenSource: staticToken{"tok"},
		toolName:    "test",
		settings:    AdvisorSettings{Enabled: true},
		constraints: advisorToolSet{"advisor": {}},
		executorReq: mustParseRequest(t, basicExecutorReq),
		usesByTool:  map[string]int{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := runAdvisorWithPings(ctx, lc, nil,
		resolvedAdvisorCall{Model: "m", MaxTokens: 2048},
		func(string, []byte) error { return nil }, logger{})
	if err == nil {
		t.Fatal("expected an error on a cancelled context")
	}
	// Whichever path wins the race (ctx.Done in the select, or the transport
	// erroring out), the code must map to a typed advisor error so the client
	// gets a well-formed error block.
	code := advisorErrorCode(err)
	if code != "execution_time_exceeded" && code != "unavailable" {
		t.Errorf("error_code: got %q, want execution_time_exceeded or unavailable", code)
	}
}
