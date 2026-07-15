package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- #83 hooks tree parity tests ---
//
// The command-tree registry in commands.go is the single source of truth for
// the hooks subcommand surface. These tests pin the tree shape: the children
// the dispatcher (runHooksCommand) cares about are present, each carries the
// flags the runner reads from r.Strings/r.Bools, and the top-level node is a
// pure dispatcher with no flags of its own.
//
// Bidirectional verification was performed during #83: temporarily removing
// `hooksCommand` from rootCommand.Subcommands causes
// TestHooksCommandTreeParity to fail loudly (rootCommand.Subcommand("hooks")
// returns nil), confirming the test catches drift in either direction.

// TestHooksCommandTreeParity walks the hooks subcommand tree and asserts the
// children the dispatcher routes to (install / uninstall / session-start)
// are declared with the expected flag set. If a runner is added without a
// tree node — or a flag is plumbed through a runner without being declared
// on the tree — this test fails loudly so completion + help stay aligned.
func TestHooksCommandTreeParity(t *testing.T) {
	root := rootCommand.Subcommand("hooks")
	if root == nil {
		t.Fatal("rootCommand should have a `hooks` subcommand declared")
	}
	if len(root.AllFlags()) != 0 {
		t.Errorf("hooks (top-level dispatcher) should declare no flags of its own; got %d", len(root.AllFlags()))
	}

	cases := []struct {
		name      string
		wantFlags []string
	}{
		{name: "install", wantFlags: []string{"--profile", "--port", "--help"}},
		{name: "uninstall", wantFlags: []string{"--help"}},
		{name: "session-start", wantFlags: []string{"--port", "--help"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := hooksCommand.Subcommand(c.name)
			if node == nil {
				t.Fatalf("hooksCommand should have a %q child", c.name)
			}
			known := node.KnownFlags()
			for _, want := range c.wantFlags {
				if !known[want] {
					t.Errorf("hooks %s missing %q in its known-flag set", c.name, want)
				}
			}
			// Catch the reverse: any flag declared on the tree must be
			// expected by this assertion. Surfacing a stray flag here means
			// either the tree gained a flag the runner doesn't read OR this
			// test went stale; either is a drift signal.
			for k := range known {
				found := false
				for _, want := range c.wantFlags {
					if k == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("hooks %s has unexpected flag %q (update test or remove flag)", c.name, k)
				}
			}
		})
	}
}

// TestHooksCommandHasNestedSubcommands asserts the install/uninstall/
// session-start children are declared so completion can offer them nested
// (`databricks-opencode hooks <TAB>` → install/uninstall/session-start).
// Drives the AC: "Shell completion completes `hooks <TAB>`".
func TestHooksCommandHasNestedSubcommands(t *testing.T) {
	want := []string{"install", "uninstall", "session-start"}
	got := make(map[string]bool, len(hooksCommand.Subcommands))
	for _, s := range hooksCommand.Subcommands {
		got[s.Name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("hooksCommand should have nested `%s` subcommand for nested completion", w)
		}
	}
}

// TestRootCompletionOffersHooksSubcommand asserts `hooks` is surfaced in the
// root command-tree's CompletionSubcommands output, with all three children
// reachable through the recursive walk. Without this, `databricks-opencode
// <TAB>` would not offer `hooks`, and `hooks <TAB>` would not offer the
// leaves.
func TestRootCompletionOffersHooksSubcommand(t *testing.T) {
	for _, sc := range knownSubcommands {
		if sc.Name == "hooks" {
			gotChildren := make(map[string]bool, len(sc.Subcommands))
			for _, child := range sc.Subcommands {
				gotChildren[child.Name] = true
			}
			for _, want := range []string{"install", "uninstall", "session-start"} {
				if !gotChildren[want] {
					t.Errorf("knownSubcommands `hooks` is missing nested child %q (recursive completion broken)", want)
				}
			}
			return
		}
	}
	t.Error("knownSubcommands should offer `hooks` as a position-1 subcommand")
}

// TestPluginJSTemplate_InvokesNewSubcommand is the load-bearing JS-content
// check: the plugin file must invoke `<wrapper> hooks session-start` (the
// new subcommand introduced in #83), not the legacy `--headless-ensure` root
// flag (which #83 removed). Drives the migration callout — users with stale
// plugins from before #83 see the legacy invocation fail at session start
// because the flag no longer exists.
func TestPluginJSTemplate_InvokesNewSubcommand(t *testing.T) {
	if !strings.Contains(pluginJSTemplate, "hooks session-start") {
		t.Errorf("pluginJSTemplate should invoke `<wrapper> hooks session-start`, got:\n%s", pluginJSTemplate)
	}
	if strings.Contains(pluginJSTemplate, "--headless-ensure") {
		t.Errorf("pluginJSTemplate must not invoke the removed --headless-ensure flag, got:\n%s", pluginJSTemplate)
	}
}

// TestInstallHooksIdempotency walks the full install → install → uninstall →
// uninstall cycle in an isolated $XDG_CONFIG_HOME and asserts:
//
//  1. Re-running install overwrites the plugin file rather than duplicating
//     it (file content is byte-identical between runs).
//  2. Re-running uninstall when nothing is installed is a no-op (no error,
//     no leftover plugin file).
//
// Uses t.Setenv("XDG_CONFIG_HOME", …) so opencodeConfigDir resolves to a
// per-test temp directory without leaking into the user's real config —
// the critical pitfall called out in the #83 plan.
func TestInstallHooksIdempotency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	pluginPath := filepath.Join(dir, "opencode", "plugins", "databricks-proxy", "index.js")

	// --- 1. First install ---
	if err := installHooks(); err != nil {
		t.Fatalf("first installHooks: %v", err)
	}
	first, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("plugin file missing after first install: %v", err)
	}
	if !strings.Contains(string(first), "hooks session-start") {
		t.Errorf("plugin file should invoke `hooks session-start` after install, got:\n%s", string(first))
	}

	// --- 2. Second install: byte-identical content (idempotent) ---
	if err := installHooks(); err != nil {
		t.Fatalf("second installHooks: %v", err)
	}
	second, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("plugin file missing after second install: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("plugin file content should be byte-identical between idempotent installs\nfirst:\n%s\nsecond:\n%s", string(first), string(second))
	}

	// --- 3. First uninstall: removes plugin file ---
	if err := uninstallHooks(); err != nil {
		t.Fatalf("first uninstallHooks: %v", err)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Errorf("plugin file should not exist after uninstall; got err=%v", err)
	}

	// --- 4. Second uninstall: no-op (no error) ---
	if err := uninstallHooks(); err != nil {
		t.Errorf("second uninstallHooks should be a no-op when plugin is already absent, got: %v", err)
	}
}
