package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
	"github.com/IceRhymers/databricks-claude/pkg/cli"
	"github.com/IceRhymers/databricks-claude/pkg/completion"
	"github.com/IceRhymers/databricks-claude/pkg/health"
	"github.com/IceRhymers/databricks-claude/pkg/lifecycle"
	"github.com/IceRhymers/databricks-claude/pkg/mdmprofile"
	"github.com/IceRhymers/databricks-claude/pkg/portbind"
	"github.com/IceRhymers/databricks-claude/pkg/proxy"
	"github.com/IceRhymers/databricks-claude/pkg/refcount"
	"github.com/IceRhymers/databricks-claude/pkg/updater"
	"github.com/IceRhymers/databricks-claude/pkg/websearch"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// Args holds all parsed command-line arguments for databricks-claude.
type Args struct {
	Profile                 string
	Verbose                 bool
	Version                 bool
	ShowHelp                bool
	PrintEnv                bool
	OTEL                    bool
	OTELMetricsTable        string
	OTELMetricsTableSet     bool
	OTELLogsTable           string
	OTELLogsTableSet        bool
	OTELTraces              bool
	OTELTracesTable         string
	OTELTracesTableSet      bool
	Upstream                string
	LogFile                 string
	NoOTEL                  bool
	NoOTELMetrics           bool
	NoOTELLogs              bool
	NoOTELTraces            bool
	ProxyAPIKey             string
	TLSCert                 string
	TLSKey                  string
	Port                    int
	Headless                bool
	IdleTimeout             time.Duration
	InstallHooks            bool
	UninstallHooks          bool
	HeadlessEnsure          bool
	HeadlessRelease         bool
	NoUpdateCheck           bool
	WithWebSearch           bool
	WithWebSearchSet        bool
	WebSearchBackend        string
	WebSearchBackendSet     bool
	WebSearchFetchBudget    int
	WebSearchFetchBudgetSet bool
	ClaudeArgs              []string
}

func main() {
	// completion <shell> — must be the very first check, before any flag parsing,
	// auth, or state loading. Safe to call in the Homebrew install sandbox.
	if len(os.Args) >= 2 && os.Args[1] == "completion" {
		completion.Run(os.Args[2:], flagDefs, "databricks-claude")
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

	// Parse databricks-claude flags, passing everything else through to claude.
	// Usage: databricks-claude [databricks-claude-flags] [--] [claude-args...]
	// Unknown flags are forwarded to claude automatically.
	// Tip: use "databricks-claude -- completion" to pass "completion" to claude.
	a, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "databricks-claude:", err)
		os.Exit(1)
	}

	if a.ShowHelp {
		handleHelp(a.Upstream)
		os.Exit(0)
	}

	if a.Version {
		fmt.Printf("databricks-claude %s\n", Version)
		os.Exit(0)
	}

	// --- Hook lifecycle commands (handled before auth/config setup) ---
	if a.InstallHooks || a.UninstallHooks {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("databricks-claude: cannot determine home dir: %v", err)
		}
		sp := filepath.Join(homeDir, ".claude", "settings.json")
		if a.InstallHooks {
			// First-run env setup: persist profile/port and write
			// ANTHROPIC_BASE_URL placeholder so users no longer need to run
			// `databricks-claude` once before installing hooks. The placeholder
			// URL is overwritten by --headless-ensure at session start with
			// the discovered gateway URL.
			resolvedProfile := a.Profile
			if resolvedProfile == "" {
				resolvedProfile = "DEFAULT"
			}
			port := resolvePort(a.Port, loadState())
			placeholder := fmt.Sprintf("http://127.0.0.1:%d", port)
			if err := bootstrapSettings(a.Port, resolvedProfile, placeholder, nil); err != nil {
				log.Fatalf("databricks-claude: --install-hooks bootstrap: %v", err)
			}
			if err := installHooks(sp); err != nil {
				log.Fatalf("databricks-claude: --install-hooks: %v", err)
			}
			fmt.Fprintln(os.Stderr, "databricks-claude: hooks installed — SessionStart and SessionEnd hooks added to ~/.claude/settings.json")
		} else {
			if err := uninstallHooks(sp); err != nil {
				log.Fatalf("databricks-claude: --uninstall-hooks: %v", err)
			}
			fmt.Fprintln(os.Stderr, "databricks-claude: hooks removed from ~/.claude/settings.json")
		}
		os.Exit(0)
	}

	// --- Headless hook commands (called by installed hooks, not by end users) ---
	if a.HeadlessEnsure || a.HeadlessRelease {
		state := loadState()
		port := resolvePort(a.Port, state)
		if a.HeadlessEnsure {
			headlessEnsure(port)
		} else {
			headlessRelease(port)
		}
		os.Exit(0)
	}

	// --no-otel and --no-otel-{metrics,logs,traces}: clear persisted OTEL keys
	// from settings.json. Per-signal flags clear only that signal; --no-otel
	// is the nuclear option that clears every signal plus the telemetry toggle.
	if a.NoOTEL || a.NoOTELMetrics || a.NoOTELLogs || a.NoOTELTraces {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("databricks-claude: cannot determine home dir: %v", err)
		}
		settingsPathForClear := filepath.Join(homeDir, ".claude", "settings.json")
		if a.NoOTEL {
			if err := clearOTELKeys(settingsPathForClear); err != nil {
				log.Fatalf("databricks-claude: failed to clear OTEL keys: %v", err)
			}
			fmt.Fprintln(os.Stderr, "databricks-claude: OTEL keys cleared — OTEL disabled for future sessions")
		} else {
			if a.NoOTELMetrics {
				if err := clearOTELKeysSubset(settingsPathForClear, otelMetricsKeys); err != nil {
					log.Fatalf("databricks-claude: failed to clear OTEL metrics keys: %v", err)
				}
				fmt.Fprintln(os.Stderr, "databricks-claude: OTEL metrics keys cleared")
			}
			if a.NoOTELLogs {
				if err := clearOTELKeysSubset(settingsPathForClear, otelLogsKeys); err != nil {
					log.Fatalf("databricks-claude: failed to clear OTEL logs keys: %v", err)
				}
				fmt.Fprintln(os.Stderr, "databricks-claude: OTEL logs keys cleared")
			}
			if a.NoOTELTraces {
				if err := clearOTELKeysSubset(settingsPathForClear, otelTracesKeys); err != nil {
					log.Fatalf("databricks-claude: failed to clear OTEL traces keys: %v", err)
				}
				fmt.Fprintln(os.Stderr, "databricks-claude: OTEL traces keys cleared")
			}
		}
		// Continue — flags only clear persisted state; other flags on the same
		// invocation can still re-enable specific signals.
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

	// --no-otel-{signal} disables the signal for this session. Zero out any
	// table value that the state-file fallback above may have populated —
	// the flag wins over persisted state (but the state file is left intact
	// so the next --otel invocation can pick the tables back up).
	if a.NoOTEL || a.NoOTELMetrics {
		ucMetricsTable = ""
	}
	if a.NoOTEL || a.NoOTELLogs {
		ucLogsTable = ""
	}
	if a.NoOTEL || a.NoOTELTraces {
		ucTracesTable = ""
	}

	// --- Seed token cache ---
	tp := NewTokenProvider(resolvedProfile, "")
	initialToken, err := tp.Token(context.Background())
	if err != nil {
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

	// OTEL table resolution follows table-presence semantics: a signal's env
	// vars are only emitted when its UC table is configured (flag, persisted
	// settings.json, or — for metrics/logs — the legacy --otel default).
	//
	// Metrics table: --otel-metrics-table > persisted > --otel default. Without
	// any of those, ucMetricsTable stays empty and metrics env vars are skipped.
	if a.OTELMetricsTableSet {
		ucMetricsTable = a.OTELMetricsTable
	} else if ucMetricsTable == "" && a.OTEL {
		ucMetricsTable = a.OTELMetricsTable
	}

	// Logs table: --otel-logs-table > persisted > derive-from-metrics (only
	// when metrics is itself configured). Without any of those it stays empty.
	if a.OTELLogsTableSet {
		ucLogsTable = a.OTELLogsTable
	} else if ucLogsTable == "" && ucMetricsTable != "" {
		ucLogsTable = deriveLogsTable(ucMetricsTable)
	}

	// Traces table: --otel-traces-table > persisted. No default — traces are
	// opt-in via --otel-traces / --otel-traces-table.
	if a.OTELTracesTableSet {
		ucTracesTable = a.OTELTracesTable
	}
	// If --otel-traces is passed but no table is configured (neither flag nor
	// persisted), traces is silently skipped — see the env-injection block.
	_ = a.OTELTraces

	// --- Resolve port for downstream binding ---
	port := resolvePort(a.Port, loadState())

	// --- Print env and exit if requested ---
	// This exit is intentionally before any state or settings.json writes so
	// --print-env remains a read-only diagnostic (no side effects).
	if a.PrintEnv {
		otelActive := ucMetricsTable != "" || ucLogsTable != "" || ucTracesTable != ""
		handlePrintEnv(resolvedProfile, databricksHost, inferenceUpstream, initialToken, a.Upstream, otelActive, ucMetricsTable, ucLogsTable, ucTracesTable)
		os.Exit(0)
	}

	// Persist OTel table names to the state file so they survive --no-otel.
	// We write when an explicit --otel-*-table flag was given, or when a table
	// was found in settings.json but hasn't been migrated to state yet.
	// Uses tableState (loaded above for fallback) directly to avoid a second
	// loadState call; bootstrapSettings below will do its own fresh load.
	{
		metricsToSave := ""
		logsToSave := ""
		tracesToSave := ""
		if a.OTELMetricsTableSet {
			metricsToSave = ucMetricsTable
		} else if metricsFromSettings != "" && tableState.OtelMetricsTable == "" {
			metricsToSave = metricsFromSettings
		}
		if a.OTELLogsTableSet {
			logsToSave = ucLogsTable
		} else if logsFromSettings != "" && tableState.OtelLogsTable == "" {
			logsToSave = logsFromSettings
		}
		if a.OTELTracesTableSet {
			tracesToSave = ucTracesTable
		} else if tracesFromSettings != "" && tableState.OtelTracesTable == "" {
			tracesToSave = tracesFromSettings
		}
		mutated := false
		if metricsToSave != "" && tableState.OtelMetricsTable != metricsToSave {
			tableState.OtelMetricsTable = metricsToSave
			mutated = true
		}
		if logsToSave != "" && tableState.OtelLogsTable != logsToSave {
			tableState.OtelLogsTable = logsToSave
			mutated = true
		}
		if tracesToSave != "" && tableState.OtelTracesTable != tracesToSave {
			tableState.OtelTracesTable = tracesToSave
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

	// --- Resolve --with-websearch (workaround) settings ---
	// Resolution chain: flag (if set) > saved state > default. Persist any
	// flag value back to state so users only opt in once. Mirrors the
	// OTEL-table persistence pattern.
	wsState := loadState()
	withWebSearch := wsState.WithWebSearch
	wsBackend := wsState.WebSearchBackend
	wsBudget := wsState.WebSearchFetchBudget
	if a.WithWebSearchSet {
		withWebSearch = a.WithWebSearch
	}
	if a.WebSearchBackendSet {
		wsBackend = a.WebSearchBackend
	}
	if a.WebSearchFetchBudgetSet {
		wsBudget = a.WebSearchFetchBudget
	}
	if wsBackend == "" {
		wsBackend = "duckduckgo"
	}
	if wsBudget <= 0 {
		wsBudget = 100 * 1024
	}
	{
		mutated := false
		if a.WithWebSearchSet && wsState.WithWebSearch != withWebSearch {
			wsState.WithWebSearch = withWebSearch
			mutated = true
		}
		if a.WebSearchBackendSet && wsState.WebSearchBackend != wsBackend {
			wsState.WebSearchBackend = wsBackend
			mutated = true
		}
		if a.WebSearchFetchBudgetSet && wsState.WebSearchFetchBudget != wsBudget {
			wsState.WebSearchFetchBudget = wsBudget
			mutated = true
		}
		if mutated {
			if err := saveState(wsState); err != nil {
				log.Printf("databricks-claude: warning: could not persist websearch state: %v", err)
			}
		}
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

	// --- Reference counting (before server start so lifecycle wrapper can use refcountPath) ---
	// In headless mode, sessions manage the refcount via hooks (--headless-ensure
	// acquires, --headless-release releases). The proxy itself does NOT self-acquire
	// so the last session's release brings the count to 0 and triggers shutdown.
	// In wrapper mode, the parent process acquires here and releases on exit.
	refcountPath := refcount.PathForPort(".databricks-claude-sessions", port)
	if !a.Headless {
		if err := refcount.Acquire(refcountPath); err != nil {
			log.Printf("databricks-claude: refcount acquire warning: %v", err)
		}
	}

	// In headless mode, wrap handler with /shutdown endpoint and idle timeout.
	// promoteCh is closed when this process wins the health-watcher election
	// and takes over as the primary proxy owner; WrapWithLifecycle uses it to
	// promote IsOwner so /shutdown correctly triggers shutdown after takeover.
	var doneCh chan struct{}
	var promoteCh chan struct{}
	if a.Headless {
		doneCh = make(chan struct{})
		if !isOwner {
			promoteCh = make(chan struct{})
		}
		handler = lifecycle.WrapWithLifecycle(lifecycle.Config{
			Inner:        handler,
			RefcountPath: refcountPath,
			IsOwner:      isOwner,
			PromoteCh:    promoteCh,
			IdleTimeout:  a.IdleTimeout,
			APIKey:       a.ProxyAPIKey,
			DoneCh:       doneCh,
			LogPrefix:    "databricks-claude",
		})
	}

	// --- Start proxy if we own the port; otherwise watch for owner death ---
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
		// Watch for owner death and take over the proxy if needed.
		// onTakeover closes promoteCh so the lifecycle wrapper promotes this
		// process to owner, enabling /shutdown to trigger a clean shutdown.
		onTakeover := func() {
			if promoteCh != nil {
				close(promoteCh)
			}
		}
		go health.WatchProxy(port, handler, a.TLSCert, a.TLSKey, "databricks-claude", onTakeover)
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
		// Also write Databricks-specific keys for full setup.
		otelEnv["ANTHROPIC_MODEL"] = "databricks-claude-opus-4-7"
		otelEnv["ANTHROPIC_DEFAULT_OPUS_MODEL"] = "databricks-claude-opus-4-7"
		otelEnv["ANTHROPIC_DEFAULT_SONNET_MODEL"] = "databricks-claude-sonnet-4-6"
		otelEnv["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = "databricks-claude-haiku-4-5"
		otelEnv["ANTHROPIC_CUSTOM_HEADERS"] = "x-databricks-use-coding-agent-mode: true"
		otelEnv["CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS"] = "1"
	}

	if err := bootstrapSettings(a.Port, resolvedProfile, proxyURL, otelEnv); err != nil {
		if a.Headless {
			fmt.Fprintf(os.Stderr, "databricks-claude: warning: config write failed: %v\n", err)
		} else {
			log.Fatalf("databricks-claude: %v", err)
		}
	}

	// --- Log startup info ---
	log.Printf("databricks-claude: proxy on %s (owner=%v), profile=%s, upstream=%s",
		proxyURL, isOwner, resolvedProfile, inferenceUpstream)

	if a.Headless {
		runHeadless(proxyURL, ln, isOwner, refcountPath, doneCh)
		return
	}

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

// runHeadless runs the proxy without launching a claude child process.
// It prints the proxy URL to stdout, then blocks until SIGINT/SIGTERM
// or until doneCh is closed (by /shutdown or idle timeout).
// The watchProxy goroutine (for non-owner sessions) is already started
// before this function is called.
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
// databricks-claude owns: --profile, --verbose/-v, --log-file, --version,
// --otel, --otel-metrics-table, --otel-logs-table, --otel-traces,
// --otel-traces-table, --no-otel, --no-otel-metrics, --no-otel-logs,
// --no-otel-traces, --proxy-api-key, --tls-cert, --tls-key.
// Everything else (including unknown flags like --debug) passes through to claude.
// An explicit "--" separator is supported but not required.
func parseArgs(args []string) (*Args, error) {
	a := &Args{
		OTELMetricsTable: "main.claude_telemetry.claude_otel_metrics", // default
		IdleTimeout:      30 * time.Minute,                            // default
	}

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
				case "--otel-metrics-table":
					if value != "" {
						a.OTELMetricsTable = value
						a.OTELMetricsTableSet = true
					} else if i+1 < len(args) {
						i++
						a.OTELMetricsTable = args[i]
						a.OTELMetricsTableSet = true
					}
				case "--otel-logs-table":
					if value != "" {
						a.OTELLogsTable = value
						a.OTELLogsTableSet = true
					} else if i+1 < len(args) {
						i++
						a.OTELLogsTable = args[i]
						a.OTELLogsTableSet = true
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
				case "--print-env":
					a.PrintEnv = true
				case "--otel":
					a.OTEL = true
				case "--otel-traces":
					a.OTELTraces = true
				case "--otel-traces-table":
					if value != "" {
						a.OTELTracesTable = value
						a.OTELTracesTableSet = true
					} else if i+1 < len(args) {
						i++
						a.OTELTracesTable = args[i]
						a.OTELTracesTableSet = true
					}
				case "--no-otel":
					a.NoOTEL = true
				case "--no-otel-metrics":
					a.NoOTELMetrics = true
				case "--no-otel-logs":
					a.NoOTELLogs = true
				case "--no-otel-traces":
					a.NoOTELTraces = true
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
				case "--headless":
					a.Headless = true
				case "--install-hooks":
					a.InstallHooks = true
				case "--uninstall-hooks":
					a.UninstallHooks = true
				case "--headless-ensure":
					a.HeadlessEnsure = true
				case "--headless-release":
					a.HeadlessRelease = true
				case "--no-update-check":
					a.NoUpdateCheck = true
				case "--with-websearch":
					a.WithWebSearch = true
					a.WithWebSearchSet = true
					// Allow optional explicit value: --with-websearch=true|false
					if value != "" {
						a.WithWebSearch = (value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes"))
					}
				case "--websearch-backend":
					if value != "" {
						a.WebSearchBackend = value
						a.WebSearchBackendSet = true
					} else if i+1 < len(args) {
						i++
						a.WebSearchBackend = args[i]
						a.WebSearchBackendSet = true
					}
				case "--websearch-fetch-budget":
					raw := value
					if raw == "" && i+1 < len(args) {
						i++
						raw = args[i]
					}
					if raw != "" {
						if n, err := strconv.Atoi(raw); err == nil {
							a.WebSearchFetchBudget = n
							a.WebSearchFetchBudgetSet = true
						} else {
							return nil, fmt.Errorf("--websearch-fetch-budget: %q is not an integer", raw)
						}
					}
				case "--idle-timeout":
					raw := value
					if raw == "" && i+1 < len(args) {
						i++
						raw = args[i]
					}
					if raw != "" {
						if d, err := time.ParseDuration(raw); err == nil {
							a.IdleTimeout = d
						} else {
							return nil, fmt.Errorf("--idle-timeout: %q is not a valid duration (use e.g. 30s, 5m, 1h)", raw)
						}
					}
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

// handleHelp prints the databricks-claude help message and appends claude's own --help output.
func handleHelp(upstreamBinary string) {
	fmt.Printf(`databricks-claude v%s — Databricks AI Gateway proxy for Claude Code

Transparently proxies the Claude Code CLI with Databricks AI Gateway authentication
injected via environment variables.

Usage:
  databricks-claude [databricks-claude flags] [claude flags] [claude args]

Databricks-Claude Flags:
  --profile string      Databricks config profile (saved to ~/.claude/.databricks-claude.json)
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --print-env           Print resolved configuration and exit (token redacted)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --otel                       Enable OpenTelemetry metrics + logs export
  --otel-metrics-table string  Unity Catalog table for OTEL metrics
  --otel-logs-table string     Unity Catalog table for OTEL logs (derived from metrics table if omitted)
  --otel-traces                Enable OpenTelemetry traces export (Claude Code beta;
                               requires --otel-traces-table; can be used standalone)
  --otel-traces-table string   Unity Catalog table for OTEL traces (cat.schema.table)
  --no-otel                    Clear persisted OTEL keys and disable OTEL for future sessions
  --no-otel-metrics            Clear persisted OTEL metrics keys (other signals untouched)
  --no-otel-logs               Clear persisted OTEL logs keys (other signals untouched)
  --no-otel-traces             Clear persisted OTEL traces keys (other signals untouched)
  --proxy-api-key string       Require Bearer token auth on all proxy requests
  --tls-cert string            Path to TLS certificate file (requires --tls-key)
  --tls-key string             Path to TLS private key file (requires --tls-cert)
  --port int                   Fixed proxy port (default: 49153, saved to state)
  --headless                   Start proxy without launching claude (for IDE extensions)
  --headless-ensure            Start proxy if not running (called by SessionStart hook)
  --headless-release           Decrement proxy refcount (called by Stop hook)
  --idle-timeout duration      Idle timeout for headless mode (default 30m, 0 disables; use e.g. 30s, 5m, 1h)
  --install-hooks              Install SessionStart/SessionEnd hooks AND perform first-run
                               env setup (idempotent). Accepts --profile and --port to
                               persist them; no prior databricks-claude invocation needed.
  --uninstall-hooks            Remove databricks-claude hooks from ~/.claude/settings.json
  --no-update-check            Skip the automatic update check on startup
  --with-websearch             Enable local fulfillment of Anthropic web_search/web_fetch
                               tools (workaround until Databricks FMAPI ships native
                               support; saved to state). Default: disabled.
  --websearch-backend string   Search backend when --with-websearch is enabled.
                               Values: duckduckgo (default, zero config), none
  --websearch-fetch-budget int Max bytes returned per web_fetch call (default 102400)
  --version                    Print version and exit
  --help, -h                   Show this help message

Subcommands:
  completion <shell>           Generate shell completions (bash, zsh, fish)
  update                       Check for a newer release and print upgrade instructions
  desktop <action>             Claude Desktop integration. Run 'databricks-claude desktop'
                               for actions (generate-config, credential-helper) and flags.
  setup [flags]                Idempotent auth bootstrap. Persists the profile to state and
                               runs 'databricks auth login' when not authenticated. Designed
                               for fleet init scripts and per-user login agents.
                               Run 'databricks-claude setup --help' for flags.

Example Unity Catalog table setup (run in a Databricks SQL warehouse):

  CREATE TABLE main.claude_telemetry.claude_otel_metrics (
    ... -- see https://docs.databricks.com/aws/en/ai-gateway/coding-agent-integration-beta
  ) USING DELTA TBLPROPERTIES ('otel.schemaVersion' = 'v1');

  CREATE TABLE main.claude_telemetry.claude_otel_logs (
    ... -- see https://docs.databricks.com/aws/en/ai-gateway/coding-agent-integration-beta
  ) USING DELTA TBLPROPERTIES ('otel.schemaVersion' = 'v1');

────────────────────────────────────────────────────────────────────────────────
Claude CLI Options:
`, Version)

	// Determine which binary to run for --help passthrough.
	claudeBin := upstreamBinary
	if claudeBin == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			claudeBin = p
		}
	}

	if claudeBin == "" {
		fmt.Println("(claude binary not found on PATH — install from https://claude.ai/code)")
		return
	}

	var buf bytes.Buffer
	cmd := exec.Command(claudeBin, "--help")
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	_ = cmd.Run()
	fmt.Print(buf.String())
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
