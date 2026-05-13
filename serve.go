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
	"strings"
	"syscall"

	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
	"github.com/IceRhymers/databricks-claude/pkg/mdmprofile"
	"github.com/IceRhymers/databricks-claude/pkg/proxy"
)

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

// parseServeFlags parses the args slice for serve-specific flags.
func parseServeFlags(args []string) serveFlags {
	var f serveFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case arg == "--port":
			f.port, _ = strconv.Atoi(next())
		case strings.HasPrefix(arg, "--port="):
			f.port, _ = strconv.Atoi(strings.TrimPrefix(arg, "--port="))
		case arg == "--log-file":
			f.logFile = next()
		case strings.HasPrefix(arg, "--log-file="):
			f.logFile = strings.TrimPrefix(arg, "--log-file=")
		case arg == "--verbose" || arg == "-v":
			f.verbose = true
		case arg == "--profile":
			f.profile = next()
		case strings.HasPrefix(arg, "--profile="):
			f.profile = strings.TrimPrefix(arg, "--profile=")
		case arg == "--otel-metrics-table":
			f.metricsTable = next()
			f.metricsTableSet = true
		case strings.HasPrefix(arg, "--otel-metrics-table="):
			f.metricsTable = strings.TrimPrefix(arg, "--otel-metrics-table=")
			f.metricsTableSet = true
		case arg == "--otel-logs-table":
			f.logsTable = next()
			f.logsTableSet = true
		case strings.HasPrefix(arg, "--otel-logs-table="):
			f.logsTable = strings.TrimPrefix(arg, "--otel-logs-table=")
			f.logsTableSet = true
		case arg == "--otel-traces-table":
			f.tracesTable = next()
			f.tracesTableSet = true
		case strings.HasPrefix(arg, "--otel-traces-table="):
			f.tracesTable = strings.TrimPrefix(arg, "--otel-traces-table=")
			f.tracesTableSet = true
		}
	}
	return f
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

	if hasFlag(args, "--help") || hasFlag(args, "-h") {
		printServeHelp()
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
	if !authcheck.IsAuthenticated(resolvedProfile, "") {
		log.Fatalf("databricks-claude: serve: profile %q is not authenticated — daemon cannot prompt; "+
			"run `databricks-claude serve install` from a tty or `databricks auth login --profile %s` first",
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
	h, err := proxy.NewServer(&proxy.Config{
		InferenceUpstream: inferenceUpstream,
		OTELUpstream:      otelUpstream,
		UCMetricsTable:    metricsTable,
		UCLogsTable:       logsTable,
		UCTracesTable:     tracesTable,
		TokenSource:       tp,
		Verbose:           f.verbose,
		ToolName:          "databricks-claude",
		Version:           Version,
		Daemon:            true,
		Profile:           resolvedProfile,
	})
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

	// Block until SIGINT or SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	signal.Stop(sigCh)
	log.Printf("databricks-claude: serve: received %s, shutting down", sig)
	ln.Close()
}

// printServeHelp prints usage for the `serve` subcommand to stderr.
func printServeHelp() {
	fmt.Fprint(os.Stderr, `Usage: databricks-claude serve [flags]
       databricks-claude serve <install|uninstall|status> [flags]

Long-lived daemon that serves Claude Code and Claude Desktop with persistent
Databricks OAuth. A third deployment mode alongside the per-session CLI wrapper
(databricks-claude claude ...) and SessionStart hooks — useful when you want a
single OAuth-refreshing proxy that survives across sessions.

Owns Databricks OAuth refresh and exposes inference + OTLP on 127.0.0.1.
Distinguished from --headless mode by: no session refcount, no /shutdown
route, append-only logging, and daemon:true in /health so hooks can detect
and no-op.

Designed for LaunchAgent or systemd service deployment, where a plist or
unit file invokes 'databricks-claude serve' once and keeps it running.
Configure your client to point at the daemon:
  Claude Desktop: via MDM, set gatewayBaseUrl: http://127.0.0.1:<port>.
  Claude Code:    edit ~/.claude/settings.json once to set
                  ANTHROPIC_BASE_URL=http://127.0.0.1:<port> in the env block.
The daemon does NOT mutate settings.json itself — it stays outside the
per-tool lifecycle by design.

Sub-subcommands (OS service management):
  install    Register and start the daemon as a per-user OS service.
             Uses: launchctl (macOS), schtasks (Windows), systemctl --user (Linux).
             Run 'databricks-claude serve install --help' for flags.
  uninstall  Stop and remove the daemon OS service registration.
             Run 'databricks-claude serve uninstall --help' for flags.
  status     Report Registered / Running / Healthy in one shot.
             Run 'databricks-claude serve status --help' for flags.

Flags (for the daemon itself, not sub-subcommands):
  --port int                   Proxy listen port (default: 49153). The daemon
                               binds this port exclusively — MDM-baked
                               gatewayBaseUrl is a fixed URL and cannot follow
                               a fallback port.
  --profile string             Databricks config profile (default: saved
                               state > MDM databricksProfile key > "DEFAULT")
  --log-file string            Append to this file instead of discarding logs.
                               Safe for log rotation (O_APPEND). Restarts
                               preserve prior content (not O_TRUNC).
  --verbose, -v                Also write debug logs to stderr (combinable
                               with --log-file)
  --otel-metrics-table string  Unity Catalog table for OTEL metrics
                               (cat.schema.table). Resolution: flag > saved
                               state > MDM otelMetricsTable key > empty.
                               Empty = no X-Databricks-UC-Table-Name header;
                               Databricks ingest rejects the request (visible,
                               actionable failure — not silent).
  --otel-logs-table string     Unity Catalog table for OTEL logs (same chain)
  --otel-traces-table string   Unity Catalog table for OTEL traces (same chain)
  --help, -h                   Show this help message

MDM keys (domain: com.icerhymers.databricks-claude):
  databricksProfile   Databricks CLI profile name
  otelMetricsTable    UC table for OTEL metrics
  otelLogsTable       UC table for OTEL logs
  otelTracesTable     UC table for OTEL traces

Note: --otel / --no-otel* flags are NOT supported for serve. Those flags
mutate ~/.claude/settings.json to configure Claude Code's OTLP emission.
In daemon mode, Claude Desktop reads OTLP config from MDM, not from any
wrapper-mutated file. Omit otlpEndpoint from the MDM profile to disable OTLP.

Endpoints:
  GET /health   Returns {"tool":"databricks-claude","daemon":true,"version":"...",
                         "profile":"...","token_valid_until":"..."}
  POST /shutdown  Not registered — returns 404. Stop the daemon via SIGTERM
                  (e.g. launchctl stop or systemctl stop).

Examples:
  # Minimal daemon on default port:
  databricks-claude serve

  # Register as an OS service and start:
  databricks-claude serve install
  databricks-claude serve install --profile databricks-ai-inference --port 49153

  # Check service status:
  databricks-claude serve status

  # Remove OS service registration:
  databricks-claude serve uninstall

  # With explicit profile, port, and log file:
  databricks-claude serve \
    --profile databricks-ai-inference \
    --port 49153 \
    --log-file /var/log/databricks-claude/daemon.log

  # With OTEL table routing:
  databricks-claude serve \
    --otel-metrics-table main.claude_telemetry.claude_otel_metrics \
    --otel-logs-table main.claude_telemetry.claude_otel_logs

Exit codes:
  0   Clean shutdown on SIGINT/SIGTERM
  1   Startup failure (auth, port collision, host discovery)
`)
}
