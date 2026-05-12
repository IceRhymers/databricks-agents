//go:build linux

package main

import (
	"strings"
	"testing"
)

// TestRenderUnit verifies that the systemd unit template renders correctly.
// Does NOT call systemctl — template output only.
func TestRenderUnit(t *testing.T) {
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "my-profile",
		logFile: "/home/user/.local/state/databricks-claude-daemon/serve.log",
	}

	out, err := renderUnit(opts)
	if err != nil {
		t.Fatalf("renderUnit error: %v", err)
	}

	checks := []string{
		"[Unit]",
		"Description=Databricks Claude long-lived daemon",
		"[Service]",
		"Type=simple",
		"ExecStart=/usr/local/bin/databricks-claude serve",
		"--port=49153",
		"--profile=my-profile",
		"--log-file=",
		"Restart=on-failure",
		"RestartSec=5",
		"[Install]",
		"WantedBy=default.target",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("unit missing %q\nfull output:\n%s", c, out)
		}
	}
}

// TestRenderUnitOtelFlags verifies OTEL flags appear in ExecStart when set.
func TestRenderUnitOtelFlags(t *testing.T) {
	opts := installOptions{
		binPath:      "/usr/local/bin/databricks-claude",
		port:         49153,
		profile:      "prod",
		logFile:      "/tmp/serve.log",
		metricsTable: "main.telem.metrics",
		logsTable:    "main.telem.logs",
		tracesTable:  "main.telem.traces",
	}

	out, err := renderUnit(opts)
	if err != nil {
		t.Fatalf("renderUnit error: %v", err)
	}

	for _, flag := range []string{
		"--otel-metrics-table=main.telem.metrics",
		"--otel-logs-table=main.telem.logs",
		"--otel-traces-table=main.telem.traces",
	} {
		if !strings.Contains(out, flag) {
			t.Errorf("unit missing OTEL flag %q\nfull output:\n%s", flag, out)
		}
	}
}

// TestRenderUnitOtelFlagsAbsentWhenEmpty verifies OTEL flags are not included
// when table names are empty.
func TestRenderUnitOtelFlagsAbsentWhenEmpty(t *testing.T) {
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "prod",
		logFile: "/tmp/serve.log",
	}

	out, err := renderUnit(opts)
	if err != nil {
		t.Fatalf("renderUnit error: %v", err)
	}

	for _, flag := range []string{"--otel-metrics-table", "--otel-logs-table", "--otel-traces-table"} {
		if strings.Contains(out, flag) {
			t.Errorf("unit should not contain %q when table is empty\nfull output:\n%s", flag, out)
		}
	}
}
