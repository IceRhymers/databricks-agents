package jsonconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func setupTestConfig(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	return NewWithPath(filepath.Join(dir, "opencode.json"))
}

func readJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

func TestPatchEmptyFile(t *testing.T) {
	c := setupTestConfig(t)

	if err := c.Patch("http://127.0.0.1:9000", "gpt-5-4", "databricks-proxy", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())

	// Check model field — should be set when absent.
	model, ok := m["model"].(string)
	if !ok || model != "databricks-proxy/gpt-5-4" {
		t.Errorf("model = %q, want %q", model, "databricks-proxy/gpt-5-4")
	}

	// Check provider injected.
	providers, _ := m["provider"].(map[string]interface{})
	dbProxy, _ := providers["databricks-proxy"].(map[string]interface{})
	if dbProxy == nil {
		t.Fatal("databricks-proxy provider not found")
	}
	options, _ := dbProxy["options"].(map[string]interface{})
	if options == nil {
		t.Fatal("databricks-proxy options not found")
	}
	if options["baseURL"] != "http://127.0.0.1:9000/v1" {
		t.Errorf("options.baseURL = %v, want %q", options["baseURL"], "http://127.0.0.1:9000/v1")
	}
	if options["apiKey"] != "databricks-proxy" {
		t.Errorf("options.apiKey = %v, want %q", options["apiKey"], "databricks-proxy")
	}
	if dbProxy["npm"] != "@ai-sdk/anthropic" {
		t.Errorf("npm = %v, want %q", dbProxy["npm"], "@ai-sdk/anthropic")
	}
	models, _ := dbProxy["models"].(map[string]interface{})
	if models == nil {
		t.Fatal("databricks-proxy models not found")
	}
	for _, m := range []string{"databricks-claude-opus-4-6", "databricks-claude-sonnet-4-6", "databricks-claude-haiku-4-5"} {
		if models[m] == nil {
			t.Errorf("models[%q] not found", m)
		}
	}
}

func TestPatchPreservesUserConfig(t *testing.T) {
	c := setupTestConfig(t)

	// Write existing config with user providers, commands, and agents.
	existing := `{
  "provider": {
    "openai": {
      "apiKey": "sk-test-123",
      "models": ["gpt-4o"]
    }
  },
  "commands": {
    "build": "npm run build"
  },
  "agents": {
    "code-review": {"model": "openai/gpt-4o"}
  }
}`
	if err := os.WriteFile(c.Path(), []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	if err := c.Patch("http://127.0.0.1:8080", "claude-4", "db-key", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())

	// Verify user providers preserved.
	providers, _ := m["provider"].(map[string]interface{})
	if _, ok := providers["openai"]; !ok {
		t.Error("openai provider was not preserved")
	}
	if _, ok := providers["databricks-proxy"]; !ok {
		t.Error("databricks-proxy provider was not injected")
	}

	// Verify commands preserved.
	commands, _ := m["commands"].(map[string]interface{})
	if commands["build"] != "npm run build" {
		t.Errorf("commands.build = %v, want %q", commands["build"], "npm run build")
	}

	// Verify agents preserved.
	agents, _ := m["agents"].(map[string]interface{})
	if agents["code-review"] == nil {
		t.Error("agents.code-review was not preserved")
	}
}

func TestPatchPreservesExistingModel(t *testing.T) {
	c := setupTestConfig(t)

	// Write config with user-configured model.
	existing := `{"model": "openai/gpt-4o", "theme": "dark"}`
	if err := os.WriteFile(c.Path(), []byte(existing), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Patch without forceModel — should preserve existing model.
	if err := c.Patch("http://127.0.0.1:9000", "gpt-5-4", "key", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())
	if m["model"] != "openai/gpt-4o" {
		t.Errorf("model = %v, want %q (should preserve existing)", m["model"], "openai/gpt-4o")
	}
}

func TestPatchForceModelOverridesExisting(t *testing.T) {
	c := setupTestConfig(t)

	// Write config with user-configured model.
	existing := `{"model": "openai/gpt-4o"}`
	if err := os.WriteFile(c.Path(), []byte(existing), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Patch with forceModel — should override.
	if err := c.Patch("http://127.0.0.1:9000", "gpt-5-4", "key", true); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())
	if m["model"] != "databricks-proxy/gpt-5-4" {
		t.Errorf("model = %v, want %q (forceModel should override)", m["model"], "databricks-proxy/gpt-5-4")
	}
}

func TestPatchSetsModelWhenAbsent(t *testing.T) {
	c := setupTestConfig(t)

	// Write config with no model key.
	existing := `{"theme": "dark"}`
	if err := os.WriteFile(c.Path(), []byte(existing), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := c.Patch("http://127.0.0.1:9000", "gpt-5-4", "key", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())
	if m["model"] != "databricks-proxy/gpt-5-4" {
		t.Errorf("model = %v, want %q (should set when absent)", m["model"], "databricks-proxy/gpt-5-4")
	}
}

func TestUpdateProxyURL(t *testing.T) {
	c := setupTestConfig(t)

	// Patch first.
	if err := c.Patch("http://127.0.0.1:5000", "model-a", "key", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	// Update proxy URL — argument is the base proxy URL; the function
	// derives /v1 (anthropic) and /v1beta (gemini) suffixes internally.
	if err := c.UpdateProxyURL("http://127.0.0.1:6000"); err != nil {
		t.Fatalf("UpdateProxyURL: %v", err)
	}

	m := readJSON(t, c.Path())
	providers, _ := m["provider"].(map[string]interface{})
	dbProxy, _ := providers["databricks-proxy"].(map[string]interface{})
	options, _ := dbProxy["options"].(map[string]interface{})
	if options == nil {
		t.Fatal("databricks-proxy options not found after UpdateProxyURL")
	}
	if options["baseURL"] != "http://127.0.0.1:6000/v1" {
		t.Errorf("options.baseURL = %v, want %q", options["baseURL"], "http://127.0.0.1:6000/v1")
	}

	// Model should be unchanged.
	if m["model"] != "databricks-proxy/model-a" {
		t.Errorf("model changed unexpectedly: %v", m["model"])
	}
}

func TestNeedsConfig_NoFile(t *testing.T) {
	c := setupTestConfig(t)
	if !c.NeedsConfig("http://127.0.0.1:49156") {
		t.Error("NeedsConfig should return true when config file does not exist")
	}
}

func TestNeedsConfig_AlreadyConfigured(t *testing.T) {
	c := setupTestConfig(t)

	// Patch the config first.
	if err := c.Patch("http://127.0.0.1:49156", "model-a", "key", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	// Same proxyURL — should return false (no-op).
	if c.NeedsConfig("http://127.0.0.1:49156") {
		t.Error("NeedsConfig should return false when baseURL already matches")
	}
}

func TestNeedsConfig_DifferentURL(t *testing.T) {
	c := setupTestConfig(t)

	// Patch with one URL.
	if err := c.Patch("http://127.0.0.1:49156", "model-a", "key", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	// Different proxyURL — should return true.
	if !c.NeedsConfig("http://127.0.0.1:50000") {
		t.Error("NeedsConfig should return true when baseURL differs")
	}
}

func TestNeedsConfig_MissingProvider(t *testing.T) {
	c := setupTestConfig(t)

	// Write config with no provider section.
	existing := `{"theme": "dark"}`
	if err := os.WriteFile(c.Path(), []byte(existing), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !c.NeedsConfig("http://127.0.0.1:49156") {
		t.Error("NeedsConfig should return true when provider section is missing")
	}
}

func TestInvalidJSONC(t *testing.T) {
	c := setupTestConfig(t)

	// Write JSONC with comments and trailing commas.
	jsoncContent := `{
  // This is a comment
  "theme": "dark",
  "provider": {
    "openai": {
      "apiKey": "sk-test", // inline comment
    },
  },
}`
	if err := os.WriteFile(c.Path(), []byte(jsoncContent), 0o600); err != nil {
		t.Fatalf("write JSONC: %v", err)
	}

	// Patch should parse JSONC correctly.
	if err := c.Patch("http://127.0.0.1:7000", "model-b", "key", false); err != nil {
		t.Fatalf("Patch with JSONC: %v", err)
	}

	m := readJSON(t, c.Path())

	// User config preserved.
	if m["theme"] != "dark" {
		t.Errorf("theme = %v, want %q", m["theme"], "dark")
	}

	// Provider injected alongside existing.
	providers, _ := m["provider"].(map[string]interface{})
	if providers["openai"] == nil {
		t.Error("openai provider lost after JSONC patch")
	}
	if providers["databricks-proxy"] == nil {
		t.Error("databricks-proxy not injected")
	}
}

func TestAddPlugin_EmptyConfig(t *testing.T) {
	c := setupTestConfig(t)

	if err := c.AddPlugin("/path/to/plugin"); err != nil {
		t.Fatalf("AddPlugin: %v", err)
	}

	m := readJSON(t, c.Path())
	plugins, _ := m["plugin"].([]interface{})
	if len(plugins) != 1 || plugins[0] != "/path/to/plugin" {
		t.Errorf("plugin = %v, want [\"/path/to/plugin\"]", plugins)
	}
}

func TestAddPlugin_Idempotent(t *testing.T) {
	c := setupTestConfig(t)

	c.AddPlugin("/path/to/plugin")
	c.AddPlugin("/path/to/plugin")

	m := readJSON(t, c.Path())
	plugins, _ := m["plugin"].([]interface{})
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin entry after double add, got %d", len(plugins))
	}
}

func TestAddPlugin_PreservesExisting(t *testing.T) {
	c := setupTestConfig(t)

	// Write config with an existing plugin.
	initial := map[string]interface{}{
		"plugin": []interface{}{"existing-plugin"},
		"model":  "some-model",
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(c.Path(), data, 0o600)

	if err := c.AddPlugin("/path/to/new"); err != nil {
		t.Fatalf("AddPlugin: %v", err)
	}

	m := readJSON(t, c.Path())
	plugins, _ := m["plugin"].([]interface{})
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d: %v", len(plugins), plugins)
	}
	if plugins[0] != "existing-plugin" {
		t.Errorf("plugins[0] = %v, want %q", plugins[0], "existing-plugin")
	}
	if m["model"] != "some-model" {
		t.Error("model key was clobbered")
	}
}

func TestRemovePlugin(t *testing.T) {
	c := setupTestConfig(t)

	c.AddPlugin("/path/to/plugin")
	if err := c.RemovePlugin("/path/to/plugin"); err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}

	m := readJSON(t, c.Path())
	if _, exists := m["plugin"]; exists {
		t.Error("expected plugin key to be removed when array is empty")
	}
}

func TestRemovePlugin_PreservesOthers(t *testing.T) {
	c := setupTestConfig(t)

	c.AddPlugin("keep-this")
	c.AddPlugin("remove-this")

	if err := c.RemovePlugin("remove-this"); err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}

	m := readJSON(t, c.Path())
	plugins, _ := m["plugin"].([]interface{})
	if len(plugins) != 1 || plugins[0] != "keep-this" {
		t.Errorf("plugin = %v, want [\"keep-this\"]", plugins)
	}
}

func TestRemovePlugin_NoFile(t *testing.T) {
	c := setupTestConfig(t)

	// Should not error on missing file.
	if err := c.RemovePlugin("/nonexistent"); err != nil {
		t.Fatalf("RemovePlugin on missing file should return nil, got: %v", err)
	}
}

// TestPatch_InjectsBothProviders verifies Patch writes both the
// databricks-proxy (Anthropic) AND databricks-gemini-proxy (Gemini)
// providers in a single pass with the correct npm package, baseURL,
// apiKey, and — for Gemini — the explicit Authorization header that
// overrides the @ai-sdk/google default x-goog-api-key auth.
func TestPatch_InjectsBothProviders(t *testing.T) {
	c := setupTestConfig(t)

	if err := c.Patch("http://127.0.0.1:49156", "databricks-claude-opus-4-7", "databricks-proxy", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())
	providers, _ := m["provider"].(map[string]interface{})
	if providers == nil {
		t.Fatal("provider section missing after Patch")
	}

	// Anthropic provider.
	dbProxy, _ := providers["databricks-proxy"].(map[string]interface{})
	if dbProxy == nil {
		t.Fatal("databricks-proxy provider missing after Patch")
	}
	if dbProxy["npm"] != "@ai-sdk/anthropic" {
		t.Errorf("databricks-proxy.npm = %v, want %q", dbProxy["npm"], "@ai-sdk/anthropic")
	}
	dbOpts, _ := dbProxy["options"].(map[string]interface{})
	if dbOpts == nil {
		t.Fatal("databricks-proxy.options missing")
	}
	if dbOpts["baseURL"] != "http://127.0.0.1:49156/v1" {
		t.Errorf("databricks-proxy.options.baseURL = %v, want %q",
			dbOpts["baseURL"], "http://127.0.0.1:49156/v1")
	}

	// Gemini provider.
	gem, _ := providers["databricks-gemini-proxy"].(map[string]interface{})
	if gem == nil {
		t.Fatal("databricks-gemini-proxy provider missing after Patch")
	}
	if gem["npm"] != "@ai-sdk/google" {
		t.Errorf("databricks-gemini-proxy.npm = %v, want %q", gem["npm"], "@ai-sdk/google")
	}
	gemOpts, _ := gem["options"].(map[string]interface{})
	if gemOpts == nil {
		t.Fatal("databricks-gemini-proxy.options missing")
	}
	if gemOpts["baseURL"] != "http://127.0.0.1:49156/v1beta" {
		t.Errorf("databricks-gemini-proxy.options.baseURL = %v, want %q",
			gemOpts["baseURL"], "http://127.0.0.1:49156/v1beta")
	}
	if _, ok := gemOpts["apiKey"].(string); !ok || gemOpts["apiKey"] == "" {
		t.Errorf("databricks-gemini-proxy.options.apiKey = %v, want non-empty (SDK requires non-empty)", gemOpts["apiKey"])
	}
	gemHeaders, _ := gemOpts["headers"].(map[string]interface{})
	if gemHeaders == nil {
		t.Fatal("databricks-gemini-proxy.options.headers missing — required to override SDK's default x-goog-api-key auth")
	}
	auth, _ := gemHeaders["Authorization"].(string)
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("databricks-gemini-proxy.options.headers.Authorization = %q, want value with %q prefix", auth, "Bearer ")
	}
}

// TestPatch_PreservesUserProvider verifies an existing user-defined
// provider (e.g. openai) is untouched when Patch injects the two
// databricks-* providers. Companion to the broader
// TestPatchPreservesUserConfig — this one isolates the
// other-provider-preserved invariant under the dual-injection contract.
func TestPatch_PreservesUserProvider(t *testing.T) {
	c := setupTestConfig(t)

	existing := `{
  "provider": {
    "openai": {
      "apiKey": "sk-user-secret",
      "models": {"gpt-4o": {}}
    }
  }
}`
	if err := os.WriteFile(c.Path(), []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	if err := c.Patch("http://127.0.0.1:49156", "databricks-claude-opus-4-7", "databricks-proxy", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())
	providers, _ := m["provider"].(map[string]interface{})
	openai, _ := providers["openai"].(map[string]interface{})
	if openai == nil {
		t.Fatal("openai provider was clobbered by Patch")
	}
	if openai["apiKey"] != "sk-user-secret" {
		t.Errorf("openai.apiKey = %v, want %q (user value preserved)", openai["apiKey"], "sk-user-secret")
	}
	if _, ok := providers["databricks-proxy"]; !ok {
		t.Error("databricks-proxy not injected alongside user provider")
	}
	if _, ok := providers["databricks-gemini-proxy"]; !ok {
		t.Error("databricks-gemini-proxy not injected alongside user provider")
	}
}

// TestNeedsConfig_DetectsStaleGeminiProvider verifies that a config
// missing the databricks-gemini-proxy provider — but with a fully
// correct databricks-proxy block at the expected anthropic baseURL —
// still returns NeedsConfig=true. This is the upgrade-from-pre-Gemini
// trigger: existing users get re-patched on first run after upgrade
// because the Gemini provider is absent.
func TestNeedsConfig_DetectsStaleGeminiProvider(t *testing.T) {
	c := setupTestConfig(t)

	// Hand-craft an anthropic-only config with the correct anthropic baseURL.
	// NeedsConfig must still return true because the gemini provider is missing.
	existing := map[string]interface{}{
		"provider": map[string]interface{}{
			"databricks-proxy": map[string]interface{}{
				"npm":  "@ai-sdk/anthropic",
				"name": "Databricks AI Gateway",
				"options": map[string]interface{}{
					"baseURL": "http://127.0.0.1:49156/v1",
					"apiKey":  "databricks-proxy",
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(c.Path(), data, 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	if !c.NeedsConfig("http://127.0.0.1:49156") {
		t.Fatal("NeedsConfig returned false for anthropic-only config; expected true (gemini provider missing)")
	}

	// Sanity: after Patch, NeedsConfig flips to false.
	if err := c.Patch("http://127.0.0.1:49156", "databricks-claude-opus-4-7", "databricks-proxy", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if c.NeedsConfig("http://127.0.0.1:49156") {
		t.Error("NeedsConfig still true after Patch injected both providers")
	}
}

// TestNeedsConfig_DetectsStaleGeminiBaseURL verifies that a config
// where the databricks-gemini-proxy.options.baseURL points at an
// outdated proxy port is detected as stale even when the
// databricks-proxy provider is current. Mirrors the existing
// TestNeedsConfig_DifferentURL guard but for the Gemini side.
func TestNeedsConfig_DetectsStaleGeminiBaseURL(t *testing.T) {
	c := setupTestConfig(t)

	// Patch with one port, then mutate only the gemini baseURL to a stale value.
	if err := c.Patch("http://127.0.0.1:49156", "databricks-claude-opus-4-7", "databricks-proxy", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	m := readJSON(t, c.Path())
	providers, _ := m["provider"].(map[string]interface{})
	gem, _ := providers["databricks-gemini-proxy"].(map[string]interface{})
	gemOpts, _ := gem["options"].(map[string]interface{})
	gemOpts["baseURL"] = "http://127.0.0.1:11111/v1beta" // stale port
	gem["options"] = gemOpts
	providers["databricks-gemini-proxy"] = gem
	m["provider"] = providers
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(c.Path(), data, 0o600); err != nil {
		t.Fatalf("write mutated config: %v", err)
	}

	if !c.NeedsConfig("http://127.0.0.1:49156") {
		t.Fatal("NeedsConfig returned false for stale gemini baseURL; expected true")
	}
}

// TestUpdateProxyURL_UpdatesBothProviders verifies UpdateProxyURL
// rewrites the baseURL on BOTH managed providers — anthropic gets the
// /v1 suffix, gemini gets /v1beta — from a single base proxy URL.
func TestUpdateProxyURL_UpdatesBothProviders(t *testing.T) {
	c := setupTestConfig(t)

	if err := c.Patch("http://127.0.0.1:49156", "databricks-claude-opus-4-7", "databricks-proxy", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	if err := c.UpdateProxyURL("http://127.0.0.1:50000"); err != nil {
		t.Fatalf("UpdateProxyURL: %v", err)
	}

	m := readJSON(t, c.Path())
	providers, _ := m["provider"].(map[string]interface{})

	dbProxy, _ := providers["databricks-proxy"].(map[string]interface{})
	dbOpts, _ := dbProxy["options"].(map[string]interface{})
	if dbOpts["baseURL"] != "http://127.0.0.1:50000/v1" {
		t.Errorf("databricks-proxy.options.baseURL = %v, want %q",
			dbOpts["baseURL"], "http://127.0.0.1:50000/v1")
	}

	gem, _ := providers["databricks-gemini-proxy"].(map[string]interface{})
	gemOpts, _ := gem["options"].(map[string]interface{})
	if gemOpts["baseURL"] != "http://127.0.0.1:50000/v1beta" {
		t.Errorf("databricks-gemini-proxy.options.baseURL = %v, want %q",
			gemOpts["baseURL"], "http://127.0.0.1:50000/v1beta")
	}
}

// TestPatch_GeminiModelsRegistered verifies all seven Databricks Gemini
// model entries land in the gemini provider's models map. Keys are
// listed inline (not looped over a slice the test itself defines) so a
// typo in the implementation still fails this test.
func TestPatch_GeminiModelsRegistered(t *testing.T) {
	c := setupTestConfig(t)

	if err := c.Patch("http://127.0.0.1:49156", "databricks-claude-opus-4-7", "databricks-proxy", false); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	m := readJSON(t, c.Path())
	providers, _ := m["provider"].(map[string]interface{})
	gem, _ := providers["databricks-gemini-proxy"].(map[string]interface{})
	if gem == nil {
		t.Fatal("databricks-gemini-proxy provider missing")
	}
	models, _ := gem["models"].(map[string]interface{})
	if models == nil {
		t.Fatal("databricks-gemini-proxy.models missing")
	}

	if _, ok := models["databricks-gemini-3-1-pro"]; !ok {
		t.Error("models.databricks-gemini-3-1-pro missing")
	}
	if _, ok := models["databricks-gemini-3-1-flash-lite"]; !ok {
		t.Error("models.databricks-gemini-3-1-flash-lite missing")
	}
	if _, ok := models["databricks-gemini-3-pro"]; !ok {
		t.Error("models.databricks-gemini-3-pro missing")
	}
	if _, ok := models["databricks-gemini-3-flash"]; !ok {
		t.Error("models.databricks-gemini-3-flash missing")
	}
	if _, ok := models["databricks-gemini-3-5-flash"]; !ok {
		t.Error("models.databricks-gemini-3-5-flash missing")
	}
	if _, ok := models["databricks-gemini-2-5-pro"]; !ok {
		t.Error("models.databricks-gemini-2-5-pro missing")
	}
	if _, ok := models["databricks-gemini-2-5-flash"]; !ok {
		t.Error("models.databricks-gemini-2-5-flash missing")
	}
}

// TestStripJSONC exercises the stdlib JSONC stripper across every
// comment/trailing-comma/string-literal edge case. Each case asserts the
// stripped output json.Unmarshals to the expected Go value (reflect
// DeepEqual), which is more robust than byte comparison. Cases whose
// input carries no JSON payload (empty, whitespace-only) set want to nil
// and instead assert the stripped output is whitespace-only.
func TestStripJSONC(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  interface{}
	}{
		{
			name:  "line comment",
			input: "{\n  // a comment\n  \"a\": 1\n}",
			want:  map[string]interface{}{"a": float64(1)},
		},
		{
			name:  "inline line comment",
			input: "{\"a\": 1 // trailing note\n}",
			want:  map[string]interface{}{"a": float64(1)},
		},
		{
			name:  "block comment",
			input: `{"a": /* inline block */ 1}`,
			want:  map[string]interface{}{"a": float64(1)},
		},
		{
			name:  "block comment spanning multiple lines",
			input: "{\n  /* line one\n     line two\n     line three */\n  \"a\": 1\n}",
			want:  map[string]interface{}{"a": float64(1)},
		},
		{
			name:  "trailing comma before brace",
			input: "{\"a\": 1,}",
			want:  map[string]interface{}{"a": float64(1)},
		},
		{
			name:  "trailing comma before bracket",
			input: "[1, 2,]",
			want:  []interface{}{float64(1), float64(2)},
		},
		{
			name:  "line-comment-like text inside string",
			input: `{"a": "a // b"}`,
			want:  map[string]interface{}{"a": "a // b"},
		},
		{
			name:  "block-comment-like text inside string",
			input: `{"a": "a /* b */ c"}`,
			want:  map[string]interface{}{"a": "a /* b */ c"},
		},
		{
			name:  "escaped quote inside string",
			input: `{"a": "say \"hi\""}`,
			want:  map[string]interface{}{"a": `say "hi"`},
		},
		{
			name:  "double slash inside url string",
			input: `{"url": "https://x.com"}`,
			want:  map[string]interface{}{"url": "https://x.com"},
		},
		{
			// Escaped backslash immediately before the closing quote: the
			// stripper must not treat the closing quote as escaped, so the
			// string terminates correctly and no following bytes are eaten.
			name:  "escaped backslash before closing quote",
			input: `{"a": "c:\\path\\", "b": 2}`,
			want:  map[string]interface{}{"a": `c:\path\`, "b": float64(2)},
		},
		{
			// A comma inside a string literal that sits right before a
			// closing bracket must NOT be treated as a trailing comma by the
			// removeTrailingCommas pass (in-string tracking must hold).
			name:  "comma inside string adjacent to bracket",
			input: `["a,", "b,"]`,
			want:  []interface{}{"a,", "b,"},
		},
		{
			name:  "comma then line comment then brace",
			input: "{\"a\":1, // c\n}",
			want:  map[string]interface{}{"a": float64(1)},
		},
		{
			name:  "comma then block comment then bracket",
			input: "[1,/*c*/]",
			want:  []interface{}{float64(1)},
		},
		{
			name:  "comma then whitespace then brace",
			input: "{\"a\":1,\n}",
			want:  map[string]interface{}{"a": float64(1)},
		},
		{
			name:  "no comments or commas round-trips",
			input: `{"a": 1, "b": "two", "c": [1, 2, 3]}`,
			want: map[string]interface{}{
				"a": float64(1),
				"b": "two",
				"c": []interface{}{float64(1), float64(2), float64(3)},
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace only",
			input: "  \n\t  \n",
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripJSONC([]byte(tc.input))

			if tc.want == nil {
				// No JSON payload: the stripper must not fabricate content.
				if strings.TrimSpace(string(got)) != "" {
					t.Errorf("stripJSONC(%q) = %q, want whitespace-only", tc.input, got)
				}
				return
			}

			var v interface{}
			if err := json.Unmarshal(got, &v); err != nil {
				t.Fatalf("stripJSONC(%q) = %q; json.Unmarshal failed: %v", tc.input, got, err)
			}
			if !reflect.DeepEqual(v, tc.want) {
				t.Errorf("stripJSONC(%q) unmarshaled to %#v, want %#v", tc.input, v, tc.want)
			}
		})
	}
}
