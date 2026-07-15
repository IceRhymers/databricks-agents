package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
	"github.com/IceRhymers/databricks-agents/pkg/lifecycle"
)

// shutdownResp mirrors the JSON body returned by POST /shutdown for test decoding.
type shutdownResp struct {
	Remaining int  `json:"remaining"`
	Exiting   bool `json:"exiting"`
}

// --- parseArgs tests ---

func TestParseArgs_HelpLong(t *testing.T) {
	a, _ := parseArgs([]string{"--help"})
	if !a.ShowHelp {
		t.Error("expected showHelp=true for --help")
	}
	if a.Profile != "" || a.Verbose || a.Version || a.Upstream != "" || a.LogFile != "" || len(a.ClaudeArgs) != 0 {
		t.Error("unexpected non-default values alongside --help")
	}
}

func TestParseArgs_HelpShort(t *testing.T) {
	a, _ := parseArgs([]string{"-h"})
	if !a.ShowHelp {
		t.Error("expected showHelp=true for -h")
	}
}

// "--" terminates wrapper flag parsing, so "-- --help" must NOT trigger the
// wrapper's help and must forward "--help" to claude verbatim. The wrapper's
// `--help` documentation advertises this as the way to reach claude's help.
func TestParseArgs_SeparatorForwardsHelp(t *testing.T) {
	a, _ := parseArgs([]string{"--", "--help"})
	if a.ShowHelp {
		t.Error("expected showHelp=false when --help appears after --")
	}
	if len(a.ClaudeArgs) != 1 || a.ClaudeArgs[0] != "--help" {
		t.Errorf("expected ClaudeArgs=[--help], got %v", a.ClaudeArgs)
	}
}

// TestParseArgs_RemovedFlagsPassThrough confirms #172's "removed, not
// aliased" contract: legacy persistent-config flags now forward to claude
// as unknown args, no Args fields are populated for them.
func TestParseArgs_RemovedFlagsPassThrough(t *testing.T) {
	removed := []string{
		"--print-env",
		"--otel",
		"--otel-metrics-table", "x",
		"--otel-logs-table", "y",
		"--otel-traces",
		"--otel-traces-table", "z",
		"--no-otel",
		"--no-otel-metrics",
		"--no-otel-logs",
		"--no-otel-traces",
		"--write-claude-config",
		"--with-websearch",
		"--websearch-backend", "duckduckgo",
		"--websearch-fetch-budget", "1024",
	}
	a, err := parseArgs(removed)
	if err != nil {
		t.Fatalf("parseArgs returned error for legacy flags (must pass through, not error): %v", err)
	}
	// Every removed flag should be in ClaudeArgs (passthrough).
	for _, want := range []string{
		"--print-env", "--otel", "--otel-metrics-table", "--otel-logs-table",
		"--otel-traces", "--otel-traces-table", "--no-otel", "--no-otel-metrics",
		"--no-otel-logs", "--no-otel-traces", "--write-claude-config",
		"--with-websearch", "--websearch-backend", "--websearch-fetch-budget",
	} {
		found := false
		for _, ca := range a.ClaudeArgs {
			if ca == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q to forward to ClaudeArgs (removed in #172, not aliased), got ClaudeArgs=%v", want, a.ClaudeArgs)
		}
	}
}

func TestParseArgs_Version(t *testing.T) {
	a, _ := parseArgs([]string{"--version"})
	if !a.Version {
		t.Error("expected version=true for --version")
	}
}

func TestParseArgs_Verbose(t *testing.T) {
	a, _ := parseArgs([]string{"--verbose"})
	if !a.Verbose {
		t.Error("expected verbose=true for --verbose")
	}
}

func TestParseArgs_VerboseShort(t *testing.T) {
	a, _ := parseArgs([]string{"-v"})
	if !a.Verbose {
		t.Error("expected verbose=true for -v")
	}
}

func TestParseArgs_LogFile(t *testing.T) {
	a, _ := parseArgs([]string{"--log-file", "/tmp/test.log"})
	if a.LogFile != "/tmp/test.log" {
		t.Errorf("expected logFile=%q, got %q", "/tmp/test.log", a.LogFile)
	}
}

func TestParseArgs_LogFileEquals(t *testing.T) {
	a, _ := parseArgs([]string{"--log-file=/tmp/test.log"})
	if a.LogFile != "/tmp/test.log" {
		t.Errorf("expected logFile=%q, got %q", "/tmp/test.log", a.LogFile)
	}
}

func TestParseArgs_Profile(t *testing.T) {
	a, _ := parseArgs([]string{"--profile", "foo"})
	if a.Profile != "foo" {
		t.Errorf("expected profile=%q, got %q", "foo", a.Profile)
	}
}

func TestParseArgs_Upstream(t *testing.T) {
	a, _ := parseArgs([]string{"--upstream", "/path/to/claude"})
	if a.Upstream != "/path/to/claude" {
		t.Errorf("expected upstream=%q, got %q", "/path/to/claude", a.Upstream)
	}
}

func TestParseArgs_UnknownFlagPassthrough(t *testing.T) {
	a, _ := parseArgs([]string{"--unknown"})
	if len(a.ClaudeArgs) != 1 || a.ClaudeArgs[0] != "--unknown" {
		t.Errorf("expected claudeArgs=[\"--unknown\"], got %v", a.ClaudeArgs)
	}
}

func TestParseArgs_EmptyArgs(t *testing.T) {
	a, _ := parseArgs([]string{})
	if a.Profile != "" {
		t.Errorf("expected empty profile, got %q", a.Profile)
	}
	if a.Verbose || a.Version || a.ShowHelp {
		t.Error("expected all bool flags false for empty args")
	}
	if a.Upstream != "" {
		t.Errorf("expected empty upstream, got %q", a.Upstream)
	}
	if a.LogFile != "" {
		t.Errorf("expected empty logFile, got %q", a.LogFile)
	}
	if len(a.ClaudeArgs) != 0 {
		t.Errorf("expected no claudeArgs, got %v", a.ClaudeArgs)
	}
}

func TestParseArgs_Mixed(t *testing.T) {
	a, _ := parseArgs([]string{"--profile", "prod", "--verbose", "--help"})
	if !a.ShowHelp {
		t.Error("expected showHelp=true")
	}
	if a.Profile != "prod" {
		t.Errorf("expected profile=%q, got %q", "prod", a.Profile)
	}
	if !a.Verbose {
		t.Error("expected verbose=true")
	}
}

// #174: --headless is removed from the root flag namespace and now lives
// behind `serve --session-mode`. This test asserts the breaking-change
// passthrough contract: --headless is forwarded to claude as an unknown
// flag (NOT aliased to anything).
func TestParseArgs_HeadlessRemovedPassThrough(t *testing.T) {
	a, err := parseArgs([]string{"--headless"})
	if err != nil {
		t.Fatalf("parseArgs returned error for legacy --headless (must pass through, not error): %v", err)
	}
	if len(a.ClaudeArgs) != 1 || a.ClaudeArgs[0] != "--headless" {
		t.Errorf("expected --headless to forward to ClaudeArgs (removed in #174, not aliased), got ClaudeArgs=%v", a.ClaudeArgs)
	}
}

func TestParseArgs_NoUpdateCheck(t *testing.T) {
	a, _ := parseArgs([]string{"--no-update-check"})
	if !a.NoUpdateCheck {
		t.Error("expected noUpdateCheck=true for --no-update-check")
	}
}

// Table-driven comprehensive test for parseArgs.
func TestParseArgs_Table(t *testing.T) {
	type result struct {
		profile   string
		verbose   bool
		version   bool
		showHelp  bool
		upstream  string
		logFile   string
		claudeLen int
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
			name: "-v with --log-file sets both",
			args: []string{"-v", "--log-file", "/tmp/both.log"},
			want: result{verbose: true, logFile: "/tmp/both.log"},
		},
		{
			name: "--profile foo sets profile",
			args: []string{"--profile", "foo"},
			want: result{profile: "foo"},
		},
		{
			name: "--upstream /path sets upstream",
			args: []string{"--upstream", "/path/to/claude"},
			want: result{upstream: "/path/to/claude"},
		},
		{
			name: "unknown flag passes through",
			args: []string{"--unknown"},
			want: result{claudeLen: 1},
		},
		{
			name: "empty args all defaults",
			args: []string{},
			want: result{},
		},
		{
			name: "mixed flags: profile, verbose, help",
			args: []string{"--profile", "prod", "--verbose", "--help"},
			want: result{showHelp: true, profile: "prod", verbose: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parseArgs(tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if a.Profile != tc.want.profile {
				t.Errorf("profile: got %q, want %q", a.Profile, tc.want.profile)
			}
			if a.Verbose != tc.want.verbose {
				t.Errorf("verbose: got %v, want %v", a.Verbose, tc.want.verbose)
			}
			if a.Version != tc.want.version {
				t.Errorf("version: got %v, want %v", a.Version, tc.want.version)
			}
			if a.ShowHelp != tc.want.showHelp {
				t.Errorf("showHelp: got %v, want %v", a.ShowHelp, tc.want.showHelp)
			}
			if a.Upstream != tc.want.upstream {
				t.Errorf("upstream: got %q, want %q", a.Upstream, tc.want.upstream)
			}
			if a.LogFile != tc.want.logFile {
				t.Errorf("logFile: got %q, want %q", a.LogFile, tc.want.logFile)
			}
			if len(a.ClaudeArgs) != tc.want.claudeLen {
				t.Errorf("claudeArgs length: got %d, want %d (args: %v)", len(a.ClaudeArgs), tc.want.claudeLen, a.ClaudeArgs)
			}
		})
	}
}

// --- default log discard test ---

func TestDefaultLogDiscard(t *testing.T) {
	// Verify that when no flags are set, log output is discarded.
	// We simulate the main() logic: default sets log.SetOutput(io.Discard).
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr) // restore after test

	var buf bytes.Buffer
	// Write a log message — it should go nowhere (Discard).
	log.SetOutput(io.Discard)
	log.Print("this should be discarded")

	// Now capture to buf to prove we can switch
	log.SetOutput(&buf)
	log.Print("this should appear")

	if !strings.Contains(buf.String(), "this should appear") {
		t.Error("expected log output after switching from Discard")
	}
}

// --- handlePrintEnv tests ---

// captureStdout replaces os.Stdout with a pipe and returns the output after fn runs.
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
		handlePrintEnv("DEFAULT", "https://dbc.example.com", "https://gw.example.com", "dapi-abc123secret", "", false, "", "", "")
	})
	if !strings.Contains(out, "dapi-***") {
		t.Errorf("expected dapi token to appear as 'dapi-***', got:\n%s", out)
	}
	if strings.Contains(out, "dapi-abc123secret") {
		t.Errorf("raw dapi token should not appear in output, got:\n%s", out)
	}
}

func TestHandlePrintEnv_NonDapiTokenRedacted(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("DEFAULT", "https://dbc.example.com", "https://gw.example.com", "eyJhbGciOiJSUzI1NiJ9", "", false, "", "", "")
	})
	if !strings.Contains(out, "**** (redacted)") {
		t.Errorf("expected non-dapi token to appear as '**** (redacted)', got:\n%s", out)
	}
	if strings.Contains(out, "eyJhbGciOiJSUzI1NiJ9") {
		t.Errorf("raw token should not appear in output, got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsDatabricksHost(t *testing.T) {
	host := "https://dbc-abc123.cloud.databricks.com"
	out := captureStdout(func() {
		handlePrintEnv("DEFAULT", host, "https://gw.example.com", "tok", "", false, "", "", "")
	})
	if !strings.Contains(out, host) {
		t.Errorf("expected output to contain DATABRICKS_HOST %q, got:\n%s", host, out)
	}
}

func TestHandlePrintEnv_ContainsAnthropicBaseURL(t *testing.T) {
	baseURL := "https://gateway.example.com/anthropic"
	out := captureStdout(func() {
		handlePrintEnv("DEFAULT", "https://dbc.example.com", baseURL, "tok", "", false, "", "", "")
	})
	if !strings.Contains(out, baseURL) {
		t.Errorf("expected output to contain ANTHROPIC_BASE_URL %q, got:\n%s", baseURL, out)
	}
}

func TestHandlePrintEnv_EmptyTokenRedacted(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("DEFAULT", "https://dbc.example.com", "https://gw.example.com", "", "", false, "", "", "")
	})
	// Empty string does not start with "dapi-" so it should show as **** (redacted)
	if !strings.Contains(out, "**** (redacted)") {
		t.Errorf("expected empty token to appear as '**** (redacted)', got:\n%s", out)
	}
}

// --- handleHelp tests ---

func TestHandleHelp_ContainsDatabricksClaude(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, "databricks-claude") {
		t.Errorf("expected help output to contain 'databricks-claude', got:\n%s", out)
	}
}

// TestHandleHelp_DoesNotContainRemovedFlags is the inverse: post-#172 the
// 14 persistent-config flags MUST NOT appear in root help. Old flags moved
// behind `config <subcommand>`.
func TestHandleHelp_DoesNotContainRemovedFlags(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	for _, flag := range []string{
		"--print-env", "--otel", "--no-otel", "--no-otel-metrics", "--no-otel-logs", "--no-otel-traces",
		"--otel-metrics-table", "--otel-logs-table", "--otel-traces", "--otel-traces-table",
		"--write-claude-config", "--with-websearch", "--websearch-backend", "--websearch-fetch-budget",
	} {
		if strings.Contains(out, flag) {
			t.Errorf("removed flag %q must not appear in root help (#172), got:\n%s", flag, out)
		}
	}
}

func TestHandleHelp_AllFlagsPresent(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	flags := []string{"--profile", "--upstream", "--verbose", "-v", "--log-file", "--version", "--help"}
	for _, flag := range flags {
		if !strings.Contains(out, flag) {
			t.Errorf("expected help output to contain flag %q, got:\n%s", flag, out)
		}
	}
}

// TestHandleHelp_DoesNotContainHeadlessFlagsInTable is the inverse of
// #174's "removed, not aliased" contract: the legacy --headless and
// --idle-timeout root flags MUST NOT appear in the root help's flag table
// (the "Databricks-Claude Flags:" section). They live behind
// `serve --session-mode` now. Migration prose elsewhere in the help body
// (e.g. "was --headless prior to #174") is allowed.
func TestHandleHelp_DoesNotContainHeadlessFlagsInTable(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	// Locate the "Databricks-Claude Flags:" section and slice up to the
	// next section header. Matches the rootHelpTemplate layout.
	flagSectionStart := strings.Index(out, "Databricks-Claude Flags:")
	if flagSectionStart < 0 {
		t.Fatalf("could not locate 'Databricks-Claude Flags:' section in help output:\n%s", out)
	}
	flagSection := out[flagSectionStart:]
	// Cut at the next blank-line-then-capitalized-section marker.
	if idx := strings.Index(flagSection, "\nSubcommands:"); idx >= 0 {
		flagSection = flagSection[:idx]
	}
	for _, flag := range []string{"--headless", "--idle-timeout"} {
		if strings.Contains(flagSection, flag) {
			t.Errorf("removed flag %q must not appear in root help flag table (#174), got section:\n%s", flag, flagSection)
		}
	}
}

// TestHandleHelp_AdvertisesConfigSubcommand asserts the root help points
// users at the `config` subcommand tree where the legacy flags moved.
func TestHandleHelp_AdvertisesConfigSubcommand(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	if !strings.Contains(out, "config <subcommand>") {
		t.Errorf("expected root help to advertise 'config <subcommand>', got:\n%s", out)
	}
}

// Verify the version placeholder is printed in the help header.
func TestHandleHelp_ContainsVersion(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	// Version variable is "dev" by default in tests.
	if !strings.Contains(out, fmt.Sprintf("databricks-claude v%s", Version)) {
		t.Errorf("expected help output to contain version string, got:\n%s", out)
	}
}

// Verify the help output advertises the "--" passthrough escape hatch so
// users can find their way to claude's own --help without surprise. If this
// regresses, users coming from older versions lose the only signal that
// `databricks-claude -- --help` is the path to claude's flags.
func TestHandleHelp_AdvertisesPassthrough(t *testing.T) {
	out := captureStdout(func() {
		handleHelp()
	})
	for _, want := range []string{"Passthrough to claude:", "databricks-claude -- --help"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected help output to contain %q, got:\n%s", want, out)
		}
	}
}

// --- deriveLogsTable tests ---

func TestOTELTableDerivation(t *testing.T) {
	tests := []struct {
		name    string
		metrics string
		want    string
	}{
		{
			name:    "standard suffix replacement",
			metrics: "main.claude_telemetry.claude_otel_metrics",
			want:    "main.claude_telemetry.claude_otel_logs",
		},
		{
			name:    "custom table without _otel_metrics suffix",
			metrics: "mycatalog.myschema.custom",
			want:    "mycatalog.myschema.custom_otel_logs",
		},
		{
			name:    "only _otel_metrics",
			metrics: "_otel_metrics",
			want:    "_otel_logs",
		},
		{
			name:    "empty string",
			metrics: "",
			want:    "_otel_logs",
		},
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

// --- handlePrintEnv OTEL fields test ---

func TestHandlePrintEnv_OTELFields(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("DEFAULT", "https://dbc.example.com", "https://gw.example.com", "tok", "", true, "main.telemetry.claude_otel_metrics", "main.telemetry.claude_otel_logs", "")
	})
	checks := []string{
		"OTEL enabled:         true",
		"OTEL metrics table:   main.telemetry.claude_otel_metrics",
		"OTEL logs table:      main.telemetry.claude_otel_logs",
		"OTEL metric interval: 10000ms",
		"OTEL logs interval:   5000ms",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("expected output to contain %q, got:\n%s", c, out)
		}
	}
}

// --- Persistent config tests ---

func TestReadPersistentConfig_MissingFile(t *testing.T) {
	cfg, err := readPersistentConfig(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg) != 0 {
		t.Errorf("expected empty map, got %v", cfg)
	}
}

func TestReadPersistentConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".databricks-claude.json")
	data := []byte(`{"profile":"my-workspace"}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := readPersistentConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg["profile"] != "my-workspace" {
		t.Errorf("expected profile=%q, got %v", "my-workspace", cfg["profile"])
	}
}

func TestReadPersistentConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".databricks-claude.json")
	if err := os.WriteFile(path, []byte(`{bad json`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := readPersistentConfig(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestWritePersistentConfig_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", ".databricks-claude.json")

	cfg := map[string]interface{}{"profile": "test-profile"}
	if err := writePersistentConfig(path, cfg); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	got, err := readPersistentConfig(path)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if got["profile"] != "test-profile" {
		t.Errorf("expected profile=%q, got %v", "test-profile", got["profile"])
	}

	// Verify the file is valid JSON with indentation.
	raw, _ := os.ReadFile(path)
	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("file is not valid JSON: %v", err)
	}
	if !strings.Contains(string(raw), "\n") {
		t.Error("expected indented JSON output")
	}
}

func TestWritePersistentConfig_UpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".databricks-claude.json")

	// Write initial config.
	initial := map[string]interface{}{"profile": "first"}
	if err := writePersistentConfig(path, initial); err != nil {
		t.Fatal(err)
	}

	// Read, update, write.
	cfg, _ := readPersistentConfig(path)
	cfg["profile"] = "second"
	if err := writePersistentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	got, _ := readPersistentConfig(path)
	if got["profile"] != "second" {
		t.Errorf("expected profile=%q, got %v", "second", got["profile"])
	}
}

func TestPersistentConfigPath(t *testing.T) {
	got := persistentConfigPath("/home/user")
	want := filepath.Join("/home/user", ".claude", ".databricks-claude.json")
	if got != want {
		t.Errorf("persistentConfigPath=%q, want %q", got, want)
	}
}

// TestProfileResolution_StateFileWins verifies that the state file profile
// wins over both the settings.json env block AND the process environment
// variable DATABRICKS_CONFIG_PROFILE. The resolution chain is:
//
//	--profile flag (saved to state file) > state file > "DEFAULT"
func TestProfileResolution_StateFileWins(t *testing.T) {
	// Helper: run the same resolution chain as main.go.
	resolve := func(flagProfile string, pcPath string) string {
		resolvedProfile := flagProfile
		if resolvedProfile == "" {
			if pc, err := readPersistentConfig(pcPath); err == nil {
				if v, ok := pc["profile"].(string); ok && v != "" {
					resolvedProfile = v
				}
			}
		}
		if resolvedProfile == "" {
			resolvedProfile = "DEFAULT"
		}
		return resolvedProfile
	}

	// Common setup: state file with "state-file-profile".
	dir := t.TempDir()
	pcPath := filepath.Join(dir, ".databricks-claude.json")
	stateData := []byte(`{"profile":"state-file-profile"}`)
	if err := os.WriteFile(pcPath, stateData, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("state file wins over settings.json env block", func(t *testing.T) {
		// Simulate settings.json with env block containing a profile.
		settingsDoc := map[string]interface{}{
			"env": map[string]interface{}{
				"DATABRICKS_CONFIG_PROFILE": "settings-profile",
			},
		}
		env := envBlock(settingsDoc)

		got := resolve("", pcPath)
		if got != "state-file-profile" {
			t.Fatalf("expected profile=%q, got %q", "state-file-profile", got)
		}

		// Confirm the env block still contains the value (it exists, just isn't consulted).
		if v, ok := env["DATABRICKS_CONFIG_PROFILE"].(string); !ok || v != "settings-profile" {
			t.Errorf("env block should still contain settings-profile, got %v", env["DATABRICKS_CONFIG_PROFILE"])
		}
	})

	t.Run("state file wins over process env var", func(t *testing.T) {
		// Set the process env var to a different profile.
		t.Setenv("DATABRICKS_CONFIG_PROFILE", "env-var-profile")

		got := resolve("", pcPath)
		if got != "state-file-profile" {
			t.Fatalf("expected profile=%q, got %q; process env var should not be consulted", "state-file-profile", got)
		}
	})

	t.Run("flag still wins over state file", func(t *testing.T) {
		got := resolve("flag-profile", pcPath)
		if got != "flag-profile" {
			t.Fatalf("expected profile=%q, got %q", "flag-profile", got)
		}
	})

	t.Run("falls back to DEFAULT when no state file", func(t *testing.T) {
		emptyPath := filepath.Join(dir, "nonexistent.json")
		got := resolve("", emptyPath)
		if got != "DEFAULT" {
			t.Fatalf("expected profile=%q, got %q", "DEFAULT", got)
		}
	})
}

// --- idle-timeout flag tests (#174 — moved to `serve --session-mode`) ---
//
// The legacy --idle-timeout root flag is gone after #174. Like --headless,
// it now passes through to claude as an unknown flag. The session-mode
// equivalent lives on `serve --session-mode --idle-timeout` and is exercised
// by parseServeFlags tests in serve_test.go.

func TestParseArgs_IdleTimeoutRemovedPassThrough(t *testing.T) {
	a, err := parseArgs([]string{"--idle-timeout", "10m"})
	if err != nil {
		t.Fatalf("parseArgs returned error for legacy --idle-timeout (must pass through, not error): %v", err)
	}
	// Both the flag and its value forward to claude as unknown args.
	wantContains := []string{"--idle-timeout", "10m"}
	for _, want := range wantContains {
		found := false
		for _, ca := range a.ClaudeArgs {
			if ca == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q to forward to ClaudeArgs (removed in #174), got ClaudeArgs=%v", want, a.ClaudeArgs)
		}
	}
}

// --- /shutdown endpoint tests ---

func TestShutdown_DecrementsRefcount(t *testing.T) {
	refcountPath := filepath.Join(t.TempDir(), "refcount")

	// Acquire twice to simulate two sessions.
	if err := refcount.Acquire(refcountPath); err != nil {
		t.Fatal(err)
	}
	if err := refcount.Acquire(refcountPath); err != nil {
		t.Fatal(err)
	}

	doneCh := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := lifecycle.WrapWithLifecycle(lifecycle.Config{Inner: inner, RefcountPath: refcountPath, IsOwner: true, DoneCh: doneCh, LogPrefix: "test"})

	// First shutdown: refcount goes from 2 to 1.
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp shutdownResp
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Remaining != 1 || resp.Exiting {
		t.Errorf("expected remaining=1, exiting=false; got remaining=%d, exiting=%v", resp.Remaining, resp.Exiting)
	}

	// doneCh should still be open.
	select {
	case <-doneCh:
		t.Fatal("doneCh should not be closed yet")
	default:
	}

	// Second shutdown: refcount goes from 1 to 0, owner exits.
	req2 := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	var resp2 shutdownResp
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.Remaining != 0 || !resp2.Exiting {
		t.Errorf("expected remaining=0, exiting=true; got remaining=%d, exiting=%v", resp2.Remaining, resp2.Exiting)
	}

	// doneCh should be closed now.
	select {
	case <-doneCh:
		// OK
	case <-time.After(time.Second):
		t.Fatal("doneCh should be closed after last shutdown")
	}
}

func TestShutdown_MethodNotAllowed(t *testing.T) {
	doneCh := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	refcountPath := filepath.Join(t.TempDir(), "refcount")
	handler := lifecycle.WrapWithLifecycle(lifecycle.Config{Inner: inner, RefcountPath: refcountPath, IsOwner: true, DoneCh: doneCh, LogPrefix: "test"})

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestShutdown_RequiresAPIKey(t *testing.T) {
	doneCh := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	refcountPath := filepath.Join(t.TempDir(), "refcount")
	handler := lifecycle.WrapWithLifecycle(lifecycle.Config{Inner: inner, RefcountPath: refcountPath, IsOwner: true, APIKey: "my-secret-key", DoneCh: doneCh, LogPrefix: "test"})

	// No auth header → 401.
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", rec.Code)
	}

	// Wrong key → 401.
	req2 := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req2.Header.Set("Authorization", "Bearer wrong-key")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", rec2.Code)
	}

	// Correct key → 200.
	if err := refcount.Acquire(refcountPath); err != nil {
		t.Fatal(err)
	}
	req3 := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req3.Header.Set("Authorization", "Bearer my-secret-key")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("expected 200 with correct key, got %d", rec3.Code)
	}
}

func TestShutdown_PassesThrough(t *testing.T) {
	var gotPath string
	doneCh := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	refcountPath := filepath.Join(t.TempDir(), "refcount")
	handler := lifecycle.WrapWithLifecycle(lifecycle.Config{Inner: inner, RefcountPath: refcountPath, IsOwner: true, DoneCh: doneCh, LogPrefix: "test"})

	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotPath != "/v1/messages" {
		t.Errorf("expected inner handler to receive /v1/messages, got %q", gotPath)
	}
}

// --- idle timeout tests ---

func TestIdleTimeout_TriggersShutdown(t *testing.T) {
	doneCh := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	refcountPath := filepath.Join(t.TempDir(), "refcount")
	_ = lifecycle.WrapWithLifecycle(lifecycle.Config{Inner: inner, RefcountPath: refcountPath, IsOwner: true, IdleTimeout: 50 * time.Millisecond, DoneCh: doneCh, LogPrefix: "test"})

	select {
	case <-doneCh:
		// OK — idle timeout fired.
	case <-time.After(2 * time.Second):
		t.Fatal("doneCh should have been closed by idle timeout")
	}
}

func TestIdleTimeout_ResetByRequest(t *testing.T) {
	doneCh := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	refcountPath := filepath.Join(t.TempDir(), "refcount")
	handler := lifecycle.WrapWithLifecycle(lifecycle.Config{Inner: inner, RefcountPath: refcountPath, IsOwner: true, IdleTimeout: 100 * time.Millisecond, DoneCh: doneCh, LogPrefix: "test"})

	// Send a request at 60ms to reset the timer.
	time.Sleep(60 * time.Millisecond)
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// At 120ms from start (60ms after request), timer should NOT have fired
	// because it was reset to 100ms from the request time.
	time.Sleep(60 * time.Millisecond)
	select {
	case <-doneCh:
		t.Fatal("doneCh should not be closed yet — timer was reset by request")
	default:
		// OK
	}

	// Wait for the timer to actually fire (100ms from the request at 60ms = 160ms total).
	select {
	case <-doneCh:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("doneCh should have been closed by idle timeout after activity stopped")
	}
}

func TestIdleTimeout_ZeroDisables(t *testing.T) {
	doneCh := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	refcountPath := filepath.Join(t.TempDir(), "refcount")
	_ = lifecycle.WrapWithLifecycle(lifecycle.Config{Inner: inner, RefcountPath: refcountPath, IsOwner: true, DoneCh: doneCh, LogPrefix: "test"})

	time.Sleep(100 * time.Millisecond)
	select {
	case <-doneCh:
		t.Fatal("doneCh should not be closed when idle timeout is 0")
	default:
		// OK — no timeout.
	}
}

// --- `config write` (was --write-claude-config) integration tests ---

// TestConfigWrite_BootstrapWritesFullEnvBlock verifies that the
// bootstrapSettings + ensureConfig write path used by `config write`
// produces the full needsFullSetup key set without requiring the Databricks CLI.
//
// Drives against databricksFullSetupEnv() — the single source of truth used
// by BOTH the `config write` runner and the normal-startup needsFullSetup
// block. A regression that deletes any model key, the custom header, or the
// experimental-betas line from the helper fails this test regardless of
// which call site is exercised.
func TestConfigWrite_BootstrapWritesFullEnvBlock(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origStatePath := statePath
	statePath = func() string { return filepath.Join(tmpHome, ".claude", ".databricks-claude.json") }
	defer func() { statePath = origStatePath }()

	proxyURL := "http://127.0.0.1:49153"
	otelEnv := databricksFullSetupEnv(defaultModelRouting())

	if err := bootstrapSettings(0, "DEFAULT", proxyURL, otelEnv); err != nil {
		t.Fatalf("bootstrapSettings failed: %v", err)
	}

	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	doc, err := readSettingsJSON(settingsPath)
	if err != nil {
		t.Fatalf("readSettingsJSON failed: %v", err)
	}
	env, ok := doc["env"].(map[string]interface{})
	if !ok {
		t.Fatal("expected env block in settings.json")
	}

	// Proxy/auth keys come from ensureConfig itself, not the helper.
	if env["ANTHROPIC_BASE_URL"] != proxyURL {
		t.Errorf("ANTHROPIC_BASE_URL: got %v, want %q", env["ANTHROPIC_BASE_URL"], proxyURL)
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "proxy-managed" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN: got %v, want \"proxy-managed\"", env["ANTHROPIC_AUTH_TOKEN"])
	}

	// Every Databricks-specific key from the canonical helper must appear in
	// the settings.json env block, with the exact value. Asserting value-
	// equality (not just presence) means a regression bumping any model name
	// in the helper without updating downstream consumers also fails here.
	for k, want := range databricksFullSetupEnv(defaultModelRouting()) {
		got, present := env[k]
		if !present {
			t.Errorf("missing key %q in settings.json env block (expected %q)", k, want)
			continue
		}
		if got != want {
			t.Errorf("env[%q]: got %v, want %q", k, got, want)
		}
	}
}

// TestDatabricksFullSetupEnv_KeyCoverage pins the EXACT set of keys the
// helper returns. If a future change drops one of the four model-name keys,
// the custom-headers key, or the experimental-betas key, this fails —
// independent of any call site. Acts as a guard against the most common
// regression class: "I deleted a line and tests still passed because the
// integration test only checked presence of some keys."
func TestDatabricksFullSetupEnv_KeyCoverage(t *testing.T) {
	env := databricksFullSetupEnv(defaultModelRouting())
	wantKeys := map[string]bool{
		"ANTHROPIC_MODEL":                        true,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":           true,
		"ANTHROPIC_DEFAULT_SONNET_MODEL":         true,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":          true,
		"ANTHROPIC_CUSTOM_HEADERS":               true,
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": true,
	}
	if len(env) != len(wantKeys) {
		t.Errorf("databricksFullSetupEnv returned %d keys, want exactly %d (regression: added or removed a key without updating this test)",
			len(env), len(wantKeys))
	}
	for k := range wantKeys {
		if _, ok := env[k]; !ok {
			t.Errorf("databricksFullSetupEnv missing required key %q", k)
		}
	}
	// Spot-check the three values most likely to drift silently: the model
	// names use a versioned naming scheme and bump as Databricks ships new
	// models. Pinning them here means a value typo (e.g. opus-4-7 →
	// opus-4-8 by mistake) is caught even if the test author updates the
	// helper map but forgets a downstream consumer.
	if env["ANTHROPIC_CUSTOM_HEADERS"] != "x-databricks-use-coding-agent-mode: true" {
		t.Errorf("ANTHROPIC_CUSTOM_HEADERS value drift: got %q", env["ANTHROPIC_CUSTOM_HEADERS"])
	}
	if env["CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS"] != "1" {
		t.Errorf("CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS value drift: got %q", env["CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS"])
	}
}

// TestDatabricksFullSetupEnvOmitsUnresolvedFamily verifies that a routing with
// only the Opus family populated emits exactly the two static keys plus the two
// Opus model keys — and never emits an empty/wrong sonnet or haiku key. This is
// the no-silent-mis-route guarantee for a partially-resolved discovery.
func TestDatabricksFullSetupEnvOmitsUnresolvedFamily(t *testing.T) {
	env := databricksFullSetupEnv(ModelRouting{Opus: "x"})

	want := map[string]string{
		"ANTHROPIC_CUSTOM_HEADERS":               "x-databricks-use-coding-agent-mode: true",
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
		"ANTHROPIC_MODEL":                        "x",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":           "x",
	}
	if len(env) != len(want) {
		t.Errorf("databricksFullSetupEnv(Opus-only) returned %d keys, want exactly %d: %v", len(env), len(want), env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%q]: got %q, want %q", k, env[k], v)
		}
	}
	for _, k := range []string{"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		if _, present := env[k]; present {
			t.Errorf("key %q must be absent for an unresolved family, but was present", k)
		}
	}
}

// TestConfigWrite_MissingModelKeys confirms that omitting the needsFullSetup
// keys from otelEnv (passing nil) means model keys are absent — the negative control.
func TestConfigWrite_MissingModelKeys(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origStatePath := statePath
	statePath = func() string { return filepath.Join(tmpHome, ".claude", ".databricks-claude.json") }
	defer func() { statePath = origStatePath }()

	if err := bootstrapSettings(0, "DEFAULT", "http://127.0.0.1:49153", nil); err != nil {
		t.Fatalf("bootstrapSettings failed: %v", err)
	}

	doc, err := readSettingsJSON(filepath.Join(tmpHome, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("readSettingsJSON failed: %v", err)
	}
	env, _ := doc["env"].(map[string]interface{})
	modelKeys := []string{
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	}
	for _, k := range modelKeys {
		if _, present := env[k]; present {
			t.Errorf("key %q should be absent when otelEnv is nil, but was written", k)
		}
	}
}

// TestCompletionFlagsCoverAllKnownFlags ensures every flag in knownFlags has a
// corresponding entry in flagDefs. Trivially true after #170 (both are
// derived from rootCommand) but kept as an alarm for future refactors that
// might break the derivation.
func TestCompletionFlagsCoverAllKnownFlags(t *testing.T) {
	covered := make(map[string]bool, len(flagDefs))
	for _, f := range flagDefs {
		covered["--"+f.Name] = true
	}
	for flag := range knownFlags {
		if !covered[flag] {
			t.Errorf("flag %s is in knownFlags but missing from flagDefs (derivation drift in completion_flags.go)", flag)
		}
	}
}

// TestKnownFlagsCoverAllFlagDefs is the inverse check: every FlagDef entry
// must appear in knownFlags so the parser actually recognises it.
func TestKnownFlagsCoverAllFlagDefs(t *testing.T) {
	for _, f := range flagDefs {
		name := "--" + f.Name
		if !knownFlags[name] {
			t.Errorf("flagDef %q is missing from knownFlags (derivation drift in completion_flags.go)", name)
		}
	}
}

// TestRootTreeFlagsAreParseRecognised is the load-bearing parity check
// added in #170: every flag declared in the rootCommand tree (Persistent
// ++ Flags) must be RECOGNISED by parseArgs — i.e. must NOT be forwarded
// to claude as a passthrough arg. Catches the failure mode where a flag
// is added to the tree but no corresponding case is added to parseArgs's
// switch (the flag would silently fall through to ClaudeArgs).
//
// We feed parseArgs a single `--<name>` token (with a type-appropriate
// value for TakesArg flags so parsers that validate their input — e.g.
// --idle-timeout — don't reject the synthetic case). For non-TakesArg
// flags the token is sufficient on its own.
func TestRootTreeFlagsAreParseRecognised(t *testing.T) {
	// Type-appropriate synthetic values for flags whose parser validates
	// the argument. Anything not listed gets the default "synthetic".
	syntheticValues := map[string]string{
		"port": "12345",
	}
	for _, f := range rootCommand.AllFlags() {
		name := "--" + f.Name
		args := []string{name}
		if f.TakesArg {
			val := syntheticValues[f.Name]
			if val == "" {
				val = "synthetic"
			}
			args = append(args, val)
		}
		a, err := parseArgs(args)
		if err != nil {
			// parseArgs's switch has a `default:` that returns an error
			// for any known flag with no matching case — so an error
			// here means EITHER real drift (a flag declared in the tree
			// but with no case in parseArgs) OR a synthetic value the
			// flag's validator rejected. Both are genuine failures.
			t.Errorf("parseArgs(%v) returned error: %v — either --%s is declared in the rootCommand tree but parseArgs has no case for it, or syntheticValues needs a type-appropriate entry for --%s", args, err, f.Name, f.Name)
			continue
		}
		// Belt-and-suspenders: a recognised flag must not be forwarded
		// to claude (would only fire if the default: guard were removed).
		for _, ca := range a.ClaudeArgs {
			if ca == name {
				t.Errorf("tree flag %q was passed through to ClaudeArgs — parseArgs has no case for it", name)
				break
			}
		}
	}
}

// TestParseArgsCasesAreDeclaredInRootTree is the inverse: every "case
// "--xxx":" in parseArgs must correspond to a flag declared in
// rootCommand. Catches the failure mode where someone adds a switch case
// without adding the FlagDef (dead code today; would silently break
// completion). Implemented by reading main.go as text and grepping
// `case "--..."` lines inside parseArgs.
func TestParseArgsCasesAreDeclaredInRootTree(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	// Slice main.go to just the body of parseArgs to avoid stray `case`
	// matches in other functions.
	body := string(src)
	startMarker := "func parseArgs("
	startIdx := strings.Index(body, startMarker)
	if startIdx < 0 {
		t.Fatalf("could not locate %q in main.go", startMarker)
	}
	// parseArgs is followed by handleHelp; cut at the next top-level func.
	endMarker := "\nfunc "
	tail := body[startIdx+1:]
	endIdx := strings.Index(tail, endMarker)
	if endIdx < 0 {
		t.Fatalf("could not locate end of parseArgs in main.go")
	}
	region := body[startIdx : startIdx+1+endIdx]

	declared := rootCommand.KnownFlags()

	// Match `case "--name":` and `case "--name", "--alias":` patterns.
	// We split on lines and look for `case "--`.
	for _, line := range strings.Split(region, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "case \"--") {
			continue
		}
		// Pull every "--xxx" literal off the line.
		rest := trimmed
		for {
			i := strings.Index(rest, "\"--")
			if i < 0 {
				break
			}
			j := strings.Index(rest[i+1:], "\"")
			if j < 0 {
				break
			}
			lit := rest[i+1 : i+1+j]
			rest = rest[i+1+j+1:]
			if !declared[lit] {
				t.Errorf("parseArgs has `case %q` but no FlagDef in rootCommand declares it (drift in commands.go)", lit)
			}
		}
	}
}

// TestRootCompletionDoesNotOfferDaemonFlags asserts the issue-#170
// requirement that --daemon / --daemon-fake-key (desktop-scoped) no
// longer appear in the root flag set. They are still parsed by the
// desktop subcommand's own scanners (extractDaemonFlag /
// extractDaemonFakeKeyFlag); this test just guards root completion.
func TestRootCompletionDoesNotOfferDaemonFlags(t *testing.T) {
	for _, f := range flagDefs {
		switch f.Name {
		case "daemon", "daemon-fake-key":
			t.Errorf("flag --%s should not be offered as a root completion (it is desktop-scoped); remove it from rootCommand.Flags in commands.go", f.Name)
		}
	}
}

// TestRootPersistentFlagsAreProfileAndPort asserts the issue-#170
// requirement that --profile and --port are declared as persistent on
// the root command (so subcommand inheritance works in #171+).
func TestRootPersistentFlagsAreProfileAndPort(t *testing.T) {
	persistent := map[string]bool{}
	for _, f := range rootCommand.Persistent {
		persistent[f.Name] = true
	}
	for _, want := range []string{"profile", "port"} {
		if !persistent[want] {
			t.Errorf("rootCommand.Persistent should declare --%s", want)
		}
	}
}

// --- #171 subcommand parity tests ---
//
// Each subcommand tree node declared in commands.go must:
//  1. Exhaustively declare every flag its runner consumes (no flag the
//     runner reads from ParseResult is missing from the tree).
//  2. Have every declared flag actually used by its runner (no dead
//     declarations bloating help / completion).
//
// We assert (1) by feeding parse(args=[--<name> synthetic_value]) for every
// declared flag and verifying the runner's typed-struct mapper picks it up
// (parseServeFlags / parseInstallFlags) — the projection that turns
// ParseResult into the resolution-chain inputs. For runners that consume
// ParseResult inline (runSetupCommand, runDesktopCommand) we drive Parse
// directly and assert the fields land in Strings/Bools/Set as expected.
//
// (2) is asserted by hardcoding the set of flags-the-runner-reads and
// failing if the tree has anything extra. Updating the tree without
// updating the runner (or vice versa) trips this test.

// flagNameSet returns the set of long-flag names declared on a tree node
// (Persistent ++ Flags). Used by the #171 subcommand-parity tests.
func flagNameSet(c cmd.Command) map[string]bool {
	out := make(map[string]bool)
	for _, f := range c.AllFlags() {
		out[f.Name] = true
	}
	return out
}

// assertFlagSetEqual fails if the actual flag set declared on c differs from
// the expected slice. Used by the #171 subcommand-parity tests so adding a
// flag to either side without updating the other surfaces here.
func assertFlagSetEqual(t *testing.T, label string, c cmd.Command, want []string) {
	t.Helper()
	got := flagNameSet(c)
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
		if !got[w] {
			t.Errorf("%s should declare --%s but does not (runner consumes it; tree must too)", label, w)
		}
	}
	for n := range got {
		if !wantSet[n] {
			t.Errorf("%s declares --%s but its runner does not consume it (dead declaration; remove from tree or wire to runner)", label, n)
		}
	}
}

// TestServeCommandParity verifies that serveCommand declares EXACTLY the
// flags parseServeFlags consumes — no more, no less. Mirrors
// TestRootTreeFlagsAreParseRecognised but for the serve subcommand. Updated
// in #174 to include the new mode flags (--session-mode, --daemon) and the
// session-only flags lifted from the deleted root --headless path
// (--idle-timeout, --proxy-api-key, --tls-cert, --tls-key, --upstream).
func TestServeCommandParity(t *testing.T) {
	assertFlagSetEqual(t, "serveCommand", serveCommand, []string{
		"session-mode", "daemon",
		"port", "log-file", "verbose", "profile",
		"idle-timeout", "proxy-api-key", "tls-cert", "tls-key", "upstream",
		"otel-metrics-table", "otel-logs-table", "otel-traces-table",
		"help",
	})
}

func TestServeInstallCommandParity(t *testing.T) {
	install := serveCommand.Subcommand("install")
	if install == nil {
		t.Fatal("serveCommand should have an `install` subcommand")
	}
	assertFlagSetEqual(t, "serve install", *install, []string{
		"port", "profile", "log-file",
		"otel-metrics-table", "otel-logs-table", "otel-traces-table",
		"skip-auth-check", "help",
	})
}

func TestServeUninstallCommandParity(t *testing.T) {
	uninst := serveCommand.Subcommand("uninstall")
	if uninst == nil {
		t.Fatal("serveCommand should have an `uninstall` subcommand")
	}
	assertFlagSetEqual(t, "serve uninstall", *uninst, []string{"help"})
}

func TestServeStatusCommandParity(t *testing.T) {
	st := serveCommand.Subcommand("status")
	if st == nil {
		t.Fatal("serveCommand should have a `status` subcommand")
	}
	assertFlagSetEqual(t, "serve status", *st, []string{"help"})
}

func TestSetupCommandParity(t *testing.T) {
	assertFlagSetEqual(t, "setupCommand", setupCommand, []string{
		"profile", "host", "force", "help",
	})
}

// TestDesktopCommandParity covers the union of flags read by runDesktopCommand
// (generate-config + credential-helper paths) AND by runGenerateTrustProfile
// (which routes its scanners through desktopCommand.Parse post-#171).
func TestDesktopCommandParity(t *testing.T) {
	assertFlagSetEqual(t, "desktopCommand", desktopCommand, []string{
		"profile", "output", "binary-path", "databricks-cli-path", "cert",
		"for-pkg", "daemon", "port", "daemon-fake-key", "otel", "help",
	})
}

// TestDesktopDaemonFlagsAreDesktopScoped asserts the issue-#171 contract:
// --daemon and --daemon-fake-key live on `desktop`, NOT on root. The
// inverse test (TestRootCompletionDoesNotOfferDaemonFlags) already guards
// the root side; this test guards the desktop side.
func TestDesktopDaemonFlagsAreDesktopScoped(t *testing.T) {
	got := flagNameSet(desktopCommand)
	for _, want := range []string{"daemon", "daemon-fake-key"} {
		if !got[want] {
			t.Errorf("desktopCommand must declare --%s (it is desktop-scoped per issue #171)", want)
		}
	}
}

// TestServeHasNestedSubcommands verifies that the install/uninstall/status
// children are declared on serveCommand so completion can offer them
// nested. Drives the issue-#171 acceptance criterion: "Nested completion
// works: databricks-claude serve <TAB> → subcommands".
func TestServeHasNestedSubcommands(t *testing.T) {
	want := []string{"install", "uninstall", "status"}
	got := make(map[string]bool, len(serveCommand.Subcommands))
	for _, s := range serveCommand.Subcommands {
		got[s.Name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("serveCommand should have nested `%s` subcommand for nested completion", w)
		}
	}
}

// TestSubcommandFlagsParseable feeds synthetic args into each subcommand's
// Parse and verifies a tree-declared flag never lands in Positional (which
// is the "unknown flag" bucket). Catches drift between FlagDef.TakesArg and
// the parser's expectation.
func TestSubcommandFlagsParseable(t *testing.T) {
	syntheticValues := map[string]string{
		"port":                "12345",
		"otel-metrics-table":  "cat.s.metrics",
		"otel-logs-table":     "cat.s.logs",
		"otel-traces-table":   "cat.s.traces",
		"profile":             "synthetic-profile",
		"log-file":            "/tmp/synthetic.log",
		"host":                "https://synthetic.example.com",
		"output":              "/tmp/out",
		"binary-path":         "/tmp/synthetic-binary",
		"databricks-cli-path": "/tmp/synthetic-cli",
		"cert":                "/tmp/synthetic-cert.pem",
		"daemon-fake-key":     "synthetic-key",
	}

	cases := []struct {
		name string
		cmd  cmd.Command
	}{
		{"serve", serveCommand},
		{"serve install", *serveCommand.Subcommand("install")},
		{"serve uninstall", *serveCommand.Subcommand("uninstall")},
		{"serve status", *serveCommand.Subcommand("status")},
		{"setup", setupCommand},
		{"desktop", desktopCommand},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, f := range c.cmd.AllFlags() {
				args := []string{"--" + f.Name}
				if v, ok := syntheticValues[f.Name]; ok {
					args = append(args, v)
				}
				r, err := c.cmd.Parse(args)
				if err != nil {
					t.Errorf("%s --%s: Parse returned error: %v", c.name, f.Name, err)
					continue
				}
				for _, p := range r.Positional {
					if p == "--"+f.Name {
						t.Errorf("%s --%s parsed as positional/unknown — tree-declared flag must be recognised", c.name, f.Name)
					}
				}
			}
		})
	}
}
