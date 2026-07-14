package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/tokencache"
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
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("test-token-123"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotAuth != "Bearer test-token-123" {
		t.Errorf("got Authorization %q, want %q", gotAuth, "Bearer test-token-123")
	}
}

// TestProxy_InjectsCustomHeaders verifies the Databricks coding-agent header.
func TestProxy_InjectsCustomHeaders(t *testing.T) {
	var gotHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-databricks-use-coding-agent-mode")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotHeader != "true" {
		t.Errorf("got x-databricks-use-coding-agent-mode %q, want %q", gotHeader, "true")
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
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
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
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !otelCalled {
		t.Error("otel upstream was not called")
	}
}

// TestProxy_PathAlgebra_Inference verifies that the upstream path is prepended.
// /v1/messages + upstream base /anthropic → /anthropic/v1/messages
func TestProxy_PathAlgebra_Inference(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// Append a base path to the upstream URL.
	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL + "/anthropic",
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	want := "/anthropic/v1/messages"
	if gotPath != want {
		t.Errorf("got path %q, want %q", gotPath, want)
	}
}

// TestProxy_PathAlgebra_OTEL verifies that /otel prefix is stripped and the
// upstream base path is prepended.
// /otel/v1/metrics + upstream base /api/2.0/otel → /api/2.0/otel/v1/metrics
func TestProxy_PathAlgebra_OTEL(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL + "/api/2.0/otel",
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	want := "/api/2.0/otel/v1/metrics"
	if gotPath != want {
		t.Errorf("got path %q, want %q", gotPath, want)
	}
}

// TestProxy_PreservesRequestBody verifies that POST bodies are forwarded intact.
func TestProxy_PreservesRequestBody(t *testing.T) {
	body := `{"model":"claude-opus-4-7","messages":[]}`
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
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotBody != body {
		t.Errorf("got body %q, want %q", gotBody, body)
	}
}

// TestProxy_PanicRecovery verifies that a panic in a Director returns 502 and
// does not crash the server.
func TestProxy_PanicRecovery(t *testing.T) {
	// Use a handler that panics to simulate a broken Director.
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated director panic")
	})

	recovered := recoveryHandler(panicHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	// Must not panic.
	recovered.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("got status %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if !strings.Contains(rec.Body.String(), "Internal proxy error") {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
}

// TestProxy_SSEStreaming verifies that chunked/streamed responses are not
// buffered by the proxy (FlushInterval: -1 ensures immediate flushing).
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
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	// Use a real listener so we can test actual streaming behaviour.
	l, err := StartProxy(cfg, handler)
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer l.Close()

	resp, err := http.Get("http://" + l.Addr().String() + "/v1/messages")
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

// TestProxy_OTELTableName_Metrics verifies that /otel/v1/metrics gets the metrics table header.
func TestProxy_OTELTableName_Metrics(t *testing.T) {
	var gotTable string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTable = r.Header.Get("X-Databricks-UC-Table-Name")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &ProxyConfig{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.telemetry.claude_otel_metrics",
		UCLogsTable:       "main.telemetry.claude_otel_logs",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotTable != "main.telemetry.claude_otel_metrics" {
		t.Errorf("got table %q, want %q", gotTable, "main.telemetry.claude_otel_metrics")
	}
}

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
		UCMetricsTable:    "main.telemetry.claude_otel_metrics",
		UCLogsTable:       "main.telemetry.claude_otel_logs",
		TokenProvider:     warmToken("tok"),
	}
	handler := NewProxyServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/logs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotTable != "main.telemetry.claude_otel_logs" {
		t.Errorf("got table %q, want %q", gotTable, "main.telemetry.claude_otel_logs")
	}
}
