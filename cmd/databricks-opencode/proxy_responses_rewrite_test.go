package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxy_ResponsesRewriter_EndToEnd verifies that the wrapper's proxy, with
// ResponsesRewrite enabled by default, rewrites mismatched item_ids in
// Responses-API SSE streams emitted by an upstream that mimics the Databricks
// AI Gateway's re-encoding behavior.
//
// Regression coverage for IceRhymers/databricks-opencode#1 — the AI Gateway
// emits `response.output_item.added` with one `item.id` but downstream
// `response.output_text.*` / `response.content_part.*` events carry a
// different `item_id`. @ai-sdk/openai's parser then fails with
// `text part <id> not found`. The rewriter caches the canonical id from
// `output_item.added` and rewrites any mismatched `item_id` on later events
// so opencode sees a single consistent id per output_index.
func TestProxy_ResponsesRewriter_EndToEnd(t *testing.T) {
	// Upstream simulates the AI Gateway: emits output_item.added with the
	// canonical id, then content_part.added / output_text.delta /
	// output_text.done carrying a *different* id (the mismatch bug).
	frames := []string{
		`event: response.output_item.added` + "\n" +
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item_canonical","type":"message"}}` + "\n\n",
		`event: response.content_part.added` + "\n" +
			`data: {"type":"response.content_part.added","output_index":0,"item_id":"item_WRONG","part":{"type":"output_text","text":""}}` + "\n\n",
		`event: response.output_text.delta` + "\n" +
			`data: {"type":"response.output_text.delta","output_index":0,"item_id":"item_WRONG","delta":"hello"}` + "\n\n",
		`event: response.output_text.done` + "\n" +
			`data: {"type":"response.output_text.done","output_index":0,"item_id":"item_WRONG","text":"hello"}` + "\n\n",
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream ResponseWriter does not implement Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		TokenProvider:     warmToken("tok"),
	}
	handler, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}

	l, err := StartProxy(handler, "", "")
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer l.Close()

	resp, err := http.Get("http://" + l.Addr().String() + "/v1/responses")
	if err != nil {
		t.Fatalf("GET /v1/responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	out := string(body)

	if strings.Contains(out, "item_WRONG") {
		t.Errorf("mismatched id should have been rewritten away; output still contains item_WRONG:\n%s", out)
	}
	// All downstream events should now carry the canonical id from the
	// output_item.added frame.
	if !strings.Contains(out, "item_canonical") {
		t.Errorf("canonical id missing from rewritten output:\n%s", out)
	}
	// Sanity: the delta payload should still be present.
	if !strings.Contains(out, `"delta":"hello"`) {
		t.Errorf("delta payload missing from rewritten output:\n%s", out)
	}
}

// TestProxy_ResponsesRewriter_NonResponsesPath verifies that requests to
// non-Responses paths (e.g. /v1/chat/completions) are passed through
// byte-identically — the rewriter must not interfere with other endpoints.
func TestProxy_ResponsesRewriter_NonResponsesPath(t *testing.T) {
	payload := `data: {"item_id":"item_WRONG"}` + "\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream ResponseWriter does not implement Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, payload)
		flusher.Flush()
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		TokenProvider:     warmToken("tok"),
	}
	handler, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}

	l, err := StartProxy(handler, "", "")
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer l.Close()

	resp, err := http.Get("http://" + l.Addr().String() + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("GET /v1/chat/completions: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "item_WRONG") {
		t.Errorf("non-/responses path should pass through unchanged; got:\n%s", string(body))
	}
}
