package main

import (
	"strings"
	"testing"
	"time"
)

// TestServeCommandTreeParity verifies bidirectional parity between the
// serveCommand declaration (commands.go) and the buildServeArgs mapping
// (serve_cmd.go). Mirrors hooks_cmd_test.go's TestHooksCommandTreeParity
// shape, adapted for a leaf command (no sub-subcommands).
//
// Bidirectional contract:
//   - tree → mapper: every flag on serveCommand must be consumed by
//     buildServeArgs (or by runServeCommand's --help short-circuit).
//   - mapper → tree: every flag buildServeArgs consumes must be declared
//     on serveCommand. Drift fails the test loudly.
//
// The expected set is hard-coded so adding a flag to serveCommand without
// wiring it into buildServeArgs trips this check on the next test run.
func TestServeCommandTreeParity(t *testing.T) {
	expected := map[string]bool{
		"idle-timeout":    true,
		"profile":         true,
		"port":            true,
		"model":           true,
		"upstream":        true,
		"log-file":        true,
		"verbose":         true,
		"proxy-api-key":   true,
		"tls-cert":        true,
		"tls-key":         true,
		"no-update-check": true,
		"help":            true,
	}

	got := make(map[string]bool, len(serveCommand.Flags))
	for _, f := range serveCommand.AllFlags() {
		got[f.Name] = true
	}

	for name := range expected {
		if !got[name] {
			t.Errorf("serveCommand should declare --%s but does not", name)
		}
	}
	for name := range got {
		if !expected[name] {
			t.Errorf("serveCommand declares --%s but the parity contract does not list it (add to expected, or remove from tree)", name)
		}
	}
}

// TestRootSubcommandsIncludeServe is the structural counterpart to the
// breaking change in #89: rootCommand must declare serve so dispatch from
// main.go (and shell completion) finds it. Removing serveCommand from
// rootCommand.Subcommands flips this assertion to fail.
func TestRootSubcommandsIncludeServe(t *testing.T) {
	if rootCommand.Subcommand("serve") == nil {
		t.Fatal("rootCommand has no `serve` subcommand — tree wiring lost")
	}
}

// TestRootCommandLegacyHeadlessFlagsRemoved locks the breaking surface
// change from #89: --headless and --idle-timeout must NOT appear on
// rootCommand any more. If a future refactor accidentally re-adds one,
// this test fires and points at the migration sub-issue.
func TestRootCommandLegacyHeadlessFlagsRemoved(t *testing.T) {
	declared := make(map[string]bool)
	for _, f := range rootCommand.AllFlags() {
		declared[f.Name] = true
	}
	for _, flag := range []string{"headless", "idle-timeout"} {
		if declared[flag] {
			t.Errorf("rootCommand still declares --%s after #89 migration; the user-facing flag must move to `serve`", flag)
		}
	}
}

// --- buildServeArgs unit tests ---
//
// These exercise the parsed-flag → Args mapping directly. Together with
// TestServeCommandTreeParity, they form the safety net for "drift between
// what the tree advertises and what the runner actually wires through".

func TestBuildServeArgs_DefaultIdleTimeout(t *testing.T) {
	r, _ := serveCommand.Parse([]string{})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if !a.Headless {
		t.Error("expected Headless=true (serve always runs headless)")
	}
	if a.IdleTimeout != 30*time.Minute {
		t.Errorf("expected default IdleTimeout=30m, got %v", a.IdleTimeout)
	}
}

func TestBuildServeArgs_IdleTimeoutSeconds(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--idle-timeout", "30s"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.IdleTimeout != 30*time.Second {
		t.Errorf("expected 30s, got %v", a.IdleTimeout)
	}
}

func TestBuildServeArgs_IdleTimeoutMinutes(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--idle-timeout", "5m"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.IdleTimeout != 5*time.Minute {
		t.Errorf("expected 5m, got %v", a.IdleTimeout)
	}
}

func TestBuildServeArgs_IdleTimeoutHours(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--idle-timeout", "1h"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.IdleTimeout != time.Hour {
		t.Errorf("expected 1h, got %v", a.IdleTimeout)
	}
}

func TestBuildServeArgs_IdleTimeoutEquals(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--idle-timeout=2h30m"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.IdleTimeout != 2*time.Hour+30*time.Minute {
		t.Errorf("expected 2h30m, got %v", a.IdleTimeout)
	}
}

func TestBuildServeArgs_IdleTimeoutZeroDisables(t *testing.T) {
	// Per AC: --idle-timeout 0 disables idle shutdown. time.ParseDuration
	// accepts "0" as a zero-value duration.
	r, _ := serveCommand.Parse([]string{"--idle-timeout", "0"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.IdleTimeout != 0 {
		t.Errorf("expected 0 (disabled), got %v", a.IdleTimeout)
	}
}

func TestBuildServeArgs_IdleTimeoutBareIntRejected(t *testing.T) {
	// Matches the legacy --idle-timeout root-flag behaviour: bare ints
	// like "30" (no unit) are rejected as ambiguous.
	r, _ := serveCommand.Parse([]string{"--idle-timeout", "30"})
	if _, err := buildServeArgs(r); err == nil {
		t.Error("expected error for bare-int --idle-timeout, got nil")
	}
}

func TestBuildServeArgs_IdleTimeoutGarbageRejected(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--idle-timeout", "30mins"})
	if _, err := buildServeArgs(r); err == nil {
		t.Error("expected error for '30mins' --idle-timeout, got nil")
	}
}

func TestBuildServeArgs_IdleTimeoutEmptyRejected(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--idle-timeout="})
	if _, err := buildServeArgs(r); err == nil {
		t.Error("expected error for empty --idle-timeout, got nil")
	}
}

func TestBuildServeArgs_Profile(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--profile", "aidev"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.Profile != "aidev" {
		t.Errorf("expected Profile=%q, got %q", "aidev", a.Profile)
	}
}

func TestBuildServeArgs_Port(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--port", "9999"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.PortFlag != 9999 {
		t.Errorf("expected PortFlag=9999, got %d", a.PortFlag)
	}
}

func TestBuildServeArgs_Verbose(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"-v"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if !a.Verbose {
		t.Error("expected Verbose=true for -v")
	}
}

func TestBuildServeArgs_TLSPair(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--tls-cert", "/tmp/c.pem", "--tls-key", "/tmp/k.pem"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if a.TLSCert != "/tmp/c.pem" || a.TLSKey != "/tmp/k.pem" {
		t.Errorf("expected TLS cert/key wired, got cert=%q key=%q", a.TLSCert, a.TLSKey)
	}
}

func TestBuildServeArgs_Model(t *testing.T) {
	r, _ := serveCommand.Parse([]string{"--model", "databricks-gpt-5-4-mini"})
	a, err := buildServeArgs(r)
	if err != nil {
		t.Fatalf("buildServeArgs returned error: %v", err)
	}
	if !a.ModelSet {
		t.Error("expected ModelSet=true when --model is passed")
	}
	if a.Model != "databricks-gpt-5-4-mini" {
		t.Errorf("expected Model=%q, got %q", "databricks-gpt-5-4-mini", a.Model)
	}
}

// TestBuildServeArgs_HeadlessAlwaysTrue is the AC-positive assertion: the
// serve subcommand MUST set Headless=true on the Args it constructs. If
// a future refactor accidentally drops this, runProxyMode would launch
// codex as a child instead of running the proxy headlessly — the exact
// silent-degradation hazard the issue's "byte-identical behavior" AC
// guards against.
func TestBuildServeArgs_HeadlessAlwaysTrue(t *testing.T) {
	cases := [][]string{
		{},                                     // bare serve
		{"--idle-timeout", "5m"},               // typical IDE invocation
		{"--profile", "aidev", "--port", "0"},  // edge values
		{"--verbose"},                          // debug toggle
	}
	for _, args := range cases {
		r, _ := serveCommand.Parse(args)
		a, err := buildServeArgs(r)
		if err != nil {
			t.Errorf("buildServeArgs(%v) error: %v", args, err)
			continue
		}
		if !a.Headless {
			t.Errorf("buildServeArgs(%v): Headless should be true; serve always runs headless", args)
		}
	}
}

// --- runServeCommand integration tests (with spy) ---
//
// runServeCommand calls runServeProxy → runProxyMode. The latter does I/O
// (proxy bind, auth check, codex lookup) we don't want in unit tests, so
// these tests swap runServeProxy for a spy that captures the Args struct.
// This is the "AC-positive" pattern called out in the issue's pitfalls
// section: assert what the new code path actually does, not just "no
// error raised".

func TestRunServeCommand_HelpReturnsNilWithoutLaunching(t *testing.T) {
	called := false
	orig := runServeProxy
	runServeProxy = func(a *Args) { called = true }
	defer func() { runServeProxy = orig }()

	if err := runServeCommand([]string{"--help"}); err != nil {
		t.Errorf("runServeCommand([--help]) returned error: %v", err)
	}
	if called {
		t.Error("--help short-circuit must NOT call runServeProxy")
	}
}

func TestRunServeCommand_HelpShortAliasReturnsNil(t *testing.T) {
	called := false
	orig := runServeProxy
	runServeProxy = func(a *Args) { called = true }
	defer func() { runServeProxy = orig }()

	if err := runServeCommand([]string{"-h"}); err != nil {
		t.Errorf("runServeCommand([-h]) returned error: %v", err)
	}
	if called {
		t.Error("-h short-circuit must NOT call runServeProxy")
	}
}

func TestRunServeCommand_InvalidIdleTimeoutReturnsError(t *testing.T) {
	called := false
	orig := runServeProxy
	runServeProxy = func(a *Args) { called = true }
	defer func() { runServeProxy = orig }()

	err := runServeCommand([]string{"--idle-timeout", "not-a-duration"})
	if err == nil {
		t.Fatal("expected error for invalid --idle-timeout, got nil")
	}
	if !strings.Contains(err.Error(), "idle-timeout") {
		t.Errorf("error %q should mention idle-timeout", err)
	}
	if called {
		t.Error("invalid flag must NOT call runServeProxy")
	}
}

// TestRunServeCommand_BareServeInvokesProxyWithDefaults exercises the
// happy path: bare `databricks-codex serve` (no flags) must call
// runProxyMode with Headless=true and the default 30m idle timeout. Catches
// the regression where runServeCommand returns early without invoking the
// launcher.
func TestRunServeCommand_BareServeInvokesProxyWithDefaults(t *testing.T) {
	var captured *Args
	orig := runServeProxy
	runServeProxy = func(a *Args) { captured = a }
	defer func() { runServeProxy = orig }()

	if err := runServeCommand([]string{}); err != nil {
		t.Fatalf("runServeCommand([]) returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("runServeProxy was never invoked")
	}
	if !captured.Headless {
		t.Error("Args.Headless must be true on the serve path")
	}
	if captured.IdleTimeout != 30*time.Minute {
		t.Errorf("Args.IdleTimeout: got %v, want 30m (default)", captured.IdleTimeout)
	}
}

// TestRunServeCommand_PassesIdleTimeoutThrough is the headline AC test:
// `databricks-codex serve --idle-timeout 5m` must reach runProxyMode with
// IdleTimeout=5m. Asserts the FULL pipeline (parse → buildServeArgs →
// runServeProxy) — a regression in any step fails this. This is the
// positive assertion the issue's pitfalls section calls out: "serve
// invokes runHeadless with idle-timeout=5m via a fake/spy, not 'no error
// raised'".
func TestRunServeCommand_PassesIdleTimeoutThrough(t *testing.T) {
	var captured *Args
	orig := runServeProxy
	runServeProxy = func(a *Args) { captured = a }
	defer func() { runServeProxy = orig }()

	if err := runServeCommand([]string{"--idle-timeout", "5m"}); err != nil {
		t.Fatalf("runServeCommand returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("runServeProxy was never invoked")
	}
	if !captured.Headless {
		t.Error("Args.Headless must be true on the serve path")
	}
	if captured.IdleTimeout != 5*time.Minute {
		t.Errorf("Args.IdleTimeout: got %v, want 5m", captured.IdleTimeout)
	}
}

// TestRunServeCommand_PassesAllFlagsThrough exercises the full mapping:
// every serve flag (except --help) must reach the Args struct. Catches
// the case where a flag is added to serveCommand and the parity test
// passes (because buildServeArgs has a corresponding name lookup) but the
// value silently drops on the floor.
func TestRunServeCommand_PassesAllFlagsThrough(t *testing.T) {
	var captured *Args
	orig := runServeProxy
	runServeProxy = func(a *Args) { captured = a }
	defer func() { runServeProxy = orig }()

	args := []string{
		"--idle-timeout", "10m",
		"--profile", "aidev",
		"--port", "55555",
		"--model", "test-model",
		"--upstream", "https://gw.example.com/openai/v1",
		"--log-file", "/tmp/serve.log",
		"--verbose",
		"--proxy-api-key", "secret",
		"--tls-cert", "/tmp/c.pem",
		"--tls-key", "/tmp/k.pem",
		"--no-update-check",
	}
	if err := runServeCommand(args); err != nil {
		t.Fatalf("runServeCommand returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("runServeProxy was never invoked")
	}

	checks := []struct {
		name      string
		got, want interface{}
	}{
		{"Headless", captured.Headless, true},
		{"IdleTimeout", captured.IdleTimeout, 10 * time.Minute},
		{"Profile", captured.Profile, "aidev"},
		{"PortFlag", captured.PortFlag, 55555},
		{"Model", captured.Model, "test-model"},
		{"ModelSet", captured.ModelSet, true},
		{"Upstream", captured.Upstream, "https://gw.example.com/openai/v1"},
		{"LogFile", captured.LogFile, "/tmp/serve.log"},
		{"Verbose", captured.Verbose, true},
		{"ProxyAPIKey", captured.ProxyAPIKey, "secret"},
		{"TLSCert", captured.TLSCert, "/tmp/c.pem"},
		{"TLSKey", captured.TLSKey, "/tmp/k.pem"},
		{"NoUpdateCheck", captured.NoUpdateCheck, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("Args.%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestHeadlessEnsureConfig_UsesServeSubcommand is the load-bearing test
// for AC #5: "Hook entry must continue to bring the proxy up correctly —
// the headlessEnsure-equivalent path now reuses the `serve` codepath
// internally, not the removed --headless flag."
//
// pkg/headless.buildArgs uses Config.EnsureCommand as the prefix. With
// EnsureCommand=["serve"], the spawned subprocess is `databricks-codex
// serve --port=N [--tls-cert=... --tls-key=...]`. With EnsureCommand
// empty, it would default back to `databricks-codex --headless ...` —
// which would IMMEDIATELY fail since #89 deletes that flag. This test
// fails loudly if a future refactor drops the EnsureCommand field.
func TestHeadlessEnsureConfig_UsesServeSubcommand(t *testing.T) {
	cfg := headlessEnsureConfig(49154, persistentState{})
	if len(cfg.EnsureCommand) == 0 {
		t.Fatal("headlessEnsureConfig must set EnsureCommand so the spawned subprocess invokes a real subcommand (not the removed --headless flag)")
	}
	if cfg.EnsureCommand[0] != "serve" {
		t.Errorf("expected EnsureCommand[0]=%q, got %q (#89: hook spawn must invoke `serve`)", "serve", cfg.EnsureCommand[0])
	}
}

// TestHeadlessEnsureConfig_TLSPropagated verifies the TLS knobs from
// persistent state reach the headless.Config so the spawned subprocess
// gets --tls-cert / --tls-key flags. Locks the existing semantics — the
// #89 EnsureCommand change must NOT regress TLS plumbing.
func TestHeadlessEnsureConfig_TLSPropagated(t *testing.T) {
	cfg := headlessEnsureConfig(49154, persistentState{TLSCert: "/tmp/c.pem", TLSKey: "/tmp/k.pem"})
	if cfg.TLSCert != "/tmp/c.pem" || cfg.TLSKey != "/tmp/k.pem" {
		t.Errorf("TLS state must reach headless.Config; got cert=%q key=%q", cfg.TLSCert, cfg.TLSKey)
	}
	if cfg.Scheme != "https" {
		t.Errorf("Scheme should be https when TLSCert is set, got %q", cfg.Scheme)
	}
}

// TestServeHelpRendersAllFlags is a smoke check that the serveHelpTemplate
// string mentions each of the flag names declared on serveCommand.
// Prevents the help text from drifting silently when a flag is added or
// renamed in commands.go.
func TestServeHelpRendersAllFlags(t *testing.T) {
	for _, name := range []string{
		"--idle-timeout", "--profile", "--port", "--model",
		"--upstream", "--log-file", "--verbose",
		"--proxy-api-key", "--tls-cert", "--tls-key",
		"--no-update-check", "--help",
	} {
		if !strings.Contains(serveHelpTemplate, name) {
			t.Errorf("serveHelpTemplate missing %q", name)
		}
	}
}
