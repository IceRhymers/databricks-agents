package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// fileSHA returns the hex SHA-256 of a file's contents.
// Returns empty string if the file does not exist.
func fileSHA(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// withTempHome redirects HOME and statePath to a temporary directory for the
// duration of a test. Returns the temp home path.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	orig := statePath
	statePath = func() string { return filepath.Join(dir, ".claude", ".databricks-claude.json") }
	t.Cleanup(func() { statePath = orig })
	return dir
}

func TestBootstrapSettings_FreshMachine(t *testing.T) {
	home := withTempHome(t)
	if err := bootstrapSettings(49153, "myws", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}

	st := loadState()
	if st.Profile != "myws" {
		t.Errorf("profile: got %q, want %q", st.Profile, "myws")
	}
	if st.Port != 49153 {
		t.Errorf("port: got %d, want %d", st.Port, 49153)
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	env := envBlock(doc)
	if got, _ := env["ANTHROPIC_BASE_URL"].(string); got != "http://127.0.0.1:49153" {
		t.Errorf("ANTHROPIC_BASE_URL: got %q, want %q", got, "http://127.0.0.1:49153")
	}
}

func TestBootstrapSettings_PortFlagOnly(t *testing.T) {
	withTempHome(t)
	if err := bootstrapSettings(49154, "DEFAULT", "http://127.0.0.1:49154", nil); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}
	st := loadState()
	if st.Port != 49154 {
		t.Errorf("port: got %d, want %d", st.Port, 49154)
	}
	if st.Profile != "" {
		t.Errorf("profile: got %q, want empty (DEFAULT not persisted)", st.Profile)
	}
}

func TestBootstrapSettings_ProfileOnly(t *testing.T) {
	withTempHome(t)
	if err := bootstrapSettings(0, "myws", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}
	st := loadState()
	if st.Profile != "myws" {
		t.Errorf("profile: got %q, want %q", st.Profile, "myws")
	}
	if st.Port != 0 {
		t.Errorf("port: got %d, want 0 (no flag passed)", st.Port)
	}
}

func TestBootstrapSettings_NoMutation(t *testing.T) {
	withTempHome(t)
	// portFlag=0 + profile=DEFAULT means nothing should be persisted to state.
	if err := bootstrapSettings(0, "DEFAULT", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}
	if _, err := os.Stat(statePath()); !os.IsNotExist(err) {
		t.Errorf("state file should not exist: stat err = %v", err)
	}
}

func TestBootstrapSettings_Idempotent_NoChange(t *testing.T) {
	home := withTempHome(t)
	// First run.
	if err := bootstrapSettings(49153, "myws", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	stateBefore := fileSHA(t, statePath())
	settingsBefore := fileSHA(t, settingsPath)

	// Re-run with identical args; SHAs must match.
	if err := bootstrapSettings(49153, "myws", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("second call: %v", err)
	}
	stateAfter := fileSHA(t, statePath())
	settingsAfter := fileSHA(t, settingsPath)

	if stateBefore != stateAfter {
		t.Errorf("state file changed on idempotent re-run\n  before: %s\n  after:  %s", stateBefore, stateAfter)
	}
	if settingsBefore != settingsAfter {
		t.Errorf("settings.json changed on idempotent re-run\n  before: %s\n  after:  %s", settingsBefore, settingsAfter)
	}
}

func TestBootstrapSettings_FreshMachine_NoClaudeDir(t *testing.T) {
	home := withTempHome(t)
	// Confirm the directory does not exist beforehand.
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("expected ~/.claude absent at start, got err=%v", err)
	}
	if err := bootstrapSettings(49153, "myws", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, ".claude"))
	if err != nil {
		t.Fatalf("expected ~/.claude created, got err=%v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("~/.claude perm: got %o, want 0700", mode)
	}
}

func TestBootstrapSettings_OverwritesStaleProfile(t *testing.T) {
	withTempHome(t)
	if err := saveState(persistentState{Profile: "old", Port: 49153}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	if err := bootstrapSettings(0, "new", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}
	st := loadState()
	if st.Profile != "new" {
		t.Errorf("profile: got %q, want %q", st.Profile, "new")
	}
}

func TestInstallHooks_WritesAtomically(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	if err := installHooks(settingsPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}

	// No leftover .tmp file.
	if _, err := os.Stat(settingsPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file should not exist after install: stat err=%v", err)
	}

	// File created with hooks.
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		t.Fatal("hooks block missing")
	}
	if _, ok := hooks["SessionStart"]; !ok {
		t.Error("SessionStart hook missing")
	}
	if _, ok := hooks["SessionEnd"]; !ok {
		t.Error("SessionEnd hook missing")
	}
}

func TestInstallHooks_NoSettingsFile_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	// settings path inside a subdirectory that doesn't exist yet.
	settingsPath := filepath.Join(dir, "claude", "settings.json")
	if err := installHooks(settingsPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Errorf("settings.json should exist: %v", err)
	}
}

func TestInstallHooks_Idempotent(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	if err := installHooks(settingsPath); err != nil {
		t.Fatalf("first install: %v", err)
	}
	sha1 := fileSHA(t, settingsPath)

	if err := installHooks(settingsPath); err != nil {
		t.Fatalf("second install: %v", err)
	}
	sha2 := fileSHA(t, settingsPath)

	if sha1 != sha2 {
		t.Errorf("install-hooks not idempotent\n  before: %s\n  after:  %s", sha1, sha2)
	}
}
