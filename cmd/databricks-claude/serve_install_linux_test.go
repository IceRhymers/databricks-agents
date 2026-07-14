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

// TestRenderUnit_DATABRICKS_CLI_EnvSet verifies that a non-empty cliPath gets
// rendered as Environment=DATABRICKS_CLI=<abs-path> inside the [Service] block.
// This is the systemd-side fix for the Linuxbrew PATH bug: without it, the
// daemon under systemd --user can't find a databricks binary at e.g.
// /home/linuxbrew/.linuxbrew/bin/databricks because systemd's minimal PATH
// doesn't include brew install dirs.
func TestRenderUnit_DATABRICKS_CLI_EnvSet(t *testing.T) {
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "prod",
		logFile: "/tmp/serve.log",
		cliPath: "/home/linuxbrew/.linuxbrew/bin/databricks",
	}

	out, err := renderUnit(opts)
	if err != nil {
		t.Fatalf("renderUnit error: %v", err)
	}

	want := "Environment=DATABRICKS_CLI=/home/linuxbrew/.linuxbrew/bin/databricks"
	if !strings.Contains(out, want) {
		t.Errorf("unit missing %q\nfull output:\n%s", want, out)
	}
	// Environment line must appear inside the [Service] block, before
	// ExecStart, so systemd parses it as belonging to the service unit.
	serviceIdx := strings.Index(out, "[Service]")
	execIdx := strings.Index(out, "ExecStart=")
	envIdx := strings.Index(out, want)
	if !(serviceIdx >= 0 && serviceIdx < envIdx && envIdx < execIdx) {
		t.Errorf("Environment= must appear in [Service] block before ExecStart=\nfull output:\n%s", out)
	}
}

// TestRenderUnit_DATABRICKS_CLI_AbsentWhenEmpty verifies that no Environment=
// line is emitted when cliPath is empty (the default fallback), AND that the
// resulting [Service] block has no leftover blank line where the conditional
// template branch would have rendered. Asserts exact equality of the relevant
// stanza to lock in whitespace behavior — non-containment alone wouldn't
// catch a stray newline from a future template refactor.
func TestRenderUnit_DATABRICKS_CLI_AbsentWhenEmpty(t *testing.T) {
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

	if strings.Contains(out, "DATABRICKS_CLI") {
		t.Errorf("unit should not contain DATABRICKS_CLI when cliPath is empty\nfull output:\n%s", out)
	}
	// Lock in exact [Service]→ExecStart adjacency — no blank lines between
	// "Type=simple" and "ExecStart=" when the conditional branch is skipped.
	wantSnippet := "[Service]\nType=simple\nExecStart=/usr/local/bin/databricks-claude serve --port=49153 --profile=prod --log-file=/tmp/serve.log\nRestart=on-failure"
	if !strings.Contains(out, wantSnippet) {
		t.Errorf("unit [Service] block has unexpected whitespace when cliPath is empty\nwant snippet:\n%s\n\nfull output:\n%s", wantSnippet, out)
	}
}
