//go:build windows

package main

import (
	"strings"
	"testing"
)

// TestBuildSchtasksCmd verifies the /TR argument construction for schtasks.
// Does NOT call schtasks — command-building logic only.
func TestBuildSchtasksCmd(t *testing.T) {
	opts := installOptions{
		binPath: `C:\Program Files\databricks-claude\databricks-claude.exe`,
		port:    49153,
		profile: "prod",
		logFile: `C:\Users\user\AppData\Local\databricks-claude-daemon\serve.log`,
	}

	tr := buildSchtasksCmd(opts)

	// Binary with spaces should be quoted.
	if !strings.Contains(tr, `"C:\Program Files\databricks-claude\databricks-claude.exe"`) {
		t.Errorf("expected quoted binary path in /TR value, got: %s", tr)
	}
	if !strings.Contains(tr, "serve") {
		t.Errorf("expected 'serve' subcommand in /TR value, got: %s", tr)
	}
	if !strings.Contains(tr, "--port=49153") {
		t.Errorf("expected '--port=49153' in /TR value, got: %s", tr)
	}
	if !strings.Contains(tr, "--profile=prod") {
		t.Errorf("expected '--profile=prod' in /TR value, got: %s", tr)
	}
}

// TestBuildSchtasksCmdOtelFlags verifies OTEL flags appear in /TR when set.
func TestBuildSchtasksCmdOtelFlags(t *testing.T) {
	opts := installOptions{
		binPath:      `C:\bin\databricks-claude.exe`,
		port:         49153,
		profile:      "prod",
		logFile:      `C:\tmp\serve.log`,
		metricsTable: "main.telem.metrics",
		logsTable:    "main.telem.logs",
	}

	tr := buildSchtasksCmd(opts)

	if !strings.Contains(tr, "--otel-metrics-table=main.telem.metrics") {
		t.Errorf("expected otel-metrics-table in /TR, got: %s", tr)
	}
	if !strings.Contains(tr, "--otel-logs-table=main.telem.logs") {
		t.Errorf("expected otel-logs-table in /TR, got: %s", tr)
	}
	if strings.Contains(tr, "--otel-traces-table") {
		t.Errorf("should not contain otel-traces-table when empty, got: %s", tr)
	}
}

// TestBuildSchtasksCmdNoSpaceBinary verifies a path without spaces is not quoted.
func TestBuildSchtasksCmdNoSpaceBinary(t *testing.T) {
	opts := installOptions{
		binPath: `C:\bin\databricks-claude.exe`,
		port:    49153,
		profile: "dev",
		logFile: `C:\tmp\log`,
	}

	tr := buildSchtasksCmd(opts)
	// Should not have extra quotes around no-space path.
	if strings.HasPrefix(tr, `"`) {
		t.Errorf("path without spaces should not be quoted, got: %s", tr)
	}
}
