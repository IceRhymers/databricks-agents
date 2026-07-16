package main

import (
	"os"
	"path/filepath"

	"github.com/IceRhymers/databricks-agents/internal/core/state"
)

// persistentState is the JSON schema for ~/.claude/.databricks-claude.json.
// This file survives config restores and persists across sessions.
type persistentState struct {
	// Profile is the Databricks CLI profile name. Never stored as "DEFAULT" —
	// that is a sentinel meaning "fall through the resolution chain", not a
	// real user choice. Writers must guard: resolved != "" && resolved != "DEFAULT".
	Profile string `json:"profile,omitempty"`
	Port    int    `json:"port,omitempty"`
	// DatabricksCLIPath pins the absolute path to the `databricks` CLI binary.
	// Used by the credential helper running under Claude Desktop's GUI subprocess
	// context, where the inherited PATH (launchd's /usr/bin:/bin:/usr/sbin:/sbin)
	// can't see standard install locations like /opt/homebrew/bin or
	// ~/.local/bin. Falls back to PATH search and the fallback dir scan when
	// empty. Set via `--generate-desktop-config --databricks-cli-path …` for
	// per-user pinning, or by an MDM admin dropping the state file directly.
	DatabricksCLIPath string `json:"databricks_cli_path,omitempty"`
	// OTel table names survive --no-otel / --no-otel-* so the user can toggle
	// telemetry off and back on without having to re-specify the table flags.
	// Populated by explicit --otel-*-table flags and by migration from settings.json.
	OtelMetricsTable string `json:"otel_metrics_table,omitempty"`
	OtelLogsTable    string `json:"otel_logs_table,omitempty"`
	OtelTracesTable  string `json:"otel_traces_table,omitempty"`
	// --with-websearch (workaround) — local fulfillment of Anthropic's
	// web_search/web_fetch server-side tools when Databricks FMAPI does
	// not yet support them. Persisted so the user only opts in once.
	WithWebSearch        bool   `json:"with_websearch,omitempty"`
	WebSearchBackend     string `json:"websearch_backend,omitempty"`
	WebSearchFetchBudget int    `json:"websearch_fetch_budget,omitempty"`
	// Models holds the resolved model FQN per family, written by the
	// discovery-time config writer and read by the launch path. A nil pointer
	// means discovery has never run; the launch path then falls back to
	// defaultModelRouting.
	Models *ModelRouting `json:"models,omitempty"`
}

// ModelRouting holds the resolved model FQN per family that the launch path
// writes into settings.json. The [1m] suffix, when applicable, is already
// baked into the FQN string by pkg/modeldiscovery. Empty string for a family
// means "not discovered" — the launch path fills it from defaultModelRouting.
type ModelRouting struct {
	Opus   string `json:"opus,omitempty"`
	Sonnet string `json:"sonnet,omitempty"`
	Haiku  string `json:"haiku,omitempty"`
}

const defaultPort = 49153

// resolvePort returns the port to use, following the resolution chain:
// 1. --port flag (portFlag > 0)
// 2. Saved state value (non-zero)
// 3. Default 49153
func resolvePort(portFlag int, s persistentState) int {
	return state.ResolvePort(portFlag, s.Port, defaultPort)
}

// statePath returns the path to the persistent state file.
// It is a variable so tests can override it.
var statePath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude/.databricks-claude.json"
	}
	return filepath.Join(home, ".claude", ".databricks-claude.json")
}

// loadState reads the persistent state file. Returns zero-value state if
// the file doesn't exist or can't be parsed.
func loadState() persistentState {
	s, _ := state.Load[persistentState](statePath())
	return s
}

// saveState writes the persistent state file atomically.
func saveState(s persistentState) error {
	return state.Save(statePath(), s)
}
