package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/IceRhymers/databricks-agents/internal/core/portbind"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
	"github.com/IceRhymers/databricks-agents/pkg/authcheck"
	"github.com/IceRhymers/databricks-agents/pkg/health"
	"github.com/IceRhymers/databricks-agents/pkg/lifecycle"
	"github.com/IceRhymers/databricks-agents/pkg/proxy"
)

// runServeSession implements `databricks-claude serve --session-mode`. The
// session-scoped lifecycle (refcount, /shutdown, idle-timeout, settings.json
// restore-on-exit, fallback-port bind) was the --headless root flag prior to
// #174 — see the plan's Background section for the field-by-field move
// rationale.
//
// Differences from runServe's daemon body:
//   - portbind.Bind (fallback-aware) instead of net.Listen (exclusive).
//   - authcheck.EnsureAuthenticated (interactive) instead of IsAuthenticated.
//   - Refcount acquired here; sessions manage it via hooks. The daemon body
//     intentionally does NOT acquire refcount.
//   - lifecycle.WrapWithLifecycle adds /shutdown + idle timeout.
//   - bootstrapSettings is best-effort (warn-not-fatal) so session mode
//     keeps starting even when settings.json is read-only — matches
//     pre-#174 --headless semantics.
//   - PROXY_URL=... is printed to real stdout (NOT redirected) so IDE
//     extensions can capture it. The daemon redirects stdout for log
//     hygiene; this path explicitly does not.
func runServeSession(args []string) {
	f := parseServeFlags(args)

	// Logging setup mirrors runServe's daemon body, with one difference:
	// session mode does NOT redirect stdout. The PROXY_URL=... line printed
	// at the tail must reach real stdout for IDE extensions to read it.
	log.SetOutput(io.Discard)
	var logWriters []io.Writer
	if f.verbose {
		logWriters = append(logWriters, os.Stderr)
	}
	if f.logFile != "" {
		// Session mode opens log files with O_TRUNC to match the legacy
		// --headless / wrapper semantics (each session gets a fresh log).
		// Use os.O_CREATE|os.O_WRONLY|os.O_TRUNC, NOT openLogFile (which
		// is O_APPEND for the daemon path).
		lf, err := os.OpenFile(f.logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			log.SetOutput(os.Stderr)
			log.Fatalf("databricks-claude: serve --session-mode: cannot open log file %q: %v", f.logFile, err)
		}
		// No defer close — process runs until /shutdown or signal.
		logWriters = append(logWriters, lf)
	}
	switch len(logWriters) {
	case 1:
		log.SetOutput(logWriters[0])
	case 2:
		log.SetOutput(io.MultiWriter(logWriters...))
	}

	// Resolve port: flag → state → default.
	st := loadState()
	port := resolvePort(f.port, st)

	// Resolve profile: flag → state → "DEFAULT". (No MDM tier for session
	// mode — matches pre-#174 --headless behaviour where the wrapper read
	// only flag → state → "DEFAULT".)
	resolvedProfile := f.profile
	if resolvedProfile == "" && st.Profile != "" {
		resolvedProfile = st.Profile
	}
	if resolvedProfile == "" {
		resolvedProfile = "DEFAULT"
	}

	// Auth: --session-mode CAN prompt (it's interactive — the IDE-extension
	// caller has a real tty unless something has gone very wrong).
	if err := authcheck.EnsureAuthenticated(resolvedProfile, ""); err != nil {
		log.Fatalf("databricks-claude: serve --session-mode: auth failed: %v", err)
	}

	// Startup security checks (legacy --headless surfaced these on stderr).
	for _, w := range proxy.SecurityChecks() {
		fmt.Fprintln(os.Stderr, w)
	}

	// Resolve upstream + OTEL endpoints. --upstream takes priority; otherwise
	// discover from the profile.
	databricksHost := readDatabricksCfgHost(resolvedProfile)
	inferenceUpstream := f.upstream
	if inferenceUpstream == "" {
		host, err := DiscoverHost(resolvedProfile, "")
		if err != nil {
			log.Fatalf("databricks-claude: serve --session-mode: failed to discover host for profile %q: %v\n"+
				"Run 'databricks auth login --profile %s' first", resolvedProfile, err, resolvedProfile)
		}
		databricksHost = host
		inferenceUpstream = ConstructGatewayURL(host)
	} else if databricksHost == "" {
		// Try to discover host for OTEL even when upstream is explicit.
		if h, err := DiscoverHost(resolvedProfile, ""); err == nil {
			databricksHost = h
		}
	}
	otelUpstream := inferenceUpstream
	if databricksHost != "" {
		otelUpstream = databricksHost + "/api/2.0/otel"
	}

	// Resolve OTEL tables: state file is the single tier in session mode
	// (matches pre-#174 --headless behaviour; the wrapper read these from
	// settings.json env or state). Daemon mode adds an MDM tier; session
	// mode does not.
	tableState := loadState()
	metricsTable := tableState.OtelMetricsTable
	logsTable := tableState.OtelLogsTable
	tracesTable := tableState.OtelTracesTable

	// Migrate any settings.json-only OTEL tables to state on first read so
	// pre-#172 installs continue to work. Mirrors the wrapper migration in
	// main.go.
	if homeDir, err := os.UserHomeDir(); err == nil {
		settingsDoc, _ := readSettingsJSON(homeDir + "/.claude/settings.json")
		env := envBlock(settingsDoc)
		mutated := false
		if v, _ := env["CLAUDE_OTEL_UC_METRICS_TABLE"].(string); v != "" && metricsTable == "" {
			metricsTable = v
			tableState.OtelMetricsTable = v
			mutated = true
		}
		if v, _ := env["CLAUDE_OTEL_UC_LOGS_TABLE"].(string); v != "" && logsTable == "" {
			logsTable = v
			tableState.OtelLogsTable = v
			mutated = true
		}
		if v, _ := env["CLAUDE_OTEL_UC_TRACES_TABLE"].(string); v != "" && tracesTable == "" {
			tracesTable = v
			tableState.OtelTracesTable = v
			mutated = true
		}
		if mutated {
			if err := saveState(tableState); err != nil {
				log.Printf("databricks-claude: serve --session-mode: warning: could not persist OTel tables to state: %v", err)
			}
		}
	}

	// Validate TLS config.
	if err := proxy.ValidateTLSConfig(f.tlsCert, f.tlsKey); err != nil {
		log.Fatalf("databricks-claude: serve --session-mode: %v", err)
	}

	// Seed token cache.
	tp := NewTokenProvider(resolvedProfile, "")
	if _, err := tp.Token(context.Background()); err != nil {
		log.Fatalf("databricks-claude: serve --session-mode: failed to fetch initial token for profile %q: %v", resolvedProfile, err)
	}

	// Bind port (fallback-aware).
	ln, isOwner, err := portbind.Bind("databricks-claude", port)
	if err != nil {
		log.Fatalf("databricks-claude: serve --session-mode: %v", err)
	}

	scheme := "http"
	if f.tlsCert != "" && f.tlsKey != "" {
		scheme = "https"
		fmt.Fprintln(os.Stderr, "databricks-claude: TLS enabled")
	}
	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, portbind.ListenerPort(ln, port))

	// Build the proxy handler via the unified factory. Session mode wires
	// APIKey / TLS into the proxy.Config; daemon mode never reaches that
	// branch by construction.
	r := serveResolved{
		profile:           resolvedProfile,
		inferenceUpstream: inferenceUpstream,
		otelUpstream:      otelUpstream,
		metricsTable:      metricsTable,
		logsTable:         logsTable,
		tracesTable:       tracesTable,
		tp:                tp,
		verbose:           f.verbose,
		apiKey:            f.apiKey,
		tlsCert:           f.tlsCert,
		tlsKey:            f.tlsKey,
	}
	cfg := buildServeProxyConfig(st, r, serveModeSession)

	if f.apiKey != "" {
		fmt.Fprintln(os.Stderr, "databricks-claude: proxy API key authentication enabled")
	}

	handler, err := proxy.NewServer(cfg)
	if err != nil {
		ln.Close()
		log.Fatalf("databricks-claude: serve --session-mode: failed to create proxy: %v", err)
	}
	handler = proxy.RecoveryHandler(handler)

	// Reference counting. Session mode acquires here so a "lone" --session-mode
	// invocation (no hooks) self-balances ensure/release. Hooks-driven sessions
	// also reach this path; the per-port refcount is shared, so multiple
	// sessions stack correctly.
	refcountPath := refcount.PathForPort(".databricks-claude-sessions", port)
	if err := refcount.Acquire(refcountPath); err != nil {
		log.Printf("databricks-claude: serve --session-mode: refcount acquire warning: %v", err)
	}

	// Lifecycle wrap: adds /shutdown route + idle timeout. promoteCh is
	// closed when we win the health-watcher election (non-owner case) so
	// /shutdown can correctly exit after takeover.
	doneCh := make(chan struct{})
	var promoteCh chan struct{}
	if !isOwner {
		promoteCh = make(chan struct{})
	}
	handler = lifecycle.WrapWithLifecycle(lifecycle.Config{
		Inner:        handler,
		RefcountPath: refcountPath,
		IsOwner:      isOwner,
		PromoteCh:    promoteCh,
		IdleTimeout:  f.idleTimeout,
		APIKey:       f.apiKey,
		DoneCh:       doneCh,
		LogPrefix:    "databricks-claude",
	})

	// Start serving (or watch for owner death + take over).
	if isOwner {
		go func() {
			srv := &http.Server{Handler: handler}
			if f.tlsCert != "" && f.tlsKey != "" {
				if err := srv.ServeTLS(ln, f.tlsCert, f.tlsKey); err != nil && err != http.ErrServerClosed {
					log.Printf("databricks-claude: serve --session-mode: proxy serve error: %v", err)
				}
			} else {
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Printf("databricks-claude: serve --session-mode: proxy serve error: %v", err)
				}
			}
		}()
	} else {
		onTakeover := func() {
			if promoteCh != nil {
				close(promoteCh)
			}
		}
		go health.WatchProxy(port, handler, f.tlsCert, f.tlsKey, "databricks-claude", onTakeover)
	}

	// settings.json bootstrap is best-effort in session mode — matches the
	// pre-#174 --headless behaviour where the wrapper warned (not fatal'd)
	// on a write failure. The proxy stays up so the IDE extension can use
	// PROXY_URL even if ~/.claude is read-only.
	otelEnv := buildSessionOTELEnv(proxyURL, metricsTable, logsTable, tracesTable)
	if err := bootstrapSettings(f.port, resolvedProfile, proxyURL, otelEnv); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude: serve --session-mode: warning: config write failed: %v\n", err)
	}

	log.Printf("databricks-claude: serve --session-mode: proxy on %s (owner=%v), profile=%s, upstream=%s",
		proxyURL, isOwner, resolvedProfile, inferenceUpstream)
	log.Printf("databricks-claude: serve --session-mode: with-websearch=%t idle-timeout=%s",
		cfg.WebSearch.Enabled, f.idleTimeout)

	runServeSessionLoop(proxyURL, ln, isOwner, refcountPath, doneCh)
}

// runServeSessionLoop blocks on SIGINT/SIGTERM or doneCh and tears down. Was
// `runHeadless` in main.go before #174 — same body, renamed to reflect the
// new entrypoint.
func runServeSessionLoop(proxyURL string, ln net.Listener, isOwner bool, refcountPath string, doneCh chan struct{}) {
	fmt.Printf("PROXY_URL=%s\n", proxyURL)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		signal.Stop(sigCh)
	case <-doneCh:
		// /shutdown or idle timeout fired.
	}

	n, _ := refcount.Release(refcountPath)
	if n == 0 && isOwner {
		ln.Close()
	}
}

// buildSessionOTELEnv returns the OTEL env keys that get written into
// settings.json when the session-mode proxy starts. Mirrors main.go's
// per-table emission semantics: each signal's keys appear iff its UC table
// is configured; CLAUDE_CODE_ENABLE_TELEMETRY=1 is set when any signal is
// active.
func buildSessionOTELEnv(proxyURL, metricsTable, logsTable, tracesTable string) map[string]string {
	otelEnv := map[string]string{}
	if metricsTable != "" {
		otelEnv["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"] = proxyURL + "/otel/v1/metrics"
		otelEnv["OTEL_EXPORTER_OTLP_METRICS_HEADERS"] = "content-type=application/x-protobuf"
		otelEnv["OTEL_METRICS_EXPORTER"] = "otlp"
		otelEnv["OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"] = "http/protobuf"
		otelEnv["OTEL_METRIC_EXPORT_INTERVAL"] = "10000"
		otelEnv["CLAUDE_OTEL_UC_METRICS_TABLE"] = metricsTable
	}
	if logsTable != "" {
		otelEnv["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] = proxyURL + "/otel/v1/logs"
		otelEnv["OTEL_EXPORTER_OTLP_LOGS_HEADERS"] = "content-type=application/x-protobuf"
		otelEnv["OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"] = "http/protobuf"
		otelEnv["OTEL_LOGS_EXPORTER"] = "otlp"
		otelEnv["OTEL_LOGS_EXPORT_INTERVAL"] = "5000"
		otelEnv["CLAUDE_OTEL_UC_LOGS_TABLE"] = logsTable
	}
	if tracesTable != "" {
		otelEnv["CLAUDE_CODE_ENHANCED_TELEMETRY_BETA"] = "1"
		otelEnv["OTEL_TRACES_EXPORTER"] = "otlp"
		otelEnv["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = proxyURL + "/otel/v1/traces"
		otelEnv["OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"] = "http/protobuf"
		otelEnv["OTEL_TRACES_EXPORT_INTERVAL"] = "5000"
		otelEnv["CLAUDE_OTEL_UC_TRACES_TABLE"] = tracesTable
	}
	if metricsTable != "" || logsTable != "" || tracesTable != "" {
		otelEnv["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	}
	// Always include the model routing / custom header keys so a fresh
	// session-mode start matches what the wrapper writes. databricksFullSetupEnv
	// lives in main.go.
	// launchModelRouting reads persisted state (cheap file read, no network) — the no-hot-path-discovery invariant.
	for k, v := range databricksFullSetupEnv(launchModelRouting(loadState())) {
		otelEnv[k] = v
	}
	return otelEnv
}
