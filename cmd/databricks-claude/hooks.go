package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core/health"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
	"github.com/IceRhymers/databricks-agents/pkg/headless"
)

// headlessEnsure checks whether the proxy is healthy on the given port.
// If not, it starts a detached session-scoped proxy and polls until ready
// (max 10s). Called by the SessionStart hook via: databricks-claude hooks
// session-start.
//
// The spawn invocation targets `databricks-claude serve --session-mode
// --port=<N>` (post-#174), not the deleted `--headless` root flag. The
// EnsureCommand field on pkg/headless.Config carries that override; siblings
// (databricks-codex, databricks-opencode) still emit the legacy --headless
// prefix because they leave EnsureCommand empty.
func headlessEnsure(port int) {
	if err := headless.Ensure(headless.Config{
		Port:          port,
		ManagedEnvVar: "DATABRICKS_CLAUDE_MANAGED",
		LogPrefix:     "databricks-claude",
		RefcountPath:  refcount.PathForPort(".databricks-claude-sessions", port),
		EnsureCommand: []string{"serve", "--session-mode"},
	}); err != nil {
		log.Fatalf("databricks-claude: %v", err)
	}
}

// headlessRelease calls POST /shutdown on the proxy to decrement the refcount.
// Called by the SessionEnd hook via: databricks-claude hooks session-end
// Errors are logged but not fatal — proxy may already be stopped.
func headlessRelease(port int) {
	if os.Getenv("DATABRICKS_CLAUDE_MANAGED") == "1" {
		log.Printf("databricks-claude: hooks session-end: skipped (managed session)")
		return
	}

	if mode, _ := health.ProxyMode(port, "http"); mode == "daemon" {
		log.Printf("databricks-claude: hooks session-end: managed by daemon, hook is no-op")
		return
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/shutdown", port), "application/json", nil)
	if err != nil {
		log.Printf("databricks-claude: hooks session-end: %v (proxy may already be stopped)", err)
		return
	}
	resp.Body.Close()
}

// installHooks merges the databricks-claude SessionStart and Stop hooks into
// ~/.claude/settings.json. Idempotent — safe to run after upgrades.
func installHooks(settingsPath string) error {
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		// File may not exist yet — start with an empty document.
		doc = map[string]interface{}{}
	}

	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	// Remove any existing databricks-claude hooks before re-adding (idempotent).
	removeDBXHooks(hooks)

	// SessionStart hook — starts proxy if not already running.
	sessionStart, _ := hooks["SessionStart"].([]interface{})
	sessionStart = append(sessionStart, map[string]interface{}{
		"matcher": "startup",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": "databricks-claude hooks session-start",
				"timeout": 15,
			},
		},
	})
	hooks["SessionStart"] = sessionStart

	// SessionEnd hook — decrements proxy refcount; proxy exits when last session ends.
	// Uses SessionEnd (not Stop) because Stop only fires after API activity,
	// while SessionEnd fires reliably on every exit path including immediate /exit.
	sessionEnd, _ := hooks["SessionEnd"].([]interface{})
	sessionEnd = append(sessionEnd, map[string]interface{}{
		"matcher": "*",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": "databricks-claude hooks session-end",
				"timeout": 5,
			},
		},
	})
	hooks["SessionEnd"] = sessionEnd

	doc["hooks"] = hooks
	return writeSettingsJSON(settingsPath, doc)
}

// uninstallHooks removes the databricks-claude hooks from ~/.claude/settings.json.
func uninstallHooks(settingsPath string) error {
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		return nil // nothing to remove
	}

	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		return nil
	}

	removeDBXHooks(hooks)

	// Clean up empty hook event keys.
	for k, v := range hooks {
		if arr, ok := v.([]interface{}); ok && len(arr) == 0 {
			delete(hooks, k)
		}
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}

	return writeSettingsJSON(settingsPath, doc)
}

// removeDBXHooks removes any hook entries whose command starts with
// "databricks-claude hooks " (the new prefix introduced in #173). Detector
// MUST stay aligned with the command names installHooks writes — drift here
// silently orphans hooks (uninstall no-ops) and breaks idempotent re-install
// (duplicates entries).
func removeDBXHooks(hooks map[string]interface{}) {
	for event, val := range hooks {
		arr, _ := val.([]interface{})
		filtered := arr[:0]
		for _, entry := range arr {
			if !isDBXHookEntry(entry) {
				filtered = append(filtered, entry)
			}
		}
		hooks[event] = filtered
	}
}

// isDBXHookEntry returns true if any nested hook command references
// "databricks-claude hooks " — i.e. one of the session-start / session-end
// commands written by installHooks. Prefix swap from the legacy
// "databricks-claude --headless" landed in #173 alongside the JSON command
// rewrite; the two MUST move together (see the round-trip test in
// hooks_test.go for the bidirectional guard).
func isDBXHookEntry(entry interface{}) bool {
	m, ok := entry.(map[string]interface{})
	if !ok {
		return false
	}
	inner, _ := m["hooks"].([]interface{})
	for _, h := range inner {
		hm, _ := h.(map[string]interface{})
		if cmd, _ := hm["command"].(string); len(cmd) > 0 {
			if strings.HasPrefix(cmd, "databricks-claude hooks ") {
				return true
			}
		}
	}
	return false
}
