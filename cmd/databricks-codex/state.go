package main

import (
	"os"
	"path/filepath"

	"github.com/IceRhymers/databricks-agents/internal/core/state"
)

// persistentState is the JSON schema for ~/.codex/.databricks-codex.json.
// This file survives config.toml restore and persists across sessions.
//
// OtelMetricsDisabled / OtelLogsDisabled (added in #87) are the explicit
// "user has run `config otel disable`" markers. They exist so the `config
// otel disable` UX can preserve the table-name preferences for a future
// re-enable while still suppressing OTEL export on the next session — the
// two-store equivalent of databricks-claude's "settings.json absence means
// off, state file holds preferences" model, adapted for codex where there
// is no settings.json env block (config.toml is owned by the proxy
// lifecycle and rewritten at every session start).
type persistentState struct {
	Profile             string `json:"profile,omitempty"`
	OtelLogsTable       string `json:"otel_logs_table,omitempty"`
	OtelMetricsTable    string `json:"otel_metrics_table,omitempty"`
	OtelMetricsDisabled bool   `json:"otel_metrics_disabled,omitempty"`
	OtelLogsDisabled    bool   `json:"otel_logs_disabled,omitempty"`
	Model               string `json:"model,omitempty"`
	Port                int    `json:"port,omitempty"`
	TLSCert             string `json:"tls_cert,omitempty"`
	TLSKey              string `json:"tls_key,omitempty"`
}

const defaultPort = 49154

// resolvePort returns the port to use, following the resolution chain:
// 1. --port flag (portFlag > 0)
// 2. Saved state value (non-zero)
// 3. Default 49154
func resolvePort(portFlag int, s persistentState) int {
	return state.ResolvePort(portFlag, s.Port, defaultPort)
}

// statePath returns the path to the persistent state file.
// It is a variable so tests can override it.
var statePath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex/.databricks-codex.json"
	}
	return filepath.Join(home, ".codex", ".databricks-codex.json")
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
