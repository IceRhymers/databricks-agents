package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureConfig_Idempotent verifies that calling ensureConfig twice with the
// same arguments produces identical settings.json content both times.
func TestEnsureConfig_Idempotent(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	proxyURL := "http://127.0.0.1:49153"

	// First call — creates the file.
	if err := ensureConfig(proxyURL, nil); err != nil {
		t.Fatalf("first ensureConfig: %v", err)
	}
	sha1 := fileSHA(t, settingsPath)

	// Second call — should be a no-op; file must be byte-for-byte identical.
	if err := ensureConfig(proxyURL, nil); err != nil {
		t.Fatalf("second ensureConfig: %v", err)
	}
	sha2 := fileSHA(t, settingsPath)

	if sha1 != sha2 {
		t.Errorf("ensureConfig not idempotent: settings.json changed on second call\n  first:  %s\n  second: %s", sha1, sha2)
	}
}

// TestEnsureConfig_WritesExpectedKeys verifies that ensureConfig writes the
// expected env keys to a fresh settings.json.
func TestEnsureConfig_WritesExpectedKeys(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	proxyURL := "http://127.0.0.1:49153"
	if err := ensureConfig(proxyURL, nil); err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}

	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	env, ok := doc["env"].(map[string]interface{})
	if !ok {
		t.Fatal("env block missing from settings.json")
	}
	if got, _ := env["ANTHROPIC_BASE_URL"].(string); got != proxyURL {
		t.Errorf("ANTHROPIC_BASE_URL: got %q, want %q", got, proxyURL)
	}
	if got, _ := env["ANTHROPIC_AUTH_TOKEN"].(string); got != "proxy-managed" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN: got %q, want %q", got, "proxy-managed")
	}
}

// TestEnsureConfig_PreservesExistingKeys verifies that ensureConfig does not
// drop unrelated keys already present in settings.json.
func TestEnsureConfig_PreservesExistingKeys(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Write an initial settings.json with a non-proxy key.
	initial := map[string]interface{}{
		"env": map[string]interface{}{
			"MY_CUSTOM_VAR": "keep-me",
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settingsPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write initial settings.json: %v", err)
	}

	proxyURL := "http://127.0.0.1:49153"
	if err := ensureConfig(proxyURL, nil); err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}

	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	env, ok := doc["env"].(map[string]interface{})
	if !ok {
		t.Fatal("env block missing")
	}
	if got, _ := env["MY_CUSTOM_VAR"].(string); got != "keep-me" {
		t.Errorf("MY_CUSTOM_VAR: got %q, want %q", got, "keep-me")
	}
	if got, _ := env["ANTHROPIC_BASE_URL"].(string); got != proxyURL {
		t.Errorf("ANTHROPIC_BASE_URL: got %q, want %q", got, proxyURL)
	}
}

// TestClearOTELKeysSubset verifies that clearOTELKeysSubset removes only the
// targeted OTEL keys and leaves all other env keys intact.
func TestClearOTELKeysSubset(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Write settings.json containing both OTEL metrics keys and unrelated keys.
	initial := map[string]interface{}{
		"env": map[string]interface{}{
			// OTEL metrics keys (should be removed).
			"OTEL_METRICS_EXPORTER":                  "otlp",
			"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT":    "http://127.0.0.1:49153/otel",
			"OTEL_EXPORTER_OTLP_METRICS_HEADERS":     "Authorization=Bearer token",
			"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL":    "http/protobuf",
			"OTEL_METRIC_EXPORT_INTERVAL":             "60000",
			"CLAUDE_OTEL_UC_METRICS_TABLE":            "catalog.schema.metrics",
			// Unrelated keys (must survive).
			"ANTHROPIC_BASE_URL":   "http://127.0.0.1:49153",
			"ANTHROPIC_AUTH_TOKEN": "proxy-managed",
			"MY_CUSTOM_VAR":        "untouched",
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settingsPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write initial settings.json: %v", err)
	}

	if err := clearOTELKeysSubset(settingsPath, otelMetricsKeys); err != nil {
		t.Fatalf("clearOTELKeysSubset: %v", err)
	}

	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	env, ok := doc["env"].(map[string]interface{})
	if !ok {
		t.Fatal("env block missing after clear")
	}

	// All otelMetricsKeys must be absent.
	for _, k := range otelMetricsKeys {
		if _, exists := env[k]; exists {
			t.Errorf("key %q should have been removed but is still present", k)
		}
	}

	// Non-OTEL keys must still be present.
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "MY_CUSTOM_VAR"} {
		if _, exists := env[k]; !exists {
			t.Errorf("key %q should still be present but was removed", k)
		}
	}
}

// TestClearOTELKeysSubset_MissingFile verifies that clearOTELKeysSubset is a
// no-op (and returns nil) when settings.json does not exist.
func TestClearOTELKeysSubset_MissingFile(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// File does not exist — must not error.
	if err := clearOTELKeysSubset(settingsPath, otelMetricsKeys); err != nil {
		t.Errorf("expected nil error for missing file, got: %v", err)
	}
}
