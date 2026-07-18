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

// assertNoLegacyProfile fails if the config carries either half of the legacy
// profile-selector shape: the root `profile = "databricks-proxy"` line or the
// `[profiles.databricks-proxy]` section. The assertions are scoped precisely so
// they never false-fail a user's own `[profiles.myprofile]` section or the
// `model_provider = "databricks-proxy"` key.
func assertNoLegacyProfile(t *testing.T, content string) {
	t.Helper()
	if strings.Contains(content, `profile = "databricks-proxy"`) {
		t.Errorf("expected no root profile = \"databricks-proxy\" selector, got:\n%s", content)
	}
	if strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Errorf("expected no [profiles.databricks-proxy] section, got:\n%s", content)
	}
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
	if !strings.Contains(content, `model_provider = "databricks-proxy"`) {
		t.Error("expected model_provider root key")
	}
	if !strings.Contains(content, `model = "databricks-gpt-5-5"`) {
		t.Error("expected root model key")
	}
	if !strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Error("expected model_providers section")
	}
	if !strings.Contains(content, `base_url = "http://127.0.0.1:9999"`) {
		t.Error("expected base_url in provider section")
	}
	assertNoLegacyProfile(t, content)
}

func TestPatch_PreservesUserSections(t *testing.T) {
	initial := `[projects.myapp]
sandbox_permissions = "full-auto"

[notice]
shown = true
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
	if !strings.Contains(content, "[projects.myapp]") {
		t.Error("expected [projects.myapp] to be preserved")
	}
	if !strings.Contains(content, `sandbox_permissions = "full-auto"`) {
		t.Error("expected sandbox_permissions to be preserved")
	}
	if !strings.Contains(content, "[notice]") {
		t.Error("expected [notice] to be preserved")
	}
	assertNoLegacyProfile(t, content)
}

func TestPatch_PreservesUserModel(t *testing.T) {
	// Runtime shape: an already-migrated config has the proxy as the top-level
	// default provider with a root model. Patching again (ModelExplicit=false)
	// must preserve the user's existing root model.
	initial := `model = "custom-user-model"
model_provider = "databricks-proxy"

[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://old-proxy:1234"
api_key = "databricks-proxy"
wire_api = "responses"
`
	m, configPath := setup(t, initial)

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
	assertNoLegacyProfile(t, content)
}

func TestPatch_OverridesModelWhenExplicit(t *testing.T) {
	initial := `model = "custom-user-model"
model_provider = "databricks-proxy"

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

// TestPatch_NeverEmitsLegacyProfile guards the acceptance criterion that the
// patcher NEVER writes the fatal legacy shape — neither on a fresh install nor
// when the user has an unrelated [profiles.myprofile] section.
func TestPatch_NeverEmitsLegacyProfile(t *testing.T) {
	cases := map[string]string{
		"fresh": "",
		"user-profile": `profile = "myprofile"

[profiles.myprofile]
model_provider = "openai"
model = "gpt-4"
`,
	}
	for name, initial := range cases {
		t.Run(name, func(t *testing.T) {
			m, configPath := setup(t, initial)
			if err := m.Patch(PatchConfig{
				ProxyURL: "http://127.0.0.1:9999",
				Model:    "databricks-gpt-5-5",
			}); err != nil {
				t.Fatal(err)
			}
			content := readConfig(t, configPath)
			assertNoLegacyProfile(t, content)
			if !strings.Contains(content, `model_provider = "databricks-proxy"`) {
				t.Errorf("expected root model_provider default, got:\n%s", content)
			}
		})
	}
}

// TestPatch_MigratesLegacyProfileShape seeds the OLD profile-selector shape a
// returning user carries (patch-and-leave persists it) and asserts Patch
// migrates it to the top-level default-provider shape: the root profile
// selector and [profiles.databricks-proxy] section are gone, root model +
// model_provider are present, the provider base_url is healed, and re-patching
// is byte-idempotent.
func TestPatch_MigratesLegacyProfileShape(t *testing.T) {
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "databricks-gpt-5-5"

[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://127.0.0.1:59998"
api_key = "databricks-proxy"
wire_api = "responses"
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	// Runtime path: fresh Manager per patch, NO Backup() (m.original stays nil).
	patchOnce := func() string {
		m := NewManager(configPath)
		if err := m.Patch(PatchConfig{
			ProxyURL: "http://127.0.0.1:49154",
			Model:    "databricks-gpt-5-5",
		}); err != nil {
			t.Fatal(err)
		}
		return readConfig(t, configPath)
	}

	first := patchOnce()
	assertNoLegacyProfile(t, first)
	if !strings.Contains(first, `model_provider = "databricks-proxy"`) {
		t.Errorf("expected root model_provider default after migration, got:\n%s", first)
	}
	if !strings.Contains(first, `model = "databricks-gpt-5-5"`) {
		t.Errorf("expected root model after migration, got:\n%s", first)
	}
	if !strings.Contains(first, `base_url = "http://127.0.0.1:49154"`) {
		t.Errorf("expected provider base_url healed to live proxy, got:\n%s", first)
	}
	if strings.Contains(first, "59998") {
		t.Errorf("expected stale base_url 59998 removed, got:\n%s", first)
	}

	second := patchOnce()
	if first != second {
		t.Errorf("re-patch after migration is not idempotent:\n--- first ---\n%q\n--- second ---\n%q", first, second)
	}
}

// TestPatch_MigrationNonDestructiveToForeignProfile asserts the migration only
// removes the proxy's OWN legacy shape: a user's foreign root profile selector
// and their [profiles.myprofile] section survive Patch untouched.
func TestPatch_MigrationNonDestructiveToForeignProfile(t *testing.T) {
	initial := `profile = "myprofile"

[profiles.myprofile]
model_provider = "openai"
model = "gpt-4"
`
	m, configPath := setup(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `profile = "myprofile"`) {
		t.Errorf("expected foreign root profile to survive, got:\n%s", content)
	}
	if !strings.Contains(content, "[profiles.myprofile]") {
		t.Errorf("expected [profiles.myprofile] to survive, got:\n%s", content)
	}
	if !strings.Contains(content, `model = "gpt-4"`) {
		t.Errorf("expected user's profile model to survive, got:\n%s", content)
	}
	assertNoLegacyProfile(t, content)
}

// TestPatch_ModelPrecedenceRuntime exercises the model-resolution precedence on
// the REAL runtime path — a fresh Manager with NO Backup() call, so m.original
// is nil and resolution must read the on-disk content string. Covers all three
// branches: explicit --model wins, else existing root model preserved, else the
// resolved default.
func TestPatch_ModelPrecedenceRuntime(t *testing.T) {
	provider := `
[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://127.0.0.1:9999"
api_key = "databricks-proxy"
wire_api = "responses"
`
	tests := []struct {
		name          string
		seedRootModel string // "" = no root model on disk
		cfgModel      string
		modelExplicit bool
		wantModel     string
	}{
		{
			name:          "explicit flag wins over existing root model",
			seedRootModel: "existing-model",
			cfgModel:      "flag-model",
			modelExplicit: true,
			wantModel:     "flag-model",
		},
		{
			name:          "existing root model preserved when not explicit",
			seedRootModel: "existing-model",
			cfgModel:      "default-model",
			modelExplicit: false,
			wantModel:     "existing-model",
		},
		{
			name:          "resolved default used when no root model on disk",
			seedRootModel: "",
			cfgModel:      "default-model",
			modelExplicit: false,
			wantModel:     "default-model",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.toml")
			seed := provider
			if tc.seedRootModel != "" {
				seed = "model = \"" + tc.seedRootModel + "\"\n" + provider
			}
			if err := os.WriteFile(configPath, []byte(seed), 0o600); err != nil {
				t.Fatal(err)
			}

			// Runtime path: no Backup(), so m.original is nil.
			m := NewManager(configPath)
			if err := m.Patch(PatchConfig{
				ProxyURL:      "http://127.0.0.1:9999",
				Model:         tc.cfgModel,
				ModelExplicit: tc.modelExplicit,
			}); err != nil {
				t.Fatal(err)
			}

			content := readConfig(t, configPath)
			want := `model = "` + tc.wantModel + `"`
			if !strings.Contains(content, want) {
				t.Errorf("expected %s, got:\n%s", want, content)
			}
		})
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

	// Verify patch added managed content.
	content := readConfig(t, configPath)
	if !strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Fatal("patch should have added model_providers section")
	}

	// Restore.
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content = readConfig(t, configPath)
	if strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Error("expected [model_providers.databricks-proxy] to be removed after restore")
	}
	if strings.Contains(content, `model_provider = "databricks-proxy"`) {
		t.Error("expected model_provider root key to be removed after restore")
	}
	// User section must survive.
	if !strings.Contains(content, "[projects.myapp]") {
		t.Error("expected [projects.myapp] to survive restore")
	}
}

func TestRestore_RestoresOriginalValues(t *testing.T) {
	// Original config already uses the top-level default-provider shape with a
	// non-proxy provider — Restore must put the user's root model and
	// model_provider back.
	initial := `model = "original-model"
model_provider = "openai"

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
	if !strings.Contains(content, `model = "original-model"`) {
		t.Errorf("expected original model to be restored, got:\n%s", content)
	}
	if !strings.Contains(content, `model_provider = "openai"`) {
		t.Errorf("expected original model_provider to be restored, got:\n%s", content)
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

	// No --model flag (ModelExplicit=false): should preserve the root-level model.
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
		t.Errorf("expected root-level model to be preserved, got:\n%s", content)
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
	// And the managed provider section should still be intact.
	if !strings.Contains(after, "[model_providers.databricks-proxy]") {
		t.Errorf("expected [model_providers.databricks-proxy] to survive [otel] removal, got:\n%s", after)
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

	original := `model = "mymodel"
`
	os.WriteFile(backupPath, []byte(original), 0o600)
	os.WriteFile(configPath, []byte(`model_provider = "databricks-proxy"`), 0o600)

	m := NewManager(configPath)
	restored := m.RestoreFromBackup()
	if !restored {
		t.Error("expected RestoreFromBackup to return true")
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `model = "mymodel"`) {
		t.Errorf("expected original content restored from backup, got:\n%s", content)
	}

	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("expected backup file to be removed after restore")
	}
}
