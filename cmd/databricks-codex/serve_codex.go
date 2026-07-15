package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
	"github.com/IceRhymers/databricks-agents/internal/core/health"
	"github.com/IceRhymers/databricks-agents/internal/core/lifecycle"
	"github.com/IceRhymers/databricks-agents/internal/core/portbind"
	"github.com/IceRhymers/databricks-agents/internal/core/proxy"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// runServeCommand dispatches `databricks-codex serve [flags]`. args is
// everything after the literal "serve" token (e.g. ["--idle-timeout", "5m"]).
//
// serve is codex's session/headless entrypoint — a leaf with no sub-subcommands
// (codex has no daemon mode: no LaunchAgent / systemd-user / schtasks
// equivalent). It does NOT route through core.Run: its lifecycle (lifecycle
// wrap + idle timeout, no child process) is a distinct sibling shape. It DOES
// share the exact config.toml writer with wrapper mode (codexSettingsPatcher),
// so both paths emit byte-identical config.toml for identical inputs.
//
// Flag parsing is driven by serveCommand.Parse so the tree is the single
// source of truth for the flag set.
func runServeCommand(args []string) error {
	parsed, err := serveCommand.Parse(args)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if parsed.Bools["help"] {
		_ = cmd.Render(os.Stdout, serveCommand, nil)
		return nil
	}

	a, err := buildServeArgs(parsed)
	if err != nil {
		return err
	}

	runServeProxy(a)
	return nil
}

// runServeProxy is the proxy-launch hook that runServeCommand invokes after
// flag parsing. Replaceable from tests so serve_cmd_test.go can spy on the
// Args struct without actually starting the proxy. In production it points at
// runServeSession.
var runServeProxy = func(a *Args) {
	runServeSession(a)
}

// buildServeArgs maps a parsed cmd.ParseResult into the Args struct that
// runServeSession consumes. The mapping is exhaustive over the flags declared
// on serveCommand in commands.go; a flag-set parity test (serve_cmd_test.go)
// catches drift in either direction.
//
// --idle-timeout is the only flag with strict parsing: an invalid duration
// is returned as a fail-loud error (matching the legacy --idle-timeout root
// flag's behaviour, including rejection of bare ints like "30" and empty
// values like "--idle-timeout=").
func buildServeArgs(r *cmd.ParseResult) (*Args, error) {
	a := &Args{
		Headless:    true,             // serve always runs headless
		IdleTimeout: 30 * time.Minute, // matches the pre-#89 default
	}

	if r.Set["idle-timeout"] {
		raw := r.Strings["idle-timeout"]
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("serve: --idle-timeout: %q is not a valid duration (use e.g. 30s, 5m, 1h)", raw)
		}
		a.IdleTimeout = d
	}

	a.Profile = r.Strings["profile"]
	a.Verbose = r.Bools["verbose"]
	a.LogFile = r.Strings["log-file"]
	a.Upstream = r.Strings["upstream"]
	a.ProxyAPIKey = r.Strings["proxy-api-key"]
	a.TLSCert = r.Strings["tls-cert"]
	a.TLSKey = r.Strings["tls-key"]
	a.NoUpdateCheck = r.Bools["no-update-check"]

	if r.Set["model"] {
		a.Model = r.Strings["model"]
		a.ModelSet = true
	}

	if r.Set["port"] {
		raw := r.Strings["port"]
		if raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("serve: --port: %q is not an integer", raw)
			}
			a.PortFlag = n
		}
	}

	return a, nil
}

// runServeSession runs the proxy in headless mode without launching codex.
// It replicates the codex resolution the wrapper path performs, but keeps the
// distinct headless lifecycle (lifecycle wrap + idle timeout, no child) rather
// than routing through core.Run. The config.toml write goes through the SAME
// codexSettingsPatcher.Patch used by wrapper mode (F2), so both paths emit
// byte-identical config.toml.
func runServeSession(a *Args) {
	// Default: discard all logs (silent wrapper).
	log.SetOutput(io.Discard)
	if a.Verbose {
		log.SetOutput(os.Stderr)
	}
	if a.LogFile != "" {
		f, err := os.OpenFile(a.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			log.SetOutput(os.Stderr)
			log.Fatalf("databricks-codex: cannot open log file %q: %v", a.LogFile, err)
		}
		defer f.Close()
		if a.Verbose {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		} else {
			log.SetOutput(f)
		}
	}

	// --- Resolve profile ---
	profileExplicit := a.Profile != ""
	profile := resolveProfile(a.Profile, loadState().Profile)
	if profileExplicit {
		saved := loadState()
		saved.Profile = profile
		if err := saveState(saved); err != nil {
			log.Printf("databricks-codex: failed to save profile: %v", err)
		} else {
			log.Printf("databricks-codex: saved profile %q for future sessions", profile)
		}
	}
	log.Printf("databricks-codex: using profile: %s", profile)

	// --- Resolve model ---
	modelExplicit := a.ModelSet
	savedForModel := loadState()
	model := resolveModel(a.Model, savedForModel.Model)
	if modelExplicit {
		savedForModel.Model = model
		if err := saveState(savedForModel); err != nil {
			log.Printf("databricks-codex: failed to save model: %v", err)
		}
	}
	log.Printf("databricks-codex: using model: %s", model)

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(profile, ""); err != nil {
		log.Fatalf("databricks-codex: auth failed: %v", err)
	}

	// --- Load state and resolve port ---
	state := loadState()
	port := resolvePort(a.PortFlag, state)
	if a.PortFlag > 0 {
		state.Port = port
		if err := saveState(state); err != nil {
			log.Printf("databricks-codex: failed to save port: %v", err)
		}
	}
	log.Printf("databricks-codex: using port: %d", port)

	// --- TLS validation ---
	if err := proxy.ValidateTLSConfig(a.TLSCert, a.TLSKey); err != nil {
		log.Fatalf("databricks-codex: %v", err)
	}

	// --- Save TLS config to state so headless-ensure can use the right scheme ---
	{
		s := loadState()
		if s.TLSCert != a.TLSCert || s.TLSKey != a.TLSKey {
			s.TLSCert = a.TLSCert
			s.TLSKey = a.TLSKey
			if err := saveState(s); err != nil {
				log.Printf("databricks-codex: failed to save TLS config: %v", err)
			}
		}
	}

	// --- Startup security checks ---
	for _, w := range proxy.SecurityChecks() {
		fmt.Fprintln(os.Stderr, w)
	}

	// --- Seed token cache ---
	tp := NewTokenProvider("", profile)
	if _, err := tp.Token(context.Background()); err != nil {
		log.Fatalf("databricks-codex: failed to fetch initial token: %v", err)
	}

	// --- Discover host + construct gateway URL ---
	host, err := DiscoverHost("", profile)
	if err != nil {
		log.Fatalf("databricks-codex: failed to discover host: %v\nRun 'databricks auth login' first", err)
	}
	log.Printf("databricks-codex: discovered host: %s", host)

	gatewayURL := a.Upstream
	if gatewayURL == "" {
		gatewayURL = ConstructGatewayURL(host)
	}
	log.Printf("databricks-codex: gateway URL: %s", gatewayURL)

	// --- OTEL tables (read-only over state) ---
	saved := loadState()
	otel, otelMetricsTable, otelLogsTable := resolveOtel(saved)
	if otelMetricsTable != "" {
		log.Printf("databricks-codex: using saved otel-metrics-table: %s", otelMetricsTable)
	}
	if otelLogsTable != "" {
		log.Printf("databricks-codex: using saved otel-logs-table: %s", otelLogsTable)
	}
	// serve mode deliberately skips the LookPath("codex") guard — it never
	// launches codex.

	// --- Determine OTEL upstream ---
	otelUpstream := ""
	if otel {
		otelUpstream = host + "/api/2.0/otel"
		log.Printf("databricks-codex: OTEL enabled, upstream: %s", otelUpstream)
	}

	// --- Bind proxy port ---
	listener, isOwner, err := portbind.Bind("databricks-codex", port)
	if err != nil {
		log.Fatalf("databricks-codex: %v", err)
	}

	scheme := "http"
	if a.TLSCert != "" && a.TLSKey != "" {
		scheme = "https"
		fmt.Fprintln(os.Stderr, "databricks-codex: TLS enabled")
	}
	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, portbind.ListenerPort(listener, port))

	// --- Proxy handler (needed by owner and recovery goroutine) ---
	if a.ProxyAPIKey != "" {
		fmt.Fprintln(os.Stderr, "databricks-codex: proxy API key authentication enabled")
	}
	proxyHandler, err := NewProxyServer(&ProxyConfig{
		InferenceUpstream: gatewayURL,
		OTELUpstream:      otelUpstream,
		UCMetricsTable:    otelMetricsTable,
		UCLogsTable:       otelLogsTable,
		TokenProvider:     tp,
		Verbose:           a.Verbose,
		APIKey:            a.ProxyAPIKey,
		TLSCertFile:       a.TLSCert,
		TLSKeyFile:        a.TLSKey,
		ToolName:          "databricks-codex",
		Version:           Version,
	})
	if err != nil {
		log.Fatalf("databricks-codex: failed to create proxy: %v", err)
	}

	// --- Reference counting ---
	// Headless mode does NOT acquire (the spawning session already accounted
	// for this proxy via headless.Ensure); it wraps with /shutdown + idle
	// timeout and releases on exit.
	refcountPath := refcount.PathForPort(".databricks-codex-sessions", port)
	doneCh := make(chan struct{})
	proxyHandler = lifecycle.WrapWithLifecycle(lifecycle.Config{
		Inner:        proxyHandler,
		RefcountPath: refcountPath,
		IsOwner:      isOwner,
		IdleTimeout:  a.IdleTimeout,
		APIKey:       a.ProxyAPIKey,
		DoneCh:       doneCh,
		LogPrefix:    "databricks-codex",
	})

	// --- Start proxy if we own the port ---
	if isOwner {
		servedLn, err := proxy.Serve(listener, proxyHandler, a.TLSCert, a.TLSKey)
		if err != nil {
			log.Fatalf("databricks-codex: failed to start proxy: %v", err)
		}
		listener = servedLn
		log.Printf("databricks-codex: proxy owner on :%d", port)
	} else {
		log.Printf("databricks-codex: joining existing proxy on :%d", port)
		go health.WatchProxy(port, proxyHandler, a.TLSCert, a.TLSKey, "databricks-codex", nil)
	}
	log.Printf("databricks-codex: proxy on %s (owner=%v)", proxyURL, isOwner)

	// --- Write config.toml through the SHARED writer (F2 byte-parity) ---
	patcher := newCodexPatcher(model, modelExplicit, otelMetricsTable, otelLogsTable, otel)
	if err := patcher.Patch(profile2Request(proxyURL)); err != nil {
		log.Printf("databricks-codex: warning: failed to write config.toml: %v", err)
	}

	// --- Headless mode: print proxy URL and wait for signal ---
	runHeadless(proxyURL, listener, isOwner, refcountPath, doneCh)
}

// profile2Request builds the neutral profile.PatchRequest for the shared
// codexSettingsPatcher.Patch. Only ProxyURL is load-bearing for codex (the
// patcher computes base_url + OTEL endpoints from it); the other fields are
// unused by codex's config.toml writer.
func profile2Request(proxyURL string) profile.PatchRequest {
	return profile.PatchRequest{ProxyURL: proxyURL}
}

// runHeadless runs the proxy without launching a codex child process.
// It prints the proxy URL to stdout, then blocks until SIGINT/SIGTERM
// or until doneCh is closed (by /shutdown or idle timeout). The watchProxy
// goroutine (for non-owner sessions) is already started before this is called.
func runHeadless(proxyURL string, ln net.Listener, isOwner bool, refcountPath string, doneCh chan struct{}) {
	fmt.Printf("PROXY_URL=%s\n", proxyURL)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		signal.Stop(sigCh)
	case <-doneCh:
		// Triggered by /shutdown or idle timeout.
	}

	// Release refcount. If /shutdown already released, Release floors at 0.
	n, _ := refcount.Release(refcountPath)
	if n == 0 && isOwner {
		ln.Close()
	}
}
