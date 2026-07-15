package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
)

// --- #173 hooks tree parity tests ---

// TestHooksCommandParity asserts the top-level `hooks` node carries no
// flags of its own (it's a pure dispatcher; subcommands carry the flag
// surface). Mirrors TestConfigCommandParity (#172).
func TestHooksCommandParity(t *testing.T) {
	assertFlagSetEqual(t, "hooksCommand", hooksCommand, []string{})
}

func TestHooksInstallParity(t *testing.T) {
	install := hooksCommand.Subcommand("install")
	if install == nil {
		t.Fatal("hooksCommand should have an `install` subcommand")
	}
	assertFlagSetEqual(t, "hooks install", *install, []string{
		"profile", "port", "help",
	})
}

func TestHooksUninstallParity(t *testing.T) {
	uninst := hooksCommand.Subcommand("uninstall")
	if uninst == nil {
		t.Fatal("hooksCommand should have an `uninstall` subcommand")
	}
	assertFlagSetEqual(t, "hooks uninstall", *uninst, []string{"help"})
}

func TestHooksSessionStartParity(t *testing.T) {
	ss := hooksCommand.Subcommand("session-start")
	if ss == nil {
		t.Fatal("hooksCommand should have a `session-start` subcommand")
	}
	assertFlagSetEqual(t, "hooks session-start", *ss, []string{"port", "help"})
}

func TestHooksSessionEndParity(t *testing.T) {
	se := hooksCommand.Subcommand("session-end")
	if se == nil {
		t.Fatal("hooksCommand should have a `session-end` subcommand")
	}
	assertFlagSetEqual(t, "hooks session-end", *se, []string{"port", "help"})
}

// TestHooksHasNestedSubcommands asserts the install/uninstall/session-start/
// session-end children are declared so completion can offer them nested.
// Mirrors TestServeHasNestedSubcommands (#171) and TestConfigHasNestedSubcommands
// (#172). Drives the AC: "Help + completion derived from the tree".
func TestHooksHasNestedSubcommands(t *testing.T) {
	want := []string{"install", "uninstall", "session-start", "session-end"}
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

// TestRootCompletionOffersHooksSubcommand asserts `hooks` is surfaced as a
// position-1 subcommand completion. Mirrors the implicit pattern that every
// other subcommand on the root tree gets here automatically.
func TestRootCompletionOffersHooksSubcommand(t *testing.T) {
	for _, sc := range knownSubcommands {
		if sc.Name == "hooks" {
			// Ensure the nested children come through too — install / uninstall
			// / session-start / session-end must be reachable via the recursive
			// CompletionSubcommands path. Without this, `hooks <TAB>` would not
			// offer the leaves.
			gotChildren := make(map[string]bool, len(sc.Subcommands))
			for _, child := range sc.Subcommands {
				gotChildren[child.Name] = true
			}
			for _, want := range []string{"install", "uninstall", "session-start", "session-end"} {
				if !gotChildren[want] {
					t.Errorf("knownSubcommands `hooks` is missing nested child %q (recursive completion broken)", want)
				}
			}
			return
		}
	}
	t.Error("knownSubcommands should offer `hooks` as a position-1 subcommand")
}

// --- #173 hook JSON round-trip test ---
//
// THE LOAD-BEARING TEST. Validates that installHooks writes JSON pointing at
// the new command names AND that uninstallHooks (via isDBXHookEntry) detects
// + removes them. If the JSON-name swap and the detector-prefix swap fall out
// of sync, this test fails bidirectionally (install→detect, uninstall→empty).
// See the Risk 1 callout in plan #173.

// TestInstallHooks_WritesNewCommandNames asserts the JSON written by
// installHooks invokes `databricks-claude hooks session-start` /
// `databricks-claude hooks session-end` — the new command names introduced
// in #173. The previous values were `databricks-claude --headless-ensure` /
// `--headless-release`; a stale install path that kept emitting those would
// generate hooks that no longer resolve to a known root flag and would
// silently fail at session start.
func TestInstallHooks_WritesNewCommandNames(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	if err := installHooks(settingsPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}

	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		t.Fatal("hooks block missing")
	}

	// SessionStart command must be the new `hooks session-start` form.
	startCmd := nestedHookCommand(t, hooks, "SessionStart")
	if startCmd != "databricks-claude hooks session-start" {
		t.Errorf("SessionStart command = %q, want %q", startCmd, "databricks-claude hooks session-start")
	}
	endCmd := nestedHookCommand(t, hooks, "SessionEnd")
	if endCmd != "databricks-claude hooks session-end" {
		t.Errorf("SessionEnd command = %q, want %q", endCmd, "databricks-claude hooks session-end")
	}
}

// TestInstallUninstallHooks_RoundTrip is the bidirectional round-trip:
// install writes the new command names → uninstall detects them via the
// updated `isDBXHookEntry` prefix and removes them cleanly. If the detector
// prefix stayed pinned to the old `databricks-claude --headless` form,
// uninstallHooks would silently leave the new entries behind — this test is
// the load-bearing safeguard against that drift.
func TestInstallUninstallHooks_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	if err := installHooks(settingsPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}

	// Verify hooks present (sanity check before we tear them down).
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json after install: %v", err)
	}
	if _, ok := doc["hooks"]; !ok {
		t.Fatal("hooks block missing after installHooks")
	}

	// Uninstall and re-read.
	if err := uninstallHooks(settingsPath); err != nil {
		t.Fatalf("uninstallHooks: %v", err)
	}
	doc, err = readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json after uninstall: %v", err)
	}
	if h, ok := doc["hooks"]; ok {
		// The hooks block (or its inner SessionStart/SessionEnd arrays)
		// should be empty/absent. If it persists, the detector prefix is
		// out of sync with the JSON command names — orphaned entries.
		if hm, _ := h.(map[string]interface{}); hm != nil {
			for event, val := range hm {
				if arr, _ := val.([]interface{}); len(arr) > 0 {
					t.Errorf("uninstallHooks left %d entries under %q — detector prefix likely out of sync with installHooks command names",
						len(arr), event)
				}
			}
		}
	}
}

// nestedHookCommand pulls the inner command string out of the nested hook
// JSON shape Claude Code expects:
//
//	"<event>": [{ "matcher": "...", "hooks": [{ "type": "command", "command": "..." }] }]
func nestedHookCommand(t *testing.T, hooks map[string]interface{}, event string) string {
	t.Helper()
	arr, _ := hooks[event].([]interface{})
	if len(arr) == 0 {
		t.Fatalf("%s hook missing", event)
	}
	entry, _ := arr[0].(map[string]interface{})
	inner, _ := entry["hooks"].([]interface{})
	if len(inner) == 0 {
		t.Fatalf("%s inner hooks empty", event)
	}
	hm, _ := inner[0].(map[string]interface{})
	cmd, _ := hm["command"].(string)
	return cmd
}

// --- #173 end-to-end hooks lifecycle test ---
//
// Drives the AC: "Hooks deployment mode end-to-end verified: install hooks
// → simulate SessionStart → proxy comes up (refcounted) → simulate
// SessionEnd → refcount decrements / proxy tears down."
//
// We do NOT spawn a real detached proxy — that path is `pkg/headless`'s
// concern and out of `hooks`' scope. Instead we stand up a stub health
// server (the proven `TestHeadlessEnsure_AcquiresRefcount` /
// `TestHeadlessRelease_EphemeralPostsShutdown` pattern) so headlessEnsure
// sees a healthy proxy and just acquires the refcount, and headlessRelease
// POSTs /shutdown. This exercises every code path the hook JSON triggers;
// the only stubbed piece is the proxy process itself.

// TestHooksLifecycleEndToEnd walks the full install → session-start →
// session-end cycle through the SAME code paths the runner functions call
// (bootstrapSettings / installHooks / headlessEnsure / headlessRelease).
func TestHooksLifecycleEndToEnd(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// --- 1. Install (mirrors runHooksInstall body without os.Exit) ---
	if err := bootstrapSettings(0, "DEFAULT", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("bootstrapSettings: %v", err)
	}
	if err := installHooks(settingsPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}

	// settings.json must carry the placeholder ANTHROPIC_BASE_URL — Risk 4
	// in the plan: install writes the placeholder and session-start
	// overwrites it at runtime. Assert the placeholder, not the gateway URL.
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	env := envBlock(doc)
	if got, _ := env["ANTHROPIC_BASE_URL"].(string); got != "http://127.0.0.1:49153" {
		t.Errorf("ANTHROPIC_BASE_URL after install: got %q, want placeholder %q", got, "http://127.0.0.1:49153")
	}
	// Hook entries must be present + carry the new command names.
	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		t.Fatal("hooks block missing after install")
	}
	if cmd := nestedHookCommand(t, hooks, "SessionStart"); cmd != "databricks-claude hooks session-start" {
		t.Errorf("SessionStart command = %q, want %q", cmd, "databricks-claude hooks session-start")
	}
	if cmd := nestedHookCommand(t, hooks, "SessionEnd"); cmd != "databricks-claude hooks session-end" {
		t.Errorf("SessionEnd command = %q, want %q", cmd, "databricks-claude hooks session-end")
	}

	// --- 2. Stub proxy (so headlessEnsure sees "healthy" and just
	//        acquires the refcount instead of spawning a real proxy) ---
	shutdownCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"daemon":false}`)
		case "/shutdown":
			if r.Method == http.MethodPost {
				shutdownCalled = true
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	rcPath := refcount.PathForPort(".databricks-claude-sessions", port)
	os.Remove(rcPath)
	t.Cleanup(func() { os.Remove(rcPath) })

	// --- 3. Simulate SessionStart hook firing (runHooksSessionStart body) ---
	headlessEnsure(port)

	// Refcount file must exist with count == 1.
	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("refcount file not found after session-start: %v", err)
	}
	var rc struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(data, &rc); err != nil {
		t.Fatalf("unmarshal refcount file: %v", err)
	}
	if rc.Count != 1 {
		t.Errorf("refcount after session-start = %d, want 1", rc.Count)
	}

	// --- 4. Simulate SessionEnd hook firing (runHooksSessionEnd body) ---
	headlessRelease(port)

	if !shutdownCalled {
		t.Error("/shutdown was not POSTed by headlessRelease — session-end teardown path broken")
	}
}
