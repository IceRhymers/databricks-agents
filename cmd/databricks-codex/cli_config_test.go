package main

import (
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
)

// --- #87 config tree parity tests ---
//
// Bidirectional contract: every flag declared on a config leaf must be
// consumed by its runner, and every flag the runner consumes must be
// declared on the leaf. The hand-rolled flag scanners in cli_config.go
// drive parseResult lookups; the only way to fail loudly when one side
// drifts is an explicit per-leaf parity test.
//
// Bidirectional-verification note: temporarily removing configCommand
// from rootCommand.Subcommands fails TestRootHasConfigSubcommand; adding
// an extra flag to a leaf without wiring it into the runner fails the
// `assertConfigFlagSetEqual` "declares but does not consume" arm. Both
// directions exercised manually during #87 development to confirm the
// parity tests fire.

func TestConfigCommandParity(t *testing.T) {
	assertConfigFlagSetEqual(t, "configCommand", configCommand, []string{})
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
	assertConfigFlagSetEqual(t, "config otel enable", *enable, []string{
		"metrics-table", "logs-table", "profile", "help",
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
	assertConfigFlagSetEqual(t, "config otel disable", *disable, []string{
		"metrics", "logs", "help",
	})
}

func TestConfigShowParity(t *testing.T) {
	show := configCommand.Subcommand("show")
	if show == nil {
		t.Fatal("configCommand should have a `show` subcommand")
	}
	assertConfigFlagSetEqual(t, "config show", *show, []string{"profile", "help"})
}

// TestConfigHasNestedSubcommands asserts the otel/show children are declared
// so completion can offer them nested. Locks in the surface of the new tree.
func TestConfigHasNestedSubcommands(t *testing.T) {
	want := []string{"otel", "show"}
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

// TestRootHasConfigSubcommand is the structural counterpart to the breaking
// change in #87: the root tree must declare config so dispatch from
// main.go (and shell completion) finds it.
func TestRootHasConfigSubcommand(t *testing.T) {
	for _, s := range rootCommand.Subcommands {
		if s.Name == "config" {
			return
		}
	}
	t.Error("rootCommand must declare a `config` subcommand (the surface introduced in #87)")
}

// TestRootCommandLegacyOTELFlagsRemoved locks the breaking surface change
// from #87: the legacy --otel*/--print-env flags must NOT appear on
// rootCommand any more. If a future refactor accidentally re-adds one, this
// test fires and points at the migration sub-issue.
func TestRootCommandLegacyOTELFlagsRemoved(t *testing.T) {
	declared := flagNameSetForConfig(rootCommand)
	for _, flag := range []string{
		"otel", "no-otel", "no-otel-metrics", "no-otel-logs",
		"otel-metrics-table", "otel-logs-table",
		"print-env",
	} {
		if declared[flag] {
			t.Errorf("rootCommand still declares --%s after #87 migration; the user-facing flag must move to `config otel ...` / `config show`", flag)
		}
	}
}

// --- #87 OTEL orchestration matrix test ---
//
// AC requirement: "Orchestration matrix test ported: state empty/populated
// × explicit table flags — assert the final resolved (metricsTable,
// logsTable) tuple plus the persisted state shape."
//
// Drives the pure resolveConfigOTEL function. Helper-level tests passing
// while composition is broken is the known failure mode for refactors of
// this shape; this matrix covers the cross-product so a regression in any
// branch of the resolver fails the suite loudly.

func TestResolveConfigOTEL_OrchestrationMatrix(t *testing.T) {
	emptyState := persistentState{}
	populatedState := persistentState{
		OtelMetricsTable: "main.team.metrics_prior",
		OtelLogsTable:    "main.team.logs_prior",
	}
	disabledState := persistentState{
		OtelMetricsTable:    "main.team.metrics_prior",
		OtelLogsTable:       "main.team.logs_prior",
		OtelMetricsDisabled: true,
		OtelLogsDisabled:    true,
	}

	type wantTuple struct {
		metrics, logs       string
		stateMetrics        string
		stateLogs           string
		stateMetricsDisable bool
		stateLogsDisable    bool
		stateMutated        bool
	}

	cases := []struct {
		name        string
		saved       persistentState
		metricsFlag string
		metricsSet  bool
		logsFlag    string
		logsSet     bool
		want        wantTuple
	}{
		{
			// Bare invocation, empty state: resolver itself emits empty
			// tables — the bare-toggle metrics default is the caller's
			// policy decision (runConfigOTELEnable applies it). Pure-
			// resolver test asserts the resolver does NOT invent a table
			// name on its own.
			name:  "empty state, no flags: resolver emits empty tables (caller applies default)",
			saved: emptyState,
			want: wantTuple{
				metrics:      "",
				logs:         "",
				stateMutated: false,
			},
		},
		{
			name:        "empty state, --metrics-table only → derives logs, persists metrics",
			saved:       emptyState,
			metricsFlag: "cat.s.m",
			metricsSet:  true,
			want: wantTuple{
				metrics:      "cat.s.m",
				logs:         "cat.s.m_otel_logs",
				stateMetrics: "cat.s.m",
				stateMutated: true,
			},
		},
		{
			name:        "empty state, both metrics and logs explicit → no derivation, both persisted",
			saved:       emptyState,
			metricsFlag: "cat.s.m",
			metricsSet:  true,
			logsFlag:    "cat.s.l",
			logsSet:     true,
			want: wantTuple{
				metrics:      "cat.s.m",
				logs:         "cat.s.l",
				stateMetrics: "cat.s.m",
				stateLogs:    "cat.s.l",
				stateMutated: true,
			},
		},
		{
			name:  "populated state, no flags → state values preserved, no mutation",
			saved: populatedState,
			want: wantTuple{
				metrics:      "main.team.metrics_prior",
				logs:         "main.team.logs_prior",
				stateMetrics: "main.team.metrics_prior",
				stateLogs:    "main.team.logs_prior",
				stateMutated: false,
			},
		},
		{
			name:        "populated state, --metrics-table same as state → no mutation",
			saved:       populatedState,
			metricsFlag: "main.team.metrics_prior",
			metricsSet:  true,
			want: wantTuple{
				metrics:      "main.team.metrics_prior",
				logs:         "main.team.logs_prior",
				stateMetrics: "main.team.metrics_prior",
				stateLogs:    "main.team.logs_prior",
				stateMutated: false,
			},
		},
		{
			name:        "populated state, --metrics-table changes value → mutation",
			saved:       populatedState,
			metricsFlag: "main.team.metrics_new",
			metricsSet:  true,
			want: wantTuple{
				metrics:      "main.team.metrics_new",
				logs:         "main.team.logs_prior",
				stateMetrics: "main.team.metrics_new",
				stateLogs:    "main.team.logs_prior",
				stateMutated: true,
			},
		},
		{
			// `config otel disable` previously stuck the disable bits;
			// `config otel enable` (even with no flags) must clear them
			// so the next session emits the [otel] section. This is the
			// round-trip that the two-store model requires.
			name:  "disabled state, no flags → tables preserved, Disabled bits cleared",
			saved: disabledState,
			want: wantTuple{
				metrics:             "main.team.metrics_prior",
				logs:                "main.team.logs_prior",
				stateMetrics:        "main.team.metrics_prior",
				stateLogs:           "main.team.logs_prior",
				stateMetricsDisable: false,
				stateLogsDisable:    false,
				stateMutated:        true,
			},
		},
		{
			// Empty-state with --logs-table only: logs flag wins, metrics
			// stays empty (the resolver does NOT apply the bare-toggle
			// metrics default — that's the caller's job).
			name:    "empty state, --logs-table only → logs persisted, metrics still empty",
			saved:   emptyState,
			logsFlag: "cat.s.l",
			logsSet: true,
			want: wantTuple{
				metrics:      "",
				logs:         "cat.s.l",
				stateMetrics: "",
				stateLogs:    "cat.s.l",
				stateMutated: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := resolveConfigOTEL(tc.saved,
				tc.metricsFlag, tc.metricsSet,
				tc.logsFlag, tc.logsSet,
			)

			if res.MetricsTable != tc.want.metrics {
				t.Errorf("MetricsTable: got %q, want %q", res.MetricsTable, tc.want.metrics)
			}
			if res.LogsTable != tc.want.logs {
				t.Errorf("LogsTable: got %q, want %q", res.LogsTable, tc.want.logs)
			}
			if got, want := res.NewState.OtelMetricsTable, tc.want.stateMetrics; got != want {
				t.Errorf("NewState.OtelMetricsTable: got %q, want %q", got, want)
			}
			if got, want := res.NewState.OtelLogsTable, tc.want.stateLogs; got != want {
				t.Errorf("NewState.OtelLogsTable: got %q, want %q", got, want)
			}
			if got, want := res.NewState.OtelMetricsDisabled, tc.want.stateMetricsDisable; got != want {
				t.Errorf("NewState.OtelMetricsDisabled: got %v, want %v", got, want)
			}
			if got, want := res.NewState.OtelLogsDisabled, tc.want.stateLogsDisable; got != want {
				t.Errorf("NewState.OtelLogsDisabled: got %v, want %v", got, want)
			}
			if res.StateMutated != tc.want.stateMutated {
				t.Errorf("StateMutated: got %v, want %v", res.StateMutated, tc.want.stateMutated)
			}
		})
	}
}

// TestResolveConfigOTEL_OnlyExplicitFlagsPersist asserts the sentinel-guard
// invariant: a value derived purely from saved state (no explicit flag)
// must NOT be re-persisted with a "mutated" flag. This is the issue-#149
// footgun that bit databricks-claude — assert it here so the codex
// resolver doesn't grow the same bug class.
func TestResolveConfigOTEL_OnlyExplicitFlagsPersist(t *testing.T) {
	saved := persistentState{
		OtelMetricsTable: "main.team.metrics_prior",
	}
	res := resolveConfigOTEL(saved,
		"", false, // no metrics-table flag — derived from state
		"", false,
	)
	if res.StateMutated {
		t.Errorf("StateMutated must be false when no explicit flag was passed; resolver should not echo state-derived values back to state. NewState=%+v", res.NewState)
	}
}

// TestConfigOTELEnableDisableRoundTrip exercises the full enable→disable→
// re-enable round trip end-to-end via the resolver, asserting the
// state-preservation invariant the two-store model promises:
//
//  1. enable persists tables.
//  2. disable sets Disabled bits but leaves table names intact.
//  3. re-enable (no flags) clears the Disabled bits and resurfaces the
//     same tables — no re-typing required.
func TestConfigOTELEnableDisableRoundTrip(t *testing.T) {
	// Step 1: empty state + explicit tables → enabled with both persisted.
	step1 := resolveConfigOTEL(persistentState{},
		"main.team.m", true,
		"main.team.l", true,
	)
	if step1.NewState.OtelMetricsTable != "main.team.m" || step1.NewState.OtelLogsTable != "main.team.l" {
		t.Fatalf("step 1 state not persisted as expected: %+v", step1.NewState)
	}

	// Step 2: simulate `config otel disable` — both Disabled bits true,
	// tables preserved.
	disabled := step1.NewState
	disabled.OtelMetricsDisabled = true
	disabled.OtelLogsDisabled = true

	// resolveOtel (the SESSION reader) sees both signals off.
	if otel, m, l := resolveOtel(disabled); otel || m != "" || l != "" {
		t.Errorf("after disable, session resolveOtel must see signals off; got otel=%v metrics=%q logs=%q", otel, m, l)
	}

	// Step 3: re-enable with no flags. Resolver clears Disabled bits and
	// re-emits the same tables.
	step3 := resolveConfigOTEL(disabled, "", false, "", false)
	if step3.MetricsTable != "main.team.m" {
		t.Errorf("re-enable MetricsTable: got %q, want %q (round-trip preserved)", step3.MetricsTable, "main.team.m")
	}
	if step3.LogsTable != "main.team.l" {
		t.Errorf("re-enable LogsTable: got %q, want %q (round-trip preserved)", step3.LogsTable, "main.team.l")
	}
	if step3.NewState.OtelMetricsDisabled || step3.NewState.OtelLogsDisabled {
		t.Errorf("re-enable must clear Disabled bits; got %+v", step3.NewState)
	}
	if !step3.StateMutated {
		t.Error("re-enable must mark state mutated (Disabled bits flipping false counts as a mutation)")
	}
}

// TestConfigCommandHelpRendersSubcommandNames is a light smoke check on
// the help layer: the configCommand Long body must mention each surface
// the README documents (otel enable/disable, show).
func TestConfigCommandHelpRendersSubcommandNames(t *testing.T) {
	out := configCommand.Long
	for _, want := range []string{"otel enable", "otel disable", "show"} {
		if !strings.Contains(out, want) {
			t.Errorf("configCommand.Long missing %q; help text drifted from runner surface", want)
		}
	}
}

// flagNameSetForConfig returns the set of long-flag names declared on a
// tree node (Persistent ++ Flags). Local helper so cli_config_test.go has
// no dependency on test helpers in main_test.go beyond what's needed.
func flagNameSetForConfig(c cmd.Command) map[string]bool {
	out := make(map[string]bool)
	for _, f := range c.AllFlags() {
		out[f.Name] = true
	}
	return out
}

// assertConfigFlagSetEqual fails if the actual flag set declared on c
// differs from the expected slice. Bidirectional: extra flags on either
// side surface as failures.
func assertConfigFlagSetEqual(t *testing.T, label string, c cmd.Command, want []string) {
	t.Helper()
	got := flagNameSetForConfig(c)
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
		if !got[w] {
			t.Errorf("%s should declare --%s but does not (runner consumes it; tree must too)", label, w)
		}
	}
	for n := range got {
		if !wantSet[n] {
			t.Errorf("%s declares --%s but its runner does not consume it (dead declaration; remove from tree or wire to runner)", label, n)
		}
	}
}
