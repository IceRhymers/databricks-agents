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

// mustParseArgs is a test helper that calls parseArgs and fails the test on error.
func mustParseArgs(t *testing.T, args []string) *Args {
	t.Helper()
	a, err := parseArgs(args)
	if err != nil {
		t.Fatalf("parseArgs(%v) returned unexpected error: %v", args, err)
	}
	return a
}

// equalStringSlice reports whether a and b have the same length and same
// elements in order. nil and empty slices are treated equal.
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- parseArgs tests ---

func TestParseArgs_HelpLong(t *testing.T) {
	a := mustParseArgs(t, []string{"--help"})
	if !a.ShowHelp {
		t.Error("expected ShowHelp=true for --help")
	}
	if a.Verbose || a.Version || a.Upstream != "" || a.LogFile != "" || a.Profile != "" || len(a.CodexArgs) != 0 {
		t.Error("unexpected non-default values alongside --help")
	}
}

func TestParseArgs_HelpShort(t *testing.T) {
	a := mustParseArgs(t, []string{"-h"})
	if !a.ShowHelp {
		t.Error("expected ShowHelp=true for -h")
	}
}

func TestParseArgs_Version(t *testing.T) {
	a := mustParseArgs(t, []string{"--version"})
	if !a.Version {
		t.Error("expected Version=true for --version")
	}
}

func TestParseArgs_Verbose(t *testing.T) {
	a := mustParseArgs(t, []string{"--verbose"})
	if !a.Verbose {
		t.Error("expected Verbose=true for --verbose")
	}
}

func TestParseArgs_VerboseShort(t *testing.T) {
	a := mustParseArgs(t, []string{"-v"})
	if !a.Verbose {
		t.Error("expected Verbose=true for -v")
	}
}

func TestParseArgs_LogFile(t *testing.T) {
	a := mustParseArgs(t, []string{"--log-file", "/tmp/test.log"})
	if a.LogFile != "/tmp/test.log" {
		t.Errorf("expected LogFile=%q, got %q", "/tmp/test.log", a.LogFile)
	}
}

func TestParseArgs_LogFileEquals(t *testing.T) {
	a := mustParseArgs(t, []string{"--log-file=/tmp/test.log"})
	if a.LogFile != "/tmp/test.log" {
		t.Errorf("expected LogFile=%q, got %q", "/tmp/test.log", a.LogFile)
	}
}

func TestParseArgs_Upstream(t *testing.T) {
	a := mustParseArgs(t, []string{"--upstream", "https://gw.example.com/openai/v1"})
	if a.Upstream != "https://gw.example.com/openai/v1" {
		t.Errorf("expected Upstream=%q, got %q", "https://gw.example.com/openai/v1", a.Upstream)
	}
}

func TestParseArgs_UpstreamEquals(t *testing.T) {
	a := mustParseArgs(t, []string{"--upstream=https://gw.example.com/openai/v1"})
	if a.Upstream != "https://gw.example.com/openai/v1" {
		t.Errorf("expected Upstream=%q, got %q", "https://gw.example.com/openai/v1", a.Upstream)
	}
}

// #87 removed --no-otel*/--otel-*-table; coverage moved to TestResolveConfigOTEL_*
// (state-driven resolver) and TestRootTreeFlagsAreParseRecognised (shrink-side
// of the bidirectional parity check).

func TestParseArgs_UnknownFlagPassthrough(t *testing.T) {
	a := mustParseArgs(t, []string{"--unknown"})
	if len(a.CodexArgs) != 1 || a.CodexArgs[0] != "--unknown" {
		t.Errorf("expected CodexArgs=[\"--unknown\"], got %v", a.CodexArgs)
	}
}

func TestParseArgs_EmptyArgs(t *testing.T) {
	a := mustParseArgs(t, []string{})
	if a.Verbose || a.Version || a.ShowHelp {
		t.Error("expected all bool flags false for empty args")
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
	if len(a.CodexArgs) != 0 {
		t.Errorf("expected no CodexArgs, got %v", a.CodexArgs)
	}
	// #89 removed the --idle-timeout root flag; parseArgs no longer
	// initialises Args.IdleTimeout. The default 30m default is now
	// applied by buildServeArgs (serve_cmd.go); see
	// TestBuildServeArgs_DefaultIdleTimeout.
	if a.IdleTimeout != 0 {
		t.Errorf("expected zero IdleTimeout from parseArgs (no longer parsed), got %v", a.IdleTimeout)
	}
	if a.Headless {
		t.Error("expected Headless=false from parseArgs (no longer parsed; set only by runServeCommand)")
	}
}

func TestParseArgs_Mixed(t *testing.T) {
	a := mustParseArgs(t, []string{"--verbose", "--upstream", "https://gw.example.com", "--help"})
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
	a := mustParseArgs(t, []string{"--verbose", "--", "--unknown", "arg1"})
	if !a.Verbose {
		t.Error("expected Verbose=true before separator")
	}
	if len(a.CodexArgs) != 2 || a.CodexArgs[0] != "--unknown" || a.CodexArgs[1] != "arg1" {
		t.Errorf("expected CodexArgs=[\"--unknown\", \"arg1\"], got %v", a.CodexArgs)
	}
}

// TestParseArgs_SeparatorForwardsHelp locks #95: "--" terminates wrapper
// flag parsing, so "-- --help" must NOT trigger the wrapper's help and must
// forward "--help" to codex verbatim. Together with handleHelp no longer
// shelling out to `codex --help`, this is how users reach codex's own help.
func TestParseArgs_SeparatorForwardsHelp(t *testing.T) {
	a := mustParseArgs(t, []string{"--", "--help"})
	if a.ShowHelp {
		t.Error("expected ShowHelp=false when --help appears after --")
	}
	if len(a.CodexArgs) != 1 || a.CodexArgs[0] != "--help" {
		t.Errorf("expected CodexArgs=[--help], got %v", a.CodexArgs)
	}
}

func TestParseArgs_PassthroughArgs(t *testing.T) {
	a := mustParseArgs(t, []string{"prompt text", "--unknown-flag", "gpt-4"})
	if len(a.CodexArgs) != 3 {
		t.Errorf("expected 3 CodexArgs, got %d: %v", len(a.CodexArgs), a.CodexArgs)
	}
}

// --- Model flag tests ---

func TestParseArgs_Model(t *testing.T) {
	a := mustParseArgs(t, []string{"--model", "databricks-gpt-5-4-mini"})
	if !a.ModelSet {
		t.Error("expected ModelSet=true when --model is passed")
	}
	if a.Model != "databricks-gpt-5-4-mini" {
		t.Errorf("expected Model=%q, got %q", "databricks-gpt-5-4-mini", a.Model)
	}
}

func TestParseArgs_ModelEquals(t *testing.T) {
	a := mustParseArgs(t, []string{"--model=custom-model"})
	if !a.ModelSet {
		t.Error("expected ModelSet=true when --model=value is passed")
	}
	if a.Model != "custom-model" {
		t.Errorf("expected Model=%q, got %q", "custom-model", a.Model)
	}
}

func TestParseArgs_ModelDefault(t *testing.T) {
	a := mustParseArgs(t, []string{})
	if a.ModelSet {
		t.Error("expected ModelSet=false when --model is not passed")
	}
	if a.Model != "" {
		t.Errorf("expected empty Model from parseArgs, got %q", a.Model)
	}
}

func TestParseArgs_ModelNotPassedThrough(t *testing.T) {
	a := mustParseArgs(t, []string{"--model", "my-model", "prompt"})
	if !a.ModelSet {
		t.Error("expected ModelSet=true")
	}
	if a.Model != "my-model" {
		t.Errorf("expected Model=%q, got %q", "my-model", a.Model)
	}
	if len(a.CodexArgs) != 1 || a.CodexArgs[0] != "prompt" {
		t.Errorf("expected CodexArgs=[\"prompt\"], got %v", a.CodexArgs)
	}
}

// --- --idle-timeout strict parsing tests ---
//
// #89 removed --idle-timeout from the root flag set; parseArgs no longer
// recognises it. The duration-parsing strict-validation surface (seconds /
// minutes / hours / equals / bare-int rejected / garbage rejected / empty
// rejected) lives on the serve subcommand now and is exercised by
// TestBuildServeArgs_IdleTimeout* in serve_cmd_test.go. The legacy
// --idle-timeout root flag falls through to CodexArgs (verified by
// TestParseArgs_Table's "legacy --idle-timeout passes through" case).

// Table-driven comprehensive test for parseArgs.
func TestParseArgs_Table(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Args
	}{
		{name: "--help sets showHelp", args: []string{"--help"}, want: Args{ShowHelp: true}},
		{name: "-h sets showHelp", args: []string{"-h"}, want: Args{ShowHelp: true}},
		{name: "--version sets version", args: []string{"--version"}, want: Args{Version: true}},
		{name: "--verbose sets verbose", args: []string{"--verbose"}, want: Args{Verbose: true}},
		{name: "-v sets verbose", args: []string{"-v"}, want: Args{Verbose: true}},
		{name: "--log-file sets logFile", args: []string{"--log-file", "/tmp/test.log"}, want: Args{LogFile: "/tmp/test.log"}},
		{name: "--log-file=value", args: []string{"--log-file=/tmp/test.log"}, want: Args{LogFile: "/tmp/test.log"}},
		{name: "--upstream sets upstream", args: []string{"--upstream", "https://gw.example.com"}, want: Args{Upstream: "https://gw.example.com"}},
		{name: "empty args all defaults", args: []string{}, want: Args{}},
		{name: "--profile", args: []string{"--profile", "aidev"}, want: Args{Profile: "aidev"}},
		{name: "--profile=value", args: []string{"--profile=aidev"}, want: Args{Profile: "aidev"}},
		{name: "--model", args: []string{"--model", "my-model"}, want: Args{Model: "my-model", ModelSet: true}},
		{name: "--port", args: []string{"--port", "9999"}, want: Args{PortFlag: 9999}},
		{name: "--no-update-check", args: []string{"--no-update-check"}, want: Args{NoUpdateCheck: true}},
		{
			// #88 removed --install-hooks; the legacy flag is no longer in
			// knownFlags, so parseArgs forwards it to codex unchanged.
			name: "legacy --install-hooks now passes through to codex",
			args: []string{"--install-hooks"},
			want: Args{CodexArgs: []string{"--install-hooks"}},
		},
		{
			name: "legacy --uninstall-hooks now passes through to codex",
			args: []string{"--uninstall-hooks"},
			want: Args{CodexArgs: []string{"--uninstall-hooks"}},
		},
		{
			name: "legacy --headless-ensure now passes through to codex",
			args: []string{"--headless-ensure"},
			want: Args{CodexArgs: []string{"--headless-ensure"}},
		},
		{
			// #89 removed --headless; the legacy flag is no longer in
			// knownFlags, so parseArgs forwards it to codex unchanged.
			// Users should migrate to `databricks-codex serve`.
			name: "legacy --headless now passes through to codex",
			args: []string{"--headless"},
			want: Args{CodexArgs: []string{"--headless"}},
		},
		{
			// #89 removed --idle-timeout. parseArgs forwards both the
			// flag name and its value as separate codex args (it has no
			// way to know the flag took a value).
			name: "legacy --idle-timeout now passes through to codex",
			args: []string{"--idle-timeout", "5m"},
			want: Args{CodexArgs: []string{"--idle-timeout", "5m"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if err != nil {
				t.Fatalf("parseArgs unexpected error: %v", err)
			}
			if got.Verbose != tc.want.Verbose {
				t.Errorf("Verbose: got %v, want %v", got.Verbose, tc.want.Verbose)
			}
			if got.Version != tc.want.Version {
				t.Errorf("Version: got %v, want %v", got.Version, tc.want.Version)
			}
			if got.ShowHelp != tc.want.ShowHelp {
				t.Errorf("ShowHelp: got %v, want %v", got.ShowHelp, tc.want.ShowHelp)
			}
			if got.Upstream != tc.want.Upstream {
				t.Errorf("Upstream: got %q, want %q", got.Upstream, tc.want.Upstream)
			}
			if got.LogFile != tc.want.LogFile {
				t.Errorf("LogFile: got %q, want %q", got.LogFile, tc.want.LogFile)
			}
			if got.Profile != tc.want.Profile {
				t.Errorf("Profile: got %q, want %q", got.Profile, tc.want.Profile)
			}
			if got.Model != tc.want.Model {
				t.Errorf("Model: got %q, want %q", got.Model, tc.want.Model)
			}
			if got.ModelSet != tc.want.ModelSet {
				t.Errorf("ModelSet: got %v, want %v", got.ModelSet, tc.want.ModelSet)
			}
			if got.PortFlag != tc.want.PortFlag {
				t.Errorf("PortFlag: got %d, want %d", got.PortFlag, tc.want.PortFlag)
			}
			if got.Headless != tc.want.Headless {
				t.Errorf("Headless: got %v, want %v", got.Headless, tc.want.Headless)
			}
			if got.NoUpdateCheck != tc.want.NoUpdateCheck {
				t.Errorf("NoUpdateCheck: got %v, want %v", got.NoUpdateCheck, tc.want.NoUpdateCheck)
			}
			if !equalStringSlice(got.CodexArgs, tc.want.CodexArgs) {
				t.Errorf("CodexArgs: got %v, want %v", got.CodexArgs, tc.want.CodexArgs)
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

// TestHandlePrintEnv_RedactsAllTokenShapes ensures that no matter the token
// shape (legacy dapi-, no-hyphen dapi, JWT, empty), the literal token bytes
// never appear in the printed output.
func TestHandlePrintEnv_RedactsAllTokenShapes(t *testing.T) {
	tokens := []string{
		"dapi-abc123secret", // legacy hyphenated
		"dapiabc123secret",  // no hyphen
		"eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig", // JWT-shaped
		"", // empty
	}
	for _, token := range tokens {
		t.Run(fmt.Sprintf("token=%q", token), func(t *testing.T) {
			out := captureStdout(func() {
				handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", token, "DEFAULT", "databricks-gpt-5-5", "main.codex_telemetry.codex_otel_metrics", "main.codex_telemetry.codex_otel_logs")
			})
			if !strings.Contains(out, "**** (redacted)") {
				t.Errorf("expected '**** (redacted)' in output, got:\n%s", out)
			}
			if token != "" && strings.Contains(out, token) {
				t.Errorf("raw token %q should not appear in output, got:\n%s", token, out)
			}
		})
	}
}

func TestHandlePrintEnv_NoLegacyDapiPrefix(t *testing.T) {
	// Per #71, the legacy "dapi-***" branch was removed in favour of a single
	// fixed redaction. Make sure no output ever contains the legacy form.
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "dapi-abc123", "DEFAULT", "databricks-gpt-5-5", "main.codex_telemetry.codex_otel_metrics", "main.codex_telemetry.codex_otel_logs")
	})
	if strings.Contains(out, "dapi-***") {
		t.Errorf("legacy 'dapi-***' redaction marker should be gone, got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsProfile(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "aidev", "databricks-gpt-5-5", "main.codex_telemetry.codex_otel_metrics", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, "aidev") {
		t.Errorf("expected output to contain profile 'aidev', got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsDatabricksHost(t *testing.T) {
	host := "https://dbc-abc123.cloud.databricks.com"
	out := captureStdout(func() {
		handlePrintEnv(host, "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-5", "main.codex_telemetry.codex_otel_metrics", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, host) {
		t.Errorf("expected output to contain DATABRICKS_HOST %q, got:\n%s", host, out)
	}
}

func TestHandlePrintEnv_ContainsOpenAIBaseURL(t *testing.T) {
	baseURL := "https://gw.example.com/openai/v1"
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", baseURL, "tok", "DEFAULT", "databricks-gpt-5-5", "main.codex_telemetry.codex_otel_metrics", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, baseURL) {
		t.Errorf("expected output to contain OPENAI_BASE_URL %q, got:\n%s", baseURL, out)
	}
}

func TestHandlePrintEnv_ContainsModel(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-4-mini", "main.codex_telemetry.codex_otel_metrics", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, "databricks-gpt-5-4-mini") {
		t.Errorf("expected output to contain model 'databricks-gpt-5-4-mini', got:\n%s", out)
	}
	if !strings.Contains(out, "Model:") {
		t.Errorf("expected output to contain 'Model:' label, got:\n%s", out)
	}
}

// --- handleHelp tests ---

func TestHandleHelp_ContainsDatabricksCodex(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, "databricks-codex") {
		t.Errorf("expected help output to contain 'databricks-codex', got:\n%s", out)
	}
}

func TestHandleHelp_ContainsConfigSubcommand(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, "config") {
		t.Errorf("expected help output to advertise the config subcommand, got:\n%s", out)
	}
}

// TestHandleHelp_DocumentsPassthrough locks #95: wrapper help must NOT
// append codex's own --help, and MUST document the `--` passthrough escape
// hatch so users still know how to reach codex's flags.
func TestHandleHelp_DocumentsPassthrough(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if strings.Contains(out, "Codex CLI Options:") {
		t.Errorf("wrapper help must not append codex --help under a 'Codex CLI Options:' divider, got:\n%s", out)
	}
	if !strings.Contains(out, "Passthrough to codex:") {
		t.Errorf("expected help output to document the -- passthrough, got:\n%s", out)
	}
	if !strings.Contains(out, "databricks-codex -- --help") {
		t.Errorf("expected help output to show the `-- --help` example, got:\n%s", out)
	}
}

func TestHandleHelp_NoBareNumberMinutesWording(t *testing.T) {
	// Per #70: help text must not advertise the bare-int minutes shortcut.
	out := captureStdout(func() {
		handleHelp()
	})
	if strings.Contains(out, "bare number = minutes") {
		t.Errorf("help text should no longer advertise 'bare number = minutes', got:\n%s", out)
	}
}

func TestParseArgs_Profile(t *testing.T) {
	a := mustParseArgs(t, []string{"--profile", "aidev"})
	if a.Profile != "aidev" {
		t.Errorf("expected Profile=%q, got %q", "aidev", a.Profile)
	}
}

func TestParseArgs_ProfileEquals(t *testing.T) {
	a := mustParseArgs(t, []string{"--profile=production"})
	if a.Profile != "production" {
		t.Errorf("expected Profile=%q, got %q", "production", a.Profile)
	}
}

// TestParseArgs_LegacyHeadlessPassthrough locks the breaking surface from
// #89: the deleted --headless root flag must NOT be recognised by parseArgs;
// it forwards to CodexArgs unchanged. Catches the regression where someone
// re-adds the case in parseArgs without bringing back the flag.
func TestParseArgs_LegacyHeadlessPassthrough(t *testing.T) {
	a := mustParseArgs(t, []string{"--headless"})
	if a.Headless {
		t.Error("Args.Headless must NOT be set by parseArgs after #89; only runServeCommand sets it")
	}
	if len(a.CodexArgs) != 1 || a.CodexArgs[0] != "--headless" {
		t.Errorf("legacy --headless must pass through to CodexArgs, got %v", a.CodexArgs)
	}
}

// TestParseArgs_LegacyIdleTimeoutPassthrough is the symmetric check for
// the deleted --idle-timeout root flag.
func TestParseArgs_LegacyIdleTimeoutPassthrough(t *testing.T) {
	a := mustParseArgs(t, []string{"--idle-timeout", "5m"})
	if a.IdleTimeout != 0 {
		t.Errorf("Args.IdleTimeout must NOT be set by parseArgs after #89, got %v", a.IdleTimeout)
	}
	// The flag name and its value both fall through as positional args
	// because parseArgs has no metadata to tell it the legacy flag took a
	// value. Both tokens land in CodexArgs.
	if len(a.CodexArgs) != 2 || a.CodexArgs[0] != "--idle-timeout" || a.CodexArgs[1] != "5m" {
		t.Errorf("legacy --idle-timeout must pass through to CodexArgs, got %v", a.CodexArgs)
	}
}

func TestParseArgs_NoUpdateCheck(t *testing.T) {
	a := mustParseArgs(t, []string{"--no-update-check"})
	if !a.NoUpdateCheck {
		t.Error("expected NoUpdateCheck=true for --no-update-check")
	}
}

func TestHandleHelp_AllFlagsPresent(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	// Legacy hook flags (--install-hooks / --uninstall-hooks /
	// --headless-ensure) were lifted to the `hooks` subcommand in #88; the
	// OTEL/--print-env flags moved to `config otel`/`config show` in #87.
	// --headless / --idle-timeout were lifted to the `serve` subcommand in
	// #89. The help text now lists `config`, `hooks`, and `serve` instead.
	flags := []string{"--profile", "--model", "--upstream", "--verbose", "-v", "--log-file", "--port", "--no-update-check", "--version", "--help", "config", "hooks", "serve"}
	for _, flag := range flags {
		if !strings.Contains(out, flag) {
			t.Errorf("expected help output to contain flag %q, got:\n%s", flag, out)
		}
	}
}

// TestHandleHelp_LegacyHeadlessFlagsAbsent locks the breaking surface
// change from #89: --headless and --idle-timeout must NOT appear in the
// root help text. If a future refactor re-adds them, this test fires.
func TestHandleHelp_LegacyHeadlessFlagsAbsent(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	for _, flag := range []string{"--headless", "--idle-timeout"} {
		if strings.Contains(out, flag) {
			t.Errorf("root help still mentions %q after #89 migration; users will discover the dead flag", flag)
		}
	}
}

func TestHandleHelp_LegacyOtelFlagsAbsent(t *testing.T) {
	// Locks the breaking surface: #87 removed these from the root.
	out := captureStdout(func() {
		handleHelp()
	})
	for _, flag := range []string{"--otel", "--no-otel", "--no-otel-metrics", "--no-otel-logs", "--otel-metrics-table", "--otel-logs-table", "--print-env"} {
		if strings.Contains(out, flag) {
			t.Errorf("root help still mentions %q after #87 migration; users will discover the dead flag", flag)
		}
	}
}

func TestHandleHelp_ContainsVersion(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, fmt.Sprintf("databricks-codex v%s", Version)) {
		t.Errorf("expected help output to contain version string, got:\n%s", out)
	}
}

// --- deriveLogsTable test ---
//
// resolveOtelLogsTable / resolveOtelMetricsTable were inlined into the
// `config otel enable` resolver in #87; their unit tests folded into
// TestResolveConfigOTEL_OrchestrationMatrix in cli_config_test.go.

func TestDeriveLogsTable(t *testing.T) {
	tests := []struct {
		name    string
		metrics string
		want    string
	}{
		{name: "standard _otel_metrics suffix is replaced", metrics: "main.tel.codex_otel_metrics", want: "main.tel.codex_otel_logs"},
		{name: "custom suffix gets _otel_logs appended", metrics: "cat.schema.custom", want: "cat.schema.custom_otel_logs"},
		{name: "empty metrics returns empty", metrics: "", want: ""},
		{name: "bare _otel_metrics replaced", metrics: "_otel_metrics", want: "_otel_logs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveLogsTable(tc.metrics)
			if got != tc.want {
				t.Errorf("deriveLogsTable(%q) = %q, want %q", tc.metrics, got, tc.want)
			}
		})
	}
}

// --- resolveOtel integration tests (state-only signature, post-#87) ---
//
// #87 turned resolveOtel into a pure read-only consumer of saved state.
// The session-time flag combinations that drove the legacy resolveOtel
// matrix moved to TestResolveConfigOTEL_OrchestrationMatrix
// (cli_config_test.go) — that's the writer side. resolveOtel is the
// reader side and is much smaller now.

func TestResolveOtel(t *testing.T) {
	const customMetrics = "cat.schema.codex_otel_metrics"
	const customLogs = "cat.schema.codex_otel_logs"

	tests := []struct {
		name        string
		saved       persistentState
		wantOtel    bool
		wantMetrics string
		wantLogs    string
	}{
		{
			name:     "empty state: otel off, both tables empty",
			saved:    persistentState{},
			wantOtel: false, wantMetrics: "", wantLogs: "",
		},
		{
			name:        "saved tables, no disables: otel on, both tables flow through",
			saved:       persistentState{OtelMetricsTable: customMetrics, OtelLogsTable: customLogs},
			wantOtel:    true,
			wantMetrics: customMetrics,
			wantLogs:    customLogs,
		},
		{
			name:     "saved tables but both Disabled bits set: hard off (the post-`config otel disable` shape)",
			saved:    persistentState{OtelMetricsTable: customMetrics, OtelLogsTable: customLogs, OtelMetricsDisabled: true, OtelLogsDisabled: true},
			wantOtel: false, wantMetrics: "", wantLogs: "",
		},
		{
			name:        "saved tables with metrics disabled only: logs preserved (the per-signal disable shape)",
			saved:       persistentState{OtelMetricsTable: customMetrics, OtelLogsTable: customLogs, OtelMetricsDisabled: true},
			wantOtel:    true,
			wantMetrics: "",
			wantLogs:    customLogs,
		},
		{
			name:        "saved tables with logs disabled only: metrics preserved",
			saved:       persistentState{OtelMetricsTable: customMetrics, OtelLogsTable: customLogs, OtelLogsDisabled: true},
			wantOtel:    true,
			wantMetrics: customMetrics,
			wantLogs:    "",
		},
		{
			name:        "saved metrics only: otel on, logs empty",
			saved:       persistentState{OtelMetricsTable: customMetrics},
			wantOtel:    true,
			wantMetrics: customMetrics,
			wantLogs:    "",
		},
		{
			name:     "Disabled bits set on empty tables: still off (no-op)",
			saved:    persistentState{OtelMetricsDisabled: true, OtelLogsDisabled: true},
			wantOtel: false, wantMetrics: "", wantLogs: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			otel, metrics, logs := resolveOtel(tc.saved)
			if otel != tc.wantOtel {
				t.Errorf("otel: got %v, want %v", otel, tc.wantOtel)
			}
			if metrics != tc.wantMetrics {
				t.Errorf("metricsTable: got %q, want %q", metrics, tc.wantMetrics)
			}
			if logs != tc.wantLogs {
				t.Errorf("logsTable: got %q, want %q", logs, tc.wantLogs)
			}
		})
	}
}

func TestHandlePrintEnv_ContainsOtelLogsTable(t *testing.T) {
	table := "main.custom.otel_logs"
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-5", "main.codex_telemetry.codex_otel_metrics", table)
	})
	if !strings.Contains(out, table) {
		t.Errorf("expected output to contain OTEL Logs Table %q, got:\n%s", table, out)
	}
	if !strings.Contains(out, "OTEL Logs Table:") {
		t.Errorf("expected output to contain 'OTEL Logs Table:' label, got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsOtelMetricsTable(t *testing.T) {
	table := "main.custom.otel_metrics"
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-5", table, "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, table) {
		t.Errorf("expected output to contain OTEL Metrics Table %q, got:\n%s", table, out)
	}
	if !strings.Contains(out, "OTEL Metrics Table:") {
		t.Errorf("expected output to contain 'OTEL Metrics Table:' label, got:\n%s", out)
	}
}

func TestHandlePrintEnv_DisabledTablesRenderAsDisabled(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-5", "", "")
	})
	if !strings.Contains(out, "OTEL Metrics Table:  (disabled)") {
		t.Errorf("expected '(disabled)' for metrics table when empty, got:\n%s", out)
	}
	if !strings.Contains(out, "OTEL Logs Table:     (disabled)") {
		t.Errorf("expected '(disabled)' for logs table when empty, got:\n%s", out)
	}
}

// --- resolveProfile tests ---

func TestResolveProfile_FlagWinsOverStateFile(t *testing.T) {
	got := resolveProfile("from-flag", "from-state")
	if got != "from-flag" {
		t.Errorf("expected flag value %q, got %q", "from-flag", got)
	}
}

func TestResolveProfile_StateFileWinsOverEnvVar(t *testing.T) {
	t.Setenv("DATABRICKS_CONFIG_PROFILE", "from-env")
	got := resolveProfile("", "from-state")
	if got != "from-state" {
		t.Errorf("expected state file value %q, got %q — env var should be ignored", "from-state", got)
	}
}

func TestResolveProfile_FallsBackToDefault(t *testing.T) {
	got := resolveProfile("", "")
	if got != "DEFAULT" {
		t.Errorf("expected %q, got %q", "DEFAULT", got)
	}
}

func TestResolveProfile_FlagWinsOverStateFile_Integration(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	saveState(persistentState{Profile: "saved-profile"})

	got := resolveProfile("flag-profile", loadState().Profile)
	if got != "flag-profile" {
		t.Errorf("expected flag profile %q, got %q", "flag-profile", got)
	}
}

func TestResolveProfile_StateFileUsedWhenNoFlag(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	saveState(persistentState{Profile: "saved-profile"})

	got := resolveProfile("", loadState().Profile)
	if got != "saved-profile" {
		t.Errorf("expected saved profile %q, got %q", "saved-profile", got)
	}
}

func TestResolveProfile_DefaultWhenNoStateFile(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "nonexistent.json") }
	defer func() { statePath = orig }()

	got := resolveProfile("", loadState().Profile)
	if got != "DEFAULT" {
		t.Errorf("expected %q, got %q", "DEFAULT", got)
	}
}

func TestResolveProfile_Table(t *testing.T) {
	tests := []struct {
		name       string
		flagValue  string
		savedValue string
		want       string
	}{
		{"flag wins over saved", "flag-profile", "saved-profile", "flag-profile"},
		{"saved wins over default", "", "saved-profile", "saved-profile"},
		{"default when both empty", "", "", "DEFAULT"},
		{"flag wins over empty saved", "flag-profile", "", "flag-profile"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveProfile(tc.flagValue, tc.savedValue)
			if got != tc.want {
				t.Errorf("resolveProfile(%q, %q) = %q, want %q",
					tc.flagValue, tc.savedValue, got, tc.want)
			}
		})
	}
}

// TestCompletionFlagsCoverAllKnownFlags ensures every flag in knownFlags has a
// corresponding entry in flagDefs — prevents silent drift between the real CLI
// and the generated shell completions.
func TestCompletionFlagsCoverAllKnownFlags(t *testing.T) {
	covered := make(map[string]bool, len(flagDefs))
	for _, f := range flagDefs {
		covered["--"+f.Name] = true
	}
	for flag := range knownFlags {
		if !covered[flag] {
			t.Errorf("flag %s is in knownFlags but missing from flagDefs in completion_flags.go", flag)
		}
	}
}

// TestKnownFlagsCoverAllFlagDefs is the inverse: every FlagDef must appear in
// knownFlags so the parser actually recognises it.
func TestKnownFlagsCoverAllFlagDefs(t *testing.T) {
	for _, f := range flagDefs {
		name := "--" + f.Name
		if !knownFlags[name] {
			t.Errorf("flagDef %q is missing from knownFlags in completion_flags.go", name)
		}
	}
}

// TestRootTreeFlagsAreParseRecognised verifies that every flag declared on
// rootCommand (the source of truth for the binary's CLI surface) is actually
// handled by parseArgs. Catches the case where a flag is added to the tree
// in commands.go but the matching switch case is forgotten in main.go's
// parseArgs — the explicit default arm in parseArgs returns an error in
// that scenario, which this test surfaces as a per-flag failure.
//
// This is the "tree → parser" half of the bidirectional parity check;
// TestParseArgsRecognisedFlagsAreInTree below covers the inverse direction.
func TestRootTreeFlagsAreParseRecognised(t *testing.T) {
	for _, f := range rootCommand.AllFlags() {
		var args []string
		if f.TakesArg {
			args = []string{"--" + f.Name, "value"}
		} else {
			args = []string{"--" + f.Name}
		}
		if _, err := parseArgs(args); err != nil {
			t.Errorf("parseArgs(%v) returned error %v — flag %q is declared on rootCommand but parseArgs has no case for it", args, err, f.Name)
		}
	}
}

// TestParseArgsRecognisedFlagsAreInTree is the inverse parity check: every
// flag the parser thinks it owns (knownFlags) must appear in rootCommand.
// Since knownFlags is now derived from rootCommand, this is structurally
// guaranteed — the test is here to document the contract and to fail
// loudly if a future refactor decouples the two.
func TestParseArgsRecognisedFlagsAreInTree(t *testing.T) {
	treeNames := map[string]bool{}
	for _, f := range rootCommand.AllFlags() {
		treeNames["--"+f.Name] = true
	}
	for name := range knownFlags {
		if !treeNames[name] {
			t.Errorf("knownFlags contains %q but it is not declared on rootCommand", name)
		}
	}
}

// TestResolveModel_DefaultIsGpt5_5 locks the built-in default model against
// silent drift. When no flag is passed and saved state is empty, resolveModel
// must return databricks-gpt-5-5. Bumping the default requires updating this
// test in the same commit, which serves as an explicit checkpoint.
func TestResolveModel_DefaultIsGpt5_5(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Empty saved state + no flag + no env → built-in default fires.
	got := resolveModel("", loadState().Model)
	want := "databricks-gpt-5-5"
	if got != want {
		t.Errorf("resolveModel default = %q, want %q", got, want)
	}
}
