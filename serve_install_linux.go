//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// unitTemplate is the systemd user unit template for the daemon.
const unitTemplate = `[Unit]
Description=Databricks Claude long-lived daemon
After=network-online.target

[Service]
Type=simple
ExecStart={{.BinPath}} serve --port={{.Port}} --profile={{.Profile}} --log-file={{.LogFile}}{{.OtelFlags}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`

type unitData struct {
	BinPath   string
	Port      int
	Profile   string
	LogFile   string
	OtelFlags string
}

// renderUnit renders the systemd unit file content for the given installOptions.
func renderUnit(opts installOptions) (string, error) {
	var otelParts []string
	if opts.metricsTable != "" {
		otelParts = append(otelParts, "--otel-metrics-table="+opts.metricsTable)
	}
	if opts.logsTable != "" {
		otelParts = append(otelParts, "--otel-logs-table="+opts.logsTable)
	}
	if opts.tracesTable != "" {
		otelParts = append(otelParts, "--otel-traces-table="+opts.tracesTable)
	}

	otelFlags := ""
	if len(otelParts) > 0 {
		otelFlags = " " + strings.Join(otelParts, " ")
	}

	data := unitData{
		BinPath:   opts.binPath,
		Port:      opts.port,
		Profile:   opts.profile,
		LogFile:   opts.logFile,
		OtelFlags: otelFlags,
	}

	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return "", fmt.Errorf("unit template parse: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("unit template render: %w", err)
	}
	return buf.String(), nil
}

// unitPath returns the path to the systemd user unit file.
func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", daemonServiceName+".service"), nil
}

// installDaemon writes the systemd user unit and enables + starts it.
func installDaemon(opts installOptions) error {
	unit, err := unitPath()
	if err != nil {
		return fmt.Errorf("cannot determine unit path: %w", err)
	}

	// Ensure log directory exists.
	logDir := filepath.Dir(opts.logFile)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("cannot create log directory %q: %w", logDir, err)
	}

	// Render unit file.
	content, err := renderUnit(opts)
	if err != nil {
		return err
	}

	// Write unit file atomically.
	unitDir := filepath.Dir(unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("cannot create systemd user directory: %w", err)
	}
	tmp, err := os.CreateTemp(unitDir, daemonServiceName+".*.tmp")
	if err != nil {
		return fmt.Errorf("cannot create temp unit file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("cannot write temp unit file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cannot close temp unit file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, unit); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cannot install unit file: %w", err)
	}

	// daemon-reload so systemd picks up the new/changed unit.
	reloadCmd := exec.Command("systemctl", "--user", "daemon-reload")
	if out, err := reloadCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	// Enable and start the unit.
	enableCmd := exec.Command("systemctl", "--user", "enable", "--now", daemonServiceName+".service")
	if out, err := enableCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user enable --now failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// uninstallDaemon disables + stops the systemd user unit and removes the file.
func uninstallDaemon() error {
	// Disable and stop — tolerate "Unit file does not exist".
	disableCmd := exec.Command("systemctl", "--user", "disable", "--now", daemonServiceName+".service")
	if out, err := disableCmd.CombinedOutput(); err != nil {
		s := string(out)
		if !strings.Contains(s, "does not exist") &&
			!strings.Contains(s, "not found") &&
			!strings.Contains(s, "Failed to disable") {
			fmt.Fprintf(os.Stderr, "databricks-claude serve uninstall: systemctl disable (ignored): %s\n", strings.TrimSpace(s))
		}
	}

	// Remove unit file — tolerate not-exist.
	unit, err := unitPath()
	if err != nil {
		return fmt.Errorf("cannot determine unit path: %w", err)
	}
	if err := os.Remove(unit); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove unit file %q: %w", unit, err)
	}

	// Reload to pick up the removed unit.
	reloadCmd := exec.Command("systemctl", "--user", "daemon-reload")
	if out, err := reloadCmd.CombinedOutput(); err != nil {
		// Non-fatal — log and continue.
		fmt.Fprintf(os.Stderr, "databricks-claude serve uninstall: daemon-reload (ignored): %s\n", strings.TrimSpace(string(out)))
	}

	return nil
}

// daemonStatus returns the current registration/running/healthy state.
func daemonStatus(port int) (statusResult, error) {
	var r statusResult

	unit, err := unitPath()
	if err != nil {
		return r, fmt.Errorf("cannot determine unit path: %w", err)
	}
	r.ManifestPath = unit

	// Registered: systemctl --user is-enabled exit 0.
	enabledCmd := exec.Command("systemctl", "--user", "is-enabled", daemonServiceName+".service")
	if err := enabledCmd.Run(); err == nil {
		r.Registered = true
	}

	// Running: systemctl --user is-active exit 0.
	activeCmd := exec.Command("systemctl", "--user", "is-active", daemonServiceName+".service")
	if err := activeCmd.Run(); err == nil {
		r.Running = true
	}

	// Populate BinaryPath by reading the unit file if it exists.
	if data, err := os.ReadFile(unit); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "ExecStart=") {
				parts := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
				if len(parts) > 0 {
					r.BinaryPath = parts[0]
				}
				break
			}
		}
	}

	// Healthy: probe /health endpoint.
	r.Healthy, r.HealthMode, r.Version, r.Profile = probeHealth(port)

	return r, nil
}
