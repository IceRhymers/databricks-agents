package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// staticToken is a TokenSource returning a fixed token.
type staticToken struct{ tok string }

func (s staticToken) Token(context.Context) (string, error) { return s.tok, nil }

// upstreamScript drives a fake upstream: each POST /v1/messages pops the next
// scripted response. Captured request bodies are retained for assertions.
type upstreamScript struct {
	mu        sync.Mutex
	responses []scriptedResponse
	idx       int
	requests  [][]byte
}

type scriptedResponse struct {
	status int
	body   string
}

func (s *upstreamScript) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		s.mu.Lock()
		s.requests = append(s.requests, body)
		var resp scriptedResponse
		if s.idx < len(s.responses) {
			resp = s.responses[s.idx]
			s.idx++
		} else {
			resp = scriptedResponse{status: 500, body: `{"error":"script exhausted"}`}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}
}

func (s *upstreamScript) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *upstreamScript) requestBody(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.requests) {
		return ""
	}
	return string(s.requests[i])
}

// newLoopContext wires an advisorLoopContext against a fake upstream.
func newLoopContext(t *testing.T, script *upstreamScript, executorReq string) (*advisorLoopContext, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(script.handler())
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	var req anthropic.Request
	if err := json.Unmarshal([]byte(executorReq), &req); err != nil {
		t.Fatalf("parse executor request: %v", err)
	}
	return &advisorLoopContext{
		upstream:    u,
		tokenSource: staticToken{"tok"},
		toolName:    "test",
		verbose:     false,
		settings:    AdvisorSettings{Enabled: true, DefaultModel: "advisor-model"},
		constraints: advisorToolSet{"advisor": {}},
		executorReq: &req,
		usesByTool:  map[string]int{},
	}, srv
}

const basicExecutorReq = `{"model":"exec-model","messages":[{"role":"user","content":[{"type":"text","text":"work"}]}],"tools":[{"name":"Bash","input_schema":{"type":"object"}},{"type":"advisor_20260301","name":"advisor"}]}`

func parseLoopResult(t *testing.T, body []byte) (stopReason string, blockTypes []string, decoded []json.RawMessage) {
	t.Helper()
	var resp anthropic.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("result not decodable: %v (%s)", err, body)
	}
	for _, b := range resp.Content {
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(b, &p)
		blockTypes = append(blockTypes, p.Type)
	}
	return resp.StopReason, blockTypes, resp.Content
}

// --- natural end: no tool_use at all ---

func TestRunAdvisorServerLoop_NaturalEnd(t *testing.T) {
	script := &upstreamScript{}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_1","type":"message","role":"assistant","model":"exec-model","content":[{"type":"text","text":"all done"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	stop, types, _ := parseLoopResult(t, out)
	if stop != "end_turn" {
		t.Errorf("stop_reason: got %q, want end_turn (upstream's reason preserved)", stop)
	}
	if len(types) != 1 || types[0] != "text" {
		t.Errorf("block types: got %v, want [text]", types)
	}
	if script.requestCount() != 0 {
		t.Errorf("natural end must make no upstream calls, got %d", script.requestCount())
	}
	// The envelope must be preserved.
	if !strings.Contains(string(out), `"msg_1"`) {
		t.Error("response id was not preserved from the upstream envelope")
	}
	if !strings.Contains(string(out), `"input_tokens":10`) {
		t.Error("usage was not preserved from the upstream envelope")
	}
}

func TestRunAdvisorServerLoop_PreservesNonEndTurnStopReason(t *testing.T) {
	script := &upstreamScript{}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_1","content":[{"type":"text","text":"truncated"}],"stop_reason":"max_tokens"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	stop, _, _ := parseLoopResult(t, out)
	if stop != "max_tokens" {
		t.Errorf("stop_reason: got %q, want max_tokens", stop)
	}
}

// --- client tool stop ---

func TestRunAdvisorServerLoop_ClientToolUseStops(t *testing.T) {
	script := &upstreamScript{}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_1","content":[
		{"type":"text","text":"running the build"},
		{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"make"}}
	],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	stop, types, _ := parseLoopResult(t, out)
	if stop != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use (client executes its own tools)", stop)
	}
	// Content appended verbatim — the client's tool_use must survive intact.
	if len(types) != 2 || types[0] != "text" || types[1] != "tool_use" {
		t.Errorf("block types: got %v, want [text tool_use]", types)
	}
	if !strings.Contains(string(out), `"command":"make"`) {
		t.Error("client tool_use input was not preserved verbatim")
	}
	if script.requestCount() != 0 {
		t.Errorf("client-tool stop must make no upstream calls, got %d", script.requestCount())
	}
}

// --- single advisor call + continuation ---

func TestRunAdvisorServerLoop_SingleAdvisorCallThenEnd(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		// 1: the advisor sub-inference.
		{200, `{"id":"msg_adv","content":[{"type":"text","text":"Do A then B."}],"stop_reason":"end_turn"}`},
		// 2: the continuation.
		{200, `{"id":"msg_2","content":[{"type":"text","text":"Doing A then B."}],"stop_reason":"end_turn","usage":{"output_tokens":7}}`},
	}}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_1","content":[
		{"type":"text","text":"Let me think."},
		{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}
	],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	stop, types, blocks := parseLoopResult(t, out)
	if stop != "end_turn" {
		t.Errorf("stop_reason: got %q, want end_turn (terminal turn's reason)", stop)
	}
	// Expect: text (pre-call), server_tool_use, advisor_tool_result, text (continuation)
	want := []string{"text", "server_tool_use", "advisor_tool_result", "text"}
	if len(types) != len(want) {
		t.Fatalf("block types: got %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("block %d: got %q, want %q", i, types[i], want[i])
		}
	}
	// The raw advisor tool_use must NOT appear — it is replaced by the pair.
	if strings.Contains(string(out), `"type":"tool_use"`) {
		t.Error("raw advisor tool_use leaked into the client-facing response")
	}
	// The advisor result must carry the guidance text.
	if text := anthropic.ExtractAdvisorResultText(blocks[2]); text != "Do A then B." {
		t.Errorf("advisor result text: got %q", text)
	}
	// server_tool_use id must be srvtoolu_-prefixed.
	var stu struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(blocks[1], &stu)
	if !strings.HasPrefix(stu.ID, "srvtoolu_") {
		t.Errorf("server_tool_use id must be srvtoolu_-prefixed, got %q", stu.ID)
	}

	if script.requestCount() != 2 {
		t.Fatalf("expected 2 upstream calls (advisor + continuation), got %d", script.requestCount())
	}

	// The advisor sub-request must use the configured advisor model, set
	// tool_choice:none, and strip the advisor tool (recursion guard).
	adv := script.requestBody(0)
	if !strings.Contains(adv, `"advisor-model"`) {
		t.Errorf("advisor sub-request did not use the configured model: %s", adv)
	}
	if !strings.Contains(adv, `"tool_choice":{"type":"none"}`) {
		t.Errorf("advisor sub-request must set tool_choice:none: %s", adv)
	}
	if strings.Contains(adv, "advisor_20260301") {
		t.Error("advisor sub-request must strip advisor tools (recursion guard)")
	}
	if !strings.Contains(adv, "senior strategic advisor") {
		t.Error("advisor sub-request is missing the advisor system prompt")
	}

	// The continuation must carry the tool_result and NOT be streaming.
	cont := script.requestBody(1)
	if !strings.Contains(cont, `"tool_result"`) {
		t.Errorf("continuation is missing the tool_result: %s", cont)
	}
	if !strings.Contains(cont, `"stream":false`) {
		t.Errorf("continuation must force non-streaming: %s", cont)
	}
	if !strings.Contains(cont, "Do A then B.") {
		t.Error("continuation tool_result must carry the advice text so the model can act on it")
	}
}

// --- multi-turn continuation ---

func TestRunAdvisorServerLoop_MultiTurnContinuation(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv1","content":[{"type":"text","text":"advice one"}],"stop_reason":"end_turn"}`},
		// continuation 1 → another advisor call
		{200, `{"id":"msg_2","content":[{"type":"tool_use","id":"toolu_adv2","name":"advisor","input":{}}],"stop_reason":"tool_use"}`},
		{200, `{"id":"adv2","content":[{"type":"text","text":"advice two"}],"stop_reason":"end_turn"}`},
		// continuation 2 → natural end
		{200, `{"id":"msg_3","content":[{"type":"text","text":"finished"}],"stop_reason":"end_turn"}`},
	}}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_adv1","name":"advisor","input":{}}],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	stop, types, _ := parseLoopResult(t, out)
	if stop != "end_turn" {
		t.Errorf("stop_reason: got %q, want end_turn", stop)
	}
	// Two advisor exchanges accumulated across turns, then the final text.
	want := []string{"server_tool_use", "advisor_tool_result", "server_tool_use", "advisor_tool_result", "text"}
	if len(types) != len(want) {
		t.Fatalf("block types: got %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("block %d: got %q, want %q", i, types[i], want[i])
		}
	}
	if !strings.Contains(string(out), "advice one") || !strings.Contains(string(out), "advice two") {
		t.Error("both advisor results must be accumulated in the final response")
	}
	if script.requestCount() != 4 {
		t.Errorf("expected 4 upstream calls, got %d", script.requestCount())
	}
}

// --- max_uses enforcement across turns (spec §6, §12.5) ---

func TestRunAdvisorServerLoop_MaxUsesEnforcedAcrossTurns(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv1","content":[{"type":"text","text":"the only advice"}],"stop_reason":"end_turn"}`},
		// continuation 1 → the model calls the advisor AGAIN (over the cap)
		{200, `{"id":"msg_2","content":[{"type":"tool_use","id":"toolu_adv2","name":"advisor","input":{}}],"stop_reason":"tool_use"}`},
		// continuation 2 (after the capped call) → natural end
		{200, `{"id":"msg_3","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`},
	}}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()
	lc.constraints = advisorToolSet{"advisor": {MaxUses: 1}}

	first := `{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_adv1","name":"advisor","input":{}}],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	_, types, blocks := parseLoopResult(t, out)
	want := []string{"server_tool_use", "advisor_tool_result", "server_tool_use", "advisor_tool_result", "text"}
	if len(types) != len(want) {
		t.Fatalf("block types: got %v, want %v", types, want)
	}
	// First exchange succeeded.
	if text := anthropic.ExtractAdvisorResultText(blocks[1]); text != "the only advice" {
		t.Errorf("first advisor result: got %q", text)
	}
	// Second exchange was capped pre-dispatch.
	if text := anthropic.ExtractAdvisorResultText(blocks[3]); text != "[Advisor unavailable: max_uses_exceeded]" {
		t.Errorf("second advisor result: got %q, want max_uses_exceeded", text)
	}
	// Only ONE advisor sub-inference should have been dispatched.
	advCalls := 0
	for i := 0; i < script.requestCount(); i++ {
		if strings.Contains(script.requestBody(i), "senior strategic advisor") {
			advCalls++
		}
	}
	if advCalls != 1 {
		t.Errorf("expected exactly 1 advisor sub-inference under max_uses=1, got %d", advCalls)
	}
}

func TestRunAdvisorServerLoop_MaxUsesZeroIsUncapped(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv1","content":[{"type":"text","text":"a1"}],"stop_reason":"end_turn"}`},
		{200, `{"id":"msg_2","content":[{"type":"tool_use","id":"t2","name":"advisor","input":{}}],"stop_reason":"tool_use"}`},
		{200, `{"id":"adv2","content":[{"type":"text","text":"a2"}],"stop_reason":"end_turn"}`},
		{200, `{"id":"msg_3","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`},
	}}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()
	lc.constraints = advisorToolSet{"advisor": {MaxUses: 0}}

	first := `{"id":"msg_1","content":[{"type":"tool_use","id":"t1","name":"advisor","input":{}}],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if strings.Contains(string(out), "max_uses_exceeded") {
		t.Error("max_uses=0 must be uncapped")
	}
}

// --- graceful degradation (spec §6, §12.3) ---

func TestRunAdvisorServerLoop_AdvisorFailureDegradesGracefully(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantCode string
	}{
		{"429 maps to too_many_requests", 429, "too_many_requests"},
		{"529 maps to overloaded", 529, "overloaded"},
		{"413 maps to prompt_too_long", 413, "prompt_too_long"},
		{"500 maps to unavailable", 500, "unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script := &upstreamScript{responses: []scriptedResponse{
				{tc.status, `{"error":"upstream said no"}`},
				// The continuation still happens — the model gets to react.
				{200, `{"id":"msg_2","content":[{"type":"text","text":"proceeding without advice"}],"stop_reason":"end_turn"}`},
			}}
			lc, srv := newLoopContext(t, script, basicExecutorReq)
			defer srv.Close()

			first := `{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}],"stop_reason":"tool_use"}`
			out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
			// The OUTER request must never fail because the advisor failed.
			if err != nil {
				t.Fatalf("loop returned an error — the outer request must never fail: %v", err)
			}
			_, types, blocks := parseLoopResult(t, out)
			if len(types) < 2 || types[0] != "server_tool_use" || types[1] != "advisor_tool_result" {
				t.Fatalf("expected a well-formed advisor block pair even on failure, got %v", types)
			}
			// The result block must be error-shaped with the right code.
			var res struct {
				Content struct {
					Type      string `json:"type"`
					ErrorCode string `json:"error_code"`
				} `json:"content"`
			}
			if err := json.Unmarshal(blocks[1], &res); err != nil {
				t.Fatalf("result block not decodable: %v", err)
			}
			if res.Content.Type != "advisor_tool_result_error" {
				t.Errorf("content type: got %q, want advisor_tool_result_error", res.Content.Type)
			}
			if res.Content.ErrorCode != tc.wantCode {
				t.Errorf("error_code: got %q, want %q", res.Content.ErrorCode, tc.wantCode)
			}
			// The continuation must have carried the placeholder text so the
			// executor model can react to the failure.
			cont := script.requestBody(1)
			wantText := fmt.Sprintf("[Advisor unavailable: %s]", tc.wantCode)
			if !strings.Contains(cont, wantText) {
				t.Errorf("continuation must carry %q so the model can react, got: %s", wantText, cont)
			}
		})
	}
}

func TestRunAdvisorServerLoop_ContinuationFailureReturnsPartial(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv1","content":[{"type":"text","text":"good advice"}],"stop_reason":"end_turn"}`},
		// The continuation forward fails.
		{500, `{"error":"gateway exploded"}`},
	}}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_1","content":[{"type":"text","text":"thinking"},{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop must degrade rather than error: %v", err)
	}
	stop, types, _ := parseLoopResult(t, out)
	if stop != "end_turn" {
		t.Errorf("stop_reason on degradation: got %q, want end_turn", stop)
	}
	// Everything accumulated so far must be returned, including the
	// successful advisor exchange.
	want := []string{"text", "server_tool_use", "advisor_tool_result"}
	if len(types) != len(want) {
		t.Fatalf("block types: got %v, want %v", types, want)
	}
	if !strings.Contains(string(out), "good advice") {
		t.Error("the successful advisor result must survive the continuation failure")
	}
}

func TestRunAdvisorServerLoop_UnparseableResponseDegrades(t *testing.T) {
	script := &upstreamScript{}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(`not json at all`), nil)
	if err != nil {
		t.Fatalf("unparseable response must degrade, not error: %v", err)
	}
	// A well-formed (if minimal) envelope must still come back.
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("degraded output is not valid JSON: %v (%s)", err, out)
	}
	if _, ok := resp["stop_reason"]; !ok {
		t.Error("degraded output must carry a stop_reason")
	}
}

// --- turn cap (spec §6, §12.6) ---

func TestRunAdvisorServerLoop_TurnCapReturnsPartial(t *testing.T) {
	// The model calls the advisor on EVERY turn — the loop must not spin.
	responses := []scriptedResponse{}
	for i := 0; i < maxAdvisorTurns*2+4; i++ {
		responses = append(responses,
			scriptedResponse{200, `{"id":"adv","content":[{"type":"text","text":"more advice"}],"stop_reason":"end_turn"}`},
			scriptedResponse{200, fmt.Sprintf(`{"id":"msg_%d","content":[{"type":"tool_use","id":"t%d","name":"advisor","input":{}}],"stop_reason":"tool_use"}`, i, i)},
		)
	}
	script := &upstreamScript{responses: responses}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_0","content":[{"type":"tool_use","id":"t0","name":"advisor","input":{}}],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("turn cap must degrade, not error: %v", err)
	}
	stop, types, _ := parseLoopResult(t, out)
	if stop != "end_turn" {
		t.Errorf("stop_reason at cap: got %q, want end_turn", stop)
	}
	// Exactly maxAdvisorTurns advisor exchanges (a pair each).
	pairs := 0
	for _, ty := range types {
		if ty == "server_tool_use" {
			pairs++
		}
	}
	if pairs != maxAdvisorTurns {
		t.Errorf("expected exactly %d advisor exchanges at the cap, got %d (types=%v)", maxAdvisorTurns, pairs, types)
	}
	// The loop must have stopped dispatching: maxAdvisorTurns advisor calls
	// plus maxAdvisorTurns continuations.
	if got := script.requestCount(); got != maxAdvisorTurns*2 {
		t.Errorf("expected %d upstream calls at the cap, got %d", maxAdvisorTurns*2, got)
	}
}

// --- stop_reason echo gating (spec §3.2) ---

func TestRunAdvisorServerLoop_StopReasonEchoedOnlyWhenClientSetMaxTokens(t *testing.T) {
	newScript := func() *upstreamScript {
		return &upstreamScript{responses: []scriptedResponse{
			{200, `{"id":"adv","content":[{"type":"text","text":"truncated advice"}],"stop_reason":"max_tokens"}`},
			{200, `{"id":"msg_2","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`},
		}}
	}
	first := `{"id":"msg_1","content":[{"type":"tool_use","id":"t","name":"advisor","input":{}}],"stop_reason":"tool_use"}`

	t.Run("client did NOT set max_tokens: stop_reason absent", func(t *testing.T) {
		script := newScript()
		lc, srv := newLoopContext(t, script, basicExecutorReq)
		defer srv.Close()
		lc.constraints = advisorToolSet{"advisor": {}}
		out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
		if err != nil {
			t.Fatalf("loop: %v", err)
		}
		if strings.Contains(string(out), `"stop_reason":"max_tokens"`) {
			t.Error("advisor result must not echo stop_reason when the client set no explicit cap")
		}
	})

	t.Run("client set max_tokens: stop_reason present", func(t *testing.T) {
		script := newScript()
		lc, srv := newLoopContext(t, script, basicExecutorReq)
		defer srv.Close()
		lc.constraints = advisorToolSet{"advisor": {MaxTokens: 2048, MaxTokensSet: true}}
		out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
		if err != nil {
			t.Fatalf("loop: %v", err)
		}
		_, _, blocks := parseLoopResult(t, out)
		var res struct {
			Content map[string]json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(blocks[1], &res); err != nil {
			t.Fatalf("result block: %v", err)
		}
		sr, present := res.Content["stop_reason"]
		if !present {
			t.Fatal("advisor result must echo stop_reason when the client set an explicit cap")
		}
		var s string
		_ = json.Unmarshal(sr, &s)
		if s != "max_tokens" {
			t.Errorf("echoed stop_reason: got %q, want max_tokens", s)
		}
	})
}

// --- thinking stripping in the advisor leg (spec §7.1, §12.4) ---

func TestRunAdvisorServerLoop_StripsThinkingFromAdvisorSubRequest(t *testing.T) {
	execReqWithThinking := `{"model":"exec-model","messages":[
		{"role":"user","content":[{"type":"text","text":"work"}]},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"secret reasoning","signature":"sig123"},
			{"type":"text","text":"visible"}
		]},
		{"role":"assistant","content":[{"type":"redacted_thinking","data":"blob"}]}
	],"tools":[{"type":"advisor_20260301","name":"advisor"}]}`

	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv","content":[{"type":"text","text":"advice"}],"stop_reason":"end_turn"}`},
		{200, `{"id":"msg_2","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`},
	}}
	lc, srv := newLoopContext(t, script, execReqWithThinking)
	defer srv.Close()

	// The in-progress turn also carries thinking + a dangling tool_use.
	first := `{"id":"msg_1","content":[
		{"type":"thinking","thinking":"in-flight reasoning","signature":"sig456"},
		{"type":"text","text":"about to consult"},
		{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}
	],"stop_reason":"tool_use"}`
	if _, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil); err != nil {
		t.Fatalf("loop: %v", err)
	}

	adv := script.requestBody(0)
	for _, forbidden := range []string{"thinking", "secret reasoning", "sig123", "redacted_thinking", "blob", "in-flight reasoning", "sig456"} {
		if strings.Contains(adv, forbidden) {
			t.Errorf("advisor sub-request must not contain %q (thinking signatures 400 upstream): %s", forbidden, adv)
		}
	}
	// Visible text must survive.
	if !strings.Contains(adv, "visible") {
		t.Error("non-thinking assistant text must survive the strip")
	}
	// The dangling tool_use must be stripped from the in-progress turn —
	// an unpaired tool_use 400s.
	if strings.Contains(adv, "toolu_adv") {
		t.Error("dangling tool_use must be stripped from the in-progress turn")
	}
	// The elicitation turn must close the prompt.
	if !strings.Contains(adv, "(Advisor) Provide concise strategic guidance") {
		t.Error("advisor sub-request is missing the elicitation turn")
	}

	// The continuation must also strip thinking.
	cont := script.requestBody(1)
	if strings.Contains(cont, "in-flight reasoning") || strings.Contains(cont, "sig456") {
		t.Errorf("continuation must strip thinking blocks: %s", cont)
	}
}

func TestRunAdvisorServerLoop_ForwardsExecutorSystemPromptAsContext(t *testing.T) {
	execReq := `{"model":"exec-model","system":"You are a build agent.","messages":[{"role":"user","content":[{"type":"text","text":"work"}]}],"tools":[{"type":"advisor_20260301","name":"advisor"}]}`
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"adv","content":[{"type":"text","text":"advice"}],"stop_reason":"end_turn"}`},
		{200, `{"id":"msg_2","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`},
	}}
	lc, srv := newLoopContext(t, script, execReq)
	defer srv.Close()

	first := `{"id":"msg_1","content":[{"type":"tool_use","id":"t","name":"advisor","input":{}}],"stop_reason":"tool_use"}`
	if _, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil); err != nil {
		t.Fatalf("loop: %v", err)
	}
	// Decode rather than substring-match the raw bytes: Go's JSON encoder
	// HTML-escapes < and > (\u003c / \u003e), which is valid JSON and
	// decodes back to the literal tags.
	var advReq struct {
		System string `json:"system"`
	}
	if err := json.Unmarshal([]byte(script.requestBody(0)), &advReq); err != nil {
		t.Fatalf("advisor sub-request not decodable: %v", err)
	}
	if !strings.Contains(advReq.System, "<agent_system_context>") ||
		!strings.Contains(advReq.System, "</agent_system_context>") {
		t.Errorf("executor system prompt must be wrapped in <agent_system_context>: %q", advReq.System)
	}
	if !strings.Contains(advReq.System, "You are a build agent.") {
		t.Error("executor system prompt content must be forwarded verbatim")
	}
	if !strings.HasPrefix(advReq.System, advisorSystemPrompt) {
		t.Error("the proxy-authored advisor system prompt must come first")
	}
}

// --- mixed advisor + client tool in the same turn ---

func TestRunAdvisorServerLoop_ClientToolWinsOverAdvisorInSameTurn(t *testing.T) {
	// When a turn contains BOTH an advisor call and a client tool call, the
	// loop stops and hands control to the client — it must not dispatch the
	// advisor, because the client tool's result is needed first.
	script := &upstreamScript{}
	lc, srv := newLoopContext(t, script, basicExecutorReq)
	defer srv.Close()

	first := `{"id":"msg_1","content":[
		{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}},
		{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"ls"}}
	],"stop_reason":"tool_use"}`
	out, err := runAdvisorServerLoop(context.Background(), lc, []byte(first), nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	stop, _, _ := parseLoopResult(t, out)
	if stop != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", stop)
	}
	if script.requestCount() != 0 {
		t.Errorf("no advisor dispatch should happen when a client tool is present, got %d calls", script.requestCount())
	}
}
