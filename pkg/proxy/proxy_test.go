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
	"time"
)

// staticTokenSource implements TokenSource for testing.
type staticTokenSource struct {
	token string
}

func (s *staticTokenSource) Token(_ context.Context) (string, error) {
	return s.token, nil
}

func warmToken(token string) TokenSource {
	return &staticTokenSource{token: token}
}

// TestProxy_InjectsAuthHeader verifies that the Authorization header is set.
func TestProxy_InjectsAuthHeader(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("test-token-123"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

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

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

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

	cfg := &Config{
		InferenceUpstream: inference.URL,
		OTELUpstream:      otel.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

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

	cfg := &Config{
		InferenceUpstream: inference.URL,
		OTELUpstream:      otel.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !otelCalled {
		t.Error("otel upstream was not called")
	}
}

// TestProxy_PathAlgebra_Inference verifies that the upstream path is prepended.
func TestProxy_PathAlgebra_Inference(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL + "/anthropic",
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

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
func TestProxy_PathAlgebra_OTEL(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL + "/api/2.0/otel",
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

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

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

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
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated director panic")
	})

	recovered := RecoveryHandler(panicHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

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

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	l, err := Start(handler, "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
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

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.telemetry.claude_otel_metrics",
		UCLogsTable:       "main.telemetry.claude_otel_logs",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

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

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.telemetry.claude_otel_metrics",
		UCLogsTable:       "main.telemetry.claude_otel_logs",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/logs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotTable != "main.telemetry.claude_otel_logs" {
		t.Errorf("got table %q, want %q", gotTable, "main.telemetry.claude_otel_logs")
	}
}

// TestProxy_OTELTableName_MetricsEmpty verifies that when UCMetricsTable is
// empty (e.g. databricks-codex which has no native metrics), the
// X-Databricks-UC-Table-Name header is omitted for metrics requests.
func TestProxy_OTELTableName_MetricsEmpty(t *testing.T) {
	var gotTable string
	var tableHeaderPresent bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, tableHeaderPresent = r.Header["X-Databricks-Uc-Table-Name"]
		gotTable = r.Header.Get("X-Databricks-UC-Table-Name")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "", // empty — caller does not emit metrics
		UCLogsTable:       "main.telemetry.codex_otel_logs",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if tableHeaderPresent || gotTable != "" {
		t.Errorf("expected X-Databricks-UC-Table-Name to be absent when UCMetricsTable is empty, got %q", gotTable)
	}
}

// TestProxy_OTELTableName_Traces verifies that /otel/v1/traces gets the traces table header.
func TestProxy_OTELTableName_Traces(t *testing.T) {
	var gotTable string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTable = r.Header.Get("X-Databricks-UC-Table-Name")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.telemetry.claude_otel_metrics",
		UCLogsTable:       "main.telemetry.claude_otel_logs",
		UCTracesTable:     "main.telemetry.claude_otel_traces",
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/traces", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotTable != "main.telemetry.claude_otel_traces" {
		t.Errorf("got table %q, want %q", gotTable, "main.telemetry.claude_otel_traces")
	}
}

// TestProxy_OTELTableName_TracesEmpty verifies that when UCTracesTable is
// empty, the X-Databricks-UC-Table-Name header is omitted for /v1/traces requests.
func TestProxy_OTELTableName_TracesEmpty(t *testing.T) {
	var gotTable string
	var tableHeaderPresent bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, tableHeaderPresent = r.Header["X-Databricks-Uc-Table-Name"]
		gotTable = r.Header.Get("X-Databricks-UC-Table-Name")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.telemetry.claude_otel_metrics",
		UCLogsTable:       "main.telemetry.claude_otel_logs",
		UCTracesTable:     "", // empty — caller does not emit traces
		TokenSource:       warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/otel/v1/traces", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if tableHeaderPresent || gotTable != "" {
		t.Errorf("expected X-Databricks-UC-Table-Name to be absent when UCTracesTable is empty, got %q", gotTable)
	}
}

// TestProxy_WebSocket_IsUpgradeDetected verifies that isWebSocketUpgrade
// correctly identifies WebSocket upgrade requests.
func TestProxy_WebSocket_IsUpgradeDetected(t *testing.T) {
	tests := []struct {
		name    string
		upgrade string
		want    bool
	}{
		{"websocket lowercase", "websocket", true},
		{"WebSocket mixed case", "WebSocket", true},
		{"WEBSOCKET uppercase", "WEBSOCKET", true},
		{"empty", "", false},
		{"http2", "h2c", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
			}
			if got := isWebSocketUpgrade(r); got != tc.want {
				t.Errorf("isWebSocketUpgrade(%q) = %v, want %v", tc.upgrade, got, tc.want)
			}
		})
	}
}

// TestProxy_WebSocket_UpgradeRejectedByUpstream verifies that when the upstream
// rejects the WebSocket upgrade (non-101), the status is forwarded to the client.
// This covers the codex inference path — Codex uses WebSocket; Claude uses HTTP/SSE.
func TestProxy_WebSocket_UpgradeRejectedByUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate upstream rejecting the upgrade (e.g. auth failure).
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
	}

	wsHandler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	l, err := Start(wsHandler, "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Close()

	// Send a WebSocket upgrade request through the running proxy.
	req, _ := http.NewRequest(http.MethodGet, "http://"+l.Addr().String()+"/v1/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestProxy_WebSocket_PlainHTTPNotAffected verifies that plain HTTP requests
// (no Upgrade header) are still routed through the normal httputil.ReverseProxy
// path after the WebSocket detection wrapper was added. This is the
// databricks-claude (Claude Code) path — it never sends WebSocket upgrades.
func TestProxy_WebSocket_PlainHTTPNotAffected(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("plain-http-token"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// No Upgrade header — this is a plain HTTP request (Claude Code path).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotAuth != "Bearer plain-http-token" {
		t.Errorf("got Authorization %q, want %q", gotAuth, "Bearer plain-http-token")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
}

// Ensure Start works correctly and listeners can be used.
func TestProxy_Start(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	l, err := Start(handler, "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + l.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
}

// --- API Key auth tests ---

// TestProxy_APIKey_CorrectKey verifies that a request with the correct API key
// is forwarded to the upstream (200).
func TestProxy_APIKey_CorrectKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
		APIKey:            "my-secret-key",
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer my-secret-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
}

// TestProxy_APIKey_WrongKey verifies that a request with the wrong API key
// is rejected with 401.
func TestProxy_APIKey_WrongKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called with wrong key")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
		APIKey:            "my-secret-key",
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", rec.Code)
	}
}

// TestProxy_APIKey_NoKeyConfigured verifies that when no API key is configured,
// requests pass through without auth (200).
func TestProxy_APIKey_NoKeyConfigured(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
		APIKey:            "", // no key — auth disabled
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// No Authorization header at all.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
}

// --- TLS config validation tests ---

// TestValidateTLSConfig_CertWithoutKey returns error when only cert is provided.
func TestValidateTLSConfig_CertWithoutKey(t *testing.T) {
	err := ValidateTLSConfig("/path/to/cert.pem", "")
	if err == nil {
		t.Error("expected error when cert is set but key is empty")
	}
}

// TestValidateTLSConfig_KeyWithoutCert returns error when only key is provided.
func TestValidateTLSConfig_KeyWithoutCert(t *testing.T) {
	err := ValidateTLSConfig("", "/path/to/key.pem")
	if err == nil {
		t.Error("expected error when key is set but cert is empty")
	}
}

// TestValidateTLSConfig_BothSet returns no error when both cert and key are provided.
func TestValidateTLSConfig_BothSet(t *testing.T) {
	err := ValidateTLSConfig("/path/to/cert.pem", "/path/to/key.pem")
	if err != nil {
		t.Errorf("unexpected error when both cert and key are set: %v", err)
	}
}

// TestValidateTLSConfig_NeitherSet returns no error when TLS is disabled.
func TestValidateTLSConfig_NeitherSet(t *testing.T) {
	err := ValidateTLSConfig("", "")
	if err != nil {
		t.Errorf("unexpected error when neither cert nor key is set: %v", err)
	}
}

// TestNewServer_InvalidInferenceUpstream verifies that NewServer returns a
// non-nil error for an invalid InferenceUpstream URL (no process exit).
func TestNewServer_InvalidInferenceUpstream(t *testing.T) {
	cfg := &Config{
		InferenceUpstream: "://bad-url",
		OTELUpstream:      "http://localhost",
		TokenSource:       warmToken("tok"),
	}
	_, err := NewServer(cfg)
	if err == nil {
		t.Fatal("expected non-nil error for invalid InferenceUpstream, got nil")
	}
}

// TestNewServer_InvalidOTELUpstream verifies that NewServer returns a
// non-nil error for an invalid OTELUpstream URL (no process exit).
func TestNewServer_InvalidOTELUpstream(t *testing.T) {
	cfg := &Config{
		InferenceUpstream: "http://localhost",
		OTELUpstream:      "://bad-url",
		TokenSource:       warmToken("tok"),
	}
	_, err := NewServer(cfg)
	if err == nil {
		t.Fatal("expected non-nil error for invalid OTELUpstream, got nil")
	}
}

// --- Daemon mode tests ---

// TestProxy_Daemon_HealthReturnsDaemonTrue verifies that when Daemon=true,
// GET /health returns a body containing daemon:true and the profile.
func TestProxy_Daemon_HealthReturnsDaemonTrue(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		ToolName:          "databricks-claude",
		Version:           "1.2.3",
		Daemon:            true,
		Profile:           "my-profile",
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got, ok := body["daemon"].(bool); !ok || !got {
		t.Errorf("expected daemon=true in /health body, got %v", body["daemon"])
	}
	if got, _ := body["profile"].(string); got != "my-profile" {
		t.Errorf("expected profile=my-profile in /health body, got %q", got)
	}
	if got, _ := body["version"].(string); got != "1.2.3" {
		t.Errorf("expected version=1.2.3 in /health body, got %q", got)
	}
	if _, ok := body["tool"]; !ok {
		t.Error("expected tool field in daemon /health body (needed by portbind)")
	}
}

// TestProxy_Daemon_HealthTokenExpiry verifies that when TokenSource implements
// tokenExpirer and has a non-zero expiry, token_valid_until appears in /health.
func TestProxy_Daemon_HealthTokenExpiry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	ts := &expiringTokenSource{token: "tok", expiry: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       ts,
		ToolName:          "databricks-claude",
		Daemon:            true,
		Profile:           "p",
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	expStr, _ := body["token_valid_until"].(string)
	if expStr == "" {
		t.Error("expected token_valid_until in /health body when TokenSource implements tokenExpirer")
	}
	if !strings.Contains(expStr, "2030") {
		t.Errorf("token_valid_until %q does not contain expected year 2030", expStr)
	}
}

// TestProxy_NonDaemon_HealthOmitsDaemon verifies that Daemon=false (existing
// behavior) does NOT include daemon:true in /health — regression guard.
func TestProxy_NonDaemon_HealthOmitsDaemon(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		ToolName:          "databricks-claude",
		Version:           "0.1.0",
		Daemon:            false,
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if v, ok := body["daemon"]; ok {
		t.Errorf("non-daemon /health should not contain daemon field, got %v", v)
	}
	if _, ok := body["pid"]; !ok {
		t.Error("non-daemon /health should contain pid field")
	}
}

// TestProxy_Daemon_ShutdownReturns404 verifies that in daemon mode (no lifecycle
// wrapper), POST /shutdown returns a clean 404 — the daemon explicitly rejects
// the route rather than letting it fall through to the inference catch-all
// (which would forward POST /shutdown upstream and confuse hook probes).
func TestProxy_Daemon_ShutdownReturns404(t *testing.T) {
	var upstreamHit bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		ToolName:          "databricks-claude",
		Daemon:            true,
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("daemon /shutdown: got status %d, want %d", rec.Code, http.StatusNotFound)
	}
	if upstreamHit {
		t.Error("daemon /shutdown leaked to inference upstream; explicit 404 handler should intercept")
	}
	body := rec.Body.String()
	if strings.Contains(body, `"remaining"`) || strings.Contains(body, `"exiting"`) {
		t.Error("daemon /shutdown should not return lifecycle JSON {remaining, exiting}; lifecycle is not registered")
	}
}

// TestProxy_NonDaemon_NoExplicit404 verifies the non-daemon path does NOT
// register the explicit /shutdown 404 — the route is owned by the lifecycle
// wrapper or, when lifecycle isn't applied, falls through to inference. This
// guards against the daemon-mode 404 leaking into the per-session path.
func TestProxy_NonDaemon_NoExplicit404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		ToolName:          "databricks-claude",
		Daemon:            false,
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Non-daemon mode: the bare NewServer mux falls through to inference
	// catch-all, which forwards to the test upstream returning 200. The
	// lifecycle wrapper (applied externally in --headless mode) would
	// intercept first. Either way, an explicit 404 here would be wrong.
	if rec.Code == http.StatusNotFound {
		t.Error("non-daemon /shutdown should not be intercepted with 404; route ownership belongs to lifecycle wrapper or inference fallthrough")
	}
}

// TestProxy_Daemon_EmptyOTELTables verifies that when all UC table fields are
// empty in daemon mode, no X-Databricks-UC-Table-Name header is sent —
// regression guard against "default to something" footguns.
func TestProxy_Daemon_EmptyOTELTables(t *testing.T) {
	var gotTable string
	var tableHeaderPresent bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, tableHeaderPresent = r.Header["X-Databricks-Uc-Table-Name"]
		gotTable = r.Header.Get("X-Databricks-UC-Table-Name")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "",
		UCLogsTable:       "",
		UCTracesTable:     "",
		TokenSource:       warmToken("tok"),
		Daemon:            true,
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	for _, path := range []string{"/otel/v1/metrics", "/otel/v1/logs", "/otel/v1/traces"} {
		tableHeaderPresent = false
		gotTable = ""
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if tableHeaderPresent || gotTable != "" {
			t.Errorf("daemon with empty tables: expected no X-Databricks-UC-Table-Name for %s, got %q", path, gotTable)
		}
	}
}

// --- Config.Routes (path-prefix multi-upstream) tests ---
//
// These cover #188: a single proxy port dispatching to multiple AI Gateway
// upstreams via path-prefix routes (e.g. Anthropic on / + Gemini Native on
// /v1beta). The route handler is a thin shim over inferenceHandler — strip
// the local prefix, then let the existing prepend logic add the upstream
// base path.

// TestProxy_Routes_PathPrefixDispatch verifies that requests matching a
// route's PathPrefix are forwarded to that route's upstream, and other
// requests fall through to InferenceUpstream.
func TestProxy_Routes_PathPrefixDispatch(t *testing.T) {
	var routeHits, defaultHits int
	routeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer routeUpstream.Close()

	defaultUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer defaultUpstream.Close()

	cfg := &Config{
		InferenceUpstream: defaultUpstream.URL,
		OTELUpstream:      defaultUpstream.URL,
		TokenSource:       warmToken("tok"),
		Routes: []UpstreamRoute{
			{PathPrefix: "/v1beta", Upstream: routeUpstream.URL + "/ai-gateway/gemini/v1beta"},
		},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Request matching route prefix → routed upstream.
	req1 := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-pro:generateContent", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if routeHits != 1 || defaultHits != 0 {
		t.Errorf("after /v1beta request: routeHits=%d defaultHits=%d, want 1/0", routeHits, defaultHits)
	}

	// Request not matching route prefix → default upstream.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if routeHits != 1 || defaultHits != 1 {
		t.Errorf("after /v1/messages request: routeHits=%d defaultHits=%d, want 1/1", routeHits, defaultHits)
	}
}

// TestProxy_Routes_TokenInjectionOnBothPaths verifies that the Bearer token
// is injected on both the routed upstream and the default upstream — the
// route handler shares inferenceHandler's token-injection codepath.
func TestProxy_Routes_TokenInjectionOnBothPaths(t *testing.T) {
	var routeAuth, defaultAuth string
	routeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer routeUpstream.Close()

	defaultUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer defaultUpstream.Close()

	cfg := &Config{
		InferenceUpstream: defaultUpstream.URL,
		OTELUpstream:      defaultUpstream.URL,
		TokenSource:       warmToken("shared-token"),
		Routes: []UpstreamRoute{
			{PathPrefix: "/v1beta", Upstream: routeUpstream.URL},
		},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/v1beta/models/x", nil))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	want := "Bearer shared-token"
	if routeAuth != want {
		t.Errorf("routed upstream Authorization = %q, want %q", routeAuth, want)
	}
	if defaultAuth != want {
		t.Errorf("default upstream Authorization = %q, want %q", defaultAuth, want)
	}
}

// TestProxy_Routes_StripsLocalPrefix is the prefix-collision regression pin.
// It verifies the upstream sees the EXACT expected path string after
// strip-then-prepend — not /v1beta/v1beta/... (double-prefix) and not just
// /models/x (over-strip). The plan calls this out as the most likely place
// to land a bug.
func TestProxy_Routes_StripsLocalPrefix(t *testing.T) {
	var gotPath string
	routeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer routeUpstream.Close()

	cfg := &Config{
		InferenceUpstream: routeUpstream.URL,
		OTELUpstream:      routeUpstream.URL,
		TokenSource:       warmToken("tok"),
		Routes: []UpstreamRoute{
			{
				PathPrefix: "/v1beta",
				Upstream:   routeUpstream.URL + "/ai-gateway/gemini/v1beta",
			},
		},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Exact match — not strings.Contains, not HasPrefix. The whole point of
	// this test is to pin the path algebra: incoming /v1beta/models/x with
	// upstream /ai-gateway/gemini/v1beta and StripPrefix defaulted to
	// PathPrefix should produce exactly this on the wire. A failure here
	// likely means double-prefix (/ai-gateway/gemini/v1beta/v1beta/...) or
	// over-strip (/ai-gateway/gemini/models/...).
	want := "/ai-gateway/gemini/v1beta/models/gemini-2.0-flash:generateContent"
	if gotPath != want {
		t.Errorf("upstream saw path %q, want exactly %q", gotPath, want)
	}
}

// TestProxy_Routes_EmptyRoutesIsBackwardCompatible regression-pins the
// sibling-consumer guarantee: with Routes nil, NewServer's behavior is
// byte-identical to its behavior before this field existed. databricks-codex
// / databricks-cursor / databricks-opencode all leave Routes unset.
func TestProxy_Routes_EmptyRoutesIsBackwardCompatible(t *testing.T) {
	var gotPath, gotAuth, gotCodingHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCodingHeader = r.Header.Get("x-databricks-use-coding-agent-mode")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL + "/anthropic",
		OTELUpstream:      upstream.URL,
		UCMetricsTable:    "main.t.m",
		UCLogsTable:       "main.t.l",
		TokenSource:       warmToken("tok"),
		// Routes intentionally unset (nil) — backward-compat path.
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// These three assertions mirror the load-bearing observable behavior
	// of the existing TestProxy_PathAlgebra_Inference + TestProxy_InjectsAuthHeader
	// + TestProxy_InjectsCustomHeaders tests. If any of them changes when
	// Routes is nil, sibling consumers break.
	if gotPath != "/anthropic/v1/messages" {
		t.Errorf("path: got %q, want %q", gotPath, "/anthropic/v1/messages")
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth: got %q, want %q", gotAuth, "Bearer tok")
	}
	if gotCodingHeader != "true" {
		t.Errorf("x-databricks-use-coding-agent-mode: got %q, want %q", gotCodingHeader, "true")
	}
}

// TestProxy_Routes_OrderIndependent verifies that registering two routes in
// either order produces the same dispatch — http.ServeMux matches the
// longest-prefix regardless of registration order. The plan calls this out
// as a guard against any future implementation that smuggles in registration
// order as an implicit precedence signal (e.g. switching to a slice scan).
func TestProxy_Routes_OrderIndependent(t *testing.T) {
	for _, order := range []struct {
		name   string
		routes []string // PathPrefixes in registration order
	}{
		{"a-then-b", []string{"/alpha", "/beta"}},
		{"b-then-a", []string{"/beta", "/alpha"}},
	} {
		t.Run(order.name, func(t *testing.T) {
			var alphaHits, betaHits, defaultHits int
			alphaUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				alphaHits++
				w.WriteHeader(http.StatusOK)
			}))
			defer alphaUpstream.Close()
			betaUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				betaHits++
				w.WriteHeader(http.StatusOK)
			}))
			defer betaUpstream.Close()
			defaultUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defaultHits++
				w.WriteHeader(http.StatusOK)
			}))
			defer defaultUpstream.Close()

			urlByPrefix := map[string]string{
				"/alpha": alphaUpstream.URL,
				"/beta":  betaUpstream.URL,
			}
			var routes []UpstreamRoute
			for _, p := range order.routes {
				routes = append(routes, UpstreamRoute{PathPrefix: p, Upstream: urlByPrefix[p]})
			}

			cfg := &Config{
				InferenceUpstream: defaultUpstream.URL,
				OTELUpstream:      defaultUpstream.URL,
				TokenSource:       warmToken("tok"),
				Routes:            routes,
			}
			handler, err := NewServer(cfg)
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}

			// Three requests: one for each route + one default.
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/alpha/x", nil))
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/beta/y", nil))
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/default/z", nil))

			if alphaHits != 1 || betaHits != 1 || defaultHits != 1 {
				t.Errorf("registration order %v: alpha=%d beta=%d default=%d, want 1/1/1",
					order.routes, alphaHits, betaHits, defaultHits)
			}
		})
	}
}

// expiringTokenSource implements TokenSource and tokenExpirer for testing
// the token_valid_until field in /health.
type expiringTokenSource struct {
	token  string
	expiry time.Time
}

func (e *expiringTokenSource) Token(_ context.Context) (string, error) {
	return e.token, nil
}

func (e *expiringTokenSource) Expiry() time.Time {
	return e.expiry
}
