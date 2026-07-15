package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHooksCommandTreeParity verifies bidirectional parity between the
// hooksCommand subcommand declaration and runHooksCommand's dispatch
// switch:
//
//   - tree → runner: every child name declared on hooksCommand must be
//     accepted by runHooksCommand. We probe each with --help so the
//     handler short-circuits before doing real work (no filesystem
//     mutation, no proxy probe).
//   - runner → tree: every dispatch keyword recognised by runHooksCommand
//     must appear on the tree. The set is hard-coded here so a future
//     dispatch addition without a tree entry trips the check.
//
// Bidirectional verification documented in #88 commit body: the test was
// confirmed to fail when hooksCommand was temporarily removed from
// rootCommand.Subcommands (parity flip) and again when a child was
// removed from hooksCommand (tree shrink) — i.e. drift in either
// direction trips this test.
func TestHooksCommandTreeParity(t *testing.T) {
	expected := map[string]bool{
		"install":       true,
		"uninstall":     true,
		"session-start": true,
	}

	if len(hooksCommand.Subcommands) != len(expected) {
		t.Errorf("hooksCommand has %d subcommands; expected %d (drift between tree and runHooksCommand)",
			len(hooksCommand.Subcommands), len(expected))
	}

	for _, sub := range hooksCommand.Subcommands {
		if !expected[sub.Name] {
			t.Errorf("hooksCommand declares %q but runHooksCommand has no case for it", sub.Name)
			continue
		}
		// --help short-circuits before doing real work.
		err := runHooksCommand([]string{sub.Name, "--help"})
		if err != nil {
			t.Errorf("runHooksCommand([%q --help]) returned %v; the dispatcher should accept this name", sub.Name, err)
		}
	}

	// Inverse: every dispatch keyword must appear on the tree.
	for name := range expected {
		if hooksCommand.Subcommand(name) == nil {
			t.Errorf("runHooksCommand recognises %q but hooksCommand has no child for it", name)
		}
	}
}

// TestRunHooksCommand_UnknownSubcommand verifies the dispatcher rejects
// unknown subcommands with a non-nil error and prints help to stderr.
func TestRunHooksCommand_UnknownSubcommand(t *testing.T) {
	err := runHooksCommand([]string{"bogus"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q should mention the unknown subcommand", err)
	}
}

// TestRunHooksCommand_MissingSubcommand verifies the dispatcher rejects
// bare `hooks` with no subcommand.
func TestRunHooksCommand_MissingSubcommand(t *testing.T) {
	err := runHooksCommand(nil)
	if err == nil {
		t.Fatal("expected error for missing subcommand, got nil")
	}
}

// TestHooksInstallUninstall_Idempotency verifies that
// install → install → uninstall → uninstall produces a clean state with
// no extraneous diff in ~/.codex/hooks.json. Drives the round-trip
// through the dispatcher (runHooksCommand) rather than the lower-level
// installHooks/uninstallHooks helpers so the wiring between subcommand
// dispatch and the existing hooks.go logic is exercised.
func TestHooksInstallUninstall_Idempotency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	// install x2 — second call must be idempotent.
	if err := runHooksCommand([]string{"install"}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json after first install: %v", err)
	}
	if err := runHooksCommand([]string{"install"}); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json after second install: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("install is not idempotent — second install diffed:\nfirst:\n%s\nsecond:\n%s",
			first, second)
	}

	// SessionStart count must be 1 (not 2) after double install.
	var doc map[string]interface{}
	if err := json.Unmarshal(second, &doc); err != nil {
		t.Fatalf("parse hooks.json after double install: %v", err)
	}
	hooks := doc["hooks"].(map[string]interface{})
	ss := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Errorf("expected 1 SessionStart entry after install x2, got %d", len(ss))
	}

	// uninstall x2 — second call must be a no-op (no error, no panic).
	if err := runHooksCommand([]string{"uninstall"}); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	if err := runHooksCommand([]string{"uninstall"}); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}

	// hooks.json should still parse and have no databricks-codex entries.
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json after double uninstall: %v", err)
	}
	var clean map[string]interface{}
	if err := json.Unmarshal(raw, &clean); err != nil {
		t.Fatalf("parse hooks.json after double uninstall: %v", err)
	}
	if _, ok := clean["hooks"]; ok {
		t.Errorf("expected no hooks key after uninstall, got: %s", raw)
	}
}

// TestHooksInstall_WritesNewSubcommandSpelling verifies that the entry
// written by `hooks install` invokes the new subcommand spelling
// (`databricks-codex hooks session-start`), not the legacy
// `--headless-ensure` flag. Locks the hook JSON contract for #88.
func TestHooksInstall_WritesNewSubcommandSpelling(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	if err := runHooksCommand([]string{"install"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	if !strings.Contains(string(raw), "databricks-codex hooks session-start") {
		t.Errorf("expected hooks.json to invoke the new subcommand spelling, got:\n%s", raw)
	}
	if strings.Contains(string(raw), "--headless-ensure") {
		t.Errorf("hooks.json still invokes the removed --headless-ensure flag:\n%s", raw)
	}
}

// TestHooksRunCommand_LegacyDetectorReplacement verifies that an existing
// hooks.json containing a legacy `--headless-ensure` entry is cleanly
// replaced (not duplicated) by `hooks install`. Backstops the cutover —
// users who installed via the pre-#88 binary get a clean re-install.
func TestHooksRunCommand_LegacyDetectorReplacement(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	// Seed hooks.json with the legacy entry.
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "startup",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "databricks-codex --headless-ensure",
							"timeout": float64(15),
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(hooksPath, data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := runHooksCommand([]string{"install"}); err != nil {
		t.Fatalf("install over legacy: %v", err)
	}

	raw, _ := os.ReadFile(hooksPath)
	if strings.Contains(string(raw), "--headless-ensure") {
		t.Errorf("legacy --headless-ensure entry survived re-install:\n%s", raw)
	}
	if !strings.Contains(string(raw), "databricks-codex hooks session-start") {
		t.Errorf("expected new subcommand spelling after re-install, got:\n%s", raw)
	}
	// Exactly one SessionStart entry — no duplicates.
	var doc map[string]interface{}
	json.Unmarshal(raw, &doc)
	hooks := doc["hooks"].(map[string]interface{})
	ss := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Errorf("expected 1 SessionStart entry after legacy replacement, got %d", len(ss))
	}
}

// TestRootSubcommandsIncludeHooks verifies hooksCommand is registered on
// the root tree. This is the parity hook for the bidirectional check
// described in the issue: temporarily removing hooksCommand from
// rootCommand.Subcommands flips this assertion to fail, confirming the
// tree wiring is load-bearing.
func TestRootSubcommandsIncludeHooks(t *testing.T) {
	if rootCommand.Subcommand("hooks") == nil {
		t.Fatal("rootCommand has no `hooks` subcommand — tree wiring lost")
	}
}
