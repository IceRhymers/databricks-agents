package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// databricksModelsBody is a representative /ai-gateway/v1/models response body
// (trimmed from the real fevm-west shape: query_name/display_name/capabilities).
const databricksModelsBody = `{"object":"list","data":[` +
	`{"query_name":"system.ai.gpt-oss-120b","display_name":"GPT OSS 120B","api_types":["mlflow/v1/chat/completions"],"capabilities":{"function_calling":true}},` +
	`{"query_name":"system.ai.gemini-2-5-flash","display_name":"Gemini 2.5 Flash","api_types":[],"capabilities":{}}` +
	`]}`

// TestModelsReroute_TransformsToOpenAIShape verifies GET /models fetches the
// configured upstream and returns an OpenAI-shaped list ({id, object:"model"}).
func TestModelsReroute_TransformsToOpenAIShape(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(databricksModelsBody)) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream:    "http://inference.invalid",
		OTELUpstream:         "http://otel.invalid",
		OpenAIModelsUpstream: upstream.URL + "/ai-gateway/v1/models",
		TokenSource:          warmToken("tok-abc"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/ai-gateway/v1/models" {
		t.Errorf("upstream path = %q, want /ai-gateway/v1/models", gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("upstream auth = %q, want Bearer tok-abc", gotAuth)
	}

	var out openAIModelsList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if out.Object != "list" {
		t.Errorf("object = %q, want list", out.Object)
	}
	if len(out.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(out.Data))
	}
	if out.Data[0].ID != "system.ai.gpt-oss-120b" {
		t.Errorf("data[0].id = %q, want system.ai.gpt-oss-120b", out.Data[0].ID)
	}
	if out.Data[0].Object != "model" {
		t.Errorf("data[0].object = %q, want model", out.Data[0].Object)
	}
	if out.Data[0].OwnedBy != "databricks" {
		t.Errorf("data[0].owned_by = %q, want databricks", out.Data[0].OwnedBy)
	}
	if out.Data[0].Created == 0 {
		t.Errorf("data[0].created = 0, want a non-zero timestamp")
	}
}

// TestModelsReroute_NonGETFallsThrough verifies a POST /models is NOT rerouted:
// it hits the inference catch-all (which prepends the inference base path).
func TestModelsReroute_NonGETFallsThrough(t *testing.T) {
	var gotPath string
	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer inference.Close()

	modelsUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("models upstream must not be hit for non-GET; path=%s", r.URL.Path)
	}))
	defer modelsUpstream.Close()

	cfg := &Config{
		InferenceUpstream:    inference.URL + "/ai-gateway/openai/v1",
		OTELUpstream:         inference.URL,
		OpenAIModelsUpstream: modelsUpstream.URL + "/ai-gateway/v1/models",
		TokenSource:          warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/models", strings.NewReader("{}")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/ai-gateway/openai/v1/models" {
		t.Errorf("inference path = %q, want /ai-gateway/openai/v1/models (base path prepended)", gotPath)
	}
}

// TestModelsReroute_DisabledByDefault verifies that when OpenAIModelsUpstream is
// empty, GET /models falls through to the inference catch-all (byte-identical to
// before this feature — the route is never registered).
func TestModelsReroute_DisabledByDefault(t *testing.T) {
	var gotPath string
	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer inference.Close()

	cfg := &Config{
		InferenceUpstream: inference.URL + "/ai-gateway/anthropic",
		OTELUpstream:      inference.URL,
		// OpenAIModelsUpstream deliberately unset.
		TokenSource: warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models", nil))

	if gotPath != "/ai-gateway/anthropic/models" {
		t.Errorf("path = %q, want /ai-gateway/anthropic/models (fell through to catch-all)", gotPath)
	}
}

// TestModelsReroute_UpstreamErrorForwarded verifies a non-200 from the discovery
// upstream is forwarded with its status and body rather than masked.
func TestModelsReroute_UpstreamErrorForwarded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error_code":"BAD_REQUEST","message":"nope"}`)) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream:    "http://inference.invalid",
		OTELUpstream:         "http://otel.invalid",
		OpenAIModelsUpstream: upstream.URL + "/ai-gateway/v1/models",
		TokenSource:          warmToken("tok"),
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "BAD_REQUEST") {
		t.Errorf("body = %q, want it to contain the upstream error", rec.Body.String())
	}
}

// TestTransformModelsToOpenAI_SkipsEmptyQueryName verifies entries with no
// query_name are dropped (they cannot become a usable OpenAI model id).
func TestTransformModelsToOpenAI_SkipsEmptyQueryName(t *testing.T) {
	in := `{"object":"list","data":[{"query_name":"","display_name":"x"},{"query_name":"a.b.c","display_name":"y"}]}`
	out, err := transformModelsToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	var list openAIModelsList
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "a.b.c" {
		t.Errorf("data = %+v, want single entry a.b.c", list.Data)
	}
}
