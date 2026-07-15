package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// Compile-time conformance for the three codex implementations. Mirrors the
// claude profile_claude_test.go asserts so the seam types stay interface-shaped.
var (
	_ profile.SettingsPatcher = codexSettingsPatcher{}
	_ profile.DaemonStrategy  = codexDaemon{}
	_ profile.HookInstaller   = codexHooks{}
)

// TestCodexProfile_Fields asserts the constructed Profile carries the expected
// scalar fields and non-nil interface implementations. Locks the two codex
// deviations from claude: ChildBinary="codex" and the OpenAI gateway path.
func TestCodexProfile_Fields(t *testing.T) {
	p := CodexProfile(codexSettingsPatcher{})
	if p.Name != ProfileName {
		t.Errorf("Name = %q, want %q", p.Name, ProfileName)
	}
	if p.Name != "databricks-codex" {
		t.Errorf("ProfileName = %q, want %q", p.Name, "databricks-codex")
	}
	if p.ChildBinary != "codex" {
		t.Errorf("ChildBinary = %q, want %q", p.ChildBinary, "codex")
	}
	if p.GatewayPath != "/ai-gateway/openai/v1" {
		t.Errorf("GatewayPath = %q, want %q", p.GatewayPath, "/ai-gateway/openai/v1")
	}
	if p.ConfigPath == nil {
		t.Error("ConfigPath must be non-nil")
	}
	if p.PatchSettings == nil {
		t.Error("PatchSettings must be non-nil")
	}
	if p.DaemonStrategy == nil {
		t.Error("DaemonStrategy must be non-nil")
	}
	if p.HookInstaller == nil {
		t.Error("HookInstaller must be non-nil")
	}
}

// TestCodexProfile_PatcherIsWired asserts CodexProfile threads the supplied
// field-bearing patcher through as PatchSettings (not a fresh zero value). A
// same-typed swap that dropped the argument would silently mis-route config.toml
// patching, so the test drives a distinguishing value through the seam.
func TestCodexProfile_PatcherIsWired(t *testing.T) {
	inTempCodexHome(t, func(home string) {
		patcher := codexSettingsPatcher{
			model:         "sentinel-model",
			modelExplicit: true,
		}
		p := CodexProfile(patcher)
		if err := p.PatchSettings.Patch(profile.PatchRequest{ProxyURL: "http://127.0.0.1:49154"}); err != nil {
			t.Fatalf("Patch through profile seam: %v", err)
		}
		got := readCodexConfig(t, home)
		if !strings.Contains(got, `model = "sentinel-model"`) {
			t.Errorf("field-bearing patcher was not wired through CodexProfile; config.toml:\n%s", got)
		}
	})
}

// TestCodexDaemon_UnsupportedSentinel asserts codexDaemon is an INERT
// API-shape conformance: Install/Uninstall return the ErrDaemonUnsupported
// sentinel (codex has no LaunchAgent/systemd/schtasks), and Status/Diagnostics
// return zero values without error. Codex's `serve` is a leaf; these methods
// must never succeed silently as if a daemon were installed.
func TestCodexDaemon_UnsupportedSentinel(t *testing.T) {
	var d codexDaemon

	if err := d.Install(profile.DaemonInstallRequest{}); !errors.Is(err, profile.ErrDaemonUnsupported) {
		t.Errorf("Install err = %v, want ErrDaemonUnsupported", err)
	}
	if err := d.Uninstall(); !errors.Is(err, profile.ErrDaemonUnsupported) {
		t.Errorf("Uninstall err = %v, want ErrDaemonUnsupported", err)
	}

	st, err := d.Status(49154)
	if err != nil {
		t.Errorf("Status err = %v, want nil", err)
	}
	if st != (profile.DaemonStatus{}) {
		t.Errorf("Status = %+v, want zero value", st)
	}

	diag, err := d.Diagnostics()
	if err != nil {
		t.Errorf("Diagnostics err = %v, want nil", err)
	}
	if diag != "" {
		t.Errorf("Diagnostics = %q, want empty", diag)
	}
}

// TestCodexHooks_ParityWithFreeFunctions asserts codexHooks.Install produces
// byte-identical hooks.json to the installHooks free function, and
// codexHooks.Uninstall round-trips (uninstall after install matches
// uninstallHooks output). Mirrors claude's TestClaudeHooks_ParityWithFreeFunctions.
func TestCodexHooks_ParityWithFreeFunctions(t *testing.T) {
	// Install parity: fresh install via free function vs interface impl.
	dir := t.TempDir()
	directPath := dir + "/.codex/hooks.json"
	if err := installHooks(directPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	directInstalled := readFile(t, directPath)

	var viaInterface string
	inTempCodexHome(t, func(home string) {
		if err := (codexHooks{}).Install(); err != nil {
			t.Fatalf("codexHooks.Install: %v", err)
		}
		viaInterface = readFile(t, home+"/.codex/hooks.json")
	})

	if directInstalled != viaInterface {
		t.Errorf("install bytes differ:\n--- installHooks ---\n%s\n--- codexHooks.Install ---\n%s",
			directInstalled, viaInterface)
	}

	// Uninstall parity + round-trip.
	if err := uninstallHooks(directPath); err != nil {
		t.Fatalf("uninstallHooks: %v", err)
	}
	directAfterUninstall := readFile(t, directPath)

	var viaInterfaceUninstall string
	inTempCodexHome(t, func(home string) {
		if err := (codexHooks{}).Install(); err != nil {
			t.Fatalf("codexHooks.Install (round-trip): %v", err)
		}
		if err := (codexHooks{}).Uninstall(); err != nil {
			t.Fatalf("codexHooks.Uninstall: %v", err)
		}
		viaInterfaceUninstall = readFile(t, home+"/.codex/hooks.json")
	})

	if directAfterUninstall != viaInterfaceUninstall {
		t.Errorf("uninstall bytes differ:\n--- uninstallHooks ---\n%s\n--- codexHooks.Uninstall ---\n%s",
			directAfterUninstall, viaInterfaceUninstall)
	}
}

// TestCodexSettingsPatcher_RestoreIsNoOp locks the documented #E scope: Restore
// is an explicit unwired no-op for codex (no config.toml restore free function
// on this branch). It must never error.
func TestCodexSettingsPatcher_RestoreIsNoOp(t *testing.T) {
	if err := (codexSettingsPatcher{}).Restore(profile.RestoreRequest{}); err != nil {
		t.Errorf("Restore err = %v, want nil", err)
	}
}
