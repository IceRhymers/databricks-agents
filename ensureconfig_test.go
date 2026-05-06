package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
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
			"OTEL_METRICS_EXPORTER":               "otlp",
			"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://127.0.0.1:49153/otel",
			"OTEL_EXPORTER_OTLP_METRICS_HEADERS":  "Authorization=Bearer token",
			"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": "http/protobuf",
			"OTEL_METRIC_EXPORT_INTERVAL":          "60000",
			"CLAUDE_OTEL_UC_METRICS_TABLE":         "catalog.schema.metrics",
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

// TestWriteSettingsJSON_ConcurrentWrites verifies that two goroutines writing
// to the same settings.json simultaneously produce a valid, uncorrupted JSON
// file that matches exactly one of the two payloads.
func TestWriteSettingsJSON_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	payload1 := map[string]interface{}{
		"env": map[string]interface{}{
			"ANTHROPIC_BASE_URL": "http://127.0.0.1:49153",
		},
	}
	payload2 := map[string]interface{}{
		"env": map[string]interface{}{
			"ANTHROPIC_BASE_URL": "http://127.0.0.1:49154",
		},
	}

	var wg sync.WaitGroup
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = writeSettingsJSON(settingsPath, payload1)
	}()
	go func() {
		defer wg.Done()
		err2 = writeSettingsJSON(settingsPath, payload2)
	}()
	wg.Wait()

	if err1 != nil {
		t.Errorf("goroutine 1 writeSettingsJSON error: %v", err1)
	}
	if err2 != nil {
		t.Errorf("goroutine 2 writeSettingsJSON error: %v", err2)
	}

	// The file must exist and contain valid JSON.
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("settings.json is not valid JSON after concurrent writes: %v\ncontent: %s", err, data)
	}

	// The result must exactly match one of the two payloads.
	env, _ := result["env"].(map[string]interface{})
	if env == nil {
		t.Fatal("settings.json missing 'env' block after concurrent writes")
	}
	url, _ := env["ANTHROPIC_BASE_URL"].(string)
	if url != "http://127.0.0.1:49153" && url != "http://127.0.0.1:49154" {
		t.Errorf("ANTHROPIC_BASE_URL is %q — not one of the two written values", url)
	}

	// No leftover temp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "settings.json" {
			t.Errorf("unexpected leftover file in dir: %s", e.Name())
		}
	}
}

// TestWritePersistentConfig_ConcurrentWrites verifies that two goroutines
// writing to the same persistent config file simultaneously produce a valid,
// uncorrupted JSON file that matches exactly one of the two payloads.
func TestWritePersistentConfig_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".databricks-claude.json")

	payload1 := map[string]interface{}{
		"port":    float64(49153),
		"profile": "workspace-a",
	}
	payload2 := map[string]interface{}{
		"port":    float64(49154),
		"profile": "workspace-b",
	}

	var wg sync.WaitGroup
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = writePersistentConfig(configPath, payload1)
	}()
	go func() {
		defer wg.Done()
		err2 = writePersistentConfig(configPath, payload2)
	}()
	wg.Wait()

	if err1 != nil {
		t.Errorf("goroutine 1 writePersistentConfig error: %v", err1)
	}
	if err2 != nil {
		t.Errorf("goroutine 2 writePersistentConfig error: %v", err2)
	}

	// The file must exist and contain valid JSON.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("config is not valid JSON after concurrent writes: %v\ncontent: %s", err, data)
	}

	// The result must exactly match one of the two payloads.
	profile, _ := result["profile"].(string)
	if profile != "workspace-a" && profile != "workspace-b" {
		t.Errorf("profile is %q — not one of the two written values", profile)
	}
	port, _ := result["port"].(float64)
	if port != 49153 && port != 49154 {
		t.Errorf("port is %v — not one of the two written values", port)
	}

	// port and profile must be consistent (from the same write).
	if (profile == "workspace-a" && port != 49153) || (profile == "workspace-b" && port != 49154) {
		t.Errorf("port %v and profile %q are from different writes — file is corrupt", port, profile)
	}

	// No leftover temp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != ".databricks-claude.json" {
			t.Errorf("unexpected leftover file in dir: %s", e.Name())
		}
	}
}
