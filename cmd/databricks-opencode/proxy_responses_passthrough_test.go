package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxy_ResponsesSSE_ByteIdenticalPassthrough verifies that a /responses
// SSE stream flows through the opencode proxy unmodified — status,
// Content-Type, and body — now that the client-side item_id rewriter has
// been removed (#238; the Databricks AI Gateway bug it worked around is
// fixed server-side). This is the regression guard for the inferenceHandler
// passthrough fast path, specifically the surviving header-copy loop at
// internal/core/proxy/websearch_handler.go:154-161. It replaces the #191
// rewrite coverage.
func TestProxy_ResponsesSSE_ByteIdenticalPassthrough(t *testing.T) {
	// Upstream mimics the AI Gateway's old re-encoding behavior: emits
	// output_item.added with one id, then content_part.added /
	// output_text.delta / output_text.done carrying a *different* id (the
	// former mismatch bug). The stream must now pass through unchanged.
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

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	out := string(body)

	want := strings.Join(frames, "")
	if out != want {
		t.Errorf("want byte-identical passthrough...\n got: %q\nwant: %q", out, want)
	}
}

// TestProxy_NonResponsesPath_Passthrough verifies that requests to
// non-Responses paths (e.g. /v1/chat/completions) are passed through
// byte-identically — the proxy must not interfere with other endpoints.
func TestProxy_NonResponsesPath_Passthrough(t *testing.T) {
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
