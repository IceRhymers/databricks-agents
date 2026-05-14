package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/IceRhymers/databricks-claude/internal/cmd"
)

// runConfigCommand implements the `databricks-claude config ...` dispatcher.
// args is everything after the literal "config" token. Routes to the otel,
// websearch, write, or show runner. Bare `config` (no args) prints help and
// exits 2 — same convention as `desktop` with no action.
//
// The persistent-config editor was previously a sprawl of 14 root flags
// (--otel*, --no-otel*, --write-claude-config, --print-env, --with-websearch
// / --websearch-*); #172 consolidates them under this tree. Storage
// semantics are byte-identical with the legacy paths — only the surface has
// moved. The pure-function resolvers (resolveConfigOTEL,
// resolveConfigWebSearch) make the orchestration matrix testable in
// isolation; helper-level tests passing while composition is broken is the
// known failure mode for this kind of refactor, and the matrix test in
// config_test.go is the safety net.
func runConfigCommand(args []string) {
	if len(args) == 0 {
		_ = cmd.Render(os.Stderr, configCommand, nil)
		os.Exit(2)
	}
	switch args[0] {
	case "otel":
		runConfigOTEL(args[1:])
	case "websearch":
		runConfigWebSearch(args[1:])
	case "write":
		runConfigWrite(args[1:])
	case "show":
		runConfigShow(args[1:])
	case "--help", "-h", "help":
		_ = cmd.Render(os.Stdout, configCommand, nil)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "databricks-claude: unknown config subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, configCommand, nil)
		os.Exit(1)
	}
}

// configOTELResolution is the pure-function projection of the OTEL enable
// resolution chain. Extracted so the orchestration matrix test (state
// empty/populated × explicit table flags × derive-logs-from-metrics) can
// drive it in isolation. Caller is responsible for the post-resolve
// settings.json write.
type configOTELResolution struct {
	Profile        string
	Port           int
	ProxyURL       string
	MetricsTable   string
	LogsTable      string
	TracesTable    string
	OTELEnv        map[string]string // keys to write into settings.json env block
	NewState       persistentState   // state to persist (callers gate the write on a mutated check)
	StateMutated   bool              // true when NewState differs from the input saved state
	TracesEnabled  bool              // mirrors --traces flag (for log lines / future use)
}

// resolveConfigOTEL performs the OTEL enable-side resolution that the legacy
// --write-claude-config block did inline. Pure function: no I/O, no global
// state. Drives the orchestration matrix test (#172 acceptance criterion).
//
// Resolution chain per signal: explicit flag > saved state > derive (logs
// from metrics, only when metrics is set) > unset. With NO --metrics-table
// flag AND empty state, we apply the same default the legacy --otel bare
// toggle did: "main.claude_telemetry.claude_otel_metrics". This preserves
// the issue-#172 behaviour-mapping note "preserve the
// `else if ucMetricsTable == "" && a.OTEL` default-application from
// main.go:595–597".
//
// otelEnv contains ONLY the OTEL keys (CLAUDE_OTEL_UC_*_TABLE, exporter
// endpoints/headers/protocols, intervals, CLAUDE_CODE_ENABLE_TELEMETRY).
// databricksFullSetupEnv() is intentionally NOT merged here — that is
// `config write`'s job. Callers route otelEnv through bootstrapSettings,
// which always (re)sets ANTHROPIC_BASE_URL to proxyURL — same as the legacy
// --write-claude-config flow.
//
// applyMetricsDefault gates the bare-toggle metrics default. `config otel
// enable` passes true: a bare invocation (no --metrics-table, empty state)
// must enable OTEL with the legacy --otel default table, matching the old
// `else if ucMetricsTable == "" && a.OTEL` branch. `config write` passes
// FALSE: the legacy --write-claude-config block resolved tables strictly
// flag → state → empty and NEVER applied the metrics default, so a
// fresh-install `config write` with no flags writes only the model-routing
// env block — it must NOT silently enable telemetry export to a UC table
// the user never created (which would 400 at Databricks ingest).
func resolveConfigOTEL(
	saved persistentState,
	port int,
	proxyURL string,
	metricsTableFlag string, metricsTableSet bool,
	logsTableFlag string, logsTableSet bool,
	tracesFlag bool,
	tracesTableFlag string, tracesTableSet bool,
	applyMetricsDefault bool,
) configOTELResolution {
	r := configOTELResolution{
		Port:          port,
		ProxyURL:      proxyURL,
		MetricsTable:  saved.OtelMetricsTable,
		LogsTable:     saved.OtelLogsTable,
		TracesTable:   saved.OtelTracesTable,
		NewState:      saved,
		TracesEnabled: tracesFlag,
	}

	if metricsTableSet {
		r.MetricsTable = metricsTableFlag
	} else if r.MetricsTable == "" && applyMetricsDefault {
		// Bare `config otel enable` (no --metrics-table, no state) inherits the
		// legacy --otel default. Without this, an empty-state user invoking
		// `config otel enable` with no flags would write nothing — the issue
		// explicitly maps the bare toggle to "enable with default".
		//
		// `config write` passes applyMetricsDefault=false: the legacy
		// --write-claude-config flow never applied this default, and applying
		// it here would silently enable telemetry on a fresh install.
		r.MetricsTable = "main.claude_telemetry.claude_otel_metrics"
	}

	if logsTableSet {
		r.LogsTable = logsTableFlag
	} else if r.LogsTable == "" && r.MetricsTable != "" {
		r.LogsTable = deriveLogsTable(r.MetricsTable)
	}

	if tracesTableSet {
		r.TracesTable = tracesTableFlag
	}

	// Persist explicitly-set tables to state (sentinel-guarded mutated-flag
	// pattern — empty resolved values are NOT persisted, matching shouldPersistOTELTable).
	mutated := false
	if metricsTableSet && r.MetricsTable != "" && r.NewState.OtelMetricsTable != r.MetricsTable {
		r.NewState.OtelMetricsTable = r.MetricsTable
		mutated = true
	}
	if logsTableSet && r.LogsTable != "" && r.NewState.OtelLogsTable != r.LogsTable {
		r.NewState.OtelLogsTable = r.LogsTable
		mutated = true
	}
	if tracesTableSet && r.TracesTable != "" && r.NewState.OtelTracesTable != r.TracesTable {
		r.NewState.OtelTracesTable = r.TracesTable
		mutated = true
	}
	r.StateMutated = mutated

	r.OTELEnv = buildOTELEnv(r.MetricsTable, r.LogsTable, r.TracesTable, proxyURL)
	return r
}

// buildOTELEnv constructs the OTEL env keys to write into settings.json.
// Mirrors main.go:844–875 (the normal-flow otelEnv build) and main.go:285–312
// (the legacy --write-claude-config OTEL block) byte-for-byte. Each signal's
// keys are emitted only when its UC table is configured (table-presence
// semantics).
func buildOTELEnv(metricsTable, logsTable, tracesTable, proxyURL string) map[string]string {
	env := map[string]string{}
	if metricsTable != "" {
		env["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"] = proxyURL + "/otel/v1/metrics"
		env["OTEL_EXPORTER_OTLP_METRICS_HEADERS"] = "content-type=application/x-protobuf"
		env["OTEL_METRICS_EXPORTER"] = "otlp"
		env["OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"] = "http/protobuf"
		env["OTEL_METRIC_EXPORT_INTERVAL"] = "10000"
		env["CLAUDE_OTEL_UC_METRICS_TABLE"] = metricsTable
	}
	if logsTable != "" {
		env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] = proxyURL + "/otel/v1/logs"
		env["OTEL_EXPORTER_OTLP_LOGS_HEADERS"] = "content-type=application/x-protobuf"
		env["OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"] = "http/protobuf"
		env["OTEL_LOGS_EXPORTER"] = "otlp"
		env["OTEL_LOGS_EXPORT_INTERVAL"] = "5000"
		env["CLAUDE_OTEL_UC_LOGS_TABLE"] = logsTable
	}
	if tracesTable != "" {
		env["CLAUDE_CODE_ENHANCED_TELEMETRY_BETA"] = "1"
		env["OTEL_TRACES_EXPORTER"] = "otlp"
		env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = proxyURL + "/otel/v1/traces"
		env["OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"] = "http/protobuf"
		env["OTEL_TRACES_EXPORT_INTERVAL"] = "5000"
		env["CLAUDE_OTEL_UC_TRACES_TABLE"] = tracesTable
	}
	if metricsTable != "" || logsTable != "" || tracesTable != "" {
		env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	}
	return env
}

// configWebSearchResolution is the pure-function projection of the
// websearch enable/disable resolution. Mirrors configOTELResolution shape.
type configWebSearchResolution struct {
	NewState     persistentState
	StateMutated bool
}

// resolveConfigWebSearch performs the websearch enable-side resolution.
// Pure function. Storage: state file only — websearch is a proxy-side
// feature with no settings.json key (the proxy reads with_websearch from
// state on next start).
//
// `enable` semantics: with_websearch=true; flag values override state;
// missing flags inherit existing state (NOT defaults — the defaults live in
// the proxy's main.go startup block as the fallback when state is empty).
// `disable` semantics: with_websearch=false AND clear backend/fetch-budget
// so a future enable re-applies the proper defaults.
func resolveConfigWebSearch(
	saved persistentState,
	enable bool,
	backendFlag string, backendSet bool,
	budgetFlag int, budgetSet bool,
) configWebSearchResolution {
	r := configWebSearchResolution{NewState: saved}

	if !enable {
		// Disable: zero-out everything related to websearch so a re-enable
		// re-applies the defaults from runProxy. This is NEW behaviour
		// relative to the legacy CLI (which had no explicit websearch
		// disable flag); the issue body explicitly sanctions it.
		mutated := false
		if r.NewState.WithWebSearch {
			r.NewState.WithWebSearch = false
			mutated = true
		}
		if r.NewState.WebSearchBackend != "" {
			r.NewState.WebSearchBackend = ""
			mutated = true
		}
		if r.NewState.WebSearchFetchBudget != 0 {
			r.NewState.WebSearchFetchBudget = 0
			mutated = true
		}
		r.StateMutated = mutated
		return r
	}

	// Enable: with_websearch=true; flag overrides state; otherwise preserve
	// existing state. Sentinel-guard the persist: only write when something
	// actually changed.
	mutated := false
	if !r.NewState.WithWebSearch {
		r.NewState.WithWebSearch = true
		mutated = true
	}
	if backendSet && r.NewState.WebSearchBackend != backendFlag {
		r.NewState.WebSearchBackend = backendFlag
		mutated = true
	}
	if budgetSet && r.NewState.WebSearchFetchBudget != budgetFlag {
		r.NewState.WebSearchFetchBudget = budgetFlag
		mutated = true
	}
	r.StateMutated = mutated
	return r
}

// runConfigOTEL implements `databricks-claude config otel <enable|disable>`.
func runConfigOTEL(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "databricks-claude: 'config otel' requires a subcommand: enable or disable")
		_ = cmd.Render(os.Stderr, *configCommand.Subcommand("otel"), nil)
		os.Exit(2)
	}
	switch args[0] {
	case "enable":
		runConfigOTELEnable(args[1:])
	case "disable":
		runConfigOTELDisable(args[1:])
	case "--help", "-h", "help":
		_ = cmd.Render(os.Stdout, *configCommand.Subcommand("otel"), nil)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "databricks-claude: unknown config otel subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, *configCommand.Subcommand("otel"), nil)
		os.Exit(1)
	}
}

func runConfigOTELEnable(args []string) {
	node := configCommand.Subcommand("otel").Subcommand("enable")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	profile := r.Strings["profile"]
	saved := loadState()
	if profile == "" {
		profile = saved.Profile
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	portFlag := atoiOrZero(r.Strings["port"])
	port := resolvePort(portFlag, saved)
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Validate the profile is reachable. Mirrors --write-claude-config's
	// guard at main.go:240–243 — empty/unknown profile fails fast with an
	// actionable error rather than writing a guaranteed-broken settings.json.
	if _, err := DiscoverHost(profile, ""); err != nil {
		log.Fatalf("databricks-claude: config otel enable: failed to discover host for profile %q: %v\n"+
			"Run 'databricks auth login --profile %s' first", profile, err, profile)
	}

	res := resolveConfigOTEL(
		saved,
		port,
		proxyURL,
		r.Strings["metrics-table"], r.Set["metrics-table"],
		r.Strings["logs-table"], r.Set["logs-table"],
		r.Bools["traces"],
		r.Strings["traces-table"], r.Set["traces-table"],
		true, // config otel enable: bare invocation applies the legacy --otel metrics default
	)

	if res.StateMutated {
		if err := saveState(res.NewState); err != nil {
			log.Fatalf("databricks-claude: config otel enable: could not persist OTEL tables: %v", err)
		}
	}

	if err := bootstrapSettings(portFlag, profile, proxyURL, res.OTELEnv); err != nil {
		log.Fatalf("databricks-claude: config otel enable: %v", err)
	}

	fmt.Fprintf(os.Stderr, "databricks-claude: OTEL enabled (profile=%s, base_url=%s, metrics=%s, logs=%s, traces=%s)\n",
		profile, proxyURL, displayTableOrNone(res.MetricsTable), displayTableOrNone(res.LogsTable), displayTableOrNone(res.TracesTable))
}

func runConfigOTELDisable(args []string) {
	node := configCommand.Subcommand("otel").Subcommand("disable")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("databricks-claude: cannot determine home dir: %v", err)
	}
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	clearMetrics := r.Bools["metrics"]
	clearLogs := r.Bools["logs"]
	clearTraces := r.Bools["traces"]

	// No flags = nuclear option (every OTEL key, including
	// CLAUDE_CODE_ENABLE_TELEMETRY). Matches the legacy --no-otel.
	if !clearMetrics && !clearLogs && !clearTraces {
		if err := clearOTELKeys(settingsPath); err != nil {
			log.Fatalf("databricks-claude: config otel disable: failed to clear OTEL keys: %v", err)
		}
		fmt.Fprintln(os.Stderr, "databricks-claude: OTEL keys cleared — OTEL disabled for future sessions (state file table preferences preserved)")
		return
	}

	if clearMetrics {
		if err := clearOTELKeysSubset(settingsPath, otelMetricsKeys); err != nil {
			log.Fatalf("databricks-claude: config otel disable: failed to clear OTEL metrics keys: %v", err)
		}
		fmt.Fprintln(os.Stderr, "databricks-claude: OTEL metrics keys cleared")
	}
	if clearLogs {
		if err := clearOTELKeysSubset(settingsPath, otelLogsKeys); err != nil {
			log.Fatalf("databricks-claude: config otel disable: failed to clear OTEL logs keys: %v", err)
		}
		fmt.Fprintln(os.Stderr, "databricks-claude: OTEL logs keys cleared")
	}
	if clearTraces {
		if err := clearOTELKeysSubset(settingsPath, otelTracesKeys); err != nil {
			log.Fatalf("databricks-claude: config otel disable: failed to clear OTEL traces keys: %v", err)
		}
		fmt.Fprintln(os.Stderr, "databricks-claude: OTEL traces keys cleared")
	}
}

// runConfigWebSearch implements `databricks-claude config websearch <enable|disable>`.
func runConfigWebSearch(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "databricks-claude: 'config websearch' requires a subcommand: enable or disable")
		_ = cmd.Render(os.Stderr, *configCommand.Subcommand("websearch"), nil)
		os.Exit(2)
	}
	switch args[0] {
	case "enable":
		runConfigWebSearchEnable(args[1:])
	case "disable":
		runConfigWebSearchDisable(args[1:])
	case "--help", "-h", "help":
		_ = cmd.Render(os.Stdout, *configCommand.Subcommand("websearch"), nil)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "databricks-claude: unknown config websearch subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, *configCommand.Subcommand("websearch"), nil)
		os.Exit(1)
	}
}

func runConfigWebSearchEnable(args []string) {
	node := configCommand.Subcommand("websearch").Subcommand("enable")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	saved := loadState()
	budgetFlag, budgetSet := 0, false
	if r.Set["fetch-budget"] {
		raw := r.Strings["fetch-budget"]
		n, err := strconv.Atoi(raw)
		if err != nil {
			log.Fatalf("databricks-claude: config websearch enable: --fetch-budget: %q is not an integer", raw)
		}
		budgetFlag = n
		budgetSet = true
	}

	res := resolveConfigWebSearch(saved, true, r.Strings["backend"], r.Set["backend"], budgetFlag, budgetSet)
	if res.StateMutated {
		if err := saveState(res.NewState); err != nil {
			log.Fatalf("databricks-claude: config websearch enable: could not persist websearch state: %v", err)
		}
	}

	backend := res.NewState.WebSearchBackend
	if backend == "" {
		backend = "duckduckgo"
	}
	budget := res.NewState.WebSearchFetchBudget
	if budget <= 0 {
		budget = 100 * 1024
	}
	fmt.Fprintf(os.Stderr, "databricks-claude: websearch enabled (backend=%s, fetch-budget=%d). The proxy will fulfill web_search/web_fetch locally on its next start.\n", backend, budget)
}

func runConfigWebSearchDisable(args []string) {
	node := configCommand.Subcommand("websearch").Subcommand("disable")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	saved := loadState()
	res := resolveConfigWebSearch(saved, false, "", false, 0, false)
	if res.StateMutated {
		if err := saveState(res.NewState); err != nil {
			log.Fatalf("databricks-claude: config websearch disable: could not persist websearch state: %v", err)
		}
	}
	fmt.Fprintln(os.Stderr, "databricks-claude: websearch disabled (state file with_websearch=false; backend/fetch-budget cleared)")
}

// runConfigWrite implements `databricks-claude config write` — the legacy
// --write-claude-config flow lifted into a subcommand. Bootstraps the full
// settings.json env block (proxy URL, model routing, custom headers,
// optional OTEL keys) and exits. No proxy startup, no port binding, no
// child process. Idempotent.
func runConfigWrite(args []string) {
	node := configCommand.Subcommand("write")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	profile := r.Strings["profile"]
	saved := loadState()
	if profile == "" {
		profile = saved.Profile
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	portFlag := atoiOrZero(r.Strings["port"])
	port := resolvePort(portFlag, saved)
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	if _, err := DiscoverHost(profile, ""); err != nil {
		log.Fatalf("databricks-claude: config write: failed to discover host for profile %q: %v\n"+
			"Run 'databricks auth login --profile %s' first", profile, err, profile)
	}

	// OTEL resolution: re-use the same resolver as `config otel enable` so
	// the persistence semantics (sentinel-guarded writes) are identical. Note
	// that `config write` differs from `config otel enable` only in that it
	// also merges databricksFullSetupEnv into the otelEnv map below.
	otelRes := resolveConfigOTEL(
		saved,
		port,
		proxyURL,
		r.Strings["metrics-table"], r.Set["metrics-table"],
		r.Strings["logs-table"], r.Set["logs-table"],
		r.Bools["traces"],
		r.Strings["traces-table"], r.Set["traces-table"],
		false, // config write: legacy --write-claude-config never applied the metrics default
	)
	if otelRes.StateMutated {
		if err := saveState(otelRes.NewState); err != nil {
			log.Fatalf("databricks-claude: config write: could not persist OTEL tables: %v", err)
		}
		// Reload state so the websearch resolver sees the OTEL-side mutation
		// for sentinel comparisons. Mirrors main.go:324 in the legacy
		// --write-claude-config flow ("Reload state here so we pick up any
		// OTEL writes from above").
		saved = loadState()
	}

	// `config write` is the only path that has the bare-toggle "with_websearch"
	// flag — there is no separate "fetch-budget" / "backend" knob without it
	// here either, but they are accepted in the same parse since the issue's
	// behaviour mapping requires it.
	enable := saved.WithWebSearch
	if r.Set["with-websearch"] {
		enable = r.Bools["with-websearch"]
	}
	budgetFlag, budgetSet := 0, false
	if r.Set["fetch-budget"] {
		raw := r.Strings["fetch-budget"]
		n, err := strconv.Atoi(raw)
		if err != nil {
			log.Fatalf("databricks-claude: config write: --fetch-budget: %q is not an integer", raw)
		}
		budgetFlag = n
		budgetSet = true
	}
	wsRes := resolveConfigWebSearch(saved, enable, r.Strings["backend"], r.Set["backend"], budgetFlag, budgetSet)
	if wsRes.StateMutated {
		if err := saveState(wsRes.NewState); err != nil {
			log.Fatalf("databricks-claude: config write: could not persist websearch state: %v", err)
		}
		saved = loadState()
	}

	// Compose the full env block. Same shape as main.go:285–315 in the legacy
	// path: OTEL keys + databricksFullSetupEnv (model routing + custom headers).
	envOut := map[string]string{}
	for k, v := range otelRes.OTELEnv {
		envOut[k] = v
	}
	for k, v := range databricksFullSetupEnv() {
		envOut[k] = v
	}

	if err := bootstrapSettings(portFlag, profile, proxyURL, envOut); err != nil {
		log.Fatalf("databricks-claude: config write: %v", err)
	}
	fmt.Fprintf(os.Stderr, "databricks-claude: wrote env block to ~/.claude/settings.json (profile=%s, base_url=%s, websearch=%t)\n",
		profile, proxyURL, saved.WithWebSearch)
}

// runConfigShow implements `databricks-claude config show` — the legacy
// --print-env flow lifted into a subcommand. Read-only diagnostic.
func runConfigShow(args []string) {
	node := configCommand.Subcommand("show")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	profile := r.Strings["profile"]
	saved := loadState()
	if profile == "" {
		profile = saved.Profile
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	portFlag := atoiOrZero(r.Strings["port"])
	port := resolvePort(portFlag, saved)

	host, err := DiscoverHost(profile, "")
	if err != nil {
		log.Fatalf("databricks-claude: config show: failed to discover host for profile %q: %v\n"+
			"Run 'databricks auth login --profile %s' first", profile, err, profile)
	}
	inferenceUpstream := ConstructGatewayURL(host)

	tp := NewTokenProvider(profile, "")
	token, err := tp.Token(context.Background())
	if err != nil {
		log.Fatalf("databricks-claude: config show: failed to fetch token for profile %q: %v", profile, err)
	}

	metricsTable := saved.OtelMetricsTable
	logsTable := saved.OtelLogsTable
	tracesTable := saved.OtelTracesTable
	otelActive := metricsTable != "" || logsTable != "" || tracesTable != ""

	// Reference port to avoid the unused-variable warning while keeping the
	// resolution above for parity with --print-env's input set. handlePrintEnv
	// does not consume port directly — it reads ANTHROPIC_BASE_URL from the
	// constructed inferenceUpstream, which is the gateway URL not the proxy URL.
	_ = port

	handlePrintEnv(profile, host, inferenceUpstream, token, "", otelActive, metricsTable, logsTable, tracesTable)
}

// atoiOrZero parses s as a base-10 int. Returns 0 on parse failure so
// resolvePort can fall back to state/default — the same tolerance the
// hand-rolled --port parsing in parseArgs had pre-#172.
func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// displayTableOrNone returns the literal table name or "(none)" when empty,
// for the confirmation log line emitted by `config otel enable`.
func displayTableOrNone(t string) string {
	if t == "" {
		return "(none)"
	}
	return t
}
