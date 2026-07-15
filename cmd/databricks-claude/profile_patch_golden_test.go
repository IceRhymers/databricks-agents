package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// inTempHome runs fn with HOME and statePath redirected to a fresh temp dir,
// restoring both before returning (explicit restore, not t.Cleanup, so multiple
// sequential isolated runs in one test don't interfere).
func inTempHome(t *testing.T, fn func(home string)) {
	t.Helper()
	dir := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	origState := statePath
	statePath = func() string { return filepath.Join(dir, ".claude", ".databricks-claude.json") }
	defer func() {
		if hadHome {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
		statePath = origState
	}()
	fn(dir)
}

// runInHomeReadSettings seeds settings.json (optional), runs the scenario in an
// isolated HOME, and returns the resulting settings.json bytes.
func runInHomeReadSettings(t *testing.T, seed func(settingsPath string), run func()) []byte {
	t.Helper()
	var out []byte
	inTempHome(t, func(home string) {
		settingsPath := filepath.Join(home, ".claude", "settings.json")
		if seed != nil {
			if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			seed(settingsPath)
		}
		run()
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("read settings.json: %v", err)
		}
		out = data
	})
	return out
}

// seedSettings writes a settings.json with the given env block.
func seedSettings(t *testing.T, env map[string]interface{}) func(string) {
	return func(settingsPath string) {
		doc := map[string]interface{}{"env": env}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatalf("marshal seed: %v", err)
		}
		if err := os.WriteFile(settingsPath, append(data, '\n'), 0o600); err != nil {
			t.Fatalf("write seed: %v", err)
		}
	}
}

// TestPatch_ByteIdenticalToBootstrapSettings asserts the new
// claudeSettingsPatcher.Patch produces byte-for-byte identical settings.json to
// the legacy bootstrapSettings across fresh-install, OTEL-present, idempotent
// re-run, and a seeded env-block-with-OTEL-and-stale-localhost case.
func TestPatch_ByteIdenticalToBootstrapSettings(t *testing.T) {
	const url = "http://127.0.0.1:49153"

	otelEnv := map[string]string{
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": url + "/otel/v1/metrics",
		"OTEL_METRICS_EXPORTER":               "otlp",
		"CLAUDE_OTEL_UC_METRICS_TABLE":        "main.obs.metrics",
		"CLAUDE_CODE_ENABLE_TELEMETRY":        "1",
	}

	cases := []struct {
		name    string
		seed    func(string)
		port    int
		profile string
		env     map[string]string
	}{
		{
			name:    "fresh-install-no-file",
			seed:    nil,
			port:    49153,
			profile: "myws",
			env:     nil,
		},
		{
			name:    "otel-present",
			seed:    nil,
			port:    49153,
			profile: "myws",
			env:     otelEnv,
		},
		{
			name: "idempotent-rerun-base-url-matches",
			seed: seedSettings(t, map[string]interface{}{
				"ANTHROPIC_BASE_URL":   url,
				"ANTHROPIC_AUTH_TOKEN": "proxy-managed",
			}),
			port:    49153,
			profile: "myws",
			env:     nil,
		},
		{
			name: "seeded-env-otel-and-stale-localhost",
			seed: seedSettings(t, map[string]interface{}{
				"MY_CUSTOM_VAR":                       "keep-me",
				"ANTHROPIC_BASE_URL":                  "http://127.0.0.1:59998", // stale, dead port
				"ANTHROPIC_AUTH_TOKEN":                "proxy-managed",
				"CLAUDE_OTEL_UC_METRICS_TABLE":        "old.metrics",
				"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://127.0.0.1:59998/otel/v1/metrics",
			}),
			port:    49153,
			profile: "myws",
			env:     otelEnv,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldBytes := runInHomeReadSettings(t, c.seed, func() {
				if err := bootstrapSettings(c.port, c.profile, url, c.env); err != nil {
					t.Fatalf("bootstrapSettings: %v", err)
				}
			})
			newBytes := runInHomeReadSettings(t, c.seed, func() {
				err := claudeSettingsPatcher{}.Patch(profile.PatchRequest{
					PortFlag:    c.port,
					ProfileName: c.profile,
					ProxyURL:    url,
					Env:         c.env,
				})
				if err != nil {
					t.Fatalf("Patch: %v", err)
				}
			})
			if string(oldBytes) != string(newBytes) {
				t.Errorf("settings.json bytes differ between bootstrapSettings and Patch\n--- old ---\n%s\n--- new ---\n%s",
					oldBytes, newBytes)
			}
		})
	}
}

// TestClaudeHooks_ParityWithFreeFunctions asserts claudeHooks.Install produces
// byte-identical settings.json to installHooks, and claudeHooks.Uninstall
// round-trips (uninstall after install matches uninstallHooks output).
func TestClaudeHooks_ParityWithFreeFunctions(t *testing.T) {
	// Install parity: fresh install via free function vs interface impl.
	dir := t.TempDir()
	directPath := filepath.Join(dir, "settings.json")
	if err := installHooks(directPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	directInstalled, err := os.ReadFile(directPath)
	if err != nil {
		t.Fatalf("read direct install: %v", err)
	}

	var viaInterface []byte
	inTempHome(t, func(home string) {
		if err := (claudeHooks{}).Install(); err != nil {
			t.Fatalf("claudeHooks.Install: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("read interface install: %v", err)
		}
		viaInterface = data
	})

	if string(directInstalled) != string(viaInterface) {
		t.Errorf("install bytes differ:\n--- installHooks ---\n%s\n--- claudeHooks.Install ---\n%s",
			directInstalled, viaInterface)
	}

	// Uninstall parity + round-trip: install then uninstall through both paths.
	if err := uninstallHooks(directPath); err != nil {
		t.Fatalf("uninstallHooks: %v", err)
	}
	directAfterUninstall, err := os.ReadFile(directPath)
	if err != nil {
		t.Fatalf("read direct uninstall: %v", err)
	}

	var viaInterfaceUninstall []byte
	inTempHome(t, func(home string) {
		if err := (claudeHooks{}).Install(); err != nil {
			t.Fatalf("claudeHooks.Install (round-trip): %v", err)
		}
		if err := (claudeHooks{}).Uninstall(); err != nil {
			t.Fatalf("claudeHooks.Uninstall: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("read interface uninstall: %v", err)
		}
		viaInterfaceUninstall = data
	})

	if string(directAfterUninstall) != string(viaInterfaceUninstall) {
		t.Errorf("uninstall bytes differ:\n--- uninstallHooks ---\n%s\n--- claudeHooks.Uninstall ---\n%s",
			directAfterUninstall, viaInterfaceUninstall)
	}
}
