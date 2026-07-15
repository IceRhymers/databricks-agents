package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IceRhymers/databricks-agents/internal/core/headless"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
	"github.com/IceRhymers/databricks-agents/internal/opencode/jsonconfig"
)

// headlessEnsure checks whether the proxy is healthy on the given port.
// If not, it starts a detached headless proxy and polls until ready (max 10s).
// Called by the opencode plugin at init via: databricks-opencode hooks session-start
//
// No refcount is acquired here — OpenCode has no exit hook to release it,
// so the proxy relies on its idle timeout for shutdown instead.
//
// EnsureCommand pins the spawn prefix to []string{"serve"} so the detached
// child reaches the `serve` subcommand instead of the removed --headless root
// flag. Without this override, headless.buildArgs would default to the legacy
// `--headless --port=N` shape, which falls through to opencode (since
// --headless is no longer a known wrapper flag) and the proxy never starts.
func headlessEnsure(port int) error {
	s := loadState()
	scheme := "http"
	if s.TLSCert != "" {
		scheme = "https"
	}
	return headless.Ensure(headless.Config{
		Port:          port,
		Scheme:        scheme,
		TLSCert:       s.TLSCert,
		TLSKey:        s.TLSKey,
		ManagedEnvVar: "DATABRICKS_OPENCODE_MANAGED",
		LogPrefix:     "databricks-opencode",
		EnsureCommand: []string{"serve"},
	})
}

// refcountPathForPort returns the file path used for cross-process session counting.
func refcountPathForPort(port int) string {
	return refcount.PathForPort(".databricks-opencode-sessions", port)
}

// pluginJSTemplate is the opencode plugin that ensures the headless proxy is running.
// The plugin init body runs at session startup (ESM format required by OpenCode).
// Shutdown is handled by the proxy's idle timeout — OpenCode has no exit hook.
// %s is replaced with the absolute path to the binary at install time so the
// plugin works regardless of Bun's PATH (which may not include ~/go/bin or /opt/homebrew/bin).
//
// The wrapper is invoked via the `hooks session-start` subcommand. Users on a
// stale plugin will see the old invocation fail at session start — they must
// re-run `databricks-opencode hooks install` to refresh the plugin file.
const pluginJSTemplate = `export const DatabricksProxy = async ({ $ }) => {
  await $` + "`" + `%s hooks session-start` + "`" + `;
  return {};
};
`

// installHooks writes the JS plugin and registers it in opencode.json.
// The absolute path to the binary is baked in at install time; rerun
// `hooks install` after reinstalling via a different method (e.g. switching
// from go install to Homebrew).
func installHooks() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve own binary path: %w", err)
	}

	configDir, err := opencodeConfigDir()
	if err != nil {
		return fmt.Errorf("cannot determine opencode config dir: %w", err)
	}

	// Write plugin JS file.
	pluginDir := filepath.Join(configDir, "plugins", "databricks-proxy")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return fmt.Errorf("creating plugin dir: %w", err)
	}
	pluginJS := fmt.Sprintf(pluginJSTemplate, self)
	pluginPath := filepath.Join(pluginDir, "index.js")
	if err := os.WriteFile(pluginPath, []byte(pluginJS), 0o644); err != nil {
		return fmt.Errorf("writing plugin: %w", err)
	}

	// Register plugin in opencode.json.
	cm := jsonconfig.New(configDir)
	return cm.AddPlugin(pluginDir)
}

// uninstallHooks removes the JS plugin file and its entry from opencode.json.
func uninstallHooks() error {
	configDir, err := opencodeConfigDir()
	if err != nil {
		return fmt.Errorf("cannot determine opencode config dir: %w", err)
	}

	pluginDir := filepath.Join(configDir, "plugins", "databricks-proxy")

	// Remove plugin entry from opencode.json.
	cm := jsonconfig.New(configDir)
	if err := cm.RemovePlugin(pluginDir); err != nil {
		return fmt.Errorf("removing plugin from config: %w", err)
	}

	// Remove plugin file and directory (only if empty).
	os.Remove(filepath.Join(pluginDir, "index.js"))
	os.Remove(pluginDir)
	return nil
}
