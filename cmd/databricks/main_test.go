package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// captureRun runs run(args) with os.Stdout/os.Stderr redirected to buffers and
// returns the exit code plus captured streams.
func captureRun(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	code = run(args)

	wOut.Close()
	wErr.Close()
	var bo, be bytes.Buffer
	bo.ReadFrom(rOut)
	be.ReadFrom(rErr)
	return code, bo.String(), be.String()
}

func TestRun_BareIsUsageError(t *testing.T) {
	code, _, stderr := captureRun(t, nil)
	if code != 2 {
		t.Fatalf("bare databricks: exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("bare databricks: stderr missing usage, got %q", stderr)
	}
}

func TestRun_Help(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		code, stdout, _ := captureRun(t, []string{arg})
		if code != 0 {
			t.Errorf("%s: exit = %d, want 0", arg, code)
		}
		if !strings.Contains(stdout, "databricks <agent>") {
			t.Errorf("%s: stdout missing usage, got %q", arg, stdout)
		}
	}
}

func TestRun_Version(t *testing.T) {
	code, stdout, _ := captureRun(t, []string{"--version"})
	if code != 0 {
		t.Fatalf("--version: exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "databricks ") {
		t.Errorf("--version: stdout = %q, want it to contain the version", stdout)
	}
}

func TestRun_List(t *testing.T) {
	code, stdout, _ := captureRun(t, []string{"list"})
	if code != 0 {
		t.Fatalf("list: exit = %d, want 0", code)
	}
	for _, a := range agents {
		if !strings.Contains(stdout, a.Name) {
			t.Errorf("list: stdout missing agent %q; got %q", a.Name, stdout)
		}
		if !strings.Contains(stdout, a.Summary) {
			t.Errorf("list: stdout missing summary for %q; got %q", a.Name, stdout)
		}
	}
}

func TestRun_UnknownAgent(t *testing.T) {
	code, _, stderr := captureRun(t, []string{"bogus"})
	if code != 2 {
		t.Fatalf("unknown agent: exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown agent") || !strings.Contains(stderr, "claude codex opencode") {
		t.Errorf("unknown agent: stderr = %q, want it to name the valid agents", stderr)
	}
}

// TestRun_ReservedWordsNotDispatched ensures `list` and `completion` are
// handled as reserved subcommands, never looked up as agent names.
func TestRun_ReservedWordsNotDispatched(t *testing.T) {
	if _, ok := lookup("list"); ok {
		t.Fatal("lookup(\"list\") should not resolve to an agent")
	}
	if _, ok := lookup("completion"); ok {
		t.Fatal("lookup(\"completion\") should not resolve to an agent")
	}
	// `list` must succeed as a subcommand, not error as an unknown agent.
	if code, _, _ := captureRun(t, []string{"list"}); code != 0 {
		t.Errorf("list dispatched as agent: exit = %d, want 0", code)
	}
	// `completion` with no shell is a usage error (exit 1), NOT unknown-agent (2).
	if code, _, _ := captureRun(t, []string{"completion"}); code != 1 {
		t.Errorf("completion with no shell: exit = %d, want 1", code)
	}
}

func TestLookup(t *testing.T) {
	if a, ok := lookup("claude"); !ok || a.Binary != "databricks-claude" {
		t.Errorf("lookup(claude) = %+v, %v", a, ok)
	}
	if _, ok := lookup("nope"); ok {
		t.Error("lookup(nope) should be false")
	}
}

func TestPrintList(t *testing.T) {
	var buf bytes.Buffer
	printList(&buf)
	got := buf.String()
	if !strings.Contains(got, "claude") || !strings.Contains(got, "opencode") {
		t.Errorf("printList output missing agents: %q", got)
	}
}
