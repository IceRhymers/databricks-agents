package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// bootstrapSettings persists profile + port to the state file and writes the
// env block to settings.json. Idempotent: state is mutated in memory and
// written via a single saveState call only when something actually changed;
// settings.json is written only when ANTHROPIC_BASE_URL differs from proxyURL.
//
// portFlag is the raw --port CLI flag value (0 if absent). When portFlag > 0,
// the resolved port is persisted to state — preserving the existing semantics
// that the user's explicit --port choice is sticky.
//
// resolvedProfile is the post-resolution profile string ("DEFAULT" if no
// override). Persisted only when non-empty and not "DEFAULT".
//
// proxyURL is the URL written into ANTHROPIC_BASE_URL. The first-run path
// passes the discovered gateway URL; --install-hooks passes the placeholder
// http://127.0.0.1:<port> (which the headless-ensure hook overwrites at
// session start with the discovered URL).
//
// otelEnv is forwarded to ensureConfig; nil for the install-hooks path.
func bootstrapSettings(portFlag int, resolvedProfile string, proxyURL string, otelEnv map[string]string) error {
	state := loadState()
	mutated := false
	if portFlag > 0 {
		state.Port = resolvePort(portFlag, state)
		mutated = true
	}
	if resolvedProfile != "" && resolvedProfile != "DEFAULT" && state.Profile != resolvedProfile {
		state.Profile = resolvedProfile
		mutated = true
	}
	if mutated {
		if err := saveState(state); err != nil {
			return fmt.Errorf("persist state: %w", err)
		}
	}
	return ensureConfig(proxyURL, otelEnv)
}

// ensureConfig writes the env block to ~/.claude/settings.json only if
// ANTHROPIC_BASE_URL doesn't already point at proxyURL. Idempotent.
//
// It preserves all non-env keys in settings.json. The otelKeys map is optional;
// non-empty values are written into the env block.
func ensureConfig(proxyURL string, otelEnv map[string]string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	// Read existing settings.json or start with empty doc.
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		doc = map[string]interface{}{}
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
			return err
		}
	}

	// Get or create the env block.
	env := getOrCreateEnvBlock(doc)

	// Check if already configured — no-op if ANTHROPIC_BASE_URL matches and
	// there are no pending OTEL keys to write.
	hasOtelToWrite := false
	for _, v := range otelEnv {
		if v != "" {
			hasOtelToWrite = true
			break
		}
	}
	if v, ok := env["ANTHROPIC_BASE_URL"].(string); ok && v == proxyURL && !hasOtelToWrite {
		return nil
	}

	// Write proxy values.
	env["ANTHROPIC_BASE_URL"] = proxyURL
	env["ANTHROPIC_AUTH_TOKEN"] = "proxy-managed"

	// Write any non-empty OTEL keys.
	for k, v := range otelEnv {
		if v != "" {
			env[k] = v
		}
	}

	doc["env"] = env
	return writeSettingsJSON(settingsPath, doc)
}

// readSettingsJSON reads and parses a settings.json file.
// If the file does not exist, an empty document is returned.
func readSettingsJSON(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{}, nil
		}
		return nil, err
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// writeSettingsJSON atomically writes a settings.json file. Creates the parent
// directory with mode 0700 if absent. Atomic via temp+rename in the same
// directory (cross-device rename impossible by construction).
func writeSettingsJSON(path string, doc map[string]interface{}) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "settings-*.json.tmp")
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

// getOrCreateEnvBlock returns the "env" sub-map from doc, creating it if needed.
func getOrCreateEnvBlock(doc map[string]interface{}) map[string]interface{} {
	if env, ok := doc["env"]; ok {
		if m, ok := env.(map[string]interface{}); ok {
			return m
		}
	}
	env := map[string]interface{}{}
	doc["env"] = env
	return env
}

// clearOTELKeys removes all OTEL-related env keys from ~/.claude/settings.json.
// Used by --no-otel to ensure telemetry config is fully absent from settings.
func clearOTELKeys(settingsPath string) error {
	return clearOTELKeysSubset(settingsPath, otelEnvKeys)
}

// clearOTELKeysSubset removes the supplied keys from ~/.claude/settings.json's
// env block. Used by --no-otel-{metrics,logs,traces} to clear a single signal.
func clearOTELKeysSubset(settingsPath string, keys []string) error {
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	env, ok := doc["env"].(map[string]interface{})
	if !ok {
		return nil
	}
	for _, k := range keys {
		delete(env, k)
	}
	return writeSettingsJSON(settingsPath, doc)
}

// otelMetricsKeys is the set of env keys cleared by --no-otel-metrics.
var otelMetricsKeys = []string{
	"OTEL_METRICS_EXPORTER",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_HEADERS",
	"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"OTEL_METRIC_EXPORT_INTERVAL",
	"CLAUDE_OTEL_UC_METRICS_TABLE",
}

// otelLogsKeys is the set of env keys cleared by --no-otel-logs.
var otelLogsKeys = []string{
	"OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_LOGS_HEADERS",
	"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
	"OTEL_LOGS_EXPORT_INTERVAL",
	"CLAUDE_OTEL_UC_LOGS_TABLE",
}

// otelTracesKeys is the set of env keys cleared by --no-otel-traces.
var otelTracesKeys = []string{
	"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA",
	"OTEL_TRACES_EXPORTER",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
	"OTEL_TRACES_EXPORT_INTERVAL",
	"CLAUDE_OTEL_UC_TRACES_TABLE",
}

// otelEnvKeys is the complete list of OTEL env keys managed by databricks-claude.
// --no-otel clears every key in this slice plus CLAUDE_CODE_ENABLE_TELEMETRY.
var otelEnvKeys = func() []string {
	keys := []string{"CLAUDE_CODE_ENABLE_TELEMETRY"}
	keys = append(keys, otelMetricsKeys...)
	keys = append(keys, otelLogsKeys...)
	keys = append(keys, otelTracesKeys...)
	return keys
}()
