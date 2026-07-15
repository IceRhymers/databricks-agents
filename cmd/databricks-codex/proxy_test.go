package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core/tokencache"
)

// warmToken returns a *TokenProvider whose cache is pre-loaded with the given
// token so that no subprocess invocation ever occurs during tests.
func warmToken(token string) *tokencache.TokenProvider {
	tp := tokencache.NewTokenProvider(nil) // fetcher unused — cache is pre-warmed
	tp.SetCache(token, time.Now().Add(24*time.Hour))
	return tp
}

// TestProxy_InjectsAuthHeader verifies that the Authorization header is set.
func TestProxy_InjectsAuthHeader(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("test-token-123"),
	}
	handler, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotAuth != "Bearer test-token-123" {
		t.Errorf("got Authorization %q, want %q", gotAuth, "Bearer test-token-123")
	}
}

// TestProxy_RoutesDefaultToInference verifies that non-/otel requests reach
// the inference upstream.
func TestProxy_RoutesDefaultToInference(t *testing.T) {
	inferenceCalled := false
	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inferenceCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer inference.Close()

	otel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("otel upstream called unexpectedly")
		w.WriteHeader(http.StatusOK)
	}))
	defer otel.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: inference.URL,
		OTELUpstream:      otel.URL,
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !inferenceCalled {
		t.Error("inference upstream was not called")
	}
}

// TestProxy_RoutesOTELPath verifies that /otel/* requests reach the OTEL upstream.
func TestProxy_RoutesOTELPath(t *testing.T) {
	otelCalled := false
	otel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otelCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer otel.Close()

	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inference upstream called unexpectedly for /otel/ request")
		w.WriteHeader(http.StatusOK)
	}))
	defer inference.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: inference.URL,
		OTELUpstream:      otel.URL,
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !otelCalled {
		t.Error("otel upstream was not called")
	}
}

// TestProxy_OTELTableName_Metrics verifies that /otel/v1/metrics gets the metrics table header.

// TestProxy_OTELTableName_Logs verifies that /otel/v1/logs gets the logs table header.
func TestProxy_OTELTableName_Logs(t *testing.T) {
	var gotTable string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTable = r.Header.Get("X-Databricks-UC-Table-Name")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.telemetry.codex_otel_logs",
		TokenProvider:     warmToken("tok"),
	}
	handler, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/logs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotTable != "main.telemetry.codex_otel_logs" {
		t.Errorf("got table %q, want %q", gotTable, "main.telemetry.codex_otel_logs")
	}
}

// TestProxy_PreservesRequestBody verifies that POST bodies are forwarded intact.
func TestProxy_PreservesRequestBody(t *testing.T) {
	body := `{"model":"o3-mini","input":"hello"}`
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotBody != body {
		t.Errorf("got body %q, want %q", gotBody, body)
	}
}

// TestProxy_SSEStreaming verifies that chunked/streamed responses are not buffered.
func TestProxy_SSEStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream ResponseWriter does not implement Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "data: chunk\n\n")
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
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
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	want := "data: chunk\n\n"
	if !strings.Contains(string(respBody), want) {
		t.Errorf("response body %q does not contain %q", string(respBody), want)
	}
}
