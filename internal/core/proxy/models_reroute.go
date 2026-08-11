package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// modelsRerouteHandler serves OpenAI-shaped model discovery for launchers whose
// AI Gateway upstream has no OpenAI-compliant /models endpoint.
//
// Why this exists: OpenAI-shaped clients (e.g. JetBrains IntelliJ's AI Assistant,
// pointed at the local proxy root) issue GET /models to enumerate available
// models. The OpenAI-shaped launchers use the {host}/ai-gateway/openai/v1
// upstream base path, so a naive forward of GET /models lands on
// {host}/ai-gateway/openai/v1/models — which Databricks does NOT serve (it 400s
// with "doesn't match any known API type"). The working discovery endpoint is
// {host}/ai-gateway/v1/models (note: no /openai/v1 segment), which returns a
// Databricks-shaped list rather than the OpenAI shape the client expects.
//
// This handler is registered ONLY when Config.OpenAIModelsUpstream is non-empty
// (the OpenAI-shaped launchers opt in by setting it to the working discovery
// URL). It fetches that upstream with a fresh token, transforms the body into
// the OpenAI /v1/models shape, and returns it. Launchers that do not set the
// field (claude, opencode) never register this route — byte-identical to before.
//
// Scope is exactly GET /models: the route is registered at the exact path
// "/models" (http.ServeMux exact match), and non-GET methods are delegated to
// the passed-in inference handler (the same catch-all every other request
// uses). /models/{id} is not matched here (its longer path routes to the
// catch-all), matching the "GET /models only" contract.
//
// nonGET is the inference catch-all handler; non-GET /models requests are
// handed to it verbatim so a real POST/etc. is never swallowed by discovery.
func modelsRerouteHandler(upstream string, config *Config, nonGET http.Handler) http.Handler {
	transport := http.DefaultTransport
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lg := logger{prefix: config.ToolName, verbose: config.Verbose}

		// Only GET is a discovery call; anything else on /models is forwarded
		// to the inference upstream so we don't swallow a real POST/etc.
		if r.Method != http.MethodGet {
			nonGET.ServeHTTP(w, r)
			return
		}

		token, terr := config.TokenSource.Token(r.Context())
		if terr != nil {
			lg.Logf("token fetch error (models): %v", terr)
		}

		outReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
		if err != nil {
			http.Error(w, "build models request failed", http.StatusInternalServerError)
			return
		}
		if token != "" {
			outReq.Header.Set("Authorization", "Bearer "+token)
			outReq.Header.Set("x-api-key", token)
		}
		outReq.Header.Set("x-databricks-use-coding-agent-mode", "true")

		lg.Vlogf("models → GET %s", upstream)

		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			http.Error(w, "models upstream request failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
		if err != nil {
			http.Error(w, "read models response failed", http.StatusBadGateway)
			return
		}

		// On any non-200 from upstream, forward the status and raw body so the
		// client sees the real error rather than a silently-empty list.
		if resp.StatusCode != http.StatusOK {
			lg.Logf("models upstream error %d: %s", resp.StatusCode, sanitizeLogOutput(string(body)))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(body) //nolint:errcheck
			return
		}

		openaiBody, err := transformModelsToOpenAI(body)
		if err != nil {
			// Transform failed (unexpected upstream shape): forward raw rather
			// than fabricate. The client may still cope, and we log the reason.
			lg.Logf("models transform failed (forwarding raw): %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(body) //nolint:errcheck
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(openaiBody) //nolint:errcheck
	})
}

// databricksModel is the subset of the Databricks /ai-gateway/v1/models list
// item shape this transform reads. Unknown fields are ignored.
type databricksModel struct {
	QueryName   string `json:"query_name"`
	DisplayName string `json:"display_name"`
}

// databricksModelsList is the top-level {object:"list", data:[...]} envelope
// returned by {host}/ai-gateway/v1/models.
type databricksModelsList struct {
	Object string            `json:"object"`
	Data   []databricksModel `json:"data"`
}

// openAIModel is one entry in an OpenAI /v1/models response.
type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// openAIModelsList is the OpenAI /v1/models envelope.
type openAIModelsList struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

// transformModelsToOpenAI converts a Databricks /ai-gateway/v1/models body into
// the OpenAI /v1/models shape. Each Databricks entry's query_name becomes the
// OpenAI id (the identifier a client passes back as the model), object is
// "model", owned_by is "databricks", and created is a fixed non-zero timestamp
// (the gateway does not expose per-model creation times; OpenAI clients only
// require the field to be present and numeric).
func transformModelsToOpenAI(body []byte) ([]byte, error) {
	var src databricksModelsList
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, err
	}

	out := openAIModelsList{Object: "list", Data: make([]openAIModel, 0, len(src.Data))}
	// Fixed timestamp: the discovery endpoint has no per-model created time, and
	// OpenAI clients only need a present numeric field. Using a stable constant
	// keeps the response deterministic (important for tests and caching clients).
	const created int64 = 1700000000 // 2023-11-14, arbitrary stable epoch seconds
	for _, m := range src.Data {
		if m.QueryName == "" {
			continue
		}
		out.Data = append(out.Data, openAIModel{
			ID:      m.QueryName,
			Object:  "model",
			Created: created,
			OwnedBy: "databricks",
		})
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
