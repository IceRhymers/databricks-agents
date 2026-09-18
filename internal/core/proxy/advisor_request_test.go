package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// --- tryRewriteAdvisorRequest ---

func TestTryRewriteAdvisorRequest_DisabledWithNoAdvisorToolIsNoOp(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"Bash","input_schema":{}}]}`
	out, prep, ok := tryRewriteAdvisorRequest([]byte(body), AdvisorSettings{Enabled: false}, logger{})
	if ok {
		t.Error("expected ok=false (nothing to rewrite)")
	}
	if string(out) != body {
		t.Errorf("body must be untouched, got %s", out)
	}
	if prep.constraints.any() {
		t.Error("no constraints expected")
	}
}

func TestTryRewriteAdvisorRequest_EnabledInjectsAndConverts(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`
	out, prep, ok := tryRewriteAdvisorRequest([]byte(body), AdvisorSettings{Enabled: true}, logger{})
	if !ok {
		t.Fatal("expected ok=true (injection happened)")
	}
	if !prep.constraints.any() {
		t.Error("expected constraints for the injected advisor tool")
	}
	// The forwarded body must carry a plain function tool, not a server tool.
	if strings.Contains(string(out), "advisor_20260301") {
		t.Errorf("the upstream must not see the advisor server-tool type: %s", out)
	}
	var fwd struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(out, &fwd); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	if len(fwd.Tools) != 2 {
		t.Fatalf("expected 2 forwarded tools, got %d", len(fwd.Tools))
	}
	if anthropic.ToolName(fwd.Tools[1]) != "advisor" {
		t.Errorf("forwarded advisor tool name: got %q", anthropic.ToolName(fwd.Tools[1]))
	}
	// prep.request must retain the ORIGINAL declarations so the continuation
	// re-sends what the client declared.
	foundOriginal := false
	for _, tl := range prep.request.Tools {
		if anthropic.IsAdvisorTool(tl) {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Error("prep.request must retain the original advisor declaration")
	}
}

func TestTryRewriteAdvisorRequest_EnabledButNoToolsIsNoOp(t *testing.T) {
	// The advisor is scoped to agentic workloads — a request with no tools
	// must be left alone even when the feature is on.
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	out, prep, ok := tryRewriteAdvisorRequest([]byte(body), AdvisorSettings{Enabled: true}, logger{})
	if ok {
		t.Error("expected ok=false for a toolless request")
	}
	if string(out) != body {
		t.Errorf("body must be untouched, got %s", out)
	}
	if prep.constraints.any() {
		t.Error("no constraints expected for a toolless request")
	}
}

func TestTryRewriteAdvisorRequest_DisabledStripsDeclaredAdvisorTool(t *testing.T) {
	body := `{"model":"m","messages":[],"tools":[{"name":"Bash","input_schema":{}},{"type":"advisor_20260301","name":"advisor"}]}`
	out, prep, ok := tryRewriteAdvisorRequest([]byte(body), AdvisorSettings{Enabled: false}, logger{})
	if !ok {
		t.Fatal("expected ok=true (the advisor tool was stripped)")
	}
	if strings.Contains(string(out), "advisor") {
		t.Errorf("the advisor tool must be stripped when disabled: %s", out)
	}
	if prep.constraints.any() {
		t.Error("no constraints when disabled — the proxy will not intercept")
	}
}

func TestTryRewriteAdvisorRequest_ReplayExpansionRunsEvenWhenDisabled(t *testing.T) {
	// The upstream 400s on server_tool_use / advisor_tool_result regardless of
	// whether the feature is currently enabled, so expansion is unconditional.
	body := `{"model":"m","tools":[{"name":"Bash","input_schema":{}}],"messages":[
		{"role":"assistant","content":[
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_result","text":"prior advice"}}
		]}
	]}`
	out, _, ok := tryRewriteAdvisorRequest([]byte(body), AdvisorSettings{Enabled: false}, logger{})
	if !ok {
		t.Fatal("expected ok=true (replay expansion applied)")
	}
	if strings.Contains(string(out), "server_tool_use") || strings.Contains(string(out), "advisor_tool_result") {
		t.Errorf("replayed advisor block types must never reach the upstream: %s", out)
	}
	if !strings.Contains(string(out), "prior advice") {
		t.Error("the advice text must survive as a tool_result so the model keeps its context")
	}
}

func TestTryRewriteAdvisorRequest_MalformedBodyIsNoOp(t *testing.T) {
	body := `{not json`
	out, _, ok := tryRewriteAdvisorRequest([]byte(body), AdvisorSettings{Enabled: true}, logger{})
	if ok {
		t.Error("expected ok=false for a malformed body")
	}
	if string(out) != body {
		t.Error("a malformed body must be forwarded verbatim")
	}
}

// --- requestWantsStream / forceNonStreaming ---

func TestRequestWantsStream(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{`{"stream":true}`, true},
		{`{"stream":false}`, false},
		{`{}`, false},
		{`{"stream":"yes"}`, false}, // wrong type → not streaming
	}
	for _, tc := range tests {
		var req anthropic.Request
		if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
			t.Fatalf("parse %s: %v", tc.body, err)
		}
		if got := requestWantsStream(&req); got != tc.want {
			t.Errorf("requestWantsStream(%s): got %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestForceNonStreaming(t *testing.T) {
	out, ok := forceNonStreaming([]byte(`{"model":"m","stream":true,"max_tokens":100}`))
	if !ok {
		t.Fatal("expected ok=true")
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var stream bool
	_ = json.Unmarshal(got["stream"], &stream)
	if stream {
		t.Error("stream must be forced to false")
	}
	// Other fields must survive.
	if _, ok := got["max_tokens"]; !ok {
		t.Error("max_tokens must be preserved")
	}

	if _, ok := forceNonStreaming([]byte(`{bad`)); ok {
		t.Error("malformed body should return ok=false")
	}
}

// --- end-to-end through inferenceHandler (the composition seam) ---

// newAdvisorProxy wires a real inferenceHandler against a scripted upstream
// with the advisor feature enabled.
func newAdvisorProxy(t *testing.T, script *upstreamScript) (*httptest.Server, *httptest.Server) {
	t.Helper()
	upstreamSrv := httptest.NewServer(script.handler())
	cfg := &Config{
		InferenceUpstream: upstreamSrv.URL,
		OTELUpstream:      upstreamSrv.URL,
		TokenSource:       staticToken{"tok"},
		ToolName:          "test",
		Advisor:           AdvisorSettings{Enabled: true, DefaultModel: "advisor-model"},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	return proxySrv, upstreamSrv
}

func TestInferenceHandler_AdvisorEndToEndNonStreaming(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		// 1: the executor turn — calls the advisor.
		{200, `{"id":"msg_1","type":"message","role":"assistant","model":"exec-model","content":[{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}],"stop_reason":"tool_use"}`},
		// 2: the advisor sub-inference.
		{200, `{"id":"adv","content":[{"type":"text","text":"Ship it."}],"stop_reason":"end_turn"}`},
		// 3: the continuation.
		{200, `{"id":"msg_2","content":[{"type":"text","text":"Shipping."}],"stop_reason":"end_turn"}`},
	}}
	proxySrv, upstreamSrv := newAdvisorProxy(t, script)
	defer proxySrv.Close()
	defer upstreamSrv.Close()

	reqBody := `{"model":"exec-model","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"ship the feature"}]}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`
	resp, err := http.Post(proxySrv.URL+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var out anthropic.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The client must see the Anthropic-native server-tool block pair.
	var types []string
	for _, b := range out.Content {
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(b, &p)
		types = append(types, p.Type)
	}
	want := []string{"server_tool_use", "advisor_tool_result", "text"}
	if len(types) != len(want) {
		t.Fatalf("content block types: got %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("block %d: got %q, want %q", i, types[i], want[i])
		}
	}
	if out.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %q, want end_turn (a real reason, not pause_turn)", out.StopReason)
	}
	if text := anthropic.ExtractAdvisorResultText(out.Content[1]); text != "Ship it." {
		t.Errorf("advisor result text: got %q", text)
	}

	// The FIRST upstream request must carry the auto-injected advisor tool in
	// plain function-tool form.
	first := script.requestBody(0)
	if strings.Contains(first, "advisor_20260301") {
		t.Errorf("the upstream must not see the advisor server-tool type: %s", first)
	}
	if !strings.Contains(first, `"advisor"`) {
		t.Errorf("the upstream must see the converted advisor function tool: %s", first)
	}
	if script.requestCount() != 3 {
		t.Errorf("expected 3 upstream calls (executor + advisor + continuation), got %d", script.requestCount())
	}
}

func TestInferenceHandler_AdvisorDisabledIsPassthrough(t *testing.T) {
	script := &upstreamScript{responses: []scriptedResponse{
		{200, `{"id":"msg_1","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`},
	}}
	upstreamSrv := httptest.NewServer(script.handler())
	defer upstreamSrv.Close()
	cfg := &Config{
		InferenceUpstream: upstreamSrv.URL,
		OTELUpstream:      upstreamSrv.URL,
		TokenSource:       staticToken{"tok"},
		ToolName:          "test",
		Advisor:           AdvisorSettings{Enabled: false},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()

	reqBody := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`
	resp, err := http.Post(proxySrv.URL+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	// No advisor tool should be injected.
	if strings.Contains(script.requestBody(0), "advisor") {
		t.Errorf("no advisor tool may be injected when disabled: %s", script.requestBody(0))
	}
	if script.requestCount() != 1 {
		t.Errorf("expected exactly 1 upstream call (pure passthrough), got %d", script.requestCount())
	}
}

func TestInferenceHandler_AdvisorEndToEndStreaming(t *testing.T) {
	executorStream := sse("message_start", `{"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`) +
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_adv","name":"advisor","input":{}}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)
	continuationStream := sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"acting on advice"}}`) +
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
		sse("message_stop", `{"type":"message_stop"}`)

	// The SSE upstream needs the right Content-Type for the streaming branch.
	sseScript := &sseUpstreamScript{
		responses: []string{executorStream, continuationStream},
		jsonResponses: []string{
			`{"id":"adv","content":[{"type":"text","text":"Advice text."}],"stop_reason":"end_turn"}`,
		},
	}
	upstreamSrv := httptest.NewServer(sseScript.handler())
	defer upstreamSrv.Close()

	cfg := &Config{
		InferenceUpstream: upstreamSrv.URL,
		OTELUpstream:      upstreamSrv.URL,
		TokenSource:       staticToken{"tok"},
		ToolName:          "test",
		Advisor:           AdvisorSettings{Enabled: true, DefaultModel: "advisor-model"},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()

	reqBody := `{"model":"exec-model","max_tokens":100,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"go"}]}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`
	resp, err := http.Post(proxySrv.URL+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes := make([]byte, 0, 4096)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		bodyBytes = append(bodyBytes, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	out := string(bodyBytes)

	// The client must receive synthesized advisor blocks, never a raw
	// advisor tool_use.
	if strings.Contains(out, `"type":"tool_use"`) {
		t.Errorf("the raw advisor tool_use leaked into the client stream:\n%s", out)
	}
	if !strings.Contains(out, "server_tool_use") {
		t.Errorf("expected a synthesized server_tool_use block:\n%s", out)
	}
	if !strings.Contains(out, "advisor_tool_result") {
		t.Errorf("expected a synthesized advisor_tool_result block:\n%s", out)
	}
	if !strings.Contains(out, "Advice text.") {
		t.Errorf("the advisor guidance must reach the client:\n%s", out)
	}
	// The continuation's content must be on the SAME stream.
	if !strings.Contains(out, "acting on advice") {
		t.Errorf("the continuation segment must stream on the same connection:\n%s", out)
	}
	// Exactly one terminal pair.
	if got := strings.Count(out, "event: message_stop"); got != 1 {
		t.Errorf("expected exactly 1 message_stop, got %d:\n%s", got, out)
	}
}

// sseUpstreamScript serves SSE for streaming requests and JSON for the
// (non-streaming) advisor sub-inference, dispatching on the request's stream
// flag — mirrors how a real upstream behaves.
type sseUpstreamScript struct {
	responses     []string // SSE bodies, in order
	jsonResponses []string // JSON bodies, in order
	sseIdx        int
	jsonIdx       int
}

func (s *sseUpstreamScript) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		wantsStream := strings.Contains(string(body), `"stream":true`)

		if wantsStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			if s.sseIdx < len(s.responses) {
				_, _ = w.Write([]byte(s.responses[s.sseIdx]))
				s.sseIdx++
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if s.jsonIdx < len(s.jsonResponses) {
			_, _ = w.Write([]byte(s.jsonResponses[s.jsonIdx]))
			s.jsonIdx++
			return
		}
		_, _ = w.Write([]byte(`{"id":"x","content":[{"type":"text","text":"fallback"}],"stop_reason":"end_turn"}`))
	}
}
