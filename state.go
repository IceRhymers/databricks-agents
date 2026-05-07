package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// persistentState is the JSON schema for ~/.claude/.databricks-claude.json.
// This file survives config restores and persists across sessions.
type persistentState struct {
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
}

const defaultPort = 49153

// resolvePort returns the port to use, following the resolution chain:
// 1. --port flag (portFlag > 0)
// 2. Saved state value (non-zero)
// 3. Default 49153
func resolvePort(portFlag int, state persistentState) int {
	if portFlag > 0 {
		return portFlag
	}
	if state.Port > 0 {
		return state.Port
	}
	return defaultPort
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
	data, err := os.ReadFile(statePath())
	if err != nil {
		return persistentState{}
	}
	var s persistentState
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("databricks-claude: invalid state file, ignoring: %v", err)
		return persistentState{}
	}
	return s
}

// saveState writes the persistent state file atomically.
func saveState(s persistentState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	p := statePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
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
	return os.Rename(tmpPath, p)
}
