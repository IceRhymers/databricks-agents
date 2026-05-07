package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-claude/pkg/proxy/anthropic"
	"github.com/IceRhymers/databricks-claude/pkg/websearch"
)

// fakeBackend is an in-memory websearch.Backend for tests.
type fakeBackend struct {
	results []websearch.Result
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Search(_ context.Context, _ string, _ int) ([]websearch.Result, error) {
	return f.results, nil
}

// TestProxy_WebSearchDisabled_ByteIdenticalForward is the load-bearing
// regression test: when WebSearch.Enabled is false, the request body
// forwarded upstream is byte-identical to what the client sent.
func TestProxy_WebSearchDisabled_ByteIdenticalForward(t *testing.T) {
	bodyBytes := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}],"max_tokens":1024,"stream":true}`)
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !bytes.Equal(gotBody, bodyBytes) {
		t.Errorf("body mutated when --with-websearch=false:\n in=%s\nout=%s", bodyBytes, gotBody)
	}
}

// TestProxy_WebSearchEnabled_RewritesTools verifies that a request with a
// web_search_20250305 tool entry is rewritten to an annotated client tool.
func TestProxy_WebSearchEnabled_RewritesTools(t *testing.T) {
	bodyBytes := []byte(`{"model":"claude-opus-4-7","messages":[],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5},{"type":"web_fetch_20250910","name":"web_fetch"}],"max_tokens":1024}`)
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		WebSearch: WebSearchSettings{
			Enabled:     true,
			Backend:     &fakeBackend{},
			FetchBudget: 1024,
		},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("decoded mutated body fail: %v body=%s", err, gotBody)
	}
	if len(got.Tools) != 2 {
		t.Fatalf("tools len=%d, want 2", len(got.Tools))
	}
	for _, tool := range got.Tools {
		desc, _ := tool["description"].(string)
		if !strings.HasPrefix(desc, anthropic.AnnotationPrefix) {
			t.Errorf("tool not annotated: %v", tool)
		}
		if t0, ok := tool["type"].(string); ok && (strings.HasPrefix(t0, "web_search_") || strings.HasPrefix(t0, "web_fetch_")) {
			t.Errorf("server tool type still present: %q", t0)
		}
	}
}

// TestProxy_WebSearchEnabled_SubstitutesToolResult verifies that a
// tool_result targeting a previous annotated tool_use is replaced with the
// proxy's locally-fulfilled output before forwarding upstream.
func TestProxy_WebSearchEnabled_SubstitutesToolResult(t *testing.T) {
	convo := map[string]any{
		"model": "claude-opus-4-7",
		"messages": []any{
			map[string]any{"role": "user", "content": "search the web for golang"},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_abc",
						"name":  "web_search",
						"input": map[string]any{"query": "golang"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_abc",
						"is_error":    true,
						"content":     "Tool 'web_search' not found",
					},
				},
			},
		},
		"max_tokens": 1024,
	}
	bodyBytes, _ := json.Marshal(convo)
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		WebSearch: WebSearchSettings{
			Enabled: true,
			Backend: &fakeBackend{results: []websearch.Result{
				{Title: "The Go Programming Language", URL: "https://go.dev", Snippet: "An open source programming language"},
			}},
			FetchBudget: 1024,
		},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !bytes.Contains(gotBody, []byte("go.dev")) {
		t.Errorf("expected upstream body to contain 'go.dev' from substituted tool_result, got: %s", gotBody)
	}
	if bytes.Contains(gotBody, []byte("Tool 'web_search' not found")) {
		t.Errorf("expected original is_error tool_result content removed; still present in: %s", gotBody)
	}
	// And is_error should not be set on the substituted block.
	var parsed map[string]any
	json.Unmarshal(gotBody, &parsed)
	msgs := parsed["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	content := last["content"].([]any)
	tr := content[0].(map[string]any)
	if v, ok := tr["is_error"].(bool); ok && v {
		t.Errorf("substituted tool_result should not be is_error: %v", tr)
	}
}
