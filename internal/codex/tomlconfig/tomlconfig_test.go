package tomlconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, initialContent string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if initialContent != "" {
		if err := os.WriteFile(configPath, []byte(initialContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}
	return m, configPath
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPatch_EmptyConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `profile = "databricks-proxy"`) {
		t.Error("expected profile root key")
	}
	if !strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Error("expected profiles section")
	}
	if !strings.Contains(content, `model = "databricks-gpt-5-5"`) {
		t.Error("expected model in profile section")
	}
	if !strings.Contains(content, `base_url = "http://127.0.0.1:9999"`) {
		t.Error("expected base_url in provider section")
	}
}

func TestPatch_PreservesUserSections(t *testing.T) {
	initial := `profile = "myprofile"

[projects.myapp]
sandbox_permissions = "full-auto"

[profiles.myprofile]
model_provider = "openai"
model = "gpt-4"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	// User section must survive.
	if !strings.Contains(content, "[projects.myapp]") {
		t.Error("expected [projects.myapp] to be preserved")
	}
	if !strings.Contains(content, `sandbox_permissions = "full-auto"`) {
		t.Error("expected sandbox_permissions to be preserved")
	}
	// User's other profile must survive.
	if !strings.Contains(content, "[profiles.myprofile]") {
		t.Error("expected [profiles.myprofile] to be preserved")
	}
}

func TestPatch_PreservesUserModel(t *testing.T) {
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "custom-user-model"

[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://old-proxy:1234"
api_key = "databricks-proxy"
wire_api = "responses"
`
	m, configPath := setup(t, initial)

	// ModelExplicit=false: should preserve user's model.
	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `model = "custom-user-model"`) {
		t.Errorf("expected user model to be preserved, got:\n%s", content)
	}
}

func TestPatch_OverridesModelWhenExplicit(t *testing.T) {
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "custom-user-model"

[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://old-proxy:1234"
api_key = "databricks-proxy"
wire_api = "responses"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-4-mini",
		ModelExplicit: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `model = "databricks-gpt-5-4-mini"`) {
		t.Errorf("expected model to be overridden to databricks-gpt-5-4-mini, got:\n%s", content)
	}
	if strings.Contains(content, `model = "custom-user-model"`) {
		t.Errorf("expected custom-user-model to be replaced, got:\n%s", content)
	}
}

func TestRestore_RemovesAddedKeys(t *testing.T) {
	// Start with a config that has NO databricks-proxy sections.
	initial := `[projects.myapp]
sandbox_permissions = "full-auto"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify patch added sections.
	content := readConfig(t, configPath)
	if !strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Fatal("patch should have added profiles section")
	}

	// Restore.
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content = readConfig(t, configPath)
	if strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Error("expected [profiles.databricks-proxy] to be removed after restore")
	}
	if strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Error("expected [model_providers.databricks-proxy] to be removed after restore")
	}
	if strings.Contains(content, `profile = "databricks-proxy"`) {
		t.Error("expected profile root key to be removed after restore")
	}
	// User section must survive.
	if !strings.Contains(content, "[projects.myapp]") {
		t.Error("expected [projects.myapp] to survive restore")
	}
}

func TestRestore_RestoresOriginalValues(t *testing.T) {
	initial := `profile = "myprofile"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "original-model"

[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://old-proxy:1234"
api_key = "databricks-proxy"
wire_api = "responses"

[projects.myapp]
sandbox_permissions = "full-auto"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "new-model",
		ModelExplicit: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Restore.
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `profile = "myprofile"`) {
		t.Errorf("expected original profile to be restored, got:\n%s", content)
	}
	if !strings.Contains(content, `model = "original-model"`) {
		t.Errorf("expected original model to be restored, got:\n%s", content)
	}
	if !strings.Contains(content, "[projects.myapp]") {
		t.Errorf("expected user section preserved, got:\n%s", content)
	}
}

func TestRestore_PreservesUnmanagedContent(t *testing.T) {
	initial := `custom_key = "custom_value"

[projects.myapp]
sandbox_permissions = "full-auto"

[notice]
shown = true
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:         "http://127.0.0.1:9999",
		Model:            "databricks-gpt-5-5",
		OTELLogsEndpoint: "http://127.0.0.1:9999/otel/v1/logs",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `custom_key = "custom_value"`) {
		t.Errorf("expected custom_key to survive, got:\n%s", content)
	}
	if !strings.Contains(content, "[projects.myapp]") {
		t.Errorf("expected [projects.myapp] to survive, got:\n%s", content)
	}
	if !strings.Contains(content, "[notice]") {
		t.Errorf("expected [notice] to survive, got:\n%s", content)
	}
	// OTEL section should be removed (it was absent before).
	if strings.Contains(content, "[otel]") {
		t.Errorf("expected [otel] to be removed after restore, got:\n%s", content)
	}
}

func TestRestore_NoOriginalFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	// File should be removed since it didn't exist before.
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("expected config.toml to be removed after restore when it didn't exist before")
	}
}

func TestPatch_RespectsRootLevelModel(t *testing.T) {
	initial := `model = databricks-gpt-5-3

[projects./Users/me/myproject]
trust_level = trusted
`
	m, configPath := setup(t, initial)

	// No --model flag (ModelExplicit=false), no model in [profiles.databricks-proxy].
	// Should pick up the root-level model.
	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `model = "databricks-gpt-5-3"`) {
		t.Errorf("expected root-level model to be carried into profile section, got:\n%s", content)
	}
	if strings.Contains(content, `model = "databricks-gpt-5-5"`) {
		t.Errorf("expected fallback model NOT to be used when root-level model exists, got:\n%s", content)
	}
}

func TestPatch_RootLevelModelOverriddenByExplicitFlag(t *testing.T) {
	initial := `model = databricks-gpt-5-3

[projects./Users/me/myproject]
trust_level = trusted
`
	m, configPath := setup(t, initial)

	// --model flag explicitly set: should override root-level model.
	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-4-mini",
		ModelExplicit: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `model = "databricks-gpt-5-4-mini"`) {
		t.Errorf("expected explicit --model to win over root-level model, got:\n%s", content)
	}
}

func TestPatch_WithOTEL(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL:         "http://127.0.0.1:9999",
		Model:            "databricks-gpt-5-5",
		OTELLogsEndpoint: "http://127.0.0.1:9999/otel/v1/logs",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, "[otel]") {
		t.Error("expected [otel] section")
	}
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Error("expected OTEL endpoint in config")
	}
}

func TestPatch_WithOTELMetricsOnly(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, "[otel]") {
		t.Error("expected [otel] section")
	}
	if !strings.Contains(content, `metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics_exporter key in [otel] section, got:\n%s", content)
	}
	// Specifically: there should be no bare `exporter = { otlp-http = { endpoint = "http://...logs"`
	if strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected no logs exporter when only metrics endpoint provided, got:\n%s", content)
	}
}

func TestPatch_WithBothOTELExporters(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELLogsEndpoint:    "http://127.0.0.1:9999/otel/v1/logs",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected logs endpoint in [otel] section, got:\n%s", content)
	}
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics endpoint in [otel] section, got:\n%s", content)
	}
}

func TestPatch_NoOTELSectionWhenBothEmpty(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if strings.Contains(content, "[otel]") {
		t.Errorf("expected no [otel] section when both endpoints empty, got:\n%s", content)
	}
}

// TestPatch_OTELReWriteAddsMetricsExporter exercises the EnsureConfig regression
// flagged in PR review: an existing [otel] section with only `exporter` should
// gain `metrics_exporter` on a subsequent Patch with both endpoints set.
func TestPatch_OTELReWriteAddsMetricsExporter(t *testing.T) {
	m, configPath := setup(t, "")

	// First patch: logs only.
	if err := m.Patch(PatchConfig{
		ProxyURL:         "http://127.0.0.1:9999",
		Model:            "databricks-gpt-5-5",
		OTELLogsEndpoint: "http://127.0.0.1:9999/otel/v1/logs",
	}); err != nil {
		t.Fatal(err)
	}

	// Second patch: same proxy URL, now with metrics too.
	if err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELLogsEndpoint:    "http://127.0.0.1:9999/otel/v1/logs",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	}); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics_exporter after re-patch, got:\n%s", content)
	}
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected logs endpoint to still be present after re-patch, got:\n%s", content)
	}
}

// TestPatch_RemovesOTELSectionWhenEndpointsEmpty exercises the --no-otel
// regression: a previously-written [otel] section must be removed entirely
// when a subsequent Patch comes in with both endpoints empty.
func TestPatch_RemovesOTELSectionWhenEndpointsEmpty(t *testing.T) {
	m, configPath := setup(t, "")

	// First patch: OTel enabled.
	if err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELLogsEndpoint:    "http://127.0.0.1:9999/otel/v1/logs",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	}); err != nil {
		t.Fatal(err)
	}

	before := readConfig(t, configPath)
	if !strings.Contains(before, "[otel]") {
		t.Fatalf("setup error: expected [otel] section after first patch, got:\n%s", before)
	}

	// Second patch: OTel disabled (both endpoints empty). Section must be gone.
	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	after := readConfig(t, configPath)
	if strings.Contains(after, "[otel]") {
		t.Errorf("expected [otel] section to be removed after empty-endpoints patch, got:\n%s", after)
	}
	if strings.Contains(after, "exporter = { otlp-http") {
		t.Errorf("expected no stale exporter line after removal, got:\n%s", after)
	}
	if strings.Contains(after, "metrics_exporter") {
		t.Errorf("expected no stale metrics_exporter line after removal, got:\n%s", after)
	}
	// And [profiles.databricks-proxy] should still be intact.
	if !strings.Contains(after, "[profiles.databricks-proxy]") {
		t.Errorf("expected [profiles.databricks-proxy] to survive [otel] removal, got:\n%s", after)
	}
}

func TestUpdateProxyURL(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.UpdateProxyURL("http://127.0.0.1:8888"); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `base_url = "http://127.0.0.1:8888"`) {
		t.Errorf("expected updated base_url, got:\n%s", content)
	}
}

func TestRestoreFromBackup(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	backupPath := configPath + ".databricks-codex-backup"

	original := `profile = "myprofile"
`
	os.WriteFile(backupPath, []byte(original), 0o600)
	os.WriteFile(configPath, []byte(`profile = "databricks-proxy"`), 0o600)

	m := NewManager(configPath)
	restored := m.RestoreFromBackup()
	if !restored {
		t.Error("expected RestoreFromBackup to return true")
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `profile = "myprofile"`) {
		t.Errorf("expected original content restored from backup, got:\n%s", content)
	}

	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("expected backup file to be removed after restore")
	}
}
