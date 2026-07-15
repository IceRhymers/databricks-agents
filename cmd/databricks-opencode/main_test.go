package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- parseArgs tests ---

func mustParse(t *testing.T, args []string) *Args {
	t.Helper()
	a, err := parseArgs(args)
	if err != nil {
		t.Fatalf("parseArgs(%v) unexpected error: %v", args, err)
	}
	return a
}

func TestParseArgs_HelpLong(t *testing.T) {
	a := mustParse(t, []string{"--help"})
	if !a.ShowHelp {
		t.Error("expected ShowHelp=true for --help")
	}
	if a.Verbose || a.Version || a.Model != "" || a.Upstream != "" || a.LogFile != "" || a.Profile != "" || len(a.OpencodeArgs) != 0 {
		t.Error("unexpected non-default values alongside --help")
	}
}

func TestParseArgs_HelpShort(t *testing.T) {
	a := mustParse(t, []string{"-h"})
	if !a.ShowHelp {
		t.Error("expected ShowHelp=true for -h")
	}
}

// TestParseArgs_SeparatorForwardsHelp verifies that "--" terminates wrapper
// flag parsing, so "-- --help" must NOT trigger the wrapper's help and must
// forward "--help" to opencode verbatim. This is the documented passthrough
// escape hatch users invoke to reach opencode's own help (#90).
func TestParseArgs_SeparatorForwardsHelp(t *testing.T) {
	a := mustParse(t, []string{"--", "--help"})
	if a.ShowHelp {
		t.Error("expected ShowHelp=false when --help appears after --")
	}
	if len(a.OpencodeArgs) != 1 || a.OpencodeArgs[0] != "--help" {
		t.Errorf("expected OpencodeArgs=[--help], got %v", a.OpencodeArgs)
	}
}

// TestParseArgs_PrintEnvRemoved verifies that --print-env is no longer a
// known flag (replaced by `config show` in #82). Behavior: parseArgs forwards
// unknown flags to opencode, so --print-env should appear in OpencodeArgs.
func TestParseArgs_PrintEnvRemoved(t *testing.T) {
	a := mustParse(t, []string{"--print-env"})
	if len(a.OpencodeArgs) != 1 || a.OpencodeArgs[0] != "--print-env" {
		t.Errorf("--print-env should now forward to opencode as unknown, got OpencodeArgs=%v", a.OpencodeArgs)
	}
}

func TestParseArgs_Version(t *testing.T) {
	a := mustParse(t, []string{"--version"})
	if !a.Version {
		t.Error("expected Version=true for --version")
	}
}

func TestParseArgs_Verbose(t *testing.T) {
	a := mustParse(t, []string{"--verbose"})
	if !a.Verbose {
		t.Error("expected Verbose=true for --verbose")
	}
}

func TestParseArgs_VerboseShort(t *testing.T) {
	a := mustParse(t, []string{"-v"})
	if !a.Verbose {
		t.Error("expected Verbose=true for -v")
	}
}

func TestParseArgs_LogFile(t *testing.T) {
	a := mustParse(t, []string{"--log-file", "/tmp/test.log"})
	if a.LogFile != "/tmp/test.log" {
		t.Errorf("expected LogFile=%q, got %q", "/tmp/test.log", a.LogFile)
	}
}

func TestParseArgs_LogFileEquals(t *testing.T) {
	a := mustParse(t, []string{"--log-file=/tmp/test.log"})
	if a.LogFile != "/tmp/test.log" {
		t.Errorf("expected LogFile=%q, got %q", "/tmp/test.log", a.LogFile)
	}
}

func TestParseArgs_Upstream(t *testing.T) {
	a := mustParse(t, []string{"--upstream", "https://gw.example.com/openai/v1"})
	if a.Upstream != "https://gw.example.com/openai/v1" {
		t.Errorf("expected Upstream=%q, got %q", "https://gw.example.com/openai/v1", a.Upstream)
	}
}

func TestParseArgs_UpstreamEquals(t *testing.T) {
	a := mustParse(t, []string{"--upstream=https://gw.example.com/openai/v1"})
	if a.Upstream != "https://gw.example.com/openai/v1" {
		t.Errorf("expected Upstream=%q, got %q", "https://gw.example.com/openai/v1", a.Upstream)
	}
}

func TestParseArgs_Model(t *testing.T) {
	a := mustParse(t, []string{"--model", "gpt-4o"})
	if a.Model != "gpt-4o" {
		t.Errorf("expected Model=%q, got %q", "gpt-4o", a.Model)
	}
}

func TestParseArgs_ModelEquals(t *testing.T) {
	a := mustParse(t, []string{"--model=gpt-4o"})
	if a.Model != "gpt-4o" {
		t.Errorf("expected Model=%q, got %q", "gpt-4o", a.Model)
	}
}

func TestParseArgs_UnknownFlagPassthrough(t *testing.T) {
	a := mustParse(t, []string{"--unknown"})
	if len(a.OpencodeArgs) != 1 || a.OpencodeArgs[0] != "--unknown" {
		t.Errorf("expected OpencodeArgs=[\"--unknown\"], got %v", a.OpencodeArgs)
	}
}

func TestParseArgs_EmptyArgs(t *testing.T) {
	a := mustParse(t, []string{})
	if a.Verbose || a.Version || a.ShowHelp {
		t.Error("expected all bool flags false for empty args")
	}
	if a.Model != "" {
		t.Errorf("expected empty Model, got %q", a.Model)
	}
	if a.Upstream != "" {
		t.Errorf("expected empty Upstream, got %q", a.Upstream)
	}
	if a.LogFile != "" {
		t.Errorf("expected empty LogFile, got %q", a.LogFile)
	}
	if a.Profile != "" {
		t.Errorf("expected empty Profile, got %q", a.Profile)
	}
	if len(a.OpencodeArgs) != 0 {
		t.Errorf("expected no OpencodeArgs, got %v", a.OpencodeArgs)
	}
}

func TestParseArgs_Mixed(t *testing.T) {
	a := mustParse(t, []string{"--verbose", "--upstream", "https://gw.example.com", "--help"})
	if !a.ShowHelp {
		t.Error("expected ShowHelp=true")
	}
	if !a.Verbose {
		t.Error("expected Verbose=true")
	}
	if a.Upstream != "https://gw.example.com" {
		t.Errorf("expected Upstream=%q, got %q", "https://gw.example.com", a.Upstream)
	}
}

func TestParseArgs_Separator(t *testing.T) {
	a := mustParse(t, []string{"--verbose", "--", "--unknown", "arg1"})
	if !a.Verbose {
		t.Error("expected Verbose=true before separator")
	}
	if len(a.OpencodeArgs) != 2 || a.OpencodeArgs[0] != "--unknown" || a.OpencodeArgs[1] != "arg1" {
		t.Errorf("expected OpencodeArgs=[\"--unknown\", \"arg1\"], got %v", a.OpencodeArgs)
	}
}

func TestParseArgs_PassthroughArgs(t *testing.T) {
	a := mustParse(t, []string{"prompt text", "--some-flag", "value"})
	if len(a.OpencodeArgs) != 3 {
		t.Errorf("expected 3 OpencodeArgs, got %d: %v", len(a.OpencodeArgs), a.OpencodeArgs)
	}
}

// Table-driven comprehensive test for parseArgs.
func TestParseArgs_Table(t *testing.T) {
	type result struct {
		verbose     bool
		version     bool
		showHelp    bool
		model       string
		upstream    string
		logFile     string
		profile     string
		opencodeLen int
	}

	tests := []struct {
		name string
		args []string
		want result
	}{
		{
			name: "--help sets showHelp",
			args: []string{"--help"},
			want: result{showHelp: true},
		},
		{
			name: "-h sets showHelp",
			args: []string{"-h"},
			want: result{showHelp: true},
		},
		{
			name: "--print-env removed; forwarded as unknown",
			args: []string{"--print-env"},
			want: result{opencodeLen: 1},
		},
		{
			name: "--version sets version",
			args: []string{"--version"},
			want: result{version: true},
		},
		{
			name: "--verbose sets verbose",
			args: []string{"--verbose"},
			want: result{verbose: true},
		},
		{
			name: "-v sets verbose",
			args: []string{"-v"},
			want: result{verbose: true},
		},
		{
			name: "--log-file sets logFile",
			args: []string{"--log-file", "/tmp/test.log"},
			want: result{logFile: "/tmp/test.log"},
		},
		{
			name: "--log-file=value sets logFile",
			args: []string{"--log-file=/tmp/test.log"},
			want: result{logFile: "/tmp/test.log"},
		},
		{
			name: "--upstream sets upstream",
			args: []string{"--upstream", "https://gw.example.com"},
			want: result{upstream: "https://gw.example.com"},
		},
		{
			name: "--model sets model",
			args: []string{"--model", "custom-model"},
			want: result{model: "custom-model"},
		},
		{
			name: "unknown flag passes through",
			args: []string{"--unknown"},
			want: result{opencodeLen: 1},
		},
		{
			name: "empty args all defaults",
			args: []string{},
			want: result{},
		},
		{
			name: "mixed flags: verbose, upstream, help",
			args: []string{"--verbose", "--upstream", "https://gw.example.com", "--help"},
			want: result{showHelp: true, verbose: true, upstream: "https://gw.example.com"},
		},
		{
			name: "--profile sets profile",
			args: []string{"--profile", "aidev"},
			want: result{profile: "aidev"},
		},
		{
			name: "--profile=value sets profile",
			args: []string{"--profile=aidev"},
			want: result{profile: "aidev"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := mustParse(t, tc.args)

			if a.Verbose != tc.want.verbose {
				t.Errorf("verbose: got %v, want %v", a.Verbose, tc.want.verbose)
			}
			if a.Version != tc.want.version {
				t.Errorf("version: got %v, want %v", a.Version, tc.want.version)
			}
			if a.ShowHelp != tc.want.showHelp {
				t.Errorf("showHelp: got %v, want %v", a.ShowHelp, tc.want.showHelp)
			}
			if a.Model != tc.want.model {
				t.Errorf("model: got %q, want %q", a.Model, tc.want.model)
			}
			if a.Upstream != tc.want.upstream {
				t.Errorf("upstream: got %q, want %q", a.Upstream, tc.want.upstream)
			}
			if a.LogFile != tc.want.logFile {
				t.Errorf("logFile: got %q, want %q", a.LogFile, tc.want.logFile)
			}
			if a.Profile != tc.want.profile {
				t.Errorf("profile: got %q, want %q", a.Profile, tc.want.profile)
			}
			if len(a.OpencodeArgs) != tc.want.opencodeLen {
				t.Errorf("opencodeArgs length: got %d, want %d (args: %v)", len(a.OpencodeArgs), tc.want.opencodeLen, a.OpencodeArgs)
			}
		})
	}
}

// --- default log discard test ---

func TestDefaultLogDiscard(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	var buf bytes.Buffer
	log.SetOutput(io.Discard)
	log.Print("this should be discarded")

	log.SetOutput(&buf)
	log.Print("this should appear")

	if !strings.Contains(buf.String(), "this should appear") {
		t.Error("expected log output after switching from Discard")
	}
}

// --- handlePrintEnv tests ---

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestHandlePrintEnv_DapiTokenRedacted(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "dapi-abc123secret", "DEFAULT", "databricks-claude-sonnet-4-6")
	})
	if !strings.Contains(out, "**** (redacted)") {
		t.Errorf("expected redaction sentinel '**** (redacted)', got:\n%s", out)
	}
	if strings.Contains(out, "dapi-abc123secret") {
		t.Errorf("raw dapi token should not appear in output, got:\n%s", out)
	}
}

// TestHandlePrintEnv_AllTokenShapesRedacted exercises three token shapes —
// JWT-style, dapi-prefixed legacy PAT, and dapi-prefixed without hyphen — and
// asserts that none of the literal token bytes leak into stdout. After the
// broadened redaction (#73), every shape resolves to the same fixed sentinel.
func TestHandlePrintEnv_AllTokenShapesRedacted(t *testing.T) {
	tokens := []string{
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature_part_xyz",
		"dapiabc123nohyphensecret",
		"dapi-abc123legacysecret",
	}
	for _, tok := range tokens {
		out := captureStdout(func() {
			handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", tok, "DEFAULT", "databricks-claude-sonnet-4-6")
		})
		if strings.Contains(out, tok) {
			t.Errorf("raw token %q leaked into output:\n%s", tok, out)
		}
		if !strings.Contains(out, "**** (redacted)") {
			t.Errorf("expected redaction sentinel for token %q, got:\n%s", tok, out)
		}
	}
}

func TestHandlePrintEnv_NonDapiTokenRedacted(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "eyJhbGciOiJSUzI1NiJ9", "DEFAULT", "databricks-claude-sonnet-4-6")
	})
	if !strings.Contains(out, "**** (redacted)") {
		t.Errorf("expected non-dapi token to appear as '**** (redacted)', got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsProfile(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "aidev", "databricks-claude-sonnet-4-6")
	})
	if !strings.Contains(out, "aidev") {
		t.Errorf("expected output to contain profile 'aidev', got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsDatabricksHost(t *testing.T) {
	host := "https://dbc-abc123.cloud.databricks.com"
	out := captureStdout(func() {
		handlePrintEnv(host, "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-claude-sonnet-4-6")
	})
	if !strings.Contains(out, host) {
		t.Errorf("expected output to contain DATABRICKS_HOST %q, got:\n%s", host, out)
	}
}

func TestHandlePrintEnv_ContainsOpenAIBaseURL(t *testing.T) {
	baseURL := "https://gw.example.com/openai/v1"
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", baseURL, "tok", "DEFAULT", "databricks-claude-sonnet-4-6")
	})
	if !strings.Contains(out, baseURL) {
		t.Errorf("expected output to contain OPENAI_BASE_URL %q, got:\n%s", baseURL, out)
	}
}

func TestHandlePrintEnv_ContainsModel(t *testing.T) {
	model := "databricks-claude-sonnet-4-6"
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "DEFAULT", model)
	})
	if !strings.Contains(out, model) {
		t.Errorf("expected output to contain Model %q, got:\n%s", model, out)
	}
}

// --- handleHelp tests ---

func TestHandleHelp_ContainsDatabricksOpencode(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, "databricks-opencode") {
		t.Errorf("expected help output to contain 'databricks-opencode', got:\n%s", out)
	}
}

// TestHandleHelp_PrintEnvRemoved verifies that --print-env no longer
// appears in the help body (#82 replaced it with `config show`).
func TestHandleHelp_PrintEnvRemoved(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if strings.Contains(out, "--print-env") {
		t.Errorf("help output should not mention --print-env (replaced by 'config show'), got:\n%s", out)
	}
}

// TestHandleHelp_ContainsConfigSubcommand verifies that the new `config`
// subcommand surfaces in the help body so users can discover `config show`.
func TestHandleHelp_ContainsConfigSubcommand(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, "config show") {
		t.Errorf("expected help output to mention 'config show', got:\n%s", out)
	}
}

// TestHandleHelp_OmitsAppendedOpencodeHelp verifies that the wrapper no
// longer execs `opencode --help` and appends its output (#90). The legacy
// "OpenCode CLI Options:" separator label and the horizontal rule above it
// must be absent so users see only wrapper help — opencode's own help is
// reachable via the `--` passthrough escape hatch.
func TestHandleHelp_OmitsAppendedOpencodeHelp(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	for _, ghost := range []string{"OpenCode CLI Options:", "────────────────────────"} {
		if strings.Contains(out, ghost) {
			t.Errorf("help output should not contain %q (appended opencode help removed in #90), got:\n%s", ghost, out)
		}
	}
}

// TestHandleHelp_DocumentsPassthrough verifies that the help body documents
// the `--` passthrough escape hatch as the discoverable replacement for the
// removed appended `opencode --help` block.
func TestHandleHelp_DocumentsPassthrough(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	for _, want := range []string{"Passthrough to opencode:", "databricks-opencode -- --help"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected help output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestParseArgs_Profile(t *testing.T) {
	a := mustParse(t, []string{"--profile", "aidev"})
	if a.Profile != "aidev" {
		t.Errorf("expected Profile=%q, got %q", "aidev", a.Profile)
	}
}

func TestParseArgs_ProfileEquals(t *testing.T) {
	a := mustParse(t, []string{"--profile=production"})
	if a.Profile != "production" {
		t.Errorf("expected Profile=%q, got %q", "production", a.Profile)
	}
}

// --- Profile resolution tests ---
// These mirror the resolution chain in main(): --profile flag → saved state → "DEFAULT".
// The env var DATABRICKS_CONFIG_PROFILE is intentionally skipped.

func TestProfileResolution_StateFileWinsOverEnvVar(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Save a profile to state file.
	saveState(persistentState{Profile: "state-profile"})

	// Set the env var that used to take priority.
	t.Setenv("DATABRICKS_CONFIG_PROFILE", "env-profile")

	// Simulate resolution chain from main(): --profile flag → saved state → "DEFAULT".
	profile := "" // no --profile flag
	if profile == "" {
		if saved := loadState(); saved.Profile != "" {
			profile = saved.Profile
		}
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	if profile != "state-profile" {
		t.Errorf("profile = %q, want %q (state file should win over env var)", profile, "state-profile")
	}
}

func TestProfileResolution_FlagWinsOverStateFile(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Save a profile to state file.
	saveState(persistentState{Profile: "state-profile"})

	// Simulate resolution chain with explicit --profile flag.
	profile := "flag-profile" // --profile flag set
	if profile == "" {
		if saved := loadState(); saved.Profile != "" {
			profile = saved.Profile
		}
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	if profile != "flag-profile" {
		t.Errorf("profile = %q, want %q (flag should win over state file)", profile, "flag-profile")
	}
}

func TestProfileResolution_DefaultWhenNoStateFile(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "nonexistent.json") }
	defer func() { statePath = orig }()

	// Simulate resolution chain with no flag, no state file.
	profile := ""
	if profile == "" {
		if saved := loadState(); saved.Profile != "" {
			profile = saved.Profile
		}
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	if profile != "DEFAULT" {
		t.Errorf("profile = %q, want %q (should fall back to DEFAULT)", profile, "DEFAULT")
	}
}

func TestParseArgs_Port(t *testing.T) {
	a := mustParse(t, []string{"--port", "8080"})
	if a.Port != 8080 {
		t.Errorf("expected Port=8080, got %d", a.Port)
	}
}

func TestParseArgs_PortEquals(t *testing.T) {
	a := mustParse(t, []string{"--port=9000"})
	if a.Port != 9000 {
		t.Errorf("expected Port=9000, got %d", a.Port)
	}
}

// TestParseArgs_HeadlessRemoved verifies the legacy --headless root flag is
// no longer recognised by parseArgs (#84 moved it under the `serve`
// subcommand). Behaviour: parseArgs forwards unknown flags to opencode, so
// --headless should appear in OpencodeArgs and Args.Headless stays false.
func TestParseArgs_HeadlessRemoved(t *testing.T) {
	a := mustParse(t, []string{"--headless"})
	if a.Headless {
		t.Error("Args.Headless must remain false; the flag is no longer parsed at the root")
	}
	if len(a.OpencodeArgs) != 1 || a.OpencodeArgs[0] != "--headless" {
		t.Errorf("--headless should now forward to opencode as unknown, got OpencodeArgs=%v", a.OpencodeArgs)
	}
}

// TestParseArgs_IdleTimeoutRemoved verifies --idle-timeout is no longer
// recognised at the root (#84 moved it under `serve`). It now forwards
// transparently to opencode like any other unknown flag.
func TestParseArgs_IdleTimeoutRemoved(t *testing.T) {
	a := mustParse(t, []string{"--idle-timeout", "30m"})
	if a.IdleTimeout != 0 {
		t.Errorf("Args.IdleTimeout must remain 0; the flag is no longer parsed at the root, got %v", a.IdleTimeout)
	}
	// Both tokens should pass through to opencode unchanged.
	if len(a.OpencodeArgs) != 2 || a.OpencodeArgs[0] != "--idle-timeout" || a.OpencodeArgs[1] != "30m" {
		t.Errorf("--idle-timeout 30m should forward to opencode, got OpencodeArgs=%v", a.OpencodeArgs)
	}
}

func TestParseArgs_NoUpdateCheck(t *testing.T) {
	a := mustParse(t, []string{"--no-update-check"})
	if !a.NoUpdateCheck {
		t.Error("expected NoUpdateCheck=true for --no-update-check")
	}
}

func TestParseArgs_HeadlessDefault(t *testing.T) {
	a := mustParse(t, []string{})
	if a.Headless {
		t.Error("expected Headless=false for empty args")
	}
}

func TestHandleHelp_AllFlagsPresent(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	// Note: --print-env removed in #82 (replaced by `config show`); the
	// hooks lifecycle flags --install-hooks, --uninstall-hooks, and
	// --headless-ensure removed in #83 (replaced by the `hooks` subcommand
	// — see TestHandleHelp_HookFlagsRemoved). --headless and --idle-timeout
	// removed in #84 (replaced by the `serve` subcommand — see
	// TestHandleHelp_HeadlessFlagsRemoved). None of those sets are listed
	// here.
	flags := []string{"--profile", "--upstream", "--verbose", "-v", "--log-file", "--model", "--version", "--help", "--port", "--no-update-check"}
	for _, flag := range flags {
		if !strings.Contains(out, flag) {
			t.Errorf("expected help output to contain flag %q, got:\n%s", flag, out)
		}
	}
}

// TestHandleHelp_HeadlessFlagsRemoved verifies the legacy --headless and
// --idle-timeout root flags no longer appear in the help body (#84
// replaced them with the `serve` subcommand). The serve-subcommand line
// itself should mention `--idle-timeout` because the help template lists
// the new home for the flag, so we only assert the bare-flag forms are
// absent — the `serve --idle-timeout` callout in the body is fine.
func TestHandleHelp_HeadlessFlagsRemoved(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	// "--headless" must not appear at all (the new entrypoint is `serve`).
	if strings.Contains(out, "--headless") {
		t.Errorf("help output should not mention --headless (replaced by `serve`), got:\n%s", out)
	}
	// "--idle-timeout" appears only inside the `serve [--idle-timeout]`
	// line of the subcommands table; assert it is NOT listed in the
	// flags table by checking it doesn't appear on a leading-whitespace
	// flag line of its own (e.g. "  --idle-timeout duration").
	for _, ghost := range []string{"  --idle-timeout duration", "  --idle-timeout string"} {
		if strings.Contains(out, ghost) {
			t.Errorf("help output should not list --idle-timeout as a root flag, got:\n%s", out)
		}
	}
}

// TestHandleHelp_ContainsServeSubcommand verifies the new `serve`
// subcommand surfaces in the help body so users can discover it as the
// replacement for `--headless`.
func TestHandleHelp_ContainsServeSubcommand(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, "serve") {
		t.Errorf("expected help output to mention `serve` subcommand, got:\n%s", out)
	}
}

// TestHandleHelp_HookFlagsRemoved verifies that --install-hooks /
// --uninstall-hooks / --headless-ensure no longer appear in the help body
// (#83 replaced them with the `hooks` subcommand).
func TestHandleHelp_HookFlagsRemoved(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	for _, removed := range []string{"--install-hooks", "--uninstall-hooks", "--headless-ensure"} {
		if strings.Contains(out, removed) {
			t.Errorf("help output should not mention %q (replaced by `hooks` subcommand), got:\n%s", removed, out)
		}
	}
}

// TestHandleHelp_ContainsHooksSubcommand verifies that the new `hooks`
// subcommand surfaces in the help body so users can discover it.
func TestHandleHelp_ContainsHooksSubcommand(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	for _, want := range []string{"hooks install", "hooks uninstall", "hooks session-start"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected help output to mention %q, got:\n%s", want, out)
		}
	}
}

func TestHandleHelp_ContainsVersion(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, fmt.Sprintf("databricks-opencode v%s", Version)) {
		t.Errorf("expected help output to contain version string, got:\n%s", out)
	}
}

// --- Anti-drift tests: flagDefs ↔ knownFlags ---

// TestCompletionFlagsCoverAllKnownFlags ensures every key in knownFlags has a
// corresponding entry in flagDefs. If a flag is added to knownFlags without
// updating flagDefs, completions will be silently missing.
func TestCompletionFlagsCoverAllKnownFlags(t *testing.T) {
	for flag := range knownFlags {
		name := strings.TrimPrefix(flag, "--")
		found := false
		for _, def := range flagDefs {
			if def.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("knownFlags contains %q but flagDefs has no matching entry — add it to completion_flags.go", flag)
		}
	}
}

// TestKnownFlagsCoverAllFlagDefs ensures every entry in flagDefs is reflected
// in knownFlags. If a flag is added to flagDefs without updating knownFlags,
// it will be forwarded to opencode instead of being handled by the wrapper.
func TestKnownFlagsCoverAllFlagDefs(t *testing.T) {
	for _, def := range flagDefs {
		key := "--" + def.Name
		if !knownFlags[key] {
			t.Errorf("flagDefs contains %q but knownFlags is missing it — check the knownFlags initializer in completion_flags.go", key)
		}
	}
}

// --idle-timeout strict-parsing tests (issue #72, PR #76) lived here while the
// flag was a root flag. #84 lifted --idle-timeout under the `serve`
// subcommand and dropped the root parser's case for it; the bare-number-is-
// minutes grammar plus the "valid duration" cases are exercised against the
// new parseServeIdleTimeout helper in serve_cmd_test.go.

// --- Tree ↔ parser parity tests (#82) ---
//
// The command-tree registry in commands.go is the single source of truth for
// the root flag set. flagDefs and knownFlags derive from it. parseArgs
// individually handles each flag in a switch. These two tests pin the
// bidirectional invariant: every flag declared in the tree is recognised by
// parseArgs (no silent drops), and every flag parseArgs handles is declared
// in the tree (no parse-only ghost flags).
//
// Bidirectional verification was performed during #82: temporarily deleting a
// flag from rootCommand causes TestRootTreeFlagsAreParseRecognised to fail
// loudly, confirming the test catches drift in either direction.

// TestRootTreeFlagsAreParseRecognised exercises every flag declared in
// rootCommand (Persistent + Flags) and verifies parseArgs accepts it without
// erroring. A flag added to the tree but missing a switch case in parseArgs
// will hit the default arm and return the "internal: …" error this test
// asserts against.
func TestRootTreeFlagsAreParseRecognised(t *testing.T) {
	for _, f := range rootCommand.AllFlags() {
		t.Run(f.Name, func(t *testing.T) {
			args := []string{"--" + f.Name}
			if f.TakesArg {
				// Provide a placeholder value; for --idle-timeout it must
				// parse as a duration so the post-switch validator passes.
				switch f.Name {
				case "idle-timeout":
					args = append(args, "30m")
				case "port":
					args = append(args, "12345")
				default:
					args = append(args, "x")
				}
			}
			if _, err := parseArgs(args); err != nil {
				t.Errorf("parseArgs(%v) errored: %v — flag declared in rootCommand but unhandled in parseArgs", args, err)
			}
		})
	}
}

// TestParseArgsCasesAreDeclaredInRootTree is the reverse parity: every flag
// in knownFlags must be declared on rootCommand. Since knownFlags is now
// derived from rootCommand.KnownFlags(), this test is structurally
// equivalent to a sanity check on the derivation, but it documents the
// contract explicitly so a future refactor that hand-rolls knownFlags would
// fail loudly.
func TestParseArgsCasesAreDeclaredInRootTree(t *testing.T) {
	declared := map[string]bool{}
	for _, f := range rootCommand.AllFlags() {
		declared["--"+f.Name] = true
	}
	for k := range knownFlags {
		if !declared[k] {
			t.Errorf("knownFlags has %q but rootCommand has no matching FlagDef — declare it in commands.go", k)
		}
	}
}

// TestConfigShowMatchesLegacyPrintEnv verifies that the new config-show
// dispatch produces the same output shape as the removed --print-env root
// flag. We exercise handlePrintEnv directly with the same inputs both code
// paths feed it; runConfigShow itself requires `databricks` on PATH and
// valid auth, which CI may not have. This test pins the shared output
// contract so byte-equivalence smoke tests at the binary level (run during
// PR verification) have a unit-level companion.
func TestConfigShowMatchesLegacyPrintEnv(t *testing.T) {
	host := "https://dbc.example.com"
	gateway := "https://dbc.example.com/ai-gateway/anthropic"
	token := "dapi-secret"
	profile := "DEFAULT"
	model := "databricks-claude-opus-4-7"

	out := captureStdout(func() {
		handlePrintEnv(host, gateway, token, profile, model)
	})

	// Required keys present (the legacy --print-env contract).
	for _, want := range []string{"Profile:", "Model:", "DATABRICKS_HOST:", "ANTHROPIC_BASE_URL:", "Auth Token:", "OpenCode binary:", host, gateway, profile, model} {
		if !strings.Contains(out, want) {
			t.Errorf("config show output missing %q (legacy --print-env contract):\n%s", want, out)
		}
	}
	// Token must be redacted regardless of input shape.
	if strings.Contains(out, token) {
		t.Errorf("token %q leaked into config show output:\n%s", token, out)
	}
}

// TestConfigShowSubcommandKnownFlags verifies the `show` subcommand's tree
// declares the expected flags so its own Parse call (in runConfigShow)
// recognises --profile / --port / --help.
func TestConfigShowSubcommandKnownFlags(t *testing.T) {
	show := configCommand.Subcommand("show")
	if show == nil {
		t.Fatal("config command tree missing 'show' subcommand")
	}
	known := show.KnownFlags()
	for _, want := range []string{"--profile", "--port", "--help"} {
		if !known[want] {
			t.Errorf("config show missing %q in its known-flag set", want)
		}
	}
}
