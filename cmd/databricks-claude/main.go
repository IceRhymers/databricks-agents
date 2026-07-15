package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
	"github.com/IceRhymers/databricks-agents/internal/core/cli"
	"github.com/IceRhymers/databricks-agents/internal/core/completion"
	"github.com/IceRhymers/databricks-agents/internal/core/portbind"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
	"github.com/IceRhymers/databricks-agents/internal/core/updater"
	"github.com/IceRhymers/databricks-agents/pkg/health"
	"github.com/IceRhymers/databricks-agents/pkg/mdmprofile"
	"github.com/IceRhymers/databricks-agents/pkg/proxy"
	"github.com/IceRhymers/databricks-agents/pkg/websearch"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// Args holds all parsed command-line arguments for databricks-claude.
//
// #172 removed the 14 "persistent config editor" fields (PrintEnv, OTEL,
// OTELMetricsTable*, OTELLogsTable*, OTELTraces, OTELTracesTable*, NoOTEL*,
// WriteClaudeConfig, WithWebSearch*, WebSearchBackend*, WebSearchFetchBudget*).
// Their mutation paths now live under the `config` subcommand tree.
//
// #173 removed the 4 hooks-lifecycle fields (InstallHooks, UninstallHooks,
// HeadlessEnsure, HeadlessRelease). Their entrypoints now live under the
// `hooks` subcommand tree (install / uninstall / session-start / session-end).
//
// #174 removed Headless and IdleTimeout. The session-scoped standalone proxy
// now lives behind `serve --session-mode` (with --idle-timeout colocated as
// a `serve` flag).
type Args struct {
	Profile       string
	Verbose       bool
	Version       bool
	ShowHelp      bool
	Upstream      string
	LogFile       string
	ProxyAPIKey   string
	TLSCert       string
	TLSKey        string
	Port          int
	NoUpdateCheck bool
	ClaudeArgs    []string
}

func main() {
	// completion <shell> — must be the very first check, before any flag parsing,
	// auth, or state loading. Safe to call in the Homebrew install sandbox.
	if len(os.Args) >= 2 && os.Args[1] == "completion" {
		completion.Run(os.Args[2:], flagDefs, "databricks-claude", knownSubcommands...)
		os.Exit(0)
	}

	// update — force-check for a newer release and print instructions.
	if len(os.Args) >= 2 && os.Args[1] == "update" {
		if os.Getenv("DATABRICKS_NO_UPDATE_CHECK") == "1" {
			fmt.Fprintln(os.Stderr, "databricks-claude: update check disabled via DATABRICKS_NO_UPDATE_CHECK")
			os.Exit(0)
		}
		cfg := buildUpdaterConfig()
		cfg.CacheTTL = 0 // force fresh check
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := updater.Check(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: update check failed: %v\n", err)
			os.Exit(1)
		}
		if !r.UpdateAvailable {
			fmt.Fprintf(os.Stderr, "databricks-claude v%s is already the latest version\n", Version)
			os.Exit(0)
		}
		if r.IsHomebrew {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Run: brew upgrade databricks-claude\n", r.LatestVersion)
		} else {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Download from: %s\n", r.LatestVersion, r.ReleaseURL)
		}
		os.Exit(0)
	}

	// Wire the MDM reader so ResolveDatabricksCLI can consult the MDM-managed
	// databricksCliPath key. Hoisted ABOVE the early-exit branches so all
	// entry points (credential-helper alias, `desktop`, `setup`, and the
	// normal proxy flow) see the real reader before any code path can call
	// ResolveDatabricksCLI. The logger remains helper-specific (wired inside
	// runCredentialHelper) since only the helper has a debug-log surface.
	cli.SetMDMReader(mdmprofile.ReadKey)

	// `serve` subcommand — long-lived daemon for MDM-deployed Claude Desktop
	// deployments. Owns Databricks OAuth refresh; exposes inference + OTLP on
	// 127.0.0.1. Distinguished from --headless: no refcount, no /shutdown route,
	// append-only logging, daemon:true in /health.
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		runServe(os.Args[2:])
		return
	}

	// argv[0] alias `databricks-claude-credential-helper` — Desktop's
	// mobileconfig accepts only a path with no arguments; install methods
	// drop a symlink at this name pointing at the main binary so Desktop's
	// inferenceCredentialHelper can target a stable path.
	if isCredentialHelperBinaryName(os.Args[0]) {
		runCredentialHelper(extractProfileFlag(os.Args[1:]))
		return
	}

	// `desktop` subcommand — Claude Desktop integration setup. Encapsulates
	// `generate-config` and an explicit `credential-helper` action so these
	// flags don't pollute the root flag namespace.
	if len(os.Args) >= 2 && os.Args[1] == "desktop" {
		runDesktopCommand(os.Args[2:])
		return
	}

	// `setup` subcommand — idempotent auth bootstrap for fleet init scripts.
	// Resolves + persists the profile, then runs `databricks auth login` when
	// not already authenticated (or always, with --force).
	if len(os.Args) >= 2 && os.Args[1] == "setup" {
		runSetupCommand(os.Args[2:])
		return
	}

	// `config` subcommand — persistent config editor (OTEL signals, websearch,
	// settings.json env block, resolved-config diagnostic). Consolidates the
	// 14 flags removed from the root in #172 — the flags that mutate state /
	// settings.json for FUTURE runs rather than affecting the current
	// invocation. The transparent-proxy launcher path below is intentionally
	// flag-driven and bare; persistent state mutation lives behind this tree.
	if len(os.Args) >= 2 && os.Args[1] == "config" {
		runConfigCommand(os.Args[2:])
		return
	}

	// `hooks` subcommand — session-hook deployment mode. install/uninstall
	// manage the SessionStart/SessionEnd entries in ~/.claude/settings.json;
	// session-start/session-end are the hook-invoked refcount-managed proxy
	// lifecycle internals (formerly --headless-ensure / --headless-release
	// before #173 consolidated them off the root flag namespace).
	if len(os.Args) >= 2 && os.Args[1] == "hooks" {
		runHooksCommand(os.Args[2:])
		return
	}

	// `doctor` subcommand — non-interactive model-routing diagnostic. Runs
	// model discovery, diffs the discovered per-family models against the pins
	// in ~/.claude/settings.json, prints the delta, and rewrites settings.json
	// ONLY under --fix (through bootstrapSettings). The sanctioned recovery
	// path for the hook/daemon flow that can't prompt.
	if len(os.Args) >= 2 && os.Args[1] == "doctor" {
		runDoctor(os.Args[2:])
		return
	}

	// Parse databricks-claude flags, passing everything else through to claude.
	// Usage: databricks-claude [databricks-claude-flags] [--] [claude-args...]
	// Unknown flags are forwarded to claude automatically.
	// Tip: anything after "--" is forwarded to claude verbatim — e.g.
	// "databricks-claude -- --help" shows claude's own help, and
	// "databricks-claude -- completion" passes "completion" to claude.
	a, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "databricks-claude:", err)
		os.Exit(1)
	}

	if a.ShowHelp {
		handleHelp()
		os.Exit(0)
	}

	if a.Version {
		fmt.Printf("databricks-claude %s\n", Version)
		os.Exit(0)
	}

	// --- Resolve config from settings.json ---
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("databricks-claude: cannot determine home dir: %v", err)
	}

	// Default: discard all logs (silent wrapper — identical to vanilla claude).
	log.SetOutput(io.Discard)

	if a.Verbose {
		log.SetOutput(os.Stderr)
	}
	if a.LogFile != "" {
		f, err := os.OpenFile(a.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			log.SetOutput(os.Stderr) // ensure this fatal is visible
			log.Fatalf("databricks-claude: cannot open log file %q: %v", a.LogFile, err)
		}
		defer f.Close()
		if a.Verbose {
			// Both stderr and file.
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		} else {
			log.SetOutput(f)
		}
	}
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	settingsDoc, err := readSettingsJSON(settingsPath)
	if err != nil {
		log.Fatalf("databricks-claude: cannot read settings.json: %v", err)
	}

	env := envBlock(settingsDoc)

	// Resolve profile: --profile flag (saved to state file) > state file > "DEFAULT"
	// The env var DATABRICKS_CONFIG_PROFILE is NOT consulted here because
	// Claude's settings.json injects env vars into child processes, which
	// would override the user's explicit --profile choice persisted in the
	// state file.
	resolvedProfile := a.Profile
	if resolvedProfile == "" {
		if saved := loadState(); saved.Profile != "" {
			resolvedProfile = saved.Profile
			log.Printf("databricks-claude: using profile %q from state file", saved.Profile)
		}
	}
	if resolvedProfile == "" {
		resolvedProfile = "DEFAULT"
	}

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(resolvedProfile, ""); err != nil {
		log.Fatalf("databricks-claude: auth failed: %v", err)
	}

	// --- Startup security checks ---
	for _, w := range proxy.SecurityChecks() {
		fmt.Fprintln(os.Stderr, w)
	}

	// Extract upstream values from settings.json.
	// ANTHROPIC_BASE_URL in settings.json is the real upstream at this point
	// (unless it's stale localhost, which we ignore).
	inferenceUpstream := ""
	if v, ok := env["ANTHROPIC_BASE_URL"].(string); ok {
		inferenceUpstream = v
	}
	// If it's already localhost (stale crash), treat as absent.
	if strings.HasPrefix(inferenceUpstream, "http://127.0.0.1") || strings.HasPrefix(inferenceUpstream, "https://127.0.0.1") {
		inferenceUpstream = ""
	}
	databricksHost := readDatabricksCfgHost(resolvedProfile)

	// Detect existing OTEL config.
	otelConfigured := false
	otelEndpoint := ""
	if v, ok := env["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"].(string); ok && v != "" {
		otelConfigured = true
		otelEndpoint = v
	}
	// If OTEL endpoint is stale localhost (crash leftover), clear the URL but
	// keep otelConfigured=true so OTEL re-enables with the new proxy URL.
	if otelEndpoint != "" && strings.HasPrefix(otelEndpoint, "http://127.0.0.1") {
		otelEndpoint = ""
	}

	// Read table names from settings.json env block. Track the raw values
	// separately so we can migrate them to the state file on first read.
	ucMetricsTable := ""
	metricsFromSettings := ""
	if v, ok := env["CLAUDE_OTEL_UC_METRICS_TABLE"].(string); ok && v != "" {
		ucMetricsTable = v
		metricsFromSettings = v
	}
	ucLogsTable := ""
	logsFromSettings := ""
	if v, ok := env["CLAUDE_OTEL_UC_LOGS_TABLE"].(string); ok && v != "" {
		ucLogsTable = v
		logsFromSettings = v
	}
	ucTracesTable := ""
	tracesFromSettings := ""
	if v, ok := env["CLAUDE_OTEL_UC_TRACES_TABLE"].(string); ok && v != "" {
		ucTracesTable = v
		tracesFromSettings = v
	}

	// Fall back to state file for tables absent from settings.json.
	// This lets --no-otel / --no-otel-* clear settings.json while the table
	// names survive in .databricks-claude.json for the next --otel invocation.
	tableState := loadState()
	if ucMetricsTable == "" {
		ucMetricsTable = tableState.OtelMetricsTable
	}
	if ucLogsTable == "" {
		ucLogsTable = tableState.OtelLogsTable
	}
	if ucTracesTable == "" {
		ucTracesTable = tableState.OtelTracesTable
	}

	// --- Seed token cache ---
	tp := NewTokenProvider(resolvedProfile, "")
	if _, err := tp.Token(context.Background()); err != nil {
		log.Fatalf("databricks-claude: failed to fetch initial token for profile %q: %v", resolvedProfile, err)
	}

	// --- Self-setup: discover host + gateway URL if ANTHROPIC_BASE_URL is absent ---
	needsFullSetup := false

	// --upstream flag takes highest priority for the inference endpoint.
	if a.Upstream != "" {
		inferenceUpstream = a.Upstream
		log.Printf("databricks-claude: using explicit upstream: %s", inferenceUpstream)
		if databricksHost == "" {
			// Try to discover host for OTEL even when upstream is explicit.
			if h, err := DiscoverHost(resolvedProfile, ""); err == nil {
				databricksHost = h
			}
		}
		needsFullSetup = true
	} else if inferenceUpstream == "" {
		log.Printf("databricks-claude: no ANTHROPIC_BASE_URL configured — discovering from profile %q", resolvedProfile)

		host, err := DiscoverHost(resolvedProfile, "")
		if err != nil {
			log.Fatalf("databricks-claude: failed to discover host for profile %q: %v\nRun 'databricks auth login --profile %s' first",
				resolvedProfile, err, resolvedProfile)
		}
		log.Printf("databricks-claude: discovered host: %s", host)
		databricksHost = host

		inferenceUpstream = ConstructGatewayURL(host)
		log.Printf("databricks-claude: upstream: %s", inferenceUpstream)

		needsFullSetup = true
	}

	// OTEL upstream: use host/api/2.0/otel when OTEL is enabled, else fall back.
	otelUpstream := otelEndpoint
	if otelUpstream == "" && databricksHost != "" {
		otelUpstream = databricksHost + "/api/2.0/otel"
	}
	if otelUpstream == "" {
		// Safe fallback so proxy.go doesn't crash.
		otelUpstream = inferenceUpstream
	}

	// --- Resolve port for downstream binding ---
	port := resolvePort(a.Port, loadState())

	// Migrate OTEL tables that exist in settings.json but not yet in the state
	// file. The flag-driven mutation paths moved to `config otel enable` in
	// #172, but the running proxy still needs to keep state in sync with
	// whatever it reads from settings.json — so a settings.json-only OTEL
	// config (from a pre-#172 install) gets migrated forward on first run.
	{
		mutated := false
		if metricsFromSettings != "" && tableState.OtelMetricsTable == "" {
			tableState.OtelMetricsTable = metricsFromSettings
			mutated = true
		}
		if logsFromSettings != "" && tableState.OtelLogsTable == "" {
			tableState.OtelLogsTable = logsFromSettings
			mutated = true
		}
		if tracesFromSettings != "" && tableState.OtelTracesTable == "" {
			tableState.OtelTracesTable = tracesFromSettings
			mutated = true
		}
		if mutated {
			if err := saveState(tableState); err != nil {
				log.Printf("databricks-claude: warning: could not persist OTel tables to state: %v", err)
			}
		}
	}

	// --- Validate TLS config ---
	if err := proxy.ValidateTLSConfig(a.TLSCert, a.TLSKey); err != nil {
		log.Fatalf("databricks-claude: %v", err)
	}

	// --- Read --with-websearch (workaround) settings from state ---
	// #172 moved websearch flag mutation behind `config websearch enable`.
	// The running proxy still reads with_websearch / backend / fetch-budget
	// from the state file every start so previously-enabled installs keep
	// working without code changes.
	wsState := loadState()
	withWebSearch := wsState.WithWebSearch
	wsBackend := wsState.WebSearchBackend
	wsBudget := wsState.WebSearchFetchBudget
	if wsBackend == "" {
		wsBackend = "duckduckgo"
	}
	if wsBudget <= 0 {
		wsBudget = 100 * 1024
	}

	// Build the websearch backend (if enabled) and print the workaround warning.
	var wsBackendImpl websearch.Backend
	var wsRobots websearch.RobotsChecker
	if withWebSearch {
		fmt.Fprintln(os.Stderr, "databricks-claude: --with-websearch is a workaround. Anthropic's native")
		fmt.Fprintln(os.Stderr, "  web_search and web_fetch tools are not yet supported by Databricks FMAPI.")
		fmt.Fprintf(os.Stderr, "  This proxy fulfills them locally via backend=%q (per-fetch budget=%d bytes).\n", wsBackend, wsBudget)
		fmt.Fprintln(os.Stderr, "  Limitations: no JavaScript rendering; robots.txt enforced; headless only.")
		fmt.Fprintln(os.Stderr, "  This flag will be removed (with one release of deprecation warning) when")
		fmt.Fprintln(os.Stderr, "  Databricks ships native server-side tool support.")
		b, err := buildWebSearchBackend(wsBackend)
		if err != nil {
			log.Fatalf("databricks-claude: %v", err)
		}
		wsBackendImpl = b
		wsRobots = &websearch.Robots{}
	}

	// --- Bind proxy port ---
	ln, isOwner, err := portbind.Bind("databricks-claude", port)
	if err != nil {
		log.Fatalf("databricks-claude: %v", err)
	}

	scheme := "http"
	if a.TLSCert != "" && a.TLSKey != "" {
		scheme = "https"
		fmt.Fprintln(os.Stderr, "databricks-claude: TLS enabled")
	}
	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, portbind.ListenerPort(ln, port))

	// --- Build proxy handler (needed by both owner and watchProxy) ---
	proxyConfig := &ProxyConfig{
		InferenceUpstream: inferenceUpstream,
		OTELUpstream:      otelUpstream,
		UCMetricsTable:    ucMetricsTable,
		UCLogsTable:       ucLogsTable,
		UCTracesTable:     ucTracesTable,
		TokenProvider:     tp,
		Verbose:           a.Verbose,
		APIKey:            a.ProxyAPIKey,
		TLSCertFile:       a.TLSCert,
		TLSKeyFile:        a.TLSKey,
		ToolName:          "databricks-claude",
		Version:           Version,
		WebSearch: proxy.WebSearchSettings{
			Enabled:     withWebSearch,
			Backend:     wsBackendImpl,
			Robots:      wsRobots,
			FetchBudget: wsBudget,
		},
	}
	if a.ProxyAPIKey != "" {
		fmt.Fprintln(os.Stderr, "databricks-claude: proxy API key authentication enabled")
	}
	handler := NewProxyServer(proxyConfig)

	// --- Reference counting ---
	// Wrapper mode: the parent process acquires here and releases on exit.
	// (Session-scoped standalone proxies that used to self-skip this acquire
	// when --headless was set now live in `serve --session-mode`, which
	// owns its own refcount management. See #174.)
	refcountPath := refcount.PathForPort(".databricks-claude-sessions", port)
	if err := refcount.Acquire(refcountPath); err != nil {
		log.Printf("databricks-claude: refcount acquire warning: %v", err)
	}

	// --- Start proxy if we own the port; otherwise watch for owner death ---
	// promoteCh is no longer needed in wrapper mode (was only consumed by the
	// lifecycle wrapper, which only ran in --headless). Wrapper mode does not
	// register /shutdown — sessions exit by closing the child claude process.
	if isOwner {
		go func() {
			srv := &http.Server{Handler: handler}
			if a.TLSCert != "" && a.TLSKey != "" {
				if err := srv.ServeTLS(ln, a.TLSCert, a.TLSKey); err != nil && err != http.ErrServerClosed {
					log.Printf("databricks-claude: proxy serve error: %v", err)
				}
			} else {
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Printf("databricks-claude: proxy serve error: %v", err)
				}
			}
		}()
	} else {
		go health.WatchProxy(port, handler, a.TLSCert, a.TLSKey, "databricks-claude", func() {})
	}

	// --- Write config once (idempotent) ---
	// Each signal's env vars are emitted only when its UC table is configured
	// (table-presence semantics). CLAUDE_CODE_ENABLE_TELEMETRY=1 is set when
	// any signal is active. Traces stay opt-in via --otel-traces / a persisted
	// CLAUDE_OTEL_UC_TRACES_TABLE — they are not auto-enabled by --otel.
	otelEnv := map[string]string{}
	if ucMetricsTable != "" {
		otelEnv["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"] = proxyURL + "/otel/v1/metrics"
		otelEnv["OTEL_EXPORTER_OTLP_METRICS_HEADERS"] = "content-type=application/x-protobuf"
		otelEnv["OTEL_METRICS_EXPORTER"] = "otlp"
		otelEnv["OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"] = "http/protobuf"
		otelEnv["OTEL_METRIC_EXPORT_INTERVAL"] = "10000"
		otelEnv["CLAUDE_OTEL_UC_METRICS_TABLE"] = ucMetricsTable
	}
	if ucLogsTable != "" {
		otelEnv["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] = proxyURL + "/otel/v1/logs"
		otelEnv["OTEL_EXPORTER_OTLP_LOGS_HEADERS"] = "content-type=application/x-protobuf"
		otelEnv["OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"] = "http/protobuf"
		otelEnv["OTEL_LOGS_EXPORTER"] = "otlp"
		otelEnv["OTEL_LOGS_EXPORT_INTERVAL"] = "5000"
		otelEnv["CLAUDE_OTEL_UC_LOGS_TABLE"] = ucLogsTable
	}
	if ucTracesTable != "" {
		// Traces is currently beta-gated upstream — set the beta flag so the
		// Claude Code CLI emits OTLP spans. Span-content flags
		// (OTEL_LOG_TOOL_DETAILS / _USER_PROMPTS / _TOOL_CONTENT) are
		// intentionally NOT auto-set; leave those to admin/user discretion.
		otelEnv["CLAUDE_CODE_ENHANCED_TELEMETRY_BETA"] = "1"
		otelEnv["OTEL_TRACES_EXPORTER"] = "otlp"
		otelEnv["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = proxyURL + "/otel/v1/traces"
		otelEnv["OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"] = "http/protobuf"
		otelEnv["OTEL_TRACES_EXPORT_INTERVAL"] = "5000"
		otelEnv["CLAUDE_OTEL_UC_TRACES_TABLE"] = ucTracesTable
	}
	if ucMetricsTable != "" || ucLogsTable != "" || ucTracesTable != "" {
		otelEnv["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	}
	// Reference otelConfigured to silence the unused-variable warning while
	// keeping the detection block above for future use (e.g. surfacing a
	// "stale OTEL config detected" log).
	_ = otelConfigured
	if needsFullSetup {
		// Also write Databricks-specific keys for full setup. launchModelRouting
		// reads persisted state (cheap file read, no network) — the
		// no-hot-path-discovery invariant. wsState is the already-loaded state in
		// scope; reuse it rather than re-reading.
		for k, v := range databricksFullSetupEnv(launchModelRouting(wsState)) {
			otelEnv[k] = v
		}
	}

	if err := bootstrapSettings(a.Port, resolvedProfile, proxyURL, otelEnv); err != nil {
		log.Fatalf("databricks-claude: %v", err)
	}

	// --- Log startup info ---
	log.Printf("databricks-claude: proxy on %s (owner=%v), profile=%s, upstream=%s",
		proxyURL, isOwner, resolvedProfile, inferenceUpstream)

	// --- Synchronous update check (before child to avoid stderr interleaving) ---
	if !a.NoUpdateCheck && os.Getenv("DATABRICKS_NO_UPDATE_CHECK") != "1" {
		updater.PrintUpdateNotice(buildUpdaterConfig())
	}

	// --- Run child ---
	exitCode, err := RunChild(context.Background(), a.ClaudeArgs)
	if err != nil {
		log.Printf("databricks-claude: child error: %v", err)
	}

	// --- Release refcount; if last session and owner, close listener ---
	// Called explicitly because os.Exit skips defers.
	remaining, relErr := refcount.Release(refcountPath)
	if relErr != nil {
		log.Printf("databricks-claude: refcount release warning: %v", relErr)
	}
	if remaining == 0 && isOwner {
		ln.Close()
		log.Printf("databricks-claude: last session, proxy shut down")
	}

	os.Exit(exitCode)
}

// envBlock returns the "env" sub-map from a settings document, or an empty map.
func envBlock(doc map[string]interface{}) map[string]interface{} {
	if env, ok := doc["env"]; ok {
		if m, ok := env.(map[string]interface{}); ok {
			return m
		}
	}
	return map[string]interface{}{}
}

// parseArgs separates databricks-claude flags from claude flags.
// databricks-claude owns: --profile, --port, --verbose/-v, --version, --help,
// --upstream, --log-file, --proxy-api-key, --tls-cert, --tls-key,
// --no-update-check.
// Everything else (including unknown flags like --debug) passes through to claude.
// An explicit "--" separator is supported but not required.
//
// #172 removed the 14 "persistent config editor" flags — they live behind
// the `config` subcommand tree now (config otel enable|disable, config
// websearch enable|disable, config write, config show). Old flags are
// removed, NOT aliased: `databricks-claude --otel` now passes `--otel`
// through to claude as an unknown flag.
//
// #173 removed the 4 hooks-lifecycle flags (--install-hooks,
// --uninstall-hooks, --headless-ensure, --headless-release) — they live
// behind the `hooks` subcommand tree now (hooks install / uninstall /
// session-start / session-end). Same "removed, not aliased" semantics.
//
// #174 removed --headless and --idle-timeout — they live behind
// `serve --session-mode` now. Same "removed, not aliased" semantics.
func parseArgs(args []string) (*Args, error) {
	a := &Args{}

	// knownFlags is defined at package level in completion_flags.go,
	// derived from flagDefs so completions and parsing stay in sync.

	i := 0
	for i < len(args) {
		arg := args[i]

		// Explicit separator: everything after "--" goes to claude.
		if arg == "--" {
			a.ClaudeArgs = append(a.ClaudeArgs, args[i+1:]...)
			return a, nil
		}

		// Special case: -h is a short flag for --help, -v for --verbose.
		if arg == "-h" {
			a.ShowHelp = true
			i++
			continue
		}
		if arg == "-v" {
			a.Verbose = true
			i++
			continue
		}

		// Check if it's a known databricks-claude flag.
		if strings.HasPrefix(arg, "--") {
			// Handle --flag=value form.
			name := arg
			value := ""
			if eqIdx := strings.Index(arg, "="); eqIdx >= 0 {
				name = arg[:eqIdx]
				value = arg[eqIdx+1:]
			}

			if knownFlags[name] {
				switch name {
				case "--profile":
					if value != "" {
						a.Profile = value
					} else if i+1 < len(args) {
						i++
						a.Profile = args[i]
					}
				case "--upstream":
					if value != "" {
						a.Upstream = value
					} else if i+1 < len(args) {
						i++
						a.Upstream = args[i]
					}
				case "--log-file":
					if value != "" {
						a.LogFile = value
					} else if i+1 < len(args) {
						i++
						a.LogFile = args[i]
					}
				case "--verbose":
					a.Verbose = true
				case "--version":
					a.Version = true
				case "--help":
					a.ShowHelp = true
				case "--proxy-api-key":
					if value != "" {
						a.ProxyAPIKey = value
					} else if i+1 < len(args) {
						i++
						a.ProxyAPIKey = args[i]
					}
				case "--tls-cert":
					if value != "" {
						a.TLSCert = value
					} else if i+1 < len(args) {
						i++
						a.TLSCert = args[i]
					}
				case "--tls-key":
					if value != "" {
						a.TLSKey = value
					} else if i+1 < len(args) {
						i++
						a.TLSKey = args[i]
					}
				case "--port":
					if value != "" {
						a.Port, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						a.Port, _ = strconv.Atoi(args[i])
					}
				case "--no-update-check":
					a.NoUpdateCheck = true
				default:
					// name is in knownFlags (derived from the rootCommand
					// tree) but no case above handles it. This can only
					// happen if commands.go declares a flag parseArgs was
					// never taught to parse — fail loudly instead of
					// silently swallowing it. Guards the "every tree flag
					// is parsed" half of #170's parity contract.
					return nil, fmt.Errorf("internal: %s is a known flag but parseArgs has no case for it (drift between commands.go and parseArgs)", name)
				}
				i++
				continue
			}
		}

		// Not a known databricks-claude flag — pass through to claude.
		a.ClaudeArgs = append(a.ClaudeArgs, arg)
		i++
	}
	return a, nil
}

// handleHelp prints the databricks-claude help message by rendering the
// root command-tree node from internal/cmd. The actual help body lives in
// commands.go (rootHelpTemplate, attached as rootCommand.Long); this
// function is just a thin wrapper around cmd.Render that injects the
// build-time Version. Only the wrapper's own flags and subcommands are
// documented; claude's CLI help is reachable via the `--` passthrough
// escape hatch (databricks-claude -- --help).
func handleHelp() {
	if err := cmd.Render(os.Stdout, rootCommand, map[string]string{"Version": Version}); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude: failed to render help: %v\n", err)
	}
}

// defaultModelRouting is the OFFLINE FALLBACK used when no ModelSet has been
// discovered/persisted (fresh install, offline, empty grants). Demoted from
// the former hardcoded map — deliberately NOT deleted: the launch path, where
// discovery is forbidden, must always have something legal to write.
//
// Model names are versioned and drift as Databricks ships new models;
// bumping them here is the right place.
func defaultModelRouting() ModelRouting {
	return ModelRouting{
		Opus:   "databricks-claude-opus-4-7",
		Sonnet: "databricks-claude-sonnet-4-6",
		Haiku:  "databricks-claude-haiku-4-5",
	}
}

// launchModelRouting returns the persisted ModelRouting if present, else the
// demoted default; blank per-family fields fall back to the default. NEVER
// performs network I/O — safe to call on the launch hot path.
func launchModelRouting(s persistentState) ModelRouting {
	def := defaultModelRouting()
	if s.Models == nil {
		return def
	}
	m := *s.Models
	if m.Opus == "" {
		m.Opus = def.Opus
	}
	if m.Sonnet == "" {
		m.Sonnet = def.Sonnet
	}
	if m.Haiku == "" {
		m.Haiku = def.Haiku
	}
	return m
}

// databricksFullSetupEnv returns the Databricks-specific env keys written
// during a "full setup" bootstrap (first-run, --write-claude-config, etc.).
// These cover model routing, the coding-agent-mode custom header, and the
// experimental-betas opt-out. Kept as a single source of truth so the
// --write-claude-config flag and the normal startup flow can never drift —
// regressions deleting any key fail the integration tests that assert
// against this map.
//
// A family's model key is OMITTED when its FQN is empty, so a discovery-time
// writer never emits an empty or wrongly-substituted model for an unresolved
// family (no silent mis-route). Launch callers pass launchModelRouting(...)
// which is always fully populated, so all six keys are present on the launch
// path.
func databricksFullSetupEnv(m ModelRouting) map[string]string {
	env := map[string]string{
		"ANTHROPIC_CUSTOM_HEADERS":               "x-databricks-use-coding-agent-mode: true",
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
	}
	if m.Opus != "" {
		env["ANTHROPIC_MODEL"] = m.Opus
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = m.Opus
	}
	if m.Sonnet != "" {
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = m.Sonnet
	}
	if m.Haiku != "" {
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = m.Haiku
	}
	return env
}

// buildUpdaterConfig returns the standard updater.Config for databricks-claude.
func buildUpdaterConfig() updater.Config {
	home, _ := os.UserHomeDir()
	return updater.Config{
		RepoSlug:       "IceRhymers/databricks-claude",
		CurrentVersion: Version,
		BinaryName:     "databricks-claude",
		CacheFile:      filepath.Join(home, ".claude", ".update-check.json"),
		CacheTTL:       24 * time.Hour,
	}
}

// handlePrintEnv prints resolved configuration with the token redacted.
func handlePrintEnv(profile, databricksHost, anthropicBaseURL, token, upstreamBinary string, otelEnabled bool, otelMetricsTable, otelLogsTable, otelTracesTable string) {
	// Redact token.
	redacted := "**** (redacted)"
	if strings.HasPrefix(token, "dapi-") {
		redacted = "dapi-***"
	}

	// Resolve upstream binary path for display.
	binaryPath := upstreamBinary
	if binaryPath == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			binaryPath = p
		} else {
			binaryPath = "(not found)"
		}
	}

	fmt.Printf(`databricks-claude configuration:
  Profile:              %s
  DATABRICKS_HOST:      %s
  ANTHROPIC_BASE_URL:   %s
  ANTHROPIC_AUTH_TOKEN: %s
  Upstream binary:      %s
  OTEL enabled:         %v
`, profile, databricksHost, anthropicBaseURL, redacted, binaryPath, otelEnabled)

	if otelEnabled {
		if otelMetricsTable != "" {
			fmt.Printf(`  OTEL metrics table:   %s
  OTEL metric interval: 10000ms
`, otelMetricsTable)
		}
		if otelLogsTable != "" {
			fmt.Printf(`  OTEL logs table:      %s
  OTEL logs interval:   5000ms
`, otelLogsTable)
		}
		if otelTracesTable != "" {
			fmt.Printf(`  OTEL traces table:    %s
  OTEL traces interval: 5000ms
`, otelTracesTable)
		}
	}
}

// persistentConfigPath returns the path to the persistent config file.
func persistentConfigPath(homeDir string) string {
	return filepath.Join(homeDir, ".claude", ".databricks-claude.json")
}

// readPersistentConfig reads the persistent config file. Returns an empty map
// if the file does not exist.
func readPersistentConfig(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{}, nil
		}
		return nil, err
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// writePersistentConfig atomically writes the persistent config file.
func writePersistentConfig(path string, cfg map[string]interface{}) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// deriveLogsTable derives the OTEL logs table name from the metrics table name.
// If the metrics table ends with "_otel_metrics", replace that suffix with "_otel_logs".
// Otherwise, append "_otel_logs" as a sibling.
func deriveLogsTable(metricsTable string) string {
	if strings.HasSuffix(metricsTable, "_otel_metrics") {
		return strings.TrimSuffix(metricsTable, "_otel_metrics") + "_otel_logs"
	}
	return metricsTable + "_otel_logs"
}
