package main

import (
	"net"
	"net/http"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy"
	"github.com/IceRhymers/databricks-agents/internal/core/tokencache"
)

// ProxyConfig holds the configuration for the proxy server.
type ProxyConfig struct {
	InferenceUpstream string
	// GeminiUpstream, when non-empty, registers a /v1beta path-prefix route
	// to the Databricks Gemini AI Gateway upstream so the same local proxy
	// port serves both Anthropic (catch-all) and Gemini Native (/v1beta).
	// Empty string disables the route — byte-identical to the prior
	// Anthropic-only behavior.
	GeminiUpstream string
	TokenProvider  *tokencache.TokenProvider
	Verbose        bool
	APIKey         string
	TLSCertFile    string
	TLSKeyFile     string
}

// NewProxyServer returns an http.Handler that routes requests to the
// inference upstream. No OTEL upstream is needed for OpenCode. The wrapper
// path (core.Run) builds the equivalent handler directly from the LaunchPlan;
// this facade backs the serve sibling entrypoint (serve_opencode.go), which
// does not route through core.Run.
func NewProxyServer(config *ProxyConfig) (http.Handler, error) {
	cfg := &proxy.Config{
		InferenceUpstream: config.InferenceUpstream,
		TokenSource:       config.TokenProvider,
		Verbose:           config.Verbose,
		APIKey:            config.APIKey,
		TLSCertFile:       config.TLSCertFile,
		TLSKeyFile:        config.TLSKeyFile,
		ToolName:          "databricks-opencode",
		Version:           Version,
		// ResponsesRewrite: the Databricks AI Gateway re-encodes Responses-API
		// SSE and emits a different id in response.output_item.added (item.id)
		// than in downstream response.output_text.* / response.content_part.*
		// events (item_id), which trips @ai-sdk/openai's parser with
		// "text part <id> not found". opencode is OpenAI-shaped and hits
		// /v1/responses, so the gate is default-on here. Sibling wrappers
		// (databricks-claude, databricks-codex) leave it false.
		ResponsesRewrite: proxy.ResponsesRewriteSettings{Enabled: true},
	}
	if config.GeminiUpstream != "" {
		cfg.Routes = append(cfg.Routes, proxy.UpstreamRoute{
			PathPrefix: "/v1beta",
			Upstream:   config.GeminiUpstream,
		})
	}
	return proxy.NewServer(cfg)
}

// StartProxy binds to 127.0.0.1:0, starts serving, and returns the listener.
// Callers read l.Addr() to discover the assigned port.
// When certFile and keyFile are both non-empty, the listener serves TLS.
func StartProxy(handler http.Handler, certFile, keyFile string) (net.Listener, error) {
	return proxy.Start(handler, certFile, keyFile)
}
