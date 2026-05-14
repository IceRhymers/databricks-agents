package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// --- #172 config tree parity tests ---

// TestConfigCommandParity checks the top-level `config` node carries no
// flags of its own (it's a pure dispatcher; subcommands carry the flag
// surface). #170/#171 parity-test pattern: commit to a flag set, fail loud
// on drift.
func TestConfigCommandParity(t *testing.T) {
	assertFlagSetEqual(t, "configCommand", configCommand, []string{})
}

func TestConfigOTELEnableParity(t *testing.T) {
	otel := configCommand.Subcommand("otel")
	if otel == nil {
		t.Fatal("configCommand should have an `otel` subcommand")
	}
	enable := otel.Subcommand("enable")
	if enable == nil {
		t.Fatal("config otel should have an `enable` subcommand")
	}
	assertFlagSetEqual(t, "config otel enable", *enable, []string{
		"metrics-table", "logs-table", "traces", "traces-table",
		"profile", "port", "help",
	})
}

func TestConfigOTELDisableParity(t *testing.T) {
	otel := configCommand.Subcommand("otel")
	if otel == nil {
		t.Fatal("configCommand should have an `otel` subcommand")
	}
	disable := otel.Subcommand("disable")
	if disable == nil {
		t.Fatal("config otel should have a `disable` subcommand")
	}
	assertFlagSetEqual(t, "config otel disable", *disable, []string{
		"metrics", "logs", "traces", "help",
	})
}

func TestConfigWebSearchEnableParity(t *testing.T) {
	ws := configCommand.Subcommand("websearch")
	if ws == nil {
		t.Fatal("configCommand should have a `websearch` subcommand")
	}
	enable := ws.Subcommand("enable")
	if enable == nil {
		t.Fatal("config websearch should have an `enable` subcommand")
	}
	assertFlagSetEqual(t, "config websearch enable", *enable, []string{
		"backend", "fetch-budget", "help",
	})
}

func TestConfigWebSearchDisableParity(t *testing.T) {
	ws := configCommand.Subcommand("websearch")
	if ws == nil {
		t.Fatal("configCommand should have a `websearch` subcommand")
	}
	disable := ws.Subcommand("disable")
	if disable == nil {
		t.Fatal("config websearch should have a `disable` subcommand")
	}
	assertFlagSetEqual(t, "config websearch disable", *disable, []string{"help"})
}

func TestConfigWriteParity(t *testing.T) {
	write := configCommand.Subcommand("write")
	if write == nil {
		t.Fatal("configCommand should have a `write` subcommand")
	}
	assertFlagSetEqual(t, "config write", *write, []string{
		"profile", "port",
		"metrics-table", "logs-table", "traces", "traces-table",
		"with-websearch", "backend", "fetch-budget",
		"help",
	})
}

func TestConfigShowParity(t *testing.T) {
	show := configCommand.Subcommand("show")
	if show == nil {
		t.Fatal("configCommand should have a `show` subcommand")
	}
	assertFlagSetEqual(t, "config show", *show, []string{"profile", "port", "help"})
}

// TestConfigHasNestedSubcommands asserts the otel/websearch/write/show
// children are declared so completion can offer them nested. Mirrors
// TestServeHasNestedSubcommands (#171). Drives the AC: "Help + completion
// derived from the tree".
func TestConfigHasNestedSubcommands(t *testing.T) {
	want := []string{"otel", "websearch", "write", "show"}
	got := make(map[string]bool, len(configCommand.Subcommands))
	for _, s := range configCommand.Subcommands {
		got[s.Name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("configCommand should have nested `%s` subcommand", w)
		}
	}
}

// TestConfigCompletionExposed verifies the root tree's recursive
// CompletionSubcommands walk surfaces `config otel enable` / `config
// websearch enable` etc. as nested completion entries. Drives the
// completion-side of the issue AC.
func TestConfigCompletionExposed(t *testing.T) {
	subs := rootCommand.CompletionSubcommands()
	var configSub *struct {
		name string
		kids []string
	}
	for _, s := range subs {
		if s.Name == "config" {
			c := struct {
				name string
				kids []string
			}{name: s.Name}
			for _, k := range s.Subcommands {
				c.kids = append(c.kids, k.Name)
			}
			configSub = &c
			break
		}
	}
	if configSub == nil {
		t.Fatal("rootCommand.CompletionSubcommands() missing `config`")
	}
	for _, want := range []string{"otel", "websearch", "write", "show"} {
		found := false
		for _, k := range configSub.kids {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("config completion missing nested child %q (got %v)", want, configSub.kids)
		}
	}
}

// --- #172 OTEL orchestration matrix test ---
//
// AC requirement: "Orchestration matrix test ported: state empty/populated
// × enable/disable combinations × explicit table flags — assert the final
// resolved (otel, metricsTable, logsTable, tracesTable) tuple."
//
// Drives the pure resolveConfigOTEL function. Helper-level tests passing
// while composition is broken is the known failure mode for refactors of
// this shape; this matrix covers the cross-product so a regression in any
// branch of the resolver fails the suite loudly.

func TestResolveConfigOTEL_OrchestrationMatrix(t *testing.T) {
	const proxyURL = "http://127.0.0.1:49153"
	const port = 49153

	// emptyState mirrors a fresh-install user; populatedState mirrors a
	// returning user with explicit prior table preferences.
	emptyState := persistentState{}
	populatedState := persistentState{
		OtelMetricsTable: "main.team.metrics_prior",
		OtelLogsTable:    "main.team.logs_prior",
		OtelTracesTable:  "main.team.traces_prior",
	}

	type wantTuple struct {
		metrics, logs, traces string
		otelEnvHasMetrics     bool
		otelEnvHasLogs        bool
		otelEnvHasTraces      bool
		otelEnvHasTelemetry   bool // CLAUDE_CODE_ENABLE_TELEMETRY
		stateMetrics          string
		stateLogs             string
		stateTraces           string
		stateMutated          bool
	}

	cases := []struct {
		name             string
		saved            persistentState
		metricsFlag      string
		metricsSet       bool
		logsFlag         string
		logsSet          bool
		tracesBool       bool
		tracesTableFlag  string
		tracesTableSet   bool
		applyDefault     bool
		want             wantTuple
	}{
		{
			name:         "empty state, no flags, applyDefault=true → applies legacy metrics default + derives logs",
			saved:        emptyState,
			applyDefault: true,
			want: wantTuple{
				metrics:             "main.claude_telemetry.claude_otel_metrics",
				logs:                "main.claude_telemetry.claude_otel_logs",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "",
				stateLogs:           "",
				stateMutated:        false,
			},
		},
		{
			// config write path: applyDefault=false. A fresh-install `config
			// write` with no OTEL flags must resolve every table empty and emit
			// an EMPTY otelEnv — the legacy --write-claude-config block never
			// applied the metrics default, and doing so would silently enable
			// telemetry export to a non-existent UC table.
			name:         "empty state, no flags, applyDefault=false → no tables, empty otelEnv",
			saved:        emptyState,
			applyDefault: false,
			want: wantTuple{
				metrics:             "",
				logs:                "",
				traces:              "",
				otelEnvHasMetrics:   false,
				otelEnvHasLogs:      false,
				otelEnvHasTraces:    false,
				otelEnvHasTelemetry: false,
				stateMetrics:        "",
				stateLogs:           "",
				stateMutated:        false,
			},
		},
		{
			name:         "empty state, --metrics-table only → derives logs, persists metrics",
			saved:        emptyState,
			metricsFlag:  "cat.s.m",
			metricsSet:   true,
			applyDefault: true,
			want: wantTuple{
				metrics:             "cat.s.m",
				logs:                "cat.s.m_otel_logs",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "cat.s.m",
				stateMutated:        true,
			},
		},
		{
			// applyDefault=false but an explicit --metrics-table is still
			// honoured — the gate only affects the bare-toggle default, not
			// explicit flags. This is the `config write --metrics-table` path.
			name:         "empty state, --metrics-table only, applyDefault=false → explicit flag still honoured",
			saved:        emptyState,
			metricsFlag:  "cat.s.m",
			metricsSet:   true,
			applyDefault: false,
			want: wantTuple{
				metrics:             "cat.s.m",
				logs:                "cat.s.m_otel_logs",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "cat.s.m",
				stateMutated:        true,
			},
		},
		{
			name:         "empty state, both metrics and logs explicit → no derivation",
			saved:        emptyState,
			metricsFlag:  "cat.s.m",
			metricsSet:   true,
			logsFlag:     "cat.s.l",
			logsSet:      true,
			applyDefault: true,
			want: wantTuple{
				metrics:             "cat.s.m",
				logs:                "cat.s.l",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "cat.s.m",
				stateLogs:           "cat.s.l",
				stateMutated:        true,
			},
		},
		{
			name:            "empty state, traces flag + traces-table → traces-only",
			saved:           emptyState,
			tracesBool:      true,
			tracesTableFlag: "cat.s.t",
			tracesTableSet:  true,
			applyDefault:    true,
			want: wantTuple{
				// metrics still inherits the legacy default in bare-toggle land,
				// but here the user supplied traces explicitly — the resolver
				// still applies the metrics default (matching legacy behavior).
				metrics:             "main.claude_telemetry.claude_otel_metrics",
				logs:                "main.claude_telemetry.claude_otel_logs",
				traces:              "cat.s.t",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTraces:    true,
				otelEnvHasTelemetry: true,
				stateTraces:         "cat.s.t",
				stateMutated:        true,
			},
		},
		{
			name:         "populated state, no flags → state values preserved",
			saved:        populatedState,
			applyDefault: true,
			want: wantTuple{
				metrics:             "main.team.metrics_prior",
				logs:                "main.team.logs_prior",
				traces:              "main.team.traces_prior",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTraces:    true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "main.team.metrics_prior",
				stateLogs:           "main.team.logs_prior",
				stateTraces:         "main.team.traces_prior",
				stateMutated:        false,
			},
		},
		{
			// Populated state + applyDefault=false: state values are still
			// honoured (the gate only suppresses the bare-toggle DEFAULT, not
			// state-derived values). This is the common `config write` path
			// for a returning user.
			name:         "populated state, no flags, applyDefault=false → state values still preserved",
			saved:        populatedState,
			applyDefault: false,
			want: wantTuple{
				metrics:             "main.team.metrics_prior",
				logs:                "main.team.logs_prior",
				traces:              "main.team.traces_prior",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTraces:    true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "main.team.metrics_prior",
				stateLogs:           "main.team.logs_prior",
				stateTraces:         "main.team.traces_prior",
				stateMutated:        false,
			},
		},
		{
			name:         "populated state, --metrics-table same as state → no mutation",
			saved:        populatedState,
			metricsFlag:  "main.team.metrics_prior",
			metricsSet:   true,
			applyDefault: true,
			want: wantTuple{
				metrics:             "main.team.metrics_prior",
				logs:                "main.team.logs_prior",
				traces:              "main.team.traces_prior",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTraces:    true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "main.team.metrics_prior",
				stateLogs:           "main.team.logs_prior",
				stateTraces:         "main.team.traces_prior",
				stateMutated:        false,
			},
		},
		{
			name:         "populated state, --metrics-table changes value → mutation",
			saved:        populatedState,
			metricsFlag:  "main.team.metrics_new",
			metricsSet:   true,
			applyDefault: true,
			want: wantTuple{
				metrics:             "main.team.metrics_new",
				logs:                "main.team.logs_prior",
				traces:              "main.team.traces_prior",
				otelEnvHasMetrics:   true,
				otelEnvHasLogs:      true,
				otelEnvHasTraces:    true,
				otelEnvHasTelemetry: true,
				stateMetrics:        "main.team.metrics_new",
				stateLogs:           "main.team.logs_prior",
				stateTraces:         "main.team.traces_prior",
				stateMutated:        true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := resolveConfigOTEL(tc.saved, port, proxyURL,
				tc.metricsFlag, tc.metricsSet,
				tc.logsFlag, tc.logsSet,
				tc.tracesBool,
				tc.tracesTableFlag, tc.tracesTableSet,
				tc.applyDefault,
			)

			if res.MetricsTable != tc.want.metrics {
				t.Errorf("MetricsTable: got %q, want %q", res.MetricsTable, tc.want.metrics)
			}
			if res.LogsTable != tc.want.logs {
				t.Errorf("LogsTable: got %q, want %q", res.LogsTable, tc.want.logs)
			}
			if res.TracesTable != tc.want.traces {
				t.Errorf("TracesTable: got %q, want %q", res.TracesTable, tc.want.traces)
			}
			if got, want := res.NewState.OtelMetricsTable, tc.want.stateMetrics; got != want {
				t.Errorf("NewState.OtelMetricsTable: got %q, want %q", got, want)
			}
			if got, want := res.NewState.OtelLogsTable, tc.want.stateLogs; got != want {
				t.Errorf("NewState.OtelLogsTable: got %q, want %q", got, want)
			}
			if got, want := res.NewState.OtelTracesTable, tc.want.stateTraces; got != want {
				t.Errorf("NewState.OtelTracesTable: got %q, want %q", got, want)
			}
			if res.StateMutated != tc.want.stateMutated {
				t.Errorf("StateMutated: got %v, want %v", res.StateMutated, tc.want.stateMutated)
			}

			// Env-block presence checks.
			has := func(k string) bool { _, ok := res.OTELEnv[k]; return ok }
			if got := has("CLAUDE_OTEL_UC_METRICS_TABLE"); got != tc.want.otelEnvHasMetrics {
				t.Errorf("OTELEnv has metrics table: got %v, want %v (env=%v)", got, tc.want.otelEnvHasMetrics, res.OTELEnv)
			}
			if got := has("CLAUDE_OTEL_UC_LOGS_TABLE"); got != tc.want.otelEnvHasLogs {
				t.Errorf("OTELEnv has logs table: got %v, want %v", got, tc.want.otelEnvHasLogs)
			}
			if got := has("CLAUDE_OTEL_UC_TRACES_TABLE"); got != tc.want.otelEnvHasTraces {
				t.Errorf("OTELEnv has traces table: got %v, want %v", got, tc.want.otelEnvHasTraces)
			}
			if got := has("CLAUDE_CODE_ENABLE_TELEMETRY"); got != tc.want.otelEnvHasTelemetry {
				t.Errorf("OTELEnv has telemetry toggle: got %v, want %v", got, tc.want.otelEnvHasTelemetry)
			}
		})
	}
}

// TestResolveConfigOTEL_OnlyExplicitFlagsPersist asserts the sentinel-guard
// invariant: a value derived from state (no explicit flag) must NOT be
// re-persisted with a "mutated" flag. This is the issue-#149 footgun.
func TestResolveConfigOTEL_OnlyExplicitFlagsPersist(t *testing.T) {
	saved := persistentState{
		OtelMetricsTable: "main.team.metrics_prior",
	}
	res := resolveConfigOTEL(saved, 49153, "http://127.0.0.1:49153",
		"", false, // no metrics-table flag — derived from state
		"", false,
		false,
		"", false,
		true, // applyMetricsDefault — irrelevant here (state already has metrics), proves derive-from-state still no-mutates
	)
	if res.StateMutated {
		t.Errorf("StateMutated must be false when no explicit flag was passed; resolver should not echo state-derived values back to state. NewState=%+v", res.NewState)
	}
}

// --- #172 websearch resolver tests ---

func TestResolveConfigWebSearch_EnableEmptyState(t *testing.T) {
	res := resolveConfigWebSearch(persistentState{}, true, "", false, 0, false)
	if !res.NewState.WithWebSearch {
		t.Error("expected WithWebSearch=true after enable on empty state")
	}
	if !res.StateMutated {
		t.Error("expected StateMutated=true (false→true)")
	}
}

func TestResolveConfigWebSearch_EnableAlreadyEnabled(t *testing.T) {
	res := resolveConfigWebSearch(persistentState{WithWebSearch: true}, true, "", false, 0, false)
	if !res.NewState.WithWebSearch {
		t.Error("expected WithWebSearch to stay true")
	}
	if res.StateMutated {
		t.Error("expected StateMutated=false when already enabled and no other flags")
	}
}

func TestResolveConfigWebSearch_EnableWithBackendChange(t *testing.T) {
	saved := persistentState{WithWebSearch: true, WebSearchBackend: "duckduckgo"}
	res := resolveConfigWebSearch(saved, true, "none", true, 0, false)
	if res.NewState.WebSearchBackend != "none" {
		t.Errorf("WebSearchBackend: got %q, want %q", res.NewState.WebSearchBackend, "none")
	}
	if !res.StateMutated {
		t.Error("expected StateMutated=true after backend change")
	}
}

// TestResolveConfigWebSearch_Disable_ClearsAllRelatedKeys is the
// state-preservation test from the plan: a disable must clear backend +
// fetch-budget too so a future enable re-applies defaults rather than
// inheriting stale values.
func TestResolveConfigWebSearch_Disable_ClearsAllRelatedKeys(t *testing.T) {
	saved := persistentState{
		WithWebSearch:        true,
		WebSearchBackend:     "duckduckgo",
		WebSearchFetchBudget: 204800,
	}
	res := resolveConfigWebSearch(saved, false, "", false, 0, false)
	if res.NewState.WithWebSearch {
		t.Error("expected WithWebSearch=false after disable")
	}
	if res.NewState.WebSearchBackend != "" {
		t.Errorf("WebSearchBackend should be cleared on disable, got %q", res.NewState.WebSearchBackend)
	}
	if res.NewState.WebSearchFetchBudget != 0 {
		t.Errorf("WebSearchFetchBudget should be cleared on disable, got %d", res.NewState.WebSearchFetchBudget)
	}
	if !res.StateMutated {
		t.Error("expected StateMutated=true after disable")
	}
}

func TestResolveConfigWebSearch_DisableNoOp(t *testing.T) {
	res := resolveConfigWebSearch(persistentState{}, false, "", false, 0, false)
	if res.StateMutated {
		t.Error("expected StateMutated=false when already disabled and nothing to clear")
	}
}

// --- #172 state-preservation invariant: `config otel disable` does NOT
// touch the state file ---
//
// Drives the AC requirement: "Two-store semantics unchanged: config otel
// disable touches runtime config only, preserves state-file table
// preferences; re-enable restores them." This composition test runs
// end-to-end against a tempdir HOME so the load/save semantics are
// exercised in concert.
func TestConfigOTELDisable_PreservesStateFileTables(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origStatePath := statePath
	statePath = func() string { return filepath.Join(tmpHome, ".claude", ".databricks-claude.json") }
	defer func() { statePath = origStatePath }()

	// Seed state with table preferences.
	seed := persistentState{
		OtelMetricsTable: "cat.team.metrics",
		OtelLogsTable:    "cat.team.logs",
		OtelTracesTable:  "cat.team.traces",
	}
	if err := saveState(seed); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// Seed settings.json with OTEL keys (mimics a previous `config otel
	// enable` run).
	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	if err := bootstrapSettings(0, "DEFAULT", "http://127.0.0.1:49153", buildOTELEnv(seed.OtelMetricsTable, seed.OtelLogsTable, seed.OtelTracesTable, "http://127.0.0.1:49153")); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}

	// Simulate `config otel disable` (nuclear option) by calling the
	// underlying clearer directly — same call the runner makes.
	if err := clearOTELKeys(settingsPath); err != nil {
		t.Fatalf("clearOTELKeys: %v", err)
	}

	// Settings.json must no longer carry OTEL keys.
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("readSettingsJSON: %v", err)
	}
	env, _ := doc["env"].(map[string]interface{})
	for _, k := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY",
		"CLAUDE_OTEL_UC_METRICS_TABLE",
		"CLAUDE_OTEL_UC_LOGS_TABLE",
		"CLAUDE_OTEL_UC_TRACES_TABLE",
	} {
		if _, present := env[k]; present {
			t.Errorf("settings.json must not contain %q after config otel disable", k)
		}
	}

	// State file MUST still carry the table preferences — re-enable should
	// restore them.
	got := loadState()
	if got.OtelMetricsTable != "cat.team.metrics" {
		t.Errorf("state.OtelMetricsTable: got %q, want preserved %q", got.OtelMetricsTable, "cat.team.metrics")
	}
	if got.OtelLogsTable != "cat.team.logs" {
		t.Errorf("state.OtelLogsTable: got %q, want preserved %q", got.OtelLogsTable, "cat.team.logs")
	}
	if got.OtelTracesTable != "cat.team.traces" {
		t.Errorf("state.OtelTracesTable: got %q, want preserved %q", got.OtelTracesTable, "cat.team.traces")
	}

	// Re-enable: resolveConfigOTEL with no flags should pick the state
	// values straight back up — closing the loop on the AC. applyMetricsDefault
	// is true here (the `config otel enable` path), but state is already
	// populated so the bare-toggle default is never reached.
	res := resolveConfigOTEL(got, 49153, "http://127.0.0.1:49153",
		"", false, "", false, false, "", false, true,
	)
	if res.MetricsTable != "cat.team.metrics" {
		t.Errorf("re-enable MetricsTable: got %q, want restored %q", res.MetricsTable, "cat.team.metrics")
	}
	if res.LogsTable != "cat.team.logs" {
		t.Errorf("re-enable LogsTable: got %q, want restored %q", res.LogsTable, "cat.team.logs")
	}
	if res.TracesTable != "cat.team.traces" {
		t.Errorf("re-enable TracesTable: got %q, want restored %q", res.TracesTable, "cat.team.traces")
	}
}

// TestConfigOTELDisable_PerSignal exercises the per-signal --metrics /
// --logs / --traces clears, asserting the other signals stay live.
func TestConfigOTELDisable_PerSignal(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origStatePath := statePath
	statePath = func() string { return filepath.Join(tmpHome, ".claude", ".databricks-claude.json") }
	defer func() { statePath = origStatePath }()

	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	otelEnv := buildOTELEnv("cat.t.m", "cat.t.l", "cat.t.tr", "http://127.0.0.1:49153")
	if err := bootstrapSettings(0, "DEFAULT", "http://127.0.0.1:49153", otelEnv); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}

	if err := clearOTELKeysSubset(settingsPath, otelMetricsKeys); err != nil {
		t.Fatalf("clearOTELKeysSubset: %v", err)
	}

	doc, _ := readSettingsJSON(settingsPath)
	env, _ := doc["env"].(map[string]interface{})

	// Metrics keys MUST be gone.
	for _, k := range otelMetricsKeys {
		if _, present := env[k]; present {
			t.Errorf("metrics key %q must be cleared", k)
		}
	}

	// Logs + traces keys MUST still be present.
	if _, ok := env["CLAUDE_OTEL_UC_LOGS_TABLE"]; !ok {
		t.Error("logs table key should survive --metrics-only clear")
	}
	if _, ok := env["CLAUDE_OTEL_UC_TRACES_TABLE"]; !ok {
		t.Error("traces table key should survive --metrics-only clear")
	}
}

// TestConfigCommand_Help renders the configCommand Long body and asserts
// the documented subcommand names appear. Light smoke on the help layer.
func TestConfigCommand_Help(t *testing.T) {
	var sb strings.Builder
	if err := renderConfigForTest(&sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"otel enable", "otel disable",
		"websearch enable", "websearch disable",
		"write", "show",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config help missing %q, got:\n%s", want, out)
		}
	}
}

// renderConfigForTest is a thin helper so the help test does not depend on
// internal/cmd's exported Render signature — it just exercises the Long
// template directly.
func renderConfigForTest(sb *strings.Builder) error {
	sb.WriteString(configCommand.Long)
	return nil
}
