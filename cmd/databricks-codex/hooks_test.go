package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHeadlessEnsure_SkipManaged verifies that headlessEnsure returns
// immediately when DATABRICKS_CODEX_MANAGED=1 is set, without attempting any
// network calls.
func TestHeadlessEnsure_SkipManaged(t *testing.T) {
	t.Setenv("DATABRICKS_CODEX_MANAGED", "1")
	// Should return immediately without error or network call.
	headlessEnsure(49154)
}

// TestInstallHooks_CreatesFile verifies installHooks creates hooks.json
// with the expected SessionStart hook.
func TestInstallHooks_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}

	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		t.Fatal("expected hooks key in document")
	}

	// Check SessionStart
	ss, _ := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Fatalf("expected 1 SessionStart entry, got %d", len(ss))
	}
}

// TestInstallHooks_Idempotent verifies running installHooks twice doesn't duplicate.
func TestInstallHooks_Idempotent(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("first installHooks: %v", err)
	}
	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("second installHooks: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	var doc map[string]interface{}
	json.Unmarshal(data, &doc)

	hooks := doc["hooks"].(map[string]interface{})
	ss := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Errorf("expected 1 SessionStart entry after double install, got %d", len(ss))
	}
}

// TestUninstallHooks_RemovesEntries verifies uninstallHooks removes the hooks.
func TestUninstallHooks_RemovesEntries(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	if err := uninstallHooks(hooksPath); err != nil {
		t.Fatalf("uninstallHooks: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	var doc map[string]interface{}
	json.Unmarshal(data, &doc)

	// Hooks key should be removed entirely (empty).
	if _, exists := doc["hooks"]; exists {
		t.Error("expected hooks key to be removed after uninstall")
	}
}

// TestUninstallHooks_PreservesOtherHooks verifies that uninstall only removes
// databricks-codex hooks, leaving other hooks intact.
func TestUninstallHooks_PreservesOtherHooks(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	// Create a hooks.json with a custom hook.
	os.MkdirAll(filepath.Dir(hooksPath), 0o700)
	initial := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "startup",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "my-custom-hook",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(hooksPath, data, 0o600)

	// Install then uninstall.
	installHooks(hooksPath)
	uninstallHooks(hooksPath)

	raw, _ := os.ReadFile(hooksPath)
	var doc map[string]interface{}
	json.Unmarshal(raw, &doc)

	hooks := doc["hooks"].(map[string]interface{})
	ss := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Errorf("expected 1 custom SessionStart entry preserved, got %d", len(ss))
	}
}

// TestUninstallHooks_NoFile verifies uninstallHooks is a no-op when file doesn't exist.
func TestUninstallHooks_NoFile(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, "nonexistent", "hooks.json")

	if err := uninstallHooks(hooksPath); err != nil {
		t.Fatalf("uninstallHooks on missing file should return nil, got: %v", err)
	}
}

// TestAtomicWriteFile_NoTmpDebris verifies the helper leaves no temp file
// behind after a successful write.
func TestAtomicWriteFile_NoTmpDebris(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "hooks.json")

	if err := atomicWriteFile(dest, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "hooks.json" {
			continue
		}
		t.Errorf("unexpected leftover file in dir: %s", name)
	}

	// Permissions preserved.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0o600 perms, got %v", info.Mode().Perm())
	}
}

// TestAtomicWriteFile_PreservesOriginalOnRenameFailure verifies that if the
// rename fails (simulated by making the destination a directory), the
// original file at the destination path is untouched and no temp debris
// is left behind.
func TestAtomicWriteFile_PreservesOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	// Create a directory at the destination path — os.Rename of a regular
	// file onto an existing non-empty directory fails on Linux, simulating
	// a write failure mid-operation.
	dest := filepath.Join(dir, "hooks.json")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Put a file inside so the dir is non-empty (rename will fail).
	if err := os.WriteFile(filepath.Join(dest, "marker"), []byte("orig"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := atomicWriteFile(dest, []byte("new content"), 0o600)
	if err == nil {
		t.Fatal("expected atomicWriteFile to fail when dest is a non-empty dir")
	}

	// The directory and its contents must still be intact.
	got, err := os.ReadFile(filepath.Join(dest, "marker"))
	if err != nil {
		t.Fatalf("original marker missing after failed write: %v", err)
	}
	if string(got) != "orig" {
		t.Errorf("original content corrupted: got %q", got)
	}

	// No .tmp debris left in dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "hooks.json" {
			continue
		}
		t.Errorf("unexpected leftover file: %s", e.Name())
	}
}

// TestWriteHooksDoc_AtomicReplace verifies writeHooksDoc replaces an existing
// file without leaving temp debris and preserves the new content.
func TestWriteHooksDoc_AtomicReplace(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, "hooks.json")

	// Seed with original content.
	if err := writeHooksDoc(hooksPath, map[string]interface{}{"v": float64(1)}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// Overwrite.
	if err := writeHooksDoc(hooksPath, map[string]interface{}{"v": float64(2)}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	data, _ := os.ReadFile(hooksPath)
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc["v"].(float64) != 2 {
		t.Errorf("expected v=2 after atomic replace, got %v", doc["v"])
	}

	// No .tmp debris.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "hooks.json" {
			continue
		}
		t.Errorf("unexpected leftover: %s", e.Name())
	}
}

// TestEnsureHooksFeatureFlag_NoFeaturesSection verifies that if config.toml
// has no [features] section, one is created with `hooks = true`.
func TestEnsureHooksFeatureFlag_NoFeaturesSection(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	original := "model = \"gpt-5\"\n"
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ensureHooksFeatureFlag(cfg); err != nil {
		t.Fatalf("ensureHooksFeatureFlag: %v", err)
	}
	got, _ := os.ReadFile(cfg)
	s := string(got)
	if !containsLine(s, "[features]") {
		t.Errorf("expected [features] section, got:\n%s", s)
	}
	if !hasNewHooksLine(s) {
		t.Errorf("expected `hooks = true` line, got:\n%s", s)
	}
}

// TestEnsureHooksFeatureFlag_FeaturesSectionWithOtherKey verifies that
// `hooks = true` is inserted under an existing [features] section.
func TestEnsureHooksFeatureFlag_FeaturesSectionWithOtherKey(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	original := "[features]\nfoo = true\n"
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ensureHooksFeatureFlag(cfg); err != nil {
		t.Fatalf("ensureHooksFeatureFlag: %v", err)
	}
	got, _ := os.ReadFile(cfg)
	s := string(got)
	if !containsLine(s, "foo = true") {
		t.Errorf("expected foo = true preserved, got:\n%s", s)
	}
	if !hasNewHooksLine(s) {
		t.Errorf("expected `hooks = true` line, got:\n%s", s)
	}
}

// TestEnsureHooksFeatureFlag_AlreadyEnabled verifies no-op when `hooks = true`
// already present.
func TestEnsureHooksFeatureFlag_AlreadyEnabled(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	original := "[features]\nhooks = true\n"
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ensureHooksFeatureFlag(cfg); err != nil {
		t.Fatalf("ensureHooksFeatureFlag: %v", err)
	}
	got, _ := os.ReadFile(cfg)
	if string(got) != original {
		t.Errorf("expected no-op, got:\n%s", got)
	}
}

// TestEnsureHooksFeatureFlag_LegacyOnlyAppendsNew verifies that if only the
// legacy `codex_hooks = true` is present, `hooks = true` is appended and the
// legacy line survives BYTE-IDENTICAL.
func TestEnsureHooksFeatureFlag_LegacyOnlyAppendsNew(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	original := "[features]\ncodex_hooks = true\n"
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ensureHooksFeatureFlag(cfg); err != nil {
		t.Fatalf("ensureHooksFeatureFlag: %v", err)
	}
	got, _ := os.ReadFile(cfg)
	s := string(got)
	if !containsLine(s, "codex_hooks = true") {
		t.Errorf("legacy codex_hooks line was modified or removed:\n%s", s)
	}
	if !hasNewHooksLine(s) {
		t.Errorf("expected `hooks = true` line appended, got:\n%s", s)
	}
}

// TestEnsureHooksFeatureFlag_BothKeys verifies no-op when both keys present.
func TestEnsureHooksFeatureFlag_BothKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	original := "[features]\ncodex_hooks = true\nhooks = true\n"
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ensureHooksFeatureFlag(cfg); err != nil {
		t.Fatalf("ensureHooksFeatureFlag: %v", err)
	}
	got, _ := os.ReadFile(cfg)
	if string(got) != original {
		t.Errorf("expected no-op, got:\n%s", got)
	}
}

// TestUninstallHooks_RemovesNewKeyPreservesLegacy verifies that
// --uninstall-hooks removes only `hooks =` from config.toml, leaving any
// legacy `codex_hooks =` line untouched.
func TestUninstallHooks_RemovesNewKeyPreservesLegacy(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")
	cfg := filepath.Join(dir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := "[features]\ncodex_hooks = true\nhooks = true\n"
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	if err := uninstallHooks(hooksPath); err != nil {
		t.Fatalf("uninstallHooks: %v", err)
	}

	got, _ := os.ReadFile(cfg)
	s := string(got)
	if !containsLine(s, "codex_hooks = true") {
		t.Errorf("legacy codex_hooks line should survive uninstall, got:\n%s", s)
	}
	if hasNewHooksLine(s) {
		t.Errorf("`hooks = true` line should be removed by uninstall, got:\n%s", s)
	}
}

// containsLine returns true if any line of s exactly equals target after
// trimming trailing whitespace.
func containsLine(s, target string) bool {
	for _, line := range splitLines(s) {
		if line == target {
			return true
		}
	}
	return false
}

// hasNewHooksLine returns true if any line is the canonical `hooks = true`
// (anchored — not matching `codex_hooks`).
func hasNewHooksLine(s string) bool {
	for _, line := range splitLines(s) {
		trimmed := line
		for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t') {
			trimmed = trimmed[1:]
		}
		if trimmed == "hooks = true" {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// TestIsDBXHookEntry verifies detection of databricks-codex hook entries.
// Covers both the legacy --headless-* spellings (entries left over from
// pre-#88 installs) and the new `hooks` subcommand spellings, so a
// re-install or uninstall replaces both cleanly.
func TestIsDBXHookEntry(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"legacy ensure command", "databricks-codex --headless-ensure", true},
		{"legacy release command", "databricks-codex --headless-release", true},
		{"legacy headless base", "databricks-codex --headless", true},
		{"new session-start command", "databricks-codex hooks session-start", true},
		{"new install command", "databricks-codex hooks install", true},
		{"unrelated command", "my-custom-hook", false},
		{"partial match", "databricks-codex --help", false},
		{"hooks-prefixed unrelated", "databricks-codex hooksy-thing", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": tc.cmd,
					},
				},
			}
			got := isDBXHookEntry(entry)
			if got != tc.want {
				t.Errorf("isDBXHookEntry(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
