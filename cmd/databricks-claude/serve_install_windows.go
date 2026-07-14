//go:build windows

package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// installDaemon creates a Scheduled Task that runs at logon and starts it.
func installDaemon(opts installOptions) error {
	// Ensure log directory exists.
	logDir := filepath.Dir(opts.logFile)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("cannot create log directory %q: %w", logDir, err)
	}

	// Build the /TR value: quoted binary path plus serve flags.
	tr := buildSchtasksCmd(opts)

	// Create (or overwrite) the scheduled task.
	// /F overwrites an existing task; /RL LIMITED = user-context privileges.
	createCmd := exec.Command("schtasks",
		"/create",
		"/TN", daemonServiceName,
		"/SC", "ONLOGON",
		"/F",
		"/RL", "LIMITED",
		"/TR", tr,
	)
	if out, err := createCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /create failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	// Run the task immediately.
	runCmd := exec.Command("schtasks", "/run", "/TN", daemonServiceName)
	if out, err := runCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /run failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// buildSchtasksCmd builds the /TR argument for schtasks /create.
// Windows paths with spaces need double-quoting inside the overall string.
func buildSchtasksCmd(opts installOptions) string {
	// Inner arguments for the serve command.
	args := []string{
		"serve",
		fmt.Sprintf("--port=%d", opts.port),
		fmt.Sprintf("--profile=%s", opts.profile),
		fmt.Sprintf("--log-file=%s", opts.logFile),
	}
	if opts.metricsTable != "" {
		args = append(args, "--otel-metrics-table="+opts.metricsTable)
	}
	if opts.logsTable != "" {
		args = append(args, "--otel-logs-table="+opts.logsTable)
	}
	if opts.tracesTable != "" {
		args = append(args, "--otel-traces-table="+opts.tracesTable)
	}

	// Quote each argument that contains spaces; wrap the binary path in quotes.
	var parts []string
	bin := opts.binPath
	if strings.Contains(bin, " ") {
		bin = `"` + bin + `"`
	}
	parts = append(parts, bin)
	for _, a := range args {
		if strings.Contains(a, " ") {
			parts = append(parts, `"`+a+`"`)
		} else {
			parts = append(parts, a)
		}
	}

	return strings.Join(parts, " ")
}

// uninstallDaemon deletes the Scheduled Task (which stops it if running).
func uninstallDaemon() error {
	deleteCmd := exec.Command("schtasks", "/delete", "/TN", daemonServiceName, "/F")
	out, err := deleteCmd.CombinedOutput()
	if err != nil {
		s := string(out)
		// Tolerate "task does not exist".
		if strings.Contains(strings.ToLower(s), "cannot find the file") ||
			strings.Contains(strings.ToLower(s), "the system cannot find") ||
			strings.Contains(strings.ToLower(s), "does not exist") {
			return nil
		}
		return fmt.Errorf("schtasks /delete failed: %w\n%s", err, strings.TrimSpace(s))
	}
	return nil
}

// daemonStatus returns the current registration/running/healthy state.
func daemonStatus(port int) (statusResult, error) {
	var r statusResult

	// Registered: schtasks /query exit 0.
	queryCmd := exec.Command("schtasks", "/query", "/TN", daemonServiceName)
	if err := queryCmd.Run(); err == nil {
		r.Registered = true
	}

	// Running: parse CSV output from schtasks /query /V /FO CSV.
	if r.Registered {
		csvCmd := exec.Command("schtasks", "/query", "/TN", daemonServiceName, "/V", "/FO", "CSV")
		if out, err := csvCmd.Output(); err == nil {
			status := parseSchtasksStatus(out)
			r.Running = strings.EqualFold(status, "Running")
		}
	}

	// Healthy: probe /health endpoint.
	r.Healthy, r.HealthMode, r.Version, r.Profile = probeHealth(port)

	return r, nil
}

// parseSchtasksStatus extracts the "Status" column from schtasks CSV output.
func parseSchtasksStatus(data []byte) string {
	r := csv.NewReader(bytes.NewReader(data))
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return ""
	}
	headers := records[0]
	statusCol := -1
	for i, h := range headers {
		if strings.EqualFold(strings.TrimSpace(h), "Status") {
			statusCol = i
			break
		}
	}
	if statusCol < 0 || len(records[1]) <= statusCol {
		return ""
	}
	return strings.TrimSpace(records[1][statusCol])
}

// diagnosticsTail is a Windows stub. Schtasks has no equivalent of journalctl
// or `launchctl print` that's both reliable and ad-hoc; the daemon's stderr
// log file (resolved per-OS via defaultLogFile) is the canonical place to
// look. Returning "not implemented" here is preferable to returning ("", nil)
// because the install path emits a non-empty diagnostics banner verbatim, and
// "not implemented" tells the human running install where to look manually.
func diagnosticsTail() (string, error) {
	return "not implemented on Windows; check the daemon stderr log file (set via --log-file or per-OS default under %LOCALAPPDATA%/databricks-claude-daemon/serve.log)", nil
}
