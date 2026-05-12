// Tests for serve install/uninstall/status dispatcher, flag parsing, and
// status formatting. No build tag — compiles on all platforms.
//
// Note: integration tests that call launchctl bootstrap / schtasks /create /
// systemctl --user enable are NOT included here. Those require a live user
// session and specific OS setup that CI cannot provide. Template-rendering
// tests are sufficient to verify correctness without touching the service manager.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestParseInstallFlags verifies that parseInstallFlags correctly parses
// install sub-subcommand flags.
func TestParseInstallFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want installFlags
	}{
		{
			name: "empty args",
			args: []string{},
			want: installFlags{},
		},
		{
			name: "port equals form",
			args: []string{"--port=49153"},
			want: installFlags{port: 49153},
		},
		{
			name: "port space form",
			args: []string{"--port", "8080"},
			want: installFlags{port: 8080},
		},
		{
			name: "profile equals form",
			args: []string{"--profile=my-profile"},
			want: installFlags{profile: "my-profile"},
		},
		{
			name: "profile space form",
			args: []string{"--profile", "my-profile"},
			want: installFlags{profile: "my-profile"},
		},
		{
			name: "log file",
			args: []string{"--log-file=/tmp/daemon.log"},
			want: installFlags{logFile: "/tmp/daemon.log"},
		},
		{
			name: "otel metrics table sets flag",
			args: []string{"--otel-metrics-table=cat.schema.tbl"},
			want: installFlags{metricsTable: "cat.schema.tbl", metricsTableSet: true},
		},
		{
			name: "all flags",
			args: []string{
				"--port=49153",
				"--profile=prod",
				"--log-file=/var/log/daemon.log",
				"--otel-metrics-table=main.telem.metrics",
				"--otel-logs-table=main.telem.logs",
				"--otel-traces-table=main.telem.traces",
			},
			want: installFlags{
				port:            49153,
				profile:         "prod",
				logFile:         "/var/log/daemon.log",
				metricsTable:    "main.telem.metrics",
				logsTable:       "main.telem.logs",
				tracesTable:     "main.telem.traces",
				metricsTableSet: true,
				logsTableSet:    true,
				tracesTableSet:  true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseInstallFlags(tc.args)
			if got != tc.want {
				t.Errorf("parseInstallFlags(%v)\n  got  %+v\n  want %+v", tc.args, got, tc.want)
			}
		})
	}
}

// captureStatusStdout captures os.Stdout output from fn.
func captureStatusStdout(fn func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// TestPrintStatusResult verifies the pretty-printer output format.
func TestPrintStatusResult(t *testing.T) {
	r := statusResult{
		Registered:   true,
		Running:      true,
		Healthy:      true,
		HealthMode:   "daemon",
		Profile:      "prod-profile",
		Version:      "1.2.3",
		ManifestPath: "/some/path/to/manifest",
		BinaryPath:   "/usr/local/bin/databricks-claude",
		LastExitCode: "0",
	}

	out := captureStatusStdout(func() { printStatusResult(r) })

	checks := []struct {
		contains string
	}{
		{"Service:"},
		{daemonServiceName},
		{"Registered: yes"},
		{"Running:    yes"},
		{"Healthy:    yes"},
		{"daemon"},
		{"prod-profile"},
		{"Manifest:"},
		{"/some/path/to/manifest"},
		{"Binary:"},
		{"/usr/local/bin/databricks-claude"},
	}
	for _, c := range checks {
		if !strings.Contains(out, c.contains) {
			t.Errorf("printStatusResult output missing %q\nfull output:\n%s", c.contains, out)
		}
	}
	// LastExitCode=0 should NOT appear (we suppress zero exit codes).
	if strings.Contains(out, "LastExit:") {
		t.Errorf("printStatusResult should not print LastExit when code is 0")
	}
}

// TestPrintStatusResultNotRegistered verifies the all-false case doesn't panic.
func TestPrintStatusResultNotRegistered(t *testing.T) {
	r := statusResult{} // all zero
	out := captureStatusStdout(func() { printStatusResult(r) })
	if !strings.Contains(out, "Registered: no") {
		t.Errorf("expected 'Registered: no', got:\n%s", out)
	}
	if !strings.Contains(out, "Running:    no") {
		t.Errorf("expected 'Running:    no', got:\n%s", out)
	}
	if !strings.Contains(out, "Healthy:    no") {
		t.Errorf("expected 'Healthy:    no', got:\n%s", out)
	}
}

// TestPrintStatusResultNonZeroExitCode verifies that non-zero exit codes are surfaced.
func TestPrintStatusResultNonZeroExitCode(t *testing.T) {
	r := statusResult{LastExitCode: "1"}
	out := captureStatusStdout(func() { printStatusResult(r) })
	if !strings.Contains(out, "LastExit:   1") {
		t.Errorf("expected 'LastExit:   1', got:\n%s", out)
	}
}

// TestBinaryPathMismatchWarning verifies that a mismatch between the manifest
// binary path and the current os.Executable produces a warning line.
func TestBinaryPathMismatchWarning(t *testing.T) {
	// Use a path that definitely differs from os.Executable().
	r := statusResult{
		BinaryPath: "/some/other/path/databricks-claude",
	}
	out := captureStatusStdout(func() { printStatusResult(r) })
	if !strings.Contains(out, "WARNING: manifest binary path mismatch") {
		t.Errorf("expected mismatch WARNING, got:\n%s", out)
	}
}

// TestBinaryPathNoWarningWhenEmpty verifies no spurious warning when BinaryPath is empty.
func TestBinaryPathNoWarningWhenEmpty(t *testing.T) {
	r := statusResult{BinaryPath: ""}
	out := captureStatusStdout(func() { printStatusResult(r) })
	if strings.Contains(out, "WARNING") {
		t.Errorf("unexpected WARNING when BinaryPath is empty:\n%s", out)
	}
}

// TestServeInstallHelpFlag verifies that --help args return the correct help
// function output (by checking the output contains the expected subcommand names).
func TestServeInstallHelpContent(t *testing.T) {
	tests := []struct {
		fn       func()
		contains string
	}{
		{printServeInstallRootHelp, "install"},
		{printServeInstallRootHelp, "uninstall"},
		{printServeInstallRootHelp, "status"},
		{printServeInstallHelp, "--port"},
		{printServeInstallHelp, "--profile"},
		{printServeInstallHelp, "databricks-claude-daemon"},
		{printServeUninstallHelp, "uninstall"},
		{printServeStatusHelp, "Registered"},
		{printServeStatusHelp, "Running"},
		{printServeStatusHelp, "Healthy"},
	}

	for _, tc := range tests {
		// Help prints to stderr, capture it.
		r, w, _ := os.Pipe()
		old := os.Stderr
		os.Stderr = w
		tc.fn()
		w.Close()
		os.Stderr = old
		var buf bytes.Buffer
		buf.ReadFrom(r)
		out := buf.String()
		if !strings.Contains(out, tc.contains) {
			t.Errorf("help output missing %q\nfull output:\n%s", tc.contains, out)
		}
	}
}

// TestDefaultLogFileNotEmpty ensures defaultLogFile returns a non-empty string.
func TestDefaultLogFileNotEmpty(t *testing.T) {
	path, err := defaultLogFile()
	if err != nil {
		t.Fatalf("defaultLogFile() error: %v", err)
	}
	if path == "" {
		t.Error("defaultLogFile() returned empty string")
	}
	if !strings.Contains(path, daemonServiceName) {
		t.Errorf("defaultLogFile() path %q does not contain service name %q", path, daemonServiceName)
	}
}

// TestDaemonServiceName verifies the canonical service name constant.
func TestDaemonServiceName(t *testing.T) {
	if daemonServiceName != "databricks-claude-daemon" {
		t.Errorf("daemonServiceName = %q, want %q", daemonServiceName, "databricks-claude-daemon")
	}
}

// Compile-time check: these format strings must stay in sync with the struct fields.
var _ = fmt.Sprintf("%+v", installFlags{})
var _ = fmt.Sprintf("%+v", installOptions{})
var _ = fmt.Sprintf("%+v", statusResult{})
