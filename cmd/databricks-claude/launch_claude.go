package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/IceRhymers/databricks-agents/internal/core"
	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
	"github.com/IceRhymers/databricks-agents/internal/core/proxy"
	"github.com/IceRhymers/databricks-agents/pkg/websearch"
)

// buildClaudeLaunchPlan performs the claude-specific pre-flight for wrapper mode
// and returns a neutral core.LaunchPlan for core.Run to execute. It owns
// everything up to (but not including) the proxy port bind: logging setup,
// settings.json read, profile resolution, auth (with interactive login
// fallback), the startup security warnings, upstream/OTEL discovery, token
// seeding, port resolution, the settings→state OTEL-table migration, TLS
// validation, and websearch backend construction. The proxyURL-dependent env
// block is deferred into the returned plan.BuildEnv closure, invoked by core.Run
// once the port is bound.
//
// Ordering is preserved byte-for-byte with the pre-extraction inline main():
// auth MUST run before token seeding (EnsureAuthenticated triggers the browser
// login fallback; seeding first would fail with an unfriendly error and skip
// login). Fatal conditions are returned as errors WITHOUT the "databricks-claude:"
// prefix; the caller adds it via log.Fatalf, so the silent-in-non-verbose /
// visible-in-verbose behavior of the original inline Fatalfs is preserved via
// the shared log output global this function configures.
func buildClaudeLaunchPlan(a *Args) (core.LaunchPlan, error) {
	var plan core.LaunchPlan

	// --- Resolve home dir (before log output is silenced, so a failure here is
	// visible on the caller's default stderr — matching the original order) ---
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return plan, fmt.Errorf("cannot determine home dir: %v", err)
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
			return plan, fmt.Errorf("cannot open log file %q: %v", a.LogFile, err)
		}
		// Not closed explicitly: os.Exit skips defers and the process holds the
		// file until exit (matching the original inline defer-that-never-runs).
		if a.Verbose {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		} else {
			log.SetOutput(f)
		}
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	settingsDoc, err := readSettingsJSON(settingsPath)
	if err != nil {
		return plan, fmt.Errorf("cannot read settings.json: %v", err)
	}
	env := envBlock(settingsDoc)

	// Resolve profile: --profile flag (saved to state file) > state file > "DEFAULT".
	// DATABRICKS_CONFIG_PROFILE is intentionally NOT consulted (settings.json env
	// injection would override the user's explicit --profile choice).
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

	// --- Ensure the user is authenticated before proceeding (browser login
	// fallback). MUST precede token seeding below. ---
	if err := authcheck.EnsureAuthenticated(resolvedProfile, ""); err != nil {
		return plan, fmt.Errorf("auth failed: %v", err)
	}

	// --- Startup security checks ---
	for _, w := range proxy.SecurityChecks() {
		fmt.Fprintln(os.Stderr, w)
	}

	// Extract upstream values from settings.json. ANTHROPIC_BASE_URL is the real
	// upstream unless it is stale localhost (crash leftover), which we ignore.
	inferenceUpstream := ""
	if v, ok := env["ANTHROPIC_BASE_URL"].(string); ok {
		inferenceUpstream = v
	}
	if strings.HasPrefix(inferenceUpstream, "http://127.0.0.1") || strings.HasPrefix(inferenceUpstream, "https://127.0.0.1") {
		inferenceUpstream = ""
	}
	databricksHost := readDatabricksCfgHost(resolvedProfile)

	// Detect existing OTEL endpoint; clear stale localhost so it re-resolves.
	otelEndpoint := ""
	if v, ok := env["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"].(string); ok && v != "" {
		otelEndpoint = v
	}
	if otelEndpoint != "" && strings.HasPrefix(otelEndpoint, "http://127.0.0.1") {
		otelEndpoint = ""
	}

	// Read table names from settings.json env block; track raw values so we can
	// migrate them to the state file on first read.
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
		return plan, fmt.Errorf("failed to fetch initial token for profile %q: %v", resolvedProfile, err)
	}

	// --- Self-setup: discover host + gateway URL if ANTHROPIC_BASE_URL is absent ---
	needsFullSetup := false
	if a.Upstream != "" {
		inferenceUpstream = a.Upstream
		log.Printf("databricks-claude: using explicit upstream: %s", inferenceUpstream)
		if databricksHost == "" {
			if h, err := DiscoverHost(resolvedProfile, ""); err == nil {
				databricksHost = h
			}
		}
		needsFullSetup = true
	} else if inferenceUpstream == "" {
		log.Printf("databricks-claude: no ANTHROPIC_BASE_URL configured — discovering from profile %q", resolvedProfile)
		host, err := DiscoverHost(resolvedProfile, "")
		if err != nil {
			return plan, fmt.Errorf("failed to discover host for profile %q: %v\nRun 'databricks auth login --profile %s' first",
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
		otelUpstream = inferenceUpstream
	}

	// --- Resolve port for downstream binding ---
	port := resolvePort(a.Port, loadState())

	// Migrate OTEL tables present in settings.json but not yet in the state file
	// (forward-migration for pre-#172 installs whose config lived only in
	// settings.json).
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
		return plan, fmt.Errorf("%v", err)
	}

	// --- Read --with-websearch (workaround) settings from state ---
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
			return plan, fmt.Errorf("%v", err)
		}
		wsBackendImpl = b
		wsRobots = &websearch.Robots{}
	}

	// --- BuildEnv: the proxyURL-dependent settings env block. Reproduces the
	// original inline OTEL emission (table-presence semantics) + the Databricks
	// full-setup keys. Invoked by core.Run after the port binds. All CLAUDE_*/
	// OTEL_* key knowledge stays here, in the claude launcher. ---
	buildEnv := func(proxyURL string) map[string]string {
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
		if needsFullSetup {
			// launchModelRouting reads persisted state (no network — the
			// no-hot-path-discovery invariant). wsState is already in scope.
			for k, v := range databricksFullSetupEnv(launchModelRouting(wsState)) {
				otelEnv[k] = v
			}
		}
		return otelEnv
	}

	plan = core.LaunchPlan{
		InferenceUpstream: inferenceUpstream,
		OTELUpstream:      otelUpstream,
		UCMetricsTable:    ucMetricsTable,
		UCLogsTable:       ucLogsTable,
		UCTracesTable:     ucTracesTable,
		TokenProvider:     tp,
		WebSearch: proxy.WebSearchSettings{
			Enabled:     withWebSearch,
			Backend:     wsBackendImpl,
			Robots:      wsRobots,
			FetchBudget: wsBudget,
		},
		Port:           port,
		PortFlag:       a.Port,
		ProfileName:    resolvedProfile,
		TLSCert:        a.TLSCert,
		TLSKey:         a.TLSKey,
		ProxyAPIKey:    a.ProxyAPIKey,
		Verbose:        a.Verbose,
		Version:        Version,
		ToolName:       "databricks-claude",
		RefcountPrefix: ".databricks-claude-sessions",
		ManagedEnvVar:  "DATABRICKS_CLAUDE_MANAGED=1",
		NoUpdateCheck:  a.NoUpdateCheck,
		UpdaterConfig:  buildUpdaterConfig(),
		BuildEnv:       buildEnv,
	}
	return plan, nil
}
