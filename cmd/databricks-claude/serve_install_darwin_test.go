//go:build darwin

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestRenderPlist verifies that the plist template renders correctly for a
// given installOptions. Does NOT call launchctl — template output only.
func TestRenderPlist(t *testing.T) {
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "my-profile",
		logFile: "/Users/user/Library/Logs/databricks-claude-daemon/serve.log",
	}

	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}

	checks := []string{
		"<key>Label</key><string>databricks-claude-daemon</string>",
		"<string>/usr/local/bin/databricks-claude</string>",
		"<string>serve</string>",
		"<string>--port=49153</string>",
		"<string>--profile=my-profile</string>",
		"<string>--log-file=",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key>",
		"<key>SuccessfulExit</key><false/>",
		"<key>StandardErrorPath</key>",
		"<key>StandardOutPath</key><string>/dev/null</string>",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("plist missing %q\nfull output:\n%s", c, out)
		}
	}
}

// TestRenderPlistOtelArgs verifies OTEL flags appear when set.
func TestRenderPlistOtelArgs(t *testing.T) {
	opts := installOptions{
		binPath:      "/usr/local/bin/databricks-claude",
		port:         49153,
		profile:      "prod",
		logFile:      "/tmp/serve.log",
		metricsTable: "main.telem.metrics",
		logsTable:    "main.telem.logs",
		tracesTable:  "main.telem.traces",
	}

	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}

	for _, flag := range []string{
		"--otel-metrics-table=main.telem.metrics",
		"--otel-logs-table=main.telem.logs",
		"--otel-traces-table=main.telem.traces",
	} {
		if !strings.Contains(out, flag) {
			t.Errorf("plist missing OTEL flag %q\nfull output:\n%s", flag, out)
		}
	}
}

// TestRenderPlistOtelArgsAbsentWhenEmpty verifies OTEL flags are omitted when tables are empty.
func TestRenderPlistOtelArgsAbsentWhenEmpty(t *testing.T) {
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "prod",
		logFile: "/tmp/serve.log",
	}

	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}

	for _, flag := range []string{"--otel-metrics-table", "--otel-logs-table", "--otel-traces-table"} {
		if strings.Contains(out, flag) {
			t.Errorf("plist should not contain %q when table is empty\nfull output:\n%s", flag, out)
		}
	}
}

// TestRenderPlistXMLEscaping verifies that special XML characters in paths are escaped.
func TestRenderPlistXMLEscaping(t *testing.T) {
	opts := installOptions{
		binPath: "/path/with/&amp/binary",
		port:    49153,
		profile: "prof",
		logFile: "/path/to/log",
	}

	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}
	if strings.Contains(out, "/path/with/&amp/binary") {
		t.Errorf("raw ampersand should be escaped in plist output")
	}
	if !strings.Contains(out, "&amp;amp") {
		t.Errorf("expected &amp; escaped form in plist output")
	}
}

// TestPlutilLint validates that the rendered plist is well-formed XML according
// to plutil. Skipped if plutil is not on PATH (non-macOS CI or minimal images).
func TestPlutilLint(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}

	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "test-profile",
		logFile: "/tmp/serve.log",
	}

	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}

	// Write to a temp file and run plutil -lint.
	tmpFile, err := createTempFile(t, "test-*.plist", out)
	if err != nil {
		t.Fatalf("cannot create temp plist file: %v", err)
	}

	cmd := exec.Command("plutil", "-lint", tmpFile)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("plutil -lint failed: %v\n%s\nPlist content:\n%s", err, output, out)
	}
}

// TestRenderPlist_DATABRICKS_CLI_EnvSet verifies that a non-empty cliPath
// renders an EnvironmentVariables dict containing DATABRICKS_CLI=<abs-path>.
// This is the launchd-side fix for the brew PATH bug: launchd's minimal PATH
// (/usr/bin:/bin:/usr/sbin:/sbin) doesn't include /opt/homebrew/bin so the
// daemon can't find the databricks CLI without an absolute pin.
func TestRenderPlist_DATABRICKS_CLI_EnvSet(t *testing.T) {
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "prod",
		logFile: "/tmp/serve.log",
		cliPath: "/opt/homebrew/bin/databricks",
	}

	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}

	for _, want := range []string{
		"<key>EnvironmentVariables</key>",
		"<key>DATABRICKS_CLI</key><string>/opt/homebrew/bin/databricks</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q\nfull output:\n%s", want, out)
		}
	}
}

// TestRenderPlist_DATABRICKS_CLI_AbsentWhenEmpty verifies no EnvironmentVariables
// block when cliPath is empty.
func TestRenderPlist_DATABRICKS_CLI_AbsentWhenEmpty(t *testing.T) {
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "prod",
		logFile: "/tmp/serve.log",
	}

	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}
	if strings.Contains(out, "EnvironmentVariables") {
		t.Errorf("plist should not contain EnvironmentVariables when cliPath is empty\nfull output:\n%s", out)
	}
	if strings.Contains(out, "DATABRICKS_CLI") {
		t.Errorf("plist should not contain DATABRICKS_CLI when cliPath is empty\nfull output:\n%s", out)
	}
}

// TestRenderPlist_DATABRICKS_CLI_PlutilLint verifies the rendered plist with
// EnvironmentVariables is still valid property-list XML.
func TestRenderPlist_DATABRICKS_CLI_PlutilLint(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	opts := installOptions{
		binPath: "/usr/local/bin/databricks-claude",
		port:    49153,
		profile: "prod",
		logFile: "/tmp/serve.log",
		cliPath: "/opt/homebrew/bin/databricks",
	}
	out, err := renderPlist(opts)
	if err != nil {
		t.Fatalf("renderPlist error: %v", err)
	}
	tmpFile, err := createTempFile(t, "test-cli-*.plist", out)
	if err != nil {
		t.Fatalf("cannot create temp plist: %v", err)
	}
	cmd := exec.Command("plutil", "-lint", tmpFile)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("plutil -lint failed: %v\n%s\nPlist content:\n%s", err, output, out)
	}
}

// createTempFile writes content to a temp file and returns its path.
// The file is removed when the test ends.
func createTempFile(t *testing.T, pattern, content string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name(), nil
}
