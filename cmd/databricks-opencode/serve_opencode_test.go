package main

import (
	"testing"
	"time"
)

// --- #84 serve tree parity tests ---
//
// The command-tree registry in commands.go is the single source of truth for
// the serve subcommand surface. These tests pin the tree shape: the flags
// the dispatcher (runServeCommand) reads from r.Strings/r.Bools/r.Set are
// declared on the tree, and the recursive completion walk surfaces `serve`
// as a position-1 subcommand.

// TestServeCommandTreeParity walks the serve subcommand and asserts the
// flags the dispatcher routes to (--profile, --port, --upstream, --model,
// --proxy-api-key, --tls-cert, --tls-key, --log-file, --verbose,
// --no-update-check, --idle-timeout, --help) are declared on the tree. If
// the runner reads a flag that isn't declared, completion is silently
// missing and `cmd.Parse` will route it to Positional (which the
// dispatcher rejects), so this parity is load-bearing.
func TestServeCommandTreeParity(t *testing.T) {
	root := rootCommand.Subcommand("serve")
	if root == nil {
		t.Fatal("rootCommand should have a `serve` subcommand declared")
	}
	known := root.KnownFlags()
	want := []string{
		"--profile", "--port", "--upstream", "--model",
		"--proxy-api-key", "--tls-cert", "--tls-key",
		"--log-file", "--verbose", "--no-update-check",
		"--idle-timeout", "--help",
	}
	for _, k := range want {
		if !known[k] {
			t.Errorf("serve missing %q in its known-flag set", k)
		}
	}
	// Catch the reverse: any flag declared on the tree must be expected
	// by this assertion. A stray flag means either the tree gained one
	// the runner doesn't read OR this test went stale.
	for k := range known {
		found := false
		for _, w := range want {
			if k == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("serve has unexpected flag %q (update test or remove flag)", k)
		}
	}
}

// TestRootCompletionOffersServeSubcommand asserts `serve` is surfaced in the
// root command-tree's CompletionSubcommands output so `databricks-opencode
// <TAB>` offers it as a position-1 completion. Drives AC #84:
// "shell completion completes `serve <TAB>` and `serve --idle-timeout <TAB>`."
func TestRootCompletionOffersServeSubcommand(t *testing.T) {
	for _, sc := range knownSubcommands {
		if sc.Name != "serve" {
			continue
		}
		// Confirm --idle-timeout is among the completion-flag set so the
		// recursive shell-completion generator emits it for `serve <TAB>`.
		hasIdle := false
		for _, f := range sc.Flags {
			if f.Name == "idle-timeout" {
				hasIdle = true
				break
			}
		}
		if !hasIdle {
			t.Error("knownSubcommands `serve` should declare --idle-timeout for nested completion")
		}
		return
	}
	t.Error("knownSubcommands should offer `serve` as a position-1 subcommand")
}

// --- parseServeIdleTimeout grammar tests ---
//
// AC #84:
//   - `serve --idle-timeout 5m` honored.
//   - `serve --idle-timeout 5` accepts the bare-number-is-minutes shape.
//
// The bare-number grammar is the load-bearing divergence from the pre-#84
// root-flag parser (PR #76 rejected bare numbers under --idle-timeout). The
// table covers the AC plus the boundary cases — empty, zero, negative, and
// junk — so the runner's behaviour is pinned in both directions.

func TestParseServeIdleTimeout_Default(t *testing.T) {
	got, err := parseServeIdleTimeout("", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != defaultServeIdleTimeout {
		t.Errorf("default: got %v, want %v", got, defaultServeIdleTimeout)
	}
}

func TestParseServeIdleTimeout_PresentEmpty(t *testing.T) {
	// `serve --idle-timeout` (last token, no value) leaves r.Strings[""]
	// per cmd.Parse — match the historical tolerance: fall back to default.
	got, err := parseServeIdleTimeout("", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != defaultServeIdleTimeout {
		t.Errorf("present-empty: got %v, want %v", got, defaultServeIdleTimeout)
	}
}

func TestParseServeIdleTimeout_BareNumberIsMinutes(t *testing.T) {
	// AC #84: `serve --idle-timeout 5` ≡ `serve --idle-timeout 5m`.
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"5", 5 * time.Minute},
		{"30", 30 * time.Minute},
		{"1", 1 * time.Minute},
		{"0", 0},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got, err := parseServeIdleTimeout(c.raw, true)
			if err != nil {
				t.Fatalf("--idle-timeout=%s unexpected error: %v", c.raw, err)
			}
			if got != c.want {
				t.Errorf("--idle-timeout=%s: got %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

func TestParseServeIdleTimeout_DurationStrings(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"1h", 1 * time.Hour},
		{"90m", 90 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got, err := parseServeIdleTimeout(c.raw, true)
			if err != nil {
				t.Fatalf("--idle-timeout=%s unexpected error: %v", c.raw, err)
			}
			if got != c.want {
				t.Errorf("--idle-timeout=%s: got %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

func TestParseServeIdleTimeout_ZeroDisables(t *testing.T) {
	// `0` (bare or "0s") must produce a zero duration so the
	// lifecycle wrapper interprets it as "idle timeout disabled."
	for _, raw := range []string{"0", "0s", "0m"} {
		got, err := parseServeIdleTimeout(raw, true)
		if err != nil {
			t.Fatalf("--idle-timeout=%s unexpected error: %v", raw, err)
		}
		if got != 0 {
			t.Errorf("--idle-timeout=%s: got %v, want 0 (disabled)", raw, got)
		}
	}
}

func TestParseServeIdleTimeout_NegativeRejected(t *testing.T) {
	for _, raw := range []string{"-5", "-1h"} {
		_, err := parseServeIdleTimeout(raw, true)
		if err == nil {
			t.Errorf("--idle-timeout=%s should error (negative)", raw)
		}
	}
}

func TestParseServeIdleTimeout_JunkRejected(t *testing.T) {
	cases := []string{"5min", "abc", "5x"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := parseServeIdleTimeout(raw, true)
			if err == nil {
				t.Errorf("--idle-timeout=%s should error (not a duration)", raw)
			}
		})
	}
}

// TestServeCommandParse_SyntheticArgs exercises serveCommand.Parse end-to-end
// with a representative arg list to confirm the tree-driven parser
// recognises every flag the dispatcher reads. If a flag is missed, it falls
// into Positional (which runServeCommand explicitly rejects), so this test
// guards the dispatch path against silent flag-passthrough regressions.
func TestServeCommandParse_SyntheticArgs(t *testing.T) {
	args := []string{
		"--profile", "aidev",
		"--port", "8080",
		"--upstream", "https://gw.example.com",
		"--model", "custom-model",
		"--proxy-api-key", "secret-key",
		"--tls-cert", "/tmp/cert.pem",
		"--tls-key", "/tmp/key.pem",
		"--log-file", "/tmp/log.txt",
		"--verbose",
		"--no-update-check",
		"--idle-timeout", "5",
	}
	r, err := serveCommand.Parse(args)
	if err != nil {
		t.Fatalf("serveCommand.Parse unexpected error: %v", err)
	}
	if len(r.Positional) != 0 {
		t.Errorf("serveCommand.Parse should consume all flags; leftover Positional=%v", r.Positional)
	}
	want := map[string]string{
		"profile":       "aidev",
		"port":          "8080",
		"upstream":      "https://gw.example.com",
		"model":         "custom-model",
		"proxy-api-key": "secret-key",
		"tls-cert":      "/tmp/cert.pem",
		"tls-key":       "/tmp/key.pem",
		"log-file":      "/tmp/log.txt",
		"idle-timeout":  "5",
	}
	for k, v := range want {
		if r.Strings[k] != v {
			t.Errorf("Strings[%q] = %q, want %q", k, r.Strings[k], v)
		}
	}
	if !r.Bools["verbose"] {
		t.Error("Bools[verbose] should be true")
	}
	if !r.Bools["no-update-check"] {
		t.Error("Bools[no-update-check] should be true")
	}
}
