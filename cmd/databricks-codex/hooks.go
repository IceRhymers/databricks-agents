package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/IceRhymers/databricks-agents/internal/core/headless"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
)

// hooksKeyRegexp matches a `hooks` feature-flag assignment line. It is
// anchored at start-of-line (after optional whitespace) and requires the key
// be followed by whitespace or `=`, so it does NOT match the legacy
// `codex_hooks` key.
var hooksKeyRegexp = regexp.MustCompile(`(?m)^\s*hooks\s*=`)

// headlessEnsure checks whether the proxy is healthy on the given port.
// If not, it starts a detached headless proxy and polls until ready (max 10s).
// Called by the SessionStart hook via: databricks-codex hooks session-start
// (#88 lifted this off the legacy --headless-ensure root flag; the entry
// written by installHooks now invokes the subcommand spelling).
//
// The proxy shuts itself down via idle timeout — there is no corresponding
// release hook because Codex CLI has no session-end event.
//
// #89: the spawned subprocess invokes the new `serve` subcommand instead of
// the deleted `--headless` root flag. headless.Config.EnsureCommand replaces
// the default `[]string{"--headless"}` prefix with `[]string{"serve"}`, so
// pkg/headless.buildArgs now emits `databricks-codex serve --port=N
// [--tls-cert=... --tls-key=...]`. Without this wiring, every SessionStart
// hook firing would spawn a process that immediately fails arg parsing.
func headlessEnsure(port int) error {
	s := loadState()
	return headless.Ensure(headlessEnsureConfig(port, s))
}

// headlessEnsureConfig assembles the headless.Config that headlessEnsure
// passes to headless.Ensure. Extracted so a unit test can verify the
// load-bearing fields (EnsureCommand routing through `serve`, the
// $DATABRICKS_CODEX_MANAGED guard, the codex refcount path) without
// spawning a real subprocess. Pure projection over (port, state).
func headlessEnsureConfig(port int, s persistentState) headless.Config {
	scheme := "http"
	if s.TLSCert != "" {
		scheme = "https"
	}
	return headless.Config{
		Port:          port,
		Scheme:        scheme,
		TLSCert:       s.TLSCert,
		TLSKey:        s.TLSKey,
		ManagedEnvVar: "DATABRICKS_CODEX_MANAGED",
		LogPrefix:     "databricks-codex",
		RefcountPath:  refcount.PathForPort(".databricks-codex-sessions", port),
		// #89: spawn `databricks-codex serve --port=N` instead of the
		// removed `databricks-codex --headless --port=N`.
		EnsureCommand: []string{"serve"},
	}
}

// installHooks merges the databricks-codex SessionStart and Stop hooks into
// ~/.codex/hooks.json. Idempotent — safe to run after upgrades.
func installHooks(hooksPath string) error {
	doc, err := readHooksDoc(hooksPath)
	if err != nil {
		// File may not exist yet — start with an empty document.
		doc = map[string]interface{}{}
	}

	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	// Remove any existing databricks-codex hooks before re-adding (idempotent).
	removeDBXHooks(hooks)

	// SessionStart hook — starts proxy if not already running.
	sessionStart, _ := hooks["SessionStart"].([]interface{})
	sessionStart = append(sessionStart, map[string]interface{}{
		"matcher": "startup",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": "databricks-codex hooks session-start",
				"timeout": 15,
			},
		},
	})
	hooks["SessionStart"] = sessionStart

	doc["hooks"] = hooks
	if err := writeHooksDoc(hooksPath, doc); err != nil {
		return err
	}

	// Codex CLI requires [features] hooks = true to read hooks.json.
	configPath := filepath.Join(filepath.Dir(hooksPath), "config.toml")
	return ensureHooksFeatureFlag(configPath)
}

// uninstallHooks removes the databricks-codex hooks from ~/.codex/hooks.json.
func uninstallHooks(hooksPath string) error {
	doc, err := readHooksDoc(hooksPath)
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

	if err := writeHooksDoc(hooksPath, doc); err != nil {
		return err
	}

	// Remove our `hooks = true` from config.toml. Legacy `codex_hooks =` lines
	// are left untouched — we never migrate user-authored keys.
	configPath := filepath.Join(filepath.Dir(hooksPath), "config.toml")
	return removeHooksFeatureFlag(configPath)
}

// removeDBXHooks removes any hook entries that databricks-codex installed.
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

// isDBXHookEntry returns true if any nested hook command was installed by
// databricks-codex. Recognises both spellings so re-install / uninstall
// stays idempotent across the #88 cutover:
//   - "databricks-codex --headless..." — legacy entries written by the
//     pre-#88 --install-hooks flag.
//   - "databricks-codex hooks ..." — entries written by the new
//     `hooks install` subcommand.
func isDBXHookEntry(entry interface{}) bool {
	m, ok := entry.(map[string]interface{})
	if !ok {
		return false
	}
	inner, _ := m["hooks"].([]interface{})
	for _, h := range inner {
		hm, _ := h.(map[string]interface{})
		cmd, _ := hm["command"].(string)
		if cmd == "" {
			continue
		}
		if strings.HasPrefix(cmd, "databricks-codex --headless") {
			return true
		}
		if strings.HasPrefix(cmd, "databricks-codex hooks ") {
			return true
		}
	}
	return false
}

// readHooksDoc reads and parses hooks.json, returning the full document.
func readHooksDoc(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// writeHooksDoc writes a hooks document back to disk as indented JSON.
func writeHooksDoc(path string, doc map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling hooks: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0o600)
}

// atomicWriteFile writes data to a temp file in the same directory as path,
// then renames it into place. This prevents partial-write
// corruption from concurrent invocations or crashes mid-write. The temp file
// is created in the destination directory so the rename is atomic on the same
// filesystem.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hooks-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, perm); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// ensureHooksFeatureFlag ensures config.toml contains `hooks = true`
// inside a [features] section. Surgical: if the new `hooks` key is already
// present it's a no-op; if [features] exists the key is appended inside it;
// otherwise a new section is appended at the end.
//
// Detection is anchored — a legacy `codex_hooks = ...` line is NOT treated
// as enabled, and is never read, rewritten, or removed by this function.
// If both keys end up coexisting, upstream Codex tolerates that.
//
// TODO(#72 follow-up): This writes directly to the same config.toml that
// tomlconfig.Manager owns, bypassing the manager's backup/restore bookkeeping.
// There is a TOCTOU window if a session is patching config.toml concurrently.
// Routing this through tomlconfig.Manager is out of scope for the atomic-write
// fix and tracked as a separate follow-up.
func ensureHooksFeatureFlag(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading config.toml: %w", err)
	}
	content := string(data)

	// Already enabled — nothing to do. Anchored match so `codex_hooks` does
	// not satisfy this check.
	if hooksKeyRegexp.MatchString(content) {
		return nil
	}

	// Find the [features] section header.
	idx := strings.Index(content, "[features]")
	if idx >= 0 {
		// Insert the key right after the header line.
		end := strings.Index(content[idx:], "\n")
		if end < 0 {
			end = len(content[idx:])
		}
		insertAt := idx + end
		content = content[:insertAt] + "\nhooks = true" + content[insertAt:]
	} else {
		// Append a new [features] section.
		sep := "\n"
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			sep = "\n\n"
		} else if len(content) > 0 {
			sep = "\n"
		}
		content += sep + "[features]\nhooks = true\n"
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return atomicWriteFile(configPath, []byte(content), 0o600)
}

// removeHooksFeatureFlag removes any line that assigns the new `hooks` key
// inside config.toml. It performs an anchored, line-oriented match so the
// legacy `codex_hooks =` key — and any other user-authored content — is
// preserved byte-identical.
//
// If the file does not exist, or contains no matching line, this is a no-op.
func removeHooksFeatureFlag(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading config.toml: %w", err)
	}
	content := string(data)
	if !hooksKeyRegexp.MatchString(content) {
		return nil
	}

	// Walk the file line by line and drop only the lines whose trimmed
	// prefix is the new `hooks` key (followed by whitespace or `=`).
	var b strings.Builder
	b.Grow(len(content))
	start := 0
	for i := 0; i <= len(content); i++ {
		if i == len(content) || content[i] == '\n' {
			line := content[start:i]
			if !isNewHooksAssignmentLine(line) {
				b.WriteString(line)
				if i < len(content) {
					b.WriteByte('\n')
				}
			}
			start = i + 1
		}
	}
	out := b.String()
	if out == content {
		return nil
	}
	return atomicWriteFile(configPath, []byte(out), 0o600)
}

// isNewHooksAssignmentLine reports whether a single line (no trailing
// newline) is an assignment of the new `hooks` feature flag. The check is
// anchored so `codex_hooks = ...` returns false.
func isNewHooksAssignmentLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "hooks") {
		return false
	}
	rest := trimmed[len("hooks"):]
	if len(rest) == 0 {
		return false
	}
	c := rest[0]
	return c == ' ' || c == '\t' || c == '='
}
