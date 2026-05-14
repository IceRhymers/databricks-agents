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
	"strconv"
	"syscall"

	"github.com/IceRhymers/databricks-claude/internal/cmd"
	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
	"github.com/IceRhymers/databricks-claude/pkg/mdmprofile"
	"github.com/IceRhymers/databricks-claude/pkg/proxy"
	"github.com/IceRhymers/databricks-claude/pkg/websearch"
)

// serveResolved bundles the inputs needed to build the daemon's proxy.Config.
// File-private — lives in serve.go so buildServeProxyConfig is testable in
// isolation without re-invoking the full runServe resolution chain.
type serveResolved struct {
	profile           string
	inferenceUpstream string
	otelUpstream      string
	metricsTable      string
	logsTable         string
	tracesTable       string
	tp                proxy.TokenSource
	verbose           bool
}

// buildServeProxyConfig constructs the daemon's *proxy.Config from persistent
// state and resolved runServe inputs. Extracted so the WebSearch wiring is
// covered by serve_test.go assertions and so a future refactor that drops
// the WebSearch field would fail those tests instead of silently regressing
// the workaround.
//
// Fail-soft on a bad WebSearchBackend value: emits a dual stderr+log error
// (so it lands in the LaunchAgent stderr log AND any --log-file) and
// disables websearch for this daemon run. The daemon's primary duty is OAuth
// refresh; a malformed state value must not crash-loop launchd/systemd.
func buildServeProxyConfig(st persistentState, r serveResolved) *proxy.Config {
	withWebSearch := st.WithWebSearch
	wsBackend := st.WebSearchBackend
	wsBudget := st.WebSearchFetchBudget
	if wsBackend == "" {
		wsBackend = "duckduckgo"
	}
	if wsBudget <= 0 {
		wsBudget = 100 * 1024
	}

	var wsBackendImpl websearch.Backend
	var wsRobots websearch.RobotsChecker
	if withWebSearch {
		// Unconditional stderr write — matches main.go:724-729 wrapper banner.
		// serve.go redirects os.Stdout to os.Stderr (see runServe) so this
		// always lands in the LaunchAgent / systemd stderr log regardless
		// of --verbose / --log-file gating.
		fmt.Fprintln(os.Stderr, "databricks-claude: --with-websearch is a workaround. Anthropic's native")
		fmt.Fprintln(os.Stderr, "  web_search and web_fetch tools are not yet supported by Databricks FMAPI.")
		fmt.Fprintf(os.Stderr, "  This proxy fulfills them locally via backend=%q (per-fetch budget=%d bytes).\n", wsBackend, wsBudget)
		fmt.Fprintln(os.Stderr, "  Limitations: no JavaScript rendering; robots.txt enforced; headless only.")
		fmt.Fprintln(os.Stderr, "  This flag will be removed (with one release of deprecation warning) when")
		fmt.Fprintln(os.Stderr, "  Databricks ships native server-side tool support.")

		b, err := buildWebSearchBackend(wsBackend)
		if err != nil {
			// Fail-soft. Dual signal: stderr direct write (always visible
			// in the LaunchAgent log via the os.Stdout=os.Stderr redirect)
			// AND log.Printf (lands in --log-file when set). Daemon does
			// NOT log.Fatalf — that would crash-loop under launchd/systemd
			// and bring down OAuth refresh, which is the daemon's main job.
			msg := fmt.Sprintf("databricks-claude: serve: websearch backend build failed: %v — websearch DISABLED for this daemon run", err)
			fmt.Fprintln(os.Stderr, msg)
			log.Printf("%s", msg)
			withWebSearch = false
		} else {
			wsBackendImpl = b
			wsRobots = &websearch.Robots{}
		}
	}

	return &proxy.Config{
		InferenceUpstream: r.inferenceUpstream,
		OTELUpstream:      r.otelUpstream,
		UCMetricsTable:    r.metricsTable,
		UCLogsTable:       r.logsTable,
		UCTracesTable:     r.tracesTable,
		TokenSource:       r.tp,
		Verbose:           r.verbose,
		ToolName:          "databricks-claude",
		Version:           Version,
		Daemon:            true,
		Profile:           r.profile,
		WebSearch: proxy.WebSearchSettings{
			Enabled:     withWebSearch,
			Backend:     wsBackendImpl,
			Robots:      wsRobots,
			FetchBudget: wsBudget,
		},
	}
}

const mdmDomain = "com.icerhymers.databricks-claude"

// serveFlags holds parsed flags from the serve subcommand arg list.
type serveFlags struct {
	port            int
	logFile         string
	verbose         bool
	profile         string
	metricsTable    string
	logsTable       string
	tracesTable     string
	metricsTableSet bool
	logsTableSet    bool
	tracesTableSet  bool
}

// parseServeFlags maps serveCommand.Parse(args) into the typed serveFlags
// struct that downstream resolution code consumes. The flag set is the single
// source of truth declared on serveCommand in commands.go (#171); this
// function is a pure projection. Tolerance for unknown flags is preserved by
// cmd.Parse (it routes unknowns to Positional, which we discard here — same
// behaviour as the pre-#171 hand-rolled scanner).
func parseServeFlags(args []string) serveFlags {
	r, _ := serveCommand.Parse(args)
	var f serveFlags
	if v, ok := r.Strings["port"]; ok {
		f.port, _ = strconv.Atoi(v)
	}
	f.logFile = r.Strings["log-file"]
	f.verbose = r.Bools["verbose"]
	f.profile = r.Strings["profile"]
	f.metricsTable = r.Strings["otel-metrics-table"]
	f.metricsTableSet = r.Set["otel-metrics-table"]
	f.logsTable = r.Strings["otel-logs-table"]
	f.logsTableSet = r.Set["otel-logs-table"]
	f.tracesTable = r.Strings["otel-traces-table"]
	f.tracesTableSet = r.Set["otel-traces-table"]
	return f
}

// serveHelpRequested returns true when args contain --help or -h for the
// `serve` (NOT serve install/uninstall/status) command itself. Driven off
// the tree so its known-flag set stays consistent with completion + help.
func serveHelpRequested(args []string) bool {
	r, _ := serveCommand.Parse(args)
	return r.Bools["help"]
}

// resolveTableFromChain resolves one OTEL table following flag → state → MDM → empty.
// stateVal must already be sentinel-guarded by the caller (empty string = unset).
func resolveTableFromChain(flagVal string, flagSet bool, stateVal string, mdmKey string, mdmReadFn func(string, string) (string, error)) string {
	if flagSet {
		return flagVal
	}
	if stateVal != "" {
		return stateVal
	}
	if v, err := mdmReadFn(mdmDomain, mdmKey); err == nil && v != "" {
		return v
	}
	return ""
}

// openLogFile opens path for appending, creating it if absent.
// Returns the file and any error.
func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

// shouldPersistOTELTable returns true when the OTEL table state writer should
// update state from a resolved value. Three conditions must hold:
//  1. The flag was explicitly set on this invocation (`flagSet`). State must
//     not be overwritten by a value that resolved from state itself or MDM —
//     only explicit user intent persists.
//  2. The resolved value is non-empty. Empty is the sentinel for "unset" and
//     must never be persisted to state, or it would shadow the MDM tier on
//     subsequent runs (the same footgun caught by databricks-claude PR #149
//     for state.Profile).
//  3. The resolved value differs from what's already in state. Avoids
//     unnecessary state-file writes when the flag matches existing state.
//
// All three conditions must be satisfied. This helper centralizes the guard so
// future writers cannot accidentally drop one of the conditions.
func shouldPersistOTELTable(flagSet bool, resolved, stateVal string) bool {
	return flagSet && resolved != "" && stateVal != resolved
}

// runServe implements the `databricks-claude serve` subcommand.
//
// Flags:
//
//	--port int                   Proxy listen port (default: 49153)
//	--profile string             Databricks config profile
//	--log-file string            Append-only log file (daemon preserves history)
//	--verbose, -v                Enable debug logging to stderr
//	--otel-metrics-table string  UC table for OTEL metrics (flag > state > MDM > empty)
//	--otel-logs-table string     UC table for OTEL logs   (flag > state > MDM > empty)
//	--otel-traces-table string   UC table for OTEL traces (flag > state > MDM > empty)
func runServe(args []string) {
	// Dispatch sub-subcommands BEFORE the stdout redirect and the --help
	// short-circuit. Two reasons:
	//   1. Sub-subcommand help (serve install --help, serve status --help)
	//      must reach printServe{Install,Uninstall,Status}Help, not the
	//      parent printServeHelp via the --help short-circuit below.
	//   2. `serve status` is a one-shot user-facing command — its output
	//      must go to real stdout so users can pipe / grep / parse it.
	//      The os.Stdout = os.Stderr redirect below is for the long-lived
	//      daemon path only, where it protects the LaunchAgent stdout log
	//      from SDKs that write to stdout.
	if len(args) > 0 {
		switch args[0] {
		case "install", "uninstall", "status":
			runServeInstall(args)
			return
		}
	}

	// Belt-and-suspenders: redirect stdout to stderr so any transitive SDK call
	// that writes to stdout doesn't corrupt the LaunchAgent stdout log.
	os.Stdout = os.Stderr

	if serveHelpRequested(args) {
		_ = cmd.Render(os.Stderr, serveCommand, nil)
		os.Exit(0)
	}

	f := parseServeFlags(args)

	// Set up logging: default discard, --verbose adds stderr, --log-file opens append.
	log.SetOutput(io.Discard)
	var logWriters []io.Writer
	if f.verbose {
		logWriters = append(logWriters, os.Stderr)
	}
	if f.logFile != "" {
		lf, err := openLogFile(f.logFile)
		if err != nil {
			log.SetOutput(os.Stderr)
			log.Fatalf("databricks-claude: serve: cannot open log file %q: %v", f.logFile, err)
		}
		// No defer lf.Close() — daemon runs indefinitely; file stays open.
		logWriters = append(logWriters, lf)
	}
	switch len(logWriters) {
	case 1:
		log.SetOutput(logWriters[0])
	case 2:
		log.SetOutput(io.MultiWriter(logWriters...))
	}

	// Resolve port: flag → state → 49153
	st := loadState()
	port := resolvePort(f.port, st)

	// Resolve profile: flag → state → MDM → "DEFAULT"
	resolvedProfile := f.profile
	if resolvedProfile == "" && st.Profile != "" {
		resolvedProfile = st.Profile
	}
	if resolvedProfile == "" {
		if v, err := mdmprofile.ReadKey(mdmDomain, "databricksProfile"); err == nil && v != "" {
			resolvedProfile = v
		}
	}
	if resolvedProfile == "" {
		resolvedProfile = "DEFAULT"
	}

	// Resolve OTEL tables: flag → state (sentinel-guarded: empty = unset) → MDM → empty
	metricsTable := resolveTableFromChain(f.metricsTable, f.metricsTableSet, st.OtelMetricsTable, "otelMetricsTable", mdmprofile.ReadKey)
	logsTable := resolveTableFromChain(f.logsTable, f.logsTableSet, st.OtelLogsTable, "otelLogsTable", mdmprofile.ReadKey)
	tracesTable := resolveTableFromChain(f.tracesTable, f.tracesTableSet, st.OtelTracesTable, "otelTracesTable", mdmprofile.ReadKey)

	// Persist flag-supplied table names to state (sentinel-guarded writers).
	stateMutated := false
	if shouldPersistOTELTable(f.metricsTableSet, metricsTable, st.OtelMetricsTable) {
		st.OtelMetricsTable = metricsTable
		stateMutated = true
	}
	if shouldPersistOTELTable(f.logsTableSet, logsTable, st.OtelLogsTable) {
		st.OtelLogsTable = logsTable
		stateMutated = true
	}
	if shouldPersistOTELTable(f.tracesTableSet, tracesTable, st.OtelTracesTable) {
		st.OtelTracesTable = tracesTable
		stateMutated = true
	}
	if stateMutated {
		if err := saveState(st); err != nil {
			log.Printf("databricks-claude: serve: warning: could not persist OTEL tables to state: %v", err)
		}
	}

	// Daemon-safe auth: never prompt. The interactive login flow is owned by
	// `serve install` at install time (where stdin is a real tty). If the
	// daemon was started directly (systemctl --user start / launchctl
	// kickstart / schtasks /run) without prior auth, fail loudly here rather
	// than spawning a browser prompt under a service manager with no tty —
	// which would crash-loop until the systemd start-limit gives up.
	//
	// Recovery paths the user has, in order of preference:
	//   1. `databricks-claude serve install` from a tty (re-runs the gate)
	//   2. `databricks auth login --profile <name>` then restart the daemon
	// Both work after a `serve install --skip-auth-check` deferred-auth
	// install — the daemon will simply refuse to start until one of them
	// completes.
	if !authcheck.IsAuthenticated(resolvedProfile, "") {
		log.Fatalf("databricks-claude: serve: profile %q is not authenticated — daemon cannot prompt; "+
			"run `databricks-claude serve install` from a tty, or `databricks auth login --profile %s` "+
			"then restart the daemon (this is the expected next step after `serve install --skip-auth-check`)",
			resolvedProfile, resolvedProfile)
	}

	// Discover workspace host and construct the AI Gateway URL.
	host, err := DiscoverHost(resolvedProfile, "")
	if err != nil {
		log.Fatalf("databricks-claude: serve: failed to discover host for profile %q: %v\n"+
			"Run 'databricks auth login --profile %s' first", resolvedProfile, err, resolvedProfile)
	}
	inferenceUpstream := ConstructGatewayURL(host)
	otelUpstream := host + "/api/2.0/otel"

	// Seed token cache.
	tp := NewTokenProvider(resolvedProfile, "")
	if _, err := tp.Token(context.Background()); err != nil {
		log.Fatalf("databricks-claude: serve: failed to fetch initial token: %v", err)
	}

	// Bind port exclusively — MDM-baked gatewayBaseUrl is a fixed URL, so the
	// fallback port mechanism used by the CLI wrapper is inappropriate here.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("databricks-claude: serve: port %d unavailable: %v\n"+
			"  The daemon fleet port is owned exclusively. Stop the existing instance first.", port, err)
	}

	// Build proxy handler with Daemon=true (no /shutdown registration, daemon-specific /health body).
	r := serveResolved{
		profile:           resolvedProfile,
		inferenceUpstream: inferenceUpstream,
		otelUpstream:      otelUpstream,
		metricsTable:      metricsTable,
		logsTable:         logsTable,
		tracesTable:       tracesTable,
		tp:                tp,
		verbose:           f.verbose,
	}
	cfg := buildServeProxyConfig(st, r)
	h, err := proxy.NewServer(cfg)
	if err != nil {
		ln.Close()
		log.Fatalf("databricks-claude: serve: failed to create proxy: %v", err)
	}
	h = proxy.RecoveryHandler(h)

	srv := &http.Server{Handler: h}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("databricks-claude: serve: proxy error: %v", err)
		}
	}()

	log.Printf("databricks-claude: serve: listening on http://%s (profile=%s, daemon mode)", addr, resolvedProfile)
	if metricsTable != "" {
		log.Printf("databricks-claude: serve: otel-metrics-table=%s", metricsTable)
	}
	if logsTable != "" {
		log.Printf("databricks-claude: serve: otel-logs-table=%s", logsTable)
	}
	if tracesTable != "" {
		log.Printf("databricks-claude: serve: otel-traces-table=%s", tracesTable)
	}
	log.Printf("databricks-claude: serve: with-websearch=%t", cfg.WebSearch.Enabled)

	// Block until SIGINT or SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	signal.Stop(sigCh)
	log.Printf("databricks-claude: serve: received %s, shutting down", sig)
	ln.Close()
}

// Help for `serve`, `serve install`, `serve uninstall`, `serve status` is
// rendered via cmd.Render against the corresponding tree node (see
// serveCommand and its Subcommands in commands.go). The seven hand-rolled
// printXxxHelp functions that previously lived here, in serve_install.go,
// in setup.go, and in desktop_config.go were deleted in #171; the tree is
// now the single source of truth for both flag-parsing AND help text.
