package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
)

// runConfigCommand implements the `databricks-codex config ...` dispatcher.
// args is everything after the literal "config" token. Routes to the otel
// or show runner. Bare `config` (no args) prints help and exits 2 — same
// convention as the rest of the binary's subcommand-with-no-action paths.
//
// The persistent-config editor was previously a sprawl of 7 root flags
// (--otel, --no-otel*, --otel-*-table, --print-env); #87 consolidates them
// under this tree and removes them from the root. Storage semantics — state
// file table preservation across `config otel disable`, [otel] section
// removal (not just skip-the-write) when both signals are off — are
// unchanged; this is a pure surface reshape. The pure-function resolver
// (resolveConfigOTEL) makes the orchestration matrix testable in isolation;
// helper-level tests passing while composition is broken is the known
// failure mode for this kind of refactor, and the matrix test in
// cli_config_test.go is the safety net.
func runConfigCommand(args []string) {
	if len(args) == 0 {
		_ = cmd.Render(os.Stderr, configCommand, nil)
		os.Exit(2)
	}
	switch args[0] {
	case "otel":
		runConfigOTEL(args[1:])
	case "show":
		runConfigShow(args[1:])
	case "--help", "-h", "help":
		_ = cmd.Render(os.Stdout, configCommand, nil)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "databricks-codex: unknown config subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, configCommand, nil)
		os.Exit(1)
	}
}

// configOTELResolution is the pure-function projection of the OTEL enable
// resolution chain. Extracted so the orchestration matrix test (state
// empty/populated × explicit table flags × derive-logs-from-metrics) can
// drive it in isolation. Caller is responsible for the post-resolve state
// persistence write.
type configOTELResolution struct {
	MetricsTable string
	LogsTable    string
	NewState     persistentState // state to persist (callers gate the write on StateMutated)
	StateMutated bool            // true when NewState differs from the input saved state
}

// resolveConfigOTEL performs the OTEL enable-side resolution that the legacy
// resolveOtel did inline for the regular session path. Pure function: no
// I/O, no global state, no defaults applied at this layer. Drives the
// orchestration matrix test (#87 acceptance criterion).
//
// Resolution chain per signal: explicit flag > saved state > (logs only:
// derive from metrics) > unset. The bare-toggle metrics default (the legacy
// `else if metricsTable == "" && a.Otel` branch in main.go) is applied by
// the caller (runConfigOTELEnable), NOT here — this mirrors the pattern
// caught in databricks-claude PR #172 round-1 review where a shared
// resolver applying a default that only one caller should get is the bug
// class. Codex's surface only has one caller right now (`config otel
// enable`), but keeping the resolver pure makes it easier to reuse if the
// future #88/#89 work needs it.
//
// The Disabled flags on the input saved state are CLEARED in NewState
// (config otel enable un-sticks a previous disable). Sentinel-guard the
// persist: NewState is only meaningfully different when an explicit flag
// was passed OR a Disabled flag was previously set.
func resolveConfigOTEL(
	saved persistentState,
	metricsTableFlag string, metricsTableSet bool,
	logsTableFlag string, logsTableSet bool,
) configOTELResolution {
	r := configOTELResolution{
		MetricsTable: saved.OtelMetricsTable,
		LogsTable:    saved.OtelLogsTable,
		NewState:     saved,
	}

	if metricsTableSet {
		r.MetricsTable = metricsTableFlag
	}

	if logsTableSet {
		r.LogsTable = logsTableFlag
	} else if r.LogsTable == "" && r.MetricsTable != "" {
		r.LogsTable = deriveLogsTable(r.MetricsTable)
	}

	mutated := false
	if metricsTableSet && r.MetricsTable != "" && r.NewState.OtelMetricsTable != r.MetricsTable {
		r.NewState.OtelMetricsTable = r.MetricsTable
		mutated = true
	}
	if logsTableSet && r.LogsTable != "" && r.NewState.OtelLogsTable != r.LogsTable {
		r.NewState.OtelLogsTable = r.LogsTable
		mutated = true
	}
	// Enable always clears any sticky disable bit so the next session emits
	// the [otel] section. If neither was set, no mutation.
	if r.NewState.OtelMetricsDisabled {
		r.NewState.OtelMetricsDisabled = false
		mutated = true
	}
	if r.NewState.OtelLogsDisabled {
		r.NewState.OtelLogsDisabled = false
		mutated = true
	}
	r.StateMutated = mutated
	return r
}

// runConfigOTEL implements `databricks-codex config otel <enable|disable>`.
func runConfigOTEL(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "databricks-codex: 'config otel' requires a subcommand: enable or disable")
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
		fmt.Fprintf(os.Stderr, "databricks-codex: unknown config otel subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, *configCommand.Subcommand("otel"), nil)
		os.Exit(1)
	}
}

// runConfigOTELEnable implements `databricks-codex config otel enable`.
// Persists the resolved table preferences to the state file. config.toml is
// NOT touched — the proxy lifecycle re-emits the [otel] section based on
// state at the next session start.
func runConfigOTELEnable(args []string) {
	node := configCommand.Subcommand("otel").Subcommand("enable")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	saved := loadState()
	profile := r.Strings["profile"]
	if profile == "" {
		profile = saved.Profile
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	// Validate profile reachability before persisting anything — fail fast
	// with an actionable error rather than writing state for a profile that
	// is broken at next session start.
	if _, err := DiscoverHost("", profile); err != nil {
		log.Fatalf("databricks-codex: config otel enable: failed to discover host for profile %q: %v\n"+
			"Run 'databricks auth login --profile %s' first", profile, err, profile)
	}

	res := resolveConfigOTEL(
		saved,
		r.Strings["metrics-table"], r.Set["metrics-table"],
		r.Strings["logs-table"], r.Set["logs-table"],
	)

	// Caller-level metrics default: a bare `config otel enable` (no flags,
	// empty state) inherits the legacy --otel default table. The resolver
	// itself is kept pure so it never invents a table name; the default
	// fallback is the caller's policy decision. Mirrors the
	// applyMetricsDefault gate in databricks-claude's resolveConfigOTEL,
	// implemented here as a caller-side branch since codex only has one
	// caller (no shared resolver to risk).
	if res.MetricsTable == "" && !r.Set["metrics-table"] && !r.Set["logs-table"] {
		res.MetricsTable = "main.codex_telemetry.codex_otel_metrics"
		if res.LogsTable == "" {
			res.LogsTable = deriveLogsTable(res.MetricsTable)
		}
		// Bare-toggle default is intentionally NOT persisted to state — the
		// next session resolves the same default fresh if state is still
		// empty. Persisting it would silently lock a fleet to a default the
		// user never asked for.
	}

	if res.StateMutated {
		if err := saveState(res.NewState); err != nil {
			log.Fatalf("databricks-codex: config otel enable: could not persist OTEL tables: %v", err)
		}
	}

	fmt.Fprintf(os.Stderr, "databricks-codex: OTEL enabled (profile=%s, metrics=%s, logs=%s). Take effect on next 'databricks-codex' invocation.\n",
		profile, displayTableOrNone(res.MetricsTable), displayTableOrNone(res.LogsTable))
}

// runConfigOTELDisable implements `databricks-codex config otel disable`.
// Sets the per-signal "disabled" sticky bits in the state file. The proxy
// lifecycle reads these on the next session start and removes the [otel]
// section from config.toml when all signals are off. Table-name
// preferences are PRESERVED so a future `config otel enable` restores them.
func runConfigOTELDisable(args []string) {
	node := configCommand.Subcommand("otel").Subcommand("disable")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	saved := loadState()
	clearMetrics := r.Bools["metrics"]
	clearLogs := r.Bools["logs"]

	// No flags = both signals (the legacy --no-otel nuclear option).
	if !clearMetrics && !clearLogs {
		clearMetrics = true
		clearLogs = true
	}

	mutated := false
	if clearMetrics && !saved.OtelMetricsDisabled {
		saved.OtelMetricsDisabled = true
		mutated = true
	}
	if clearLogs && !saved.OtelLogsDisabled {
		saved.OtelLogsDisabled = true
		mutated = true
	}

	if mutated {
		if err := saveState(saved); err != nil {
			log.Fatalf("databricks-codex: config otel disable: could not persist OTEL disable: %v", err)
		}
	}

	switch {
	case clearMetrics && clearLogs:
		fmt.Fprintln(os.Stderr, "databricks-codex: OTEL disabled — [otel] section will be removed from config.toml on the next session start (state file table preferences preserved)")
	case clearMetrics:
		fmt.Fprintln(os.Stderr, "databricks-codex: OTEL metrics disabled (logs preserved if previously enabled; state file table preferences preserved)")
	case clearLogs:
		fmt.Fprintln(os.Stderr, "databricks-codex: OTEL logs disabled (metrics preserved if previously enabled; state file table preferences preserved)")
	}
}

// runConfigShow implements `databricks-codex config show` — the legacy
// --print-env flow lifted into a subcommand. Read-only diagnostic.
func runConfigShow(args []string) {
	node := configCommand.Subcommand("show")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	saved := loadState()
	profile := r.Strings["profile"]
	if profile == "" {
		profile = saved.Profile
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	host, err := DiscoverHost("", profile)
	if err != nil {
		log.Fatalf("databricks-codex: config show: failed to discover host for profile %q: %v\n"+
			"Run 'databricks auth login --profile %s' first", profile, err, profile)
	}
	gatewayURL := ConstructGatewayURL(host)

	tp := NewTokenProvider("", profile)
	token, err := tp.Token(context.Background())
	if err != nil {
		log.Fatalf("databricks-codex: config show: failed to fetch token for profile %q: %v", profile, err)
	}

	model := resolveModel("", saved.Model)

	// resolveOtel reads from saved state (no flag input — the session
	// flags are gone with #87). Mirrors what the regular session path
	// will read at next start.
	_, metricsTable, logsTable := resolveOtel(saved)

	handlePrintEnv(host, gatewayURL, token, profile, model, metricsTable, logsTable)
}

// displayTableOrNone returns the literal table name or "(none)" when empty,
// for the confirmation log line emitted by `config otel enable`.
func displayTableOrNone(t string) string {
	if t == "" {
		return "(none)"
	}
	return t
}
