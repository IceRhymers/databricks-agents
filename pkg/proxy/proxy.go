package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// TokenSource provides tokens for upstream authentication.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// UpstreamRoute is an additive, optional path-prefix route to an upstream
// other than InferenceUpstream. When an incoming request path starts with
// PathPrefix, the proxy strips StripPrefix (defaults to PathPrefix when
// empty) from the path and forwards to Upstream — reusing the same
// token-injection / WebSocket-detect / upstream-base-path-prepend logic
// the default inference handler uses.
//
// Used to wire one local proxy port to multiple Databricks AI Gateway
// upstreams (e.g. Anthropic on / and Gemini Native on /v1beta).
type UpstreamRoute struct {
	// PathPrefix is matched against the incoming request path. The proxy
	// registers this route at PathPrefix+"/" on the mux, so http.ServeMux
	// longest-prefix-match handles dispatch ordering.
	PathPrefix string
	// Upstream is the full upstream URL including any base path (e.g.
	// "https://workspace.cloud.databricks.com/ai-gateway/gemini/v1beta").
	// The base path is prepended to the (post-strip) request path.
	Upstream string
	// StripPrefix is removed from the front of the incoming request path
	// before the upstream base path is prepended. Defaults to PathPrefix
	// when empty.
	StripPrefix string
}

// Config holds the configuration for the proxy server.
//
// WebSocket note: databricks-claude (Claude Code) uses HTTP with SSE for
// streaming and never issues WebSocket upgrades. databricks-codex (Codex CLI)
// uses WebSocket for its inference connection. The proxy handles both
// transparently — WebSocket upgrades are detected per-request and routed
// through handleWebSocket; plain HTTP falls through to httputil.ReverseProxy.
// Centralising this here avoids maintaining a forked proxy in databricks-codex.
type Config struct {
	InferenceUpstream string
	OTELUpstream      string
	// Routes are optional path-prefix overrides. When a request matches a
	// route's PathPrefix it is forwarded to that route's Upstream rather
	// than InferenceUpstream. Leave nil/empty for byte-identical behavior
	// to before this field existed (sibling consumers depend on this).
	Routes []UpstreamRoute
	// UCMetricsTable is the Unity Catalog table for OTEL metrics.
	// Leave empty if the caller does not emit metrics (e.g. databricks-codex,
	// which has no native metrics support). When empty the
	// X-Databricks-UC-Table-Name header is omitted for metrics requests.
	UCMetricsTable string
	UCLogsTable    string
	// UCTracesTable is the Unity Catalog table for OTEL traces (Claude Code's
	// enhanced telemetry beta). When empty the X-Databricks-UC-Table-Name
	// header is omitted for /v1/traces requests.
	UCTracesTable string
	TokenSource   TokenSource
	Verbose       bool
	// ToolName identifies this proxy in /health responses (e.g. "databricks-claude").
	ToolName string
	// Version is the build version reported by /health.
	Version string
	// APIKey, when non-empty, requires all incoming requests to present
	// Authorization: Bearer <APIKey>. Leave empty to disable auth.
	APIKey string
	// TLSCertFile and TLSKeyFile enable TLS on the listener when both are set.
	TLSCertFile string
	TLSKeyFile  string
	// WebSearch (--with-websearch) bundles the optional local-fulfillment
	// settings for Anthropic's web_search/web_fetch server-side tools when
	// Databricks FMAPI doesn't yet support them. When Enabled is false,
	// inference requests are forwarded byte-identically and this struct
	// has no effect — see pkg/proxy/websearch_handler.go.
	WebSearch WebSearchSettings
	// ResponsesRewrite (--with-responses-rewrite, or enabled by sibling
	// consumers like databricks-opencode) gates the OpenAI Responses-API SSE
	// id-reconciliation rewriter. When Enabled is false, inference responses
	// are forwarded byte-identically and this struct has no effect — see
	// pkg/proxy/responses_rewriter.go. Claude Code never targets /responses,
	// so leaving this false is byte-identical to prior behavior.
	ResponsesRewrite ResponsesRewriteSettings
	// Daemon, when true, indicates this proxy is running as a long-lived daemon
	// (databricks-claude serve). The /health response includes daemon-specific
	// fields; /shutdown is not registered (serve.go never wraps with lifecycle).
	Daemon bool
	// Profile is the Databricks config profile name, reported in /health when Daemon=true.
	Profile string
}

// tokenExpirer is an optional extension of TokenSource for reporting token expiry.
// pkg/tokencache.TokenProvider implements this interface.
type tokenExpirer interface {
	Expiry() time.Time
}

// RecoveryHandler wraps h with panic recovery, returning 502 on panic.
func RecoveryHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("databricks-claude: proxy panic recovered: %v", err)
				http.Error(w, "Internal proxy error", http.StatusBadGateway)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// isWebSocketUpgrade returns true if the request is a WebSocket upgrade.
//
// Claude Code never sends WebSocket upgrades (it uses SSE over HTTP). This
// check exists to support databricks-codex, whose upstream uses WebSocket for
// inference. The check is passive — non-upgrade requests fall through to the
// normal httputil.ReverseProxy path with zero overhead.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// handleWebSocket proxies a WebSocket upgrade request to the upstream,
// injecting a fresh Bearer token. After a successful 101 response it pipes
// data bidirectionally between client and upstream until either side closes.
//
// Why this exists here: Codex (databricks-codex) connects to its inference
// endpoint over WebSocket rather than HTTP/SSE. The token injection and
// bidirectional piping logic is centralised in this shared package so
// databricks-codex can import it rather than maintaining its own copy.
func handleWebSocket(w http.ResponseWriter, r *http.Request, upstream *url.URL, config *Config) {
	token, err := config.TokenSource.Token(r.Context())
	if err != nil {
		log.Printf("databricks-claude: ws token fetch error: %v", err)
		http.Error(w, "token fetch failed", http.StatusBadGateway)
		return
	}

	// Build upstream path: prepend upstream base path to request path.
	basePath := strings.TrimRight(upstream.Path, "/")
	upstreamPath := basePath + r.URL.Path

	// Determine host:port for dialing.
	dialHost := upstream.Host
	useTLS := upstream.Scheme == "https" || upstream.Scheme == "wss"
	if !strings.Contains(dialHost, ":") {
		if useTLS {
			dialHost += ":443"
		} else {
			dialHost += ":80"
		}
	}

	if config.Verbose {
		log.Printf("databricks-claude: ws upgrade → %s%s (tls=%v)", upstream.Host, upstreamPath, useTLS)
	}

	// Dial upstream.
	var upstreamConn net.Conn
	if useTLS {
		upstreamConn, err = tls.Dial("tcp", dialHost, &tls.Config{
			ServerName: upstream.Hostname(),
		})
	} else {
		upstreamConn, err = net.Dial("tcp", dialHost)
	}
	if err != nil {
		log.Printf("databricks-claude: ws dial failed: %v", err)
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}
	defer upstreamConn.Close()

	// Build the HTTP upgrade request for the upstream.
	upgradeReq, err := http.NewRequest(r.Method, (&url.URL{
		Path:     upstreamPath,
		RawQuery: r.URL.RawQuery,
	}).RequestURI(), nil)
	if err != nil {
		log.Printf("databricks-claude: ws build request failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Copy original headers, then override auth + host.
	for k, vv := range r.Header {
		for _, v := range vv {
			upgradeReq.Header.Add(k, v)
		}
	}
	upgradeReq.Header.Set("Authorization", "Bearer "+token)
	upgradeReq.Header.Set("x-api-key", token)
	upgradeReq.Header.Set("x-databricks-use-coding-agent-mode", "true")
	upgradeReq.Host = upstream.Host
	upgradeReq.Header.Set("Host", upstream.Host)

	// Send the upgrade request to the upstream.
	if err := upgradeReq.Write(upstreamConn); err != nil {
		log.Printf("databricks-claude: ws write upgrade failed: %v", err)
		http.Error(w, "upstream write failed", http.StatusBadGateway)
		return
	}

	// Read the upstream response.
	br := bufio.NewReader(upstreamConn)
	upstreamResp, err := http.ReadResponse(br, upgradeReq)
	if err != nil {
		log.Printf("databricks-claude: ws read response failed: %v", err)
		http.Error(w, "upstream response failed", http.StatusBadGateway)
		return
	}

	// If the upstream did not switch protocols, forward the error to the client.
	if upstreamResp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(upstreamResp.Body)
		upstreamResp.Body.Close()
		log.Printf("databricks-claude: ws upgrade rejected: %d %s", upstreamResp.StatusCode, sanitizeLogOutput(string(body)))
		w.WriteHeader(upstreamResp.StatusCode)
		w.Write(body) //nolint:errcheck
		return
	}

	// Hijack the client connection.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		log.Printf("databricks-claude: ResponseWriter does not support hijacking")
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		log.Printf("databricks-claude: ws hijack failed: %v", err)
		return
	}
	defer clientConn.Close()

	// Forward the 101 Switching Protocols response to the client.
	if err := upstreamResp.Write(clientConn); err != nil {
		log.Printf("databricks-claude: ws write 101 to client failed: %v", err)
		return
	}

	if config.Verbose {
		log.Printf("databricks-claude: ws connected, piping data")
	}

	// Bidirectional pipe. When either direction finishes, close both.
	done := make(chan struct{}, 2)

	// Client → Upstream
	go func() {
		defer func() { done <- struct{}{} }()
		// Flush any buffered data from the client's bufio reader first.
		if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
			buffered := make([]byte, clientBuf.Reader.Buffered())
			n, _ := clientBuf.Read(buffered)
			if n > 0 {
				upstreamConn.Write(buffered[:n]) //nolint:errcheck
			}
		}
		io.Copy(upstreamConn, clientConn) //nolint:errcheck
	}()

	// Upstream → Client
	go func() {
		defer func() { done <- struct{}{} }()
		// Flush any buffered data from the upstream's bufio reader first.
		if br.Buffered() > 0 {
			buffered := make([]byte, br.Buffered())
			n, _ := br.Read(buffered)
			if n > 0 {
				clientConn.Write(buffered[:n]) //nolint:errcheck
			}
		}
		io.Copy(clientConn, upstreamConn) //nolint:errcheck
	}()

	// Wait for first direction to finish, then close both to unblock the other goroutine.
	<-done
	clientConn.Close()
	upstreamConn.Close()
	<-done // wait for second goroutine to finish

	if config.Verbose {
		log.Printf("databricks-claude: ws connection closed")
	}
}

// requireAPIKey returns middleware that validates Authorization: Bearer <key>
// on every incoming request. If key is empty, authentication is disabled and
// requests pass through unchanged.
// APIKey check applies to all connections including WebSocket upgrades (used by databricks-codex)
func requireAPIKey(next http.Handler, key string) http.Handler {
	if key == "" {
		return next // auth disabled
	}
	expected := "Bearer " + key
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewServer returns an http.Handler that routes requests to the
// inference upstream (default) and the OTEL upstream (/otel/).
//
// Inference route behaviour:
//   - WebSocket upgrade requests (used by databricks-codex / Codex CLI) are
//     handled via handleWebSocket with bidirectional piping and fresh token injection.
//   - All other requests (used by databricks-claude / Claude Code, which speaks
//     HTTP + SSE) are handled by httputil.ReverseProxy.
//
// OTEL route (/otel/):
//   - /v1/logs paths   → UCLogsTable header
//   - /v1/traces paths → UCTracesTable header
//   - all other paths  → UCMetricsTable header (omitted if the chosen table is empty)
func NewServer(config *Config) (http.Handler, error) {
	mux := http.NewServeMux()

	inferenceUpstream, err := url.Parse(config.InferenceUpstream)
	if err != nil {
		return nil, fmt.Errorf("databricks-claude: invalid InferenceUpstream %q: %v", config.InferenceUpstream, err)
	}

	otelUpstream, err := url.Parse(config.OTELUpstream)
	if err != nil {
		return nil, fmt.Errorf("databricks-claude: invalid OTELUpstream %q: %v", config.OTELUpstream, err)
	}

	// Inference handler — when WebSearch.Enabled is false this is a thin
	// custom replacement for httputil.ReverseProxy that forwards bytes
	// verbatim (regression-tested for byte-identity); when true, it
	// inspects /v1/messages bodies to fulfill web_search/web_fetch locally.
	// WebSocket upgrades (used by databricks-codex) are detected inside
	// the handler and routed through handleWebSocket.
	inferenceHandlerHTTP := inferenceHandler(inferenceUpstream, config, config.WebSearch)

	// OTEL proxy — /otel/ route
	otelProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			token, err := config.TokenSource.Token(req.Context())
			if err != nil {
				log.Printf("databricks-claude: token fetch error (otel): %v", err)
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("x-api-key", token)
			}

			// Pick the correct UC table based on the OTel signal in the path.
			// Any of UCLogsTable, UCTracesTable, or UCMetricsTable may be empty
			// (signal-not-configured); in that case the header is omitted and
			// the upstream rejects the request — by design, signal env vars are
			// only emitted when the table is configured, so this branch should
			// not fire in normal operation.
			var ucTable string
			switch {
			case strings.Contains(req.URL.Path, "/v1/logs"):
				ucTable = config.UCLogsTable
			case strings.Contains(req.URL.Path, "/v1/traces"):
				ucTable = config.UCTracesTable
			default:
				ucTable = config.UCMetricsTable
			}
			if ucTable != "" {
				req.Header.Set("X-Databricks-UC-Table-Name", ucTable)
			}

			// Strip the /otel prefix and prepend the upstream base path.
			stripped := strings.TrimPrefix(req.URL.Path, "/otel")
			basePath := strings.TrimRight(otelUpstream.Path, "/")
			req.URL.Scheme = otelUpstream.Scheme
			req.URL.Host = otelUpstream.Host
			req.Host = otelUpstream.Host
			req.URL.Path = basePath + stripped
			req.URL.RawPath = ""

			if config.Verbose {
				log.Printf("databricks-claude: otel → %s %s%s", req.Method, req.URL.Host, req.URL.Path)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if config.Verbose || resp.StatusCode >= 400 {
				body, err := io.ReadAll(resp.Body)
				if err == nil {
					snippet := string(body)
					if len(snippet) > 500 {
						snippet = snippet[:500] + "..."
					}
					if resp.StatusCode >= 400 {
						log.Printf("databricks-claude: otel upstream error %d: %s", resp.StatusCode, sanitizeLogOutput(snippet))
					} else {
						log.Printf("databricks-claude: otel ← %d (%d bytes)", resp.StatusCode, len(body))
					}
					resp.Body = io.NopCloser(bytes.NewReader(body))
				}
			}
			return nil
		},
		FlushInterval: -1,
	}

	// Health endpoint — used by portbind to detect an existing proxy for this tool.
	// In daemon mode the body includes daemon:true, profile, and token_valid_until
	// so hooks (e.g. SessionStart) can detect and no-op.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if config.Daemon {
			body := map[string]interface{}{
				"tool":    config.ToolName,
				"status":  "ok",
				"daemon":  true,
				"version": config.Version,
				"profile": config.Profile,
			}
			if expirer, ok := config.TokenSource.(tokenExpirer); ok {
				if t := expirer.Expiry(); !t.IsZero() {
					body["token_valid_until"] = t.UTC().Format(time.RFC3339)
				}
			}
			json.NewEncoder(w).Encode(body)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tool":    config.ToolName,
			"version": config.Version,
			"pid":     os.Getpid(),
		})
	})

	// Optional path-prefix routes — registered before the catch-all so the
	// mux's longest-prefix-match dispatches correctly. Routes share the same
	// inferenceHandler factory (token injection + WebSocket detect + path
	// prepend) and only differ in the upstream URL and the per-route prefix
	// stripped from the incoming path before the prepend step.
	for i, route := range config.Routes {
		routeUpstream, err := url.Parse(route.Upstream)
		if err != nil {
			return nil, fmt.Errorf("databricks-claude: invalid Routes[%d].Upstream %q: %v", i, route.Upstream, err)
		}
		mux.Handle(route.PathPrefix+"/", RecoveryHandler(newRouteHandler(route, routeUpstream, config)))
	}

	mux.Handle("/otel/", RecoveryHandler(otelProxy))

	// Daemon mode: explicitly reject /shutdown with 404 so it does not fall
	// through to the inference catch-all (which would forward POST /shutdown
	// upstream). Hooks that probe /shutdown to release per-session refcounts
	// must see a clean 404 to know the daemon is not managing their session.
	if config.Daemon {
		mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}

	mux.Handle("/", RecoveryHandler(inferenceHandlerHTTP))

	// APIKey check applies to all connections including WebSocket upgrades (used by databricks-codex)
	return requireAPIKey(mux, config.APIKey), nil
}

// newRouteHandler returns an http.Handler for a single UpstreamRoute. It is
// a thin shim over inferenceHandler: strip the local PathPrefix from the
// incoming request path, then delegate to the same handler the default
// inference route uses. WebSocket detection, token injection, and the
// upstream-base-path prepend all come along for free — no logic duplicated.
func newRouteHandler(route UpstreamRoute, upstream *url.URL, config *Config) http.Handler {
	inner := inferenceHandler(upstream, config, config.WebSearch)
	stripPrefix := route.StripPrefix
	if stripPrefix == "" {
		stripPrefix = route.PathPrefix
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stripPrefix != "" {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, stripPrefix)
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
			r.URL.RawPath = ""
		}
		inner.ServeHTTP(w, r)
	})
}

// ValidateTLSConfig returns an error if the TLS configuration is incomplete
// (one of cert/key set but not the other). Both empty is valid (TLS disabled).
func ValidateTLSConfig(certFile, keyFile string) error {
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("both --tls-cert and --tls-key must be provided together")
	}
	return nil
}

// Start binds to 127.0.0.1:0, starts serving, and returns the listener.
// Callers read l.Addr() to discover the assigned port.
// When certFile and keyFile are both non-empty, the listener serves TLS.
func Start(handler http.Handler, certFile, keyFile string) (net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return Serve(l, handler, certFile, keyFile)
}

// Serve starts the proxy on an existing listener. The listener is wrapped in a
// TLS listener when certFile and keyFile are both non-empty. Returns the
// (possibly wrapped) listener that is actively serving.
func Serve(l net.Listener, handler http.Handler, certFile, keyFile string) (net.Listener, error) {
	useTLS := certFile != "" && keyFile != ""

	if useTLS {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			l.Close()
			return nil, fmt.Errorf("failed to load TLS cert/key: %w", err)
		}
		tlsListener := tls.NewListener(l, &tls.Config{
			Certificates: []tls.Certificate{cert},
		})
		go func() {
			if err := http.Serve(tlsListener, handler); err != nil {
				log.Printf("databricks-claude: proxy stopped: %v", err)
			}
		}()
		log.Printf("databricks-claude: listening on https://%s", l.Addr().String())
		return tlsListener, nil
	}

	go func() {
		if err := http.Serve(l, handler); err != nil {
			// http.Serve returns when the listener is closed; that is expected
			// during shutdown and not worth logging as an error.
			log.Printf("databricks-claude: proxy stopped: %v", err)
		}
	}()
	log.Printf("databricks-claude: listening on http://%s", l.Addr().String())
	return l, nil
}
