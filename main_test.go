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

	"github.com/IceRhymers/databricks-claude/pkg/lifecycle"
	"github.com/IceRhymers/databricks-claude/pkg/refcount"
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
	if a.Profile != "" || a.Verbose || a.Version || a.PrintEnv || a.OTEL || a.Upstream != "" || a.LogFile != "" || a.NoOTEL || len(a.ClaudeArgs) != 0 {
		t.Error("unexpected non-default values alongside --help")
	}
}

func TestParseArgs_HelpShort(t *testing.T) {
	a, _ := parseArgs([]string{"-h"})
	if !a.ShowHelp {
		t.Error("expected showHelp=true for -h")
	}
}

func TestParseArgs_PrintEnv(t *testing.T) {
	a, _ := parseArgs([]string{"--print-env"})
	if !a.PrintEnv {
		t.Error("expected printEnv=true for --print-env")
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

func TestParseArgs_Otel(t *testing.T) {
	a, _ := parseArgs([]string{"--otel"})
	if !a.OTEL {
		t.Error("expected otel=true for --otel")
	}
}

func TestParseArgs_OtelMetricsTableOverride(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-metrics-table", "main.default.otel"})
	if !a.OTELMetricsTableSet {
		t.Error("expected metricsTableSet=true when --otel-metrics-table is passed")
	}
	if a.OTELMetricsTable != "main.default.otel" {
		t.Errorf("expected metricsTable=%q, got %q", "main.default.otel", a.OTELMetricsTable)
	}
}

func TestParseArgs_OtelMetricsTableDefault(t *testing.T) {
	a, _ := parseArgs([]string{"--otel"})
	if a.OTELMetricsTableSet {
		t.Error("expected metricsTableSet=false when --otel-metrics-table is not passed")
	}
	if a.OTELMetricsTable != "main.claude_telemetry.claude_otel_metrics" {
		t.Errorf("expected default metricsTable, got %q", a.OTELMetricsTable)
	}
}

func TestParseArgs_OtelMetricsTableEquals(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-metrics-table=my.catalog.table"})
	if !a.OTELMetricsTableSet {
		t.Error("expected metricsTableSet=true for --otel-metrics-table=value")
	}
	if a.OTELMetricsTable != "my.catalog.table" {
		t.Errorf("expected metricsTable=%q, got %q", "my.catalog.table", a.OTELMetricsTable)
	}
}

func TestParseArgs_OtelLogsTableOverride(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-logs-table", "main.default.my_logs"})
	if !a.OTELLogsTableSet {
		t.Error("expected logsTableSet=true when --otel-logs-table is passed")
	}
	if a.OTELLogsTable != "main.default.my_logs" {
		t.Errorf("expected logsTable=%q, got %q", "main.default.my_logs", a.OTELLogsTable)
	}
}

func TestParseArgs_OtelLogsTableDefault(t *testing.T) {
	a, _ := parseArgs([]string{"--otel"})
	if a.OTELLogsTableSet {
		t.Error("expected logsTableSet=false when --otel-logs-table is not passed")
	}
	if a.OTELLogsTable != "" {
		t.Errorf("expected empty logsTable default, got %q", a.OTELLogsTable)
	}
}

func TestParseArgs_OtelLogsTableEquals(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-logs-table=my.catalog.logs"})
	if !a.OTELLogsTableSet {
		t.Error("expected logsTableSet=true for --otel-logs-table=value")
	}
	if a.OTELLogsTable != "my.catalog.logs" {
		t.Errorf("expected logsTable=%q, got %q", "my.catalog.logs", a.OTELLogsTable)
	}
}

func TestParseArgs_BothOtelTables(t *testing.T) {
	a, _ := parseArgs([]string{
		"--otel-metrics-table", "cat.schema.metrics",
		"--otel-logs-table", "cat.schema.logs",
	})
	if !a.OTELMetricsTableSet || !a.OTELLogsTableSet {
		t.Error("expected both table flags to be set")
	}
	if a.OTELMetricsTable != "cat.schema.metrics" {
		t.Errorf("metricsTable=%q, want cat.schema.metrics", a.OTELMetricsTable)
	}
	if a.OTELLogsTable != "cat.schema.logs" {
		t.Errorf("logsTable=%q, want cat.schema.logs", a.OTELLogsTable)
	}
}

func TestParseArgs_UnknownFlagPassthrough(t *testing.T) {
	a, _ := parseArgs([]string{"--unknown"})
	if len(a.ClaudeArgs) != 1 || a.ClaudeArgs[0] != "--unknown" {
		t.Errorf("expected claudeArgs=[\"--unknown\"], got %v", a.ClaudeArgs)
	}
}

func TestParseArgs_WithWebSearch(t *testing.T) {
	a, err := parseArgs([]string{"--with-websearch"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !a.WithWebSearch {
		t.Errorf("expected WithWebSearch=true")
	}
	if !a.WithWebSearchSet {
		t.Errorf("expected WithWebSearchSet=true when --with-websearch is passed")
	}
}

func TestParseArgs_WithWebSearchDefault(t *testing.T) {
	a, _ := parseArgs([]string{})
	if a.WithWebSearch {
		t.Errorf("expected WithWebSearch=false by default")
	}
	if a.WithWebSearchSet {
		t.Errorf("expected WithWebSearchSet=false when --with-websearch is not passed")
	}
}

func TestParseArgs_WebSearchBackend(t *testing.T) {
	a, err := parseArgs([]string{"--websearch-backend", "duckduckgo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.WebSearchBackend != "duckduckgo" {
		t.Errorf("expected WebSearchBackend=duckduckgo, got %q", a.WebSearchBackend)
	}
	if !a.WebSearchBackendSet {
		t.Errorf("expected WebSearchBackendSet=true")
	}
}

func TestParseArgs_WebSearchBackendEquals(t *testing.T) {
	a, _ := parseArgs([]string{"--websearch-backend=none"})
	if a.WebSearchBackend != "none" {
		t.Errorf("expected WebSearchBackend=none, got %q", a.WebSearchBackend)
	}
	if !a.WebSearchBackendSet {
		t.Errorf("expected WebSearchBackendSet=true")
	}
}

func TestParseArgs_WebSearchFetchBudget(t *testing.T) {
	a, err := parseArgs([]string{"--websearch-fetch-budget", "204800"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.WebSearchFetchBudget != 204800 {
		t.Errorf("expected WebSearchFetchBudget=204800, got %d", a.WebSearchFetchBudget)
	}
	if !a.WebSearchFetchBudgetSet {
		t.Errorf("expected WebSearchFetchBudgetSet=true")
	}
}

func TestParseArgs_WebSearchFetchBudgetEquals(t *testing.T) {
	a, _ := parseArgs([]string{"--websearch-fetch-budget=51200"})
	if a.WebSearchFetchBudget != 51200 {
		t.Errorf("expected WebSearchFetchBudget=51200, got %d", a.WebSearchFetchBudget)
	}
}

func TestParseArgs_AllWebSearchFlagsTogether(t *testing.T) {
	a, _ := parseArgs([]string{"--with-websearch", "--websearch-backend", "duckduckgo", "--websearch-fetch-budget", "65536"})
	if !a.WithWebSearch {
		t.Errorf("expected WithWebSearch=true")
	}
	if a.WebSearchBackend != "duckduckgo" {
		t.Errorf("expected WebSearchBackend=duckduckgo, got %q", a.WebSearchBackend)
	}
	if a.WebSearchFetchBudget != 65536 {
		t.Errorf("expected WebSearchFetchBudget=65536, got %d", a.WebSearchFetchBudget)
	}
}

func TestParseArgs_EmptyArgs(t *testing.T) {
	a, _ := parseArgs([]string{})
	if a.Profile != "" {
		t.Errorf("expected empty profile, got %q", a.Profile)
	}
	if a.Verbose || a.Version || a.ShowHelp || a.PrintEnv || a.OTEL || a.NoOTEL {
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
	// OTELMetricsTable should have the default value
	if a.OTELMetricsTable == "" {
		t.Error("expected non-empty default otelMetricsTable")
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

func TestParseArgs_Headless(t *testing.T) {
	a, _ := parseArgs([]string{"--headless"})
	if !a.Headless {
		t.Error("expected headless=true for --headless")
	}
}

func TestParseArgs_NoUpdateCheck(t *testing.T) {
	a, _ := parseArgs([]string{"--no-update-check"})
	if !a.NoUpdateCheck {
		t.Error("expected noUpdateCheck=true for --no-update-check")
	}
}

func TestParseArgs_HeadlessWithOtherFlags(t *testing.T) {
	a, _ := parseArgs([]string{"--headless", "--verbose"})
	if !a.Headless {
		t.Error("expected headless=true")
	}
	if !a.Verbose {
		t.Error("expected verbose=true")
	}
}

func TestParseArgs_NoOtel(t *testing.T) {
	a, _ := parseArgs([]string{"--no-otel"})
	if !a.NoOTEL {
		t.Error("expected noOtel=true for --no-otel")
	}
	if a.OTEL {
		t.Error("expected otel=false when only --no-otel given")
	}
	if len(a.ClaudeArgs) != 0 {
		t.Errorf("expected no claudeArgs, got %v", a.ClaudeArgs)
	}
}

func TestParseArgs_NoOtelAndOtel(t *testing.T) {
	a, _ := parseArgs([]string{"--no-otel", "--otel"})
	if !a.NoOTEL {
		t.Error("expected noOtel=true")
	}
	if !a.OTEL {
		t.Error("expected otel=true (both flags can coexist; main() handles precedence)")
	}
}

func TestParseArgs_NoOtelWithPassthrough(t *testing.T) {
	a, _ := parseArgs([]string{"--no-otel", "somearg"})
	if !a.NoOTEL {
		t.Error("expected noOtel=true")
	}
	if len(a.ClaudeArgs) != 1 || a.ClaudeArgs[0] != "somearg" {
		t.Errorf("expected claudeArgs=[\"somearg\"], got %v", a.ClaudeArgs)
	}
}

func TestParseArgs_OtelUnaffectedByNoOtel(t *testing.T) {
	a, _ := parseArgs([]string{"--otel"})
	if !a.OTEL {
		t.Error("expected otel=true for --otel")
	}
	if a.NoOTEL {
		t.Error("expected noOtel=false when only --otel given")
	}
}

// --- per-signal no-otel-* flag tests ---

func TestParseArgs_NoOtelMetrics(t *testing.T) {
	a, _ := parseArgs([]string{"--no-otel-metrics"})
	if !a.NoOTELMetrics {
		t.Error("expected noOtelMetrics=true for --no-otel-metrics")
	}
	if a.NoOTELLogs || a.NoOTELTraces {
		t.Error("expected noOtelLogs/noOtelTraces=false when only --no-otel-metrics given")
	}
}

func TestParseArgs_NoOtelLogs(t *testing.T) {
	a, _ := parseArgs([]string{"--no-otel-logs"})
	if !a.NoOTELLogs {
		t.Error("expected noOtelLogs=true for --no-otel-logs")
	}
	if a.NoOTELMetrics || a.NoOTELTraces {
		t.Error("expected noOtelMetrics/noOtelTraces=false when only --no-otel-logs given")
	}
}

func TestParseArgs_NoOtelTraces(t *testing.T) {
	a, _ := parseArgs([]string{"--no-otel-traces"})
	if !a.NoOTELTraces {
		t.Error("expected noOtelTraces=true for --no-otel-traces")
	}
	if a.NoOTELMetrics || a.NoOTELLogs {
		t.Error("expected noOtelMetrics/noOtelLogs=false when only --no-otel-traces given")
	}
}

// --- --otel-traces flag tests ---

func TestParseArgs_OtelTraces(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-traces"})
	if !a.OTELTraces {
		t.Error("expected otelTraces=true for --otel-traces")
	}
}

func TestParseArgs_OtelTracesTableOverride(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-traces-table", "main.default.traces"})
	if !a.OTELTracesTableSet {
		t.Error("expected tracesTableSet=true when --otel-traces-table is passed")
	}
	if a.OTELTracesTable != "main.default.traces" {
		t.Errorf("expected tracesTable=%q, got %q", "main.default.traces", a.OTELTracesTable)
	}
}

func TestParseArgs_OtelTracesTableEquals(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-traces-table=my.catalog.traces"})
	if !a.OTELTracesTableSet {
		t.Error("expected tracesTableSet=true for --otel-traces-table=value")
	}
	if a.OTELTracesTable != "my.catalog.traces" {
		t.Errorf("expected tracesTable=%q, got %q", "my.catalog.traces", a.OTELTracesTable)
	}
}

func TestParseArgs_OtelTracesTableDefault(t *testing.T) {
	// --otel-traces alone (no table) should not auto-populate a default table —
	// traces only activate when an explicit --otel-traces-table is provided.
	a, _ := parseArgs([]string{"--otel-traces"})
	if !a.OTELTraces {
		t.Error("expected otelTraces=true")
	}
	if a.OTELTracesTableSet {
		t.Error("expected tracesTableSet=false without --otel-traces-table")
	}
	if a.OTELTracesTable != "" {
		t.Errorf("expected empty tracesTable default, got %q", a.OTELTracesTable)
	}
}

// TestParseArgs_TracesStandalone verifies that --otel-traces-table works
// without --otel — traces is a standalone signal.
func TestParseArgs_TracesStandalone(t *testing.T) {
	a, _ := parseArgs([]string{"--otel-traces-table", "cat.schema.traces"})
	if a.OTEL {
		t.Error("expected otel=false when only --otel-traces-table given")
	}
	if !a.OTELTracesTableSet || a.OTELTracesTable != "cat.schema.traces" {
		t.Errorf("expected tracesTable=cat.schema.traces (set), got %q (set=%v)", a.OTELTracesTable, a.OTELTracesTableSet)
	}
}

// Table-driven comprehensive test for parseArgs.
func TestParseArgs_Table(t *testing.T) {
	type result struct {
		profile   string
		verbose   bool
		version   bool
		showHelp  bool
		printEnv  bool
		otel      bool
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
			name: "--print-env sets printEnv",
			args: []string{"--print-env"},
			want: result{printEnv: true},
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
			name: "--otel sets otel",
			args: []string{"--otel"},
			want: result{otel: true},
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
			if a.PrintEnv != tc.want.printEnv {
				t.Errorf("printEnv: got %v, want %v", a.PrintEnv, tc.want.printEnv)
			}
			if a.OTEL != tc.want.otel {
				t.Errorf("otel: got %v, want %v", a.OTEL, tc.want.otel)
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
		handleHelp("")
	})
	if !strings.Contains(out, "databricks-claude") {
		t.Errorf("expected help output to contain 'databricks-claude', got:\n%s", out)
	}
}

func TestHandleHelp_ContainsPrintEnvFlag(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	if !strings.Contains(out, "--print-env") {
		t.Errorf("expected help output to contain '--print-env', got:\n%s", out)
	}
}

func TestHandleHelp_ContainsClaudeCLISeparator(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	if !strings.Contains(out, "Claude CLI Options:") {
		t.Errorf("expected help output to contain 'Claude CLI Options:', got:\n%s", out)
	}
}

func TestHandleHelp_WithNonExistentUpstream(t *testing.T) {
	// When an upstream binary is given but doesn't exist, handleHelp should
	// still print the databricks-claude header without panicking.
	out := captureStdout(func() {
		handleHelp("/nonexistent/path/to/claude")
	})
	if !strings.Contains(out, "databricks-claude") {
		t.Errorf("expected header even with bad upstream binary, got:\n%s", out)
	}
}

func TestHandleHelp_AllFlagsPresent(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	flags := []string{"--profile", "--upstream", "--verbose", "-v", "--log-file", "--otel", "--otel-metrics-table", "--otel-logs-table", "--headless", "--idle-timeout", "--version", "--help"}
	for _, flag := range flags {
		if !strings.Contains(out, flag) {
			t.Errorf("expected help output to contain flag %q, got:\n%s", flag, out)
		}
	}
}

// Verify the version placeholder is printed in the help header.
func TestHandleHelp_ContainsVersion(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	// Version variable is "dev" by default in tests.
	if !strings.Contains(out, fmt.Sprintf("databricks-claude v%s", Version)) {
		t.Errorf("expected help output to contain version string, got:\n%s", out)
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

// --- idle-timeout flag tests ---

func TestParseArgs_IdleTimeoutDefault(t *testing.T) {
	a, _ := parseArgs([]string{})
	if a.IdleTimeout != 30*time.Minute {
		t.Errorf("expected default idleTimeout=30m, got %v", a.IdleTimeout)
	}
}

func TestParseArgs_IdleTimeoutCustom(t *testing.T) {
	a, _ := parseArgs([]string{"--idle-timeout", "10m"})
	if a.IdleTimeout != 10*time.Minute {
		t.Errorf("expected idleTimeout=10m, got %v", a.IdleTimeout)
	}
}

func TestParseArgs_IdleTimeoutZero(t *testing.T) {
	a, _ := parseArgs([]string{"--idle-timeout", "0"})
	if a.IdleTimeout != 0 {
		t.Errorf("expected idleTimeout=0, got %v", a.IdleTimeout)
	}
}

func TestParseArgs_IdleTimeoutEquals(t *testing.T) {
	a, _ := parseArgs([]string{"--idle-timeout=5m"})
	if a.IdleTimeout != 5*time.Minute {
		t.Errorf("expected idleTimeout=5m, got %v", a.IdleTimeout)
	}
}

func TestParseArgs_IdleTimeoutBareNumber(t *testing.T) {
	_, err := parseArgs([]string{"--idle-timeout", "30"})
	if err == nil {
		t.Error("expected error for bare integer --idle-timeout value, got nil")
	}
}

func TestParseArgs_IdleTimeoutBareNumberEquals(t *testing.T) {
	_, err := parseArgs([]string{"--idle-timeout=30"})
	if err == nil {
		t.Error("expected error for bare integer --idle-timeout=value, got nil")
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

// TestCompletionFlagsCoverAllKnownFlags ensures every flag in knownFlags has a
// corresponding entry in flagDefs. This test fails immediately if someone adds
// a flag to parseArgs without updating the completion metadata — preventing
// silent drift between the real CLI and the generated shell completions.
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

// TestKnownFlagsCoverAllFlagDefs is the inverse check: every FlagDef entry
// must appear in knownFlags so the parser actually recognises it.
func TestKnownFlagsCoverAllFlagDefs(t *testing.T) {
	for _, f := range flagDefs {
		name := "--" + f.Name
		if !knownFlags[name] {
			t.Errorf("flagDef %q is missing from knownFlags in completion_flags.go", name)
		}
	}
}
