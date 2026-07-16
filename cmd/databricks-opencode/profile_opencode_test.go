package main

import (
	"errors"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// Compile-time conformance for the three opencode implementations. Mirrors the
// claude/codex profile tests so the seam types stay interface-shaped.
var (
	_ profile.SettingsPatcher = opencodeSettingsPatcher{}
	_ profile.DaemonStrategy  = opencodeDaemon{}
	_ profile.HookInstaller   = opencodeHooks{}
)

// TestOpencodeProfile_Fields asserts the constructed Profile carries the
// expected scalar fields and non-nil interface implementations.
//
// This file exists because of #218: GatewayPath is now sourced from the
// gatewayPath const in token.go, and claude and codex already had tests pinning
// their literal. opencode had no profile test at all, so single-sourcing it here
// would have left the only unpinned one. This is that pin.
func TestOpencodeProfile_Fields(t *testing.T) {
	p := OpencodeProfile(opencodeSettingsPatcher{})
	if p.Name != ProfileName {
		t.Errorf("Name = %q, want %q", p.Name, ProfileName)
	}
	if p.Name != "databricks-opencode" {
		t.Errorf("ProfileName = %q, want %q", p.Name, "databricks-opencode")
	}
	if p.ChildBinary != "opencode" {
		t.Errorf("ChildBinary = %q, want %q", p.ChildBinary, "opencode")
	}
	// The Anthropic path. Note Profile.GatewayPath is single-valued and cannot
	// carry opencode's second (Gemini Native, /ai-gateway/gemini/v1beta)
	// upstream — see the field's doc comment in internal/profile.
	if p.GatewayPath != "/ai-gateway/anthropic" {
		t.Errorf("GatewayPath = %q, want %q", p.GatewayPath, "/ai-gateway/anthropic")
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

// TestOpencodeProfile_GatewayPathMatchesConstructor pins GatewayPath against the
// URL the launch path actually builds, so the two cannot drift apart.
func TestOpencodeProfile_GatewayPathMatchesConstructor(t *testing.T) {
	p := OpencodeProfile(opencodeSettingsPatcher{})
	const host = "https://h.example.com"
	if got, want := ConstructGatewayURL(host), host+p.GatewayPath; got != want {
		t.Errorf("ConstructGatewayURL(%q) = %q, but host+GatewayPath = %q", host, got, want)
	}
}

// TestOpencodeDaemon_Unsupported asserts the inert API-shape conformance struct:
// opencode has no daemon, so Install/Uninstall report ErrDaemonUnsupported and
// Status/Diagnostics return zero values. Never invoked at runtime.
func TestOpencodeDaemon_Unsupported(t *testing.T) {
	d := opencodeDaemon{}
	if err := d.Install(profile.DaemonInstallRequest{}); !errors.Is(err, profile.ErrDaemonUnsupported) {
		t.Errorf("Install err = %v, want ErrDaemonUnsupported", err)
	}
	if err := d.Uninstall(); !errors.Is(err, profile.ErrDaemonUnsupported) {
		t.Errorf("Uninstall err = %v, want ErrDaemonUnsupported", err)
	}
}
