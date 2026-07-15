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
	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// runServeCommand implements `databricks-opencode serve ...`. args is
// everything after the literal "serve" token. Replaces the removed
// --headless / --idle-timeout root flags with a discoverable subcommand.
// Mirrors databricks-claude / databricks-codex serve with the same
// deliberately smaller scope: no daemon mode and no install/uninstall/status —
// `serve` is the session-scoped lifecycle the removed --headless flag drove.
//
// It does NOT route through core.Run: its lifecycle (lifecycle wrap + idle
// timeout, no child process) is a distinct sibling shape. It DOES share the
// exact opencode.json writer with wrapper mode (opencodeSettingsPatcher), so
// both paths emit byte-identical opencode.json for identical inputs.
func runServeCommand(args []string) {
	r, _ := serveCommand.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, serveCommand, nil)
		os.Exit(0)
	}

	// Reject sub-subcommand-like positionals so a typo like `serve foo` doesn't
	// silently boot the proxy. install/uninstall/status are DEFERRED for
	// opencode — surfacing them as "unknown" keeps the error story honest.
	for _, p := range r.Positional {
		fmt.Fprintf(os.Stderr, "databricks-opencode: serve: unknown argument %q\n\n", p)
		_ = cmd.Render(os.Stderr, serveCommand, nil)
		os.Exit(1)
	}

	idle, err := parseServeIdleTimeout(r.Strings["idle-timeout"], r.Set["idle-timeout"])
	if err != nil {
		fmt.Fprintf(os.Stderr, "databricks-opencode: serve: %v\n", err)
		os.Exit(1)
	}

	port := 0
	if v := r.Strings["port"]; v != "" {
		port, _ = strconv.Atoi(v)
	}

	a := &Args{
		Verbose:       r.Bools["verbose"],
		Model:         r.Strings["model"],
		Upstream:      r.Strings["upstream"],
		LogFile:       r.Strings["log-file"],
		Profile:       r.Strings["profile"],
		ProxyAPIKey:   r.Strings["proxy-api-key"],
		TLSCert:       r.Strings["tls-cert"],
		TLSKey:        r.Strings["tls-key"],
		Port:          port,
		Headless:      true,
		IdleTimeout:   idle,
		NoUpdateCheck: r.Bools["no-update-check"],
	}
	runServeSession(a)
}

// defaultServeIdleTimeout is the default idle timeout when `serve` is invoked
// without --idle-timeout. Exposed as a package var so tests can pin the
// constant without re-hardcoding it in expectations.
var defaultServeIdleTimeout = 30 * time.Minute

// parseServeIdleTimeout parses the --idle-timeout value with the "bare number =
// minutes" grammar layered on top of time.ParseDuration:
//
//   - empty / unset → defaultServeIdleTimeout (30m)
//   - bare integer (e.g. "5", "30", "0") → N minutes
//   - "0" (any form parsing to zero) → idle timeout disabled
//   - any time.Duration string ("30s", "5m", "1h") → that duration
//   - anything else → error
func parseServeIdleTimeout(raw string, present bool) (time.Duration, error) {
	if !present || raw == "" {
		return defaultServeIdleTimeout, nil
	}
	// Bare integer: interpret as minutes.
	if n, err := strconv.Atoi(raw); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("--idle-timeout: %q is negative; use 0 to disable or a positive value", raw)
		}
		return time.Duration(n) * time.Minute, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("--idle-timeout: %q is not a valid duration (use e.g. 30s, 5m, 1h, or a bare number for minutes)", raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("--idle-timeout: %q is negative; use 0 to disable or a positive value", raw)
	}
	return d, nil
}

// runServeSession runs the proxy in headless mode without launching opencode.
// It replicates the opencode resolution the wrapper path performs, but keeps
// the distinct headless lifecycle (lifecycle wrap + idle timeout, no child)
// rather than routing through core.Run. The opencode.json write goes through
// the SAME opencodeSettingsPatcher.Patch used by wrapper mode, so both paths
// emit byte-identical opencode.json.
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
			log.Fatalf("databricks-opencode: cannot open log file %q: %v", a.LogFile, err)
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
			log.Printf("databricks-opencode: failed to save profile: %v", err)
		} else {
			log.Printf("databricks-opencode: saved profile %q for future sessions", profile)
		}
	}
	log.Printf("databricks-opencode: using profile: %s", profile)

	// --- Resolve model ---
	modelExplicit := a.Model != ""
	savedForModel := loadState()
	model := resolveModel(a.Model, savedForModel.Model)
	if modelExplicit {
		savedForModel.Model = model
		if err := saveState(savedForModel); err != nil {
			log.Printf("databricks-opencode: failed to save model: %v", err)
		}
	}
	log.Printf("databricks-opencode: using model: %s", model)

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(profile, ""); err != nil {
		log.Fatalf("databricks-opencode: auth failed: %v", err)
	}

	// --- Load state and resolve port ---
	state := loadState()
	port := resolvePort(a.Port, state)
	if a.Port > 0 {
		state.Port = port
		if err := saveState(state); err != nil {
			log.Printf("databricks-opencode: failed to save port: %v", err)
		}
	}
	log.Printf("databricks-opencode: using port: %d", port)

	// --- TLS validation ---
	if err := proxy.ValidateTLSConfig(a.TLSCert, a.TLSKey); err != nil {
		log.Fatalf("databricks-opencode: %v", err)
	}

	// --- Save TLS config to state so headless-ensure can use the right scheme ---
	{
		s := loadState()
		if s.TLSCert != a.TLSCert || s.TLSKey != a.TLSKey {
			s.TLSCert = a.TLSCert
			s.TLSKey = a.TLSKey
			if err := saveState(s); err != nil {
				log.Printf("databricks-opencode: failed to save TLS config: %v", err)
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
		log.Fatalf("databricks-opencode: failed to fetch initial token: %v", err)
	}

	// --- Discover host + construct gateway URLs ---
	host, err := DiscoverHost("", profile)
	if err != nil {
		log.Fatalf("databricks-opencode: failed to discover host: %v\nRun 'databricks auth login' first", err)
	}
	log.Printf("databricks-opencode: discovered host: %s", host)

	gatewayURL := a.Upstream
	if gatewayURL == "" {
		gatewayURL = ConstructGatewayURL(host)
	}
	log.Printf("databricks-opencode: gateway URL: %s", gatewayURL)

	geminiGatewayURL := ConstructGeminiGatewayURL(host)
	log.Printf("databricks-opencode: gemini gateway URL: %s", geminiGatewayURL)
	// serve mode deliberately skips the LookPath("opencode") guard — it never
	// launches opencode.

	// --- Bind proxy port ---
	listener, isOwner, err := portbind.Bind("databricks-opencode", port)
	if err != nil {
		log.Fatalf("databricks-opencode: failed to bind port %d: %v", port, err)
	}

	// --- Proxy handler (needed by owner and takeover watcher) ---
	if a.ProxyAPIKey != "" {
		fmt.Fprintln(os.Stderr, "databricks-opencode: proxy API key authentication enabled")
	}
	proxyHandler, err := NewProxyServer(&ProxyConfig{
		InferenceUpstream: gatewayURL,
		GeminiUpstream:    geminiGatewayURL,
		TokenProvider:     tp,
		Verbose:           a.Verbose,
		APIKey:            a.ProxyAPIKey,
		TLSCertFile:       a.TLSCert,
		TLSKeyFile:        a.TLSKey,
	})
	if err != nil {
		log.Fatalf("databricks-opencode: failed to create proxy server: %v", err)
	}

	// --- Wrap with /shutdown + idle timeout (no refcount) ---
	// opencode has no exit hook to release a refcount, so serve mode does not
	// use one — the proxy relies on its idle timeout for shutdown. promoteCh
	// lets a non-owner be promoted to owner via the health watcher's onTakeover
	// callback so /shutdown can fire correctly after a takeover.
	doneCh := make(chan struct{})
	promoteCh := make(chan struct{})
	proxyHandler = lifecycle.WrapWithLifecycle(lifecycle.Config{
		Inner:        proxyHandler,
		RefcountPath: "",
		IsOwner:      isOwner,
		PromoteCh:    promoteCh,
		IdleTimeout:  a.IdleTimeout,
		APIKey:       a.ProxyAPIKey,
		DoneCh:       doneCh,
		LogPrefix:    "databricks-opencode",
	})

	// --- Start proxy if we own the port ---
	if isOwner {
		servedLn, err := proxy.Serve(listener, proxyHandler, a.TLSCert, a.TLSKey)
		if err != nil {
			log.Fatalf("databricks-opencode: failed to start proxy: %v", err)
		}
		listener = servedLn
		log.Printf("databricks-opencode: proxy owner on :%d", port)
	} else {
		log.Printf("databricks-opencode: joining existing proxy on :%d", port)
		onTakeover := func() {
			if promoteCh != nil {
				close(promoteCh)
			}
		}
		go health.WatchProxy(port, proxyHandler, a.TLSCert, a.TLSKey, "databricks-opencode", onTakeover)
	}

	scheme := "http"
	if a.TLSCert != "" && a.TLSKey != "" {
		scheme = "https"
	}
	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, portbind.ListenerPort(listener, port))
	log.Printf("databricks-opencode: local proxy %s -> %s", proxyURL, gatewayURL)

	// --- Write opencode.json through the SHARED writer (byte-parity) ---
	// warn-not-fatal: a config-write failure must not tear down a serving proxy
	// (matches the legacy headless EnsureConfig semantics).
	patcher := newOpencodePatcher(model, modelExplicit, a.ProxyAPIKey)
	if err := patcher.Patch(servePatchRequest(proxyURL)); err != nil {
		log.Printf("databricks-opencode: WARNING: failed to configure opencode: %v", err)
	}

	// --- Print proxy URL and block until signal or shutdown ---
	runHeadless(proxyURL, listener, isOwner, doneCh)
}

// servePatchRequest builds the neutral profile.PatchRequest for the shared
// opencodeSettingsPatcher.Patch. Only ProxyURL is load-bearing for opencode
// (the patcher computes the provider baseURLs from it). Kept a file-scope
// helper so the `profile` package identifier isn't shadowed by the local
// `profile` string inside runServeSession.
func servePatchRequest(proxyURL string) profile.PatchRequest {
	return profile.PatchRequest{ProxyURL: proxyURL}
}

// runHeadless runs the proxy without launching an opencode child process.
// It prints the proxy URL to stdout, then blocks until SIGINT/SIGTERM
// or until doneCh is closed (by /shutdown or idle timeout).
func runHeadless(proxyURL string, ln net.Listener, isOwner bool, doneCh chan struct{}) {
	fmt.Printf("PROXY_URL=%s\n", proxyURL)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		signal.Stop(sigCh)
	case <-doneCh:
		// Triggered by /shutdown or idle timeout.
	}

	if isOwner {
		ln.Close()
	}
}
