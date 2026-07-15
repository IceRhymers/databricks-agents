package main

import (
	"log"
	"net"
	"net/http"

	"github.com/IceRhymers/databricks-agents/internal/core/tokencache"
	"github.com/IceRhymers/databricks-agents/pkg/proxy"
)

// ProxyConfig holds the configuration for the proxy server.
type ProxyConfig struct {
	InferenceUpstream string
	OTELUpstream      string
	UCMetricsTable    string
	UCLogsTable       string
	UCTracesTable     string
	TokenProvider     *tokencache.TokenProvider
	Verbose           bool
	APIKey            string
	TLSCertFile       string
	TLSKeyFile        string
	ToolName          string // reported by /health endpoint
	Version           string // reported by /health endpoint
	// WebSearch (--with-websearch) — see pkg/proxy.WebSearchSettings.
	WebSearch proxy.WebSearchSettings
}

// recoveryHandler wraps h with panic recovery, returning 502 on panic.
func recoveryHandler(next http.Handler) http.Handler {
	return proxy.RecoveryHandler(next)
}

// NewProxyServer returns an http.Handler that routes requests to the
// inference upstream (default) and the OTEL upstream (/otel/).
// Panics if the upstream URLs in config are invalid — callers in main should
// validate URLs before calling this function.
func NewProxyServer(config *ProxyConfig) http.Handler {
	h, err := proxy.NewServer(&proxy.Config{
		InferenceUpstream: config.InferenceUpstream,
		OTELUpstream:      config.OTELUpstream,
		UCMetricsTable:    config.UCMetricsTable,
		UCLogsTable:       config.UCLogsTable,
		UCTracesTable:     config.UCTracesTable,
		TokenSource:       config.TokenProvider,
		Verbose:           config.Verbose,
		APIKey:            config.APIKey,
		ToolName:          config.ToolName,
		Version:           config.Version,
		WebSearch:         config.WebSearch,
	})
	if err != nil {
		log.Fatalf("databricks-claude: %v", err)
	}
	return h
}

// ServeProxy starts the proxy on the given listener.
// When config.TLSCertFile and config.TLSKeyFile are both set, the listener serves TLS.
func ServeProxy(config *ProxyConfig, handler http.Handler, ln net.Listener) error {
	_, err := proxy.Serve(ln, handler, config.TLSCertFile, config.TLSKeyFile)
	return err
}

// StartProxy binds to 127.0.0.1:0, starts serving, and returns the listener.
// Used in tests and as a fallback when ServeProxy is not applicable.
func StartProxy(config *ProxyConfig, handler http.Handler) (net.Listener, error) {
	return proxy.Start(handler, config.TLSCertFile, config.TLSKeyFile)
}
