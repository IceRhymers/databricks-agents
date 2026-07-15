package main

import (
	"path/filepath"

	"github.com/IceRhymers/databricks-agents/internal/core/state"
)

// defaultPort is the fixed port used by databricks-opencode when no override is given.
// 49156 avoids conflict with macOS Launcher which binds 49155 by default.
const defaultPort = 49156

// persistentState is the JSON schema for <opencode-config-dir>/.databricks-opencode.json.
// This file survives config restore and persists across sessions.
type persistentState struct {
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model,omitempty"`
	Port    int    `json:"port,omitempty"`
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`
}

// resolvePort returns the port to use: flag > saved state > defaultPort.
func resolvePort(portFlag int, s persistentState) int {
	return state.ResolvePort(portFlag, s.Port, defaultPort)
}

// statePath returns the path to the persistent state file.
// It is a variable so tests can override it.
var statePath = func() string {
	dir, err := opencodeConfigDir()
	if err != nil {
		return ".databricks-opencode.json"
	}
	return filepath.Join(dir, ".databricks-opencode.json")
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
