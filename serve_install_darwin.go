//go:build darwin

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

// plistTemplate is the LaunchAgent plist template for the daemon.
// ProgramArguments values are XML-escaped via the escapeXML helper.
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{escapeXML .BinPath}}</string>
    <string>serve</string>
    <string>--port={{.Port}}</string>
    <string>--profile={{escapeXML .Profile}}</string>
    <string>--log-file={{escapeXML .LogFile}}</string>{{range .OtelArgs}}
    <string>{{escapeXML .}}</string>{{end}}
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>WorkingDirectory</key><string>{{escapeXML .HomeDir}}</string>
  <key>StandardOutPath</key><string>/dev/null</string>
  <key>StandardErrorPath</key><string>{{escapeXML .LogFile}}</string>
</dict>
</plist>
`

type plistData struct {
	Label    string
	BinPath  string
	Port     int
	Profile  string
	LogFile  string
	HomeDir  string
	OtelArgs []string
}

// escapeXML replaces XML special characters in a string.
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// renderPlist renders the LaunchAgent plist for the given installOptions.
func renderPlist(opts installOptions) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home dir: %w", err)
	}

	var otelArgs []string
	if opts.metricsTable != "" {
		otelArgs = append(otelArgs, "--otel-metrics-table="+opts.metricsTable)
	}
	if opts.logsTable != "" {
		otelArgs = append(otelArgs, "--otel-logs-table="+opts.logsTable)
	}
	if opts.tracesTable != "" {
		otelArgs = append(otelArgs, "--otel-traces-table="+opts.tracesTable)
	}

	data := plistData{
		Label:    daemonServiceName,
		BinPath:  opts.binPath,
		Port:     opts.port,
		Profile:  opts.profile,
		LogFile:  opts.logFile,
		HomeDir:  home,
		OtelArgs: otelArgs,
	}

	funcMap := template.FuncMap{"escapeXML": escapeXML}
	tmpl, err := template.New("plist").Funcs(funcMap).Parse(plistTemplate)
	if err != nil {
		return "", fmt.Errorf("plist template parse: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("plist template render: %w", err)
	}
	return buf.String(), nil
}

// plistPath returns the path to the LaunchAgent plist file.
func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", daemonServiceName+".plist"), nil
}

// currentUID returns the current user's UID as a string for launchctl domain args.
func currentUID() string {
	return fmt.Sprintf("%d", os.Getuid())
}

// launchctlDomain returns the gui/<uid>/service-name domain string.
func launchctlDomain() string {
	return fmt.Sprintf("gui/%s/%s", currentUID(), daemonServiceName)
}

// launchctlUserDomain returns gui/<uid>.
func launchctlUserDomain() string {
	return fmt.Sprintf("gui/%s", currentUID())
}

// installDaemon installs the LaunchAgent plist and starts the daemon.
func installDaemon(opts installOptions) error {
	plist, err := plistPath()
	if err != nil {
		return fmt.Errorf("cannot determine plist path: %w", err)
	}

	// Ensure log directory exists.
	logDir := filepath.Dir(opts.logFile)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("cannot create log directory %q: %w", logDir, err)
	}

	// Render plist.
	content, err := renderPlist(opts)
	if err != nil {
		return err
	}

	// Write plist atomically.
	plistDir := filepath.Dir(plist)
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		return fmt.Errorf("cannot create LaunchAgents directory: %w", err)
	}
	tmp, err := os.CreateTemp(plistDir, daemonServiceName+".*.tmp")
	if err != nil {
		return fmt.Errorf("cannot create temp plist: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("cannot write temp plist: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cannot close temp plist: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, plist); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cannot install plist: %w", err)
	}

	// Unload any existing instance (ignore "no such service" errors).
	bootoutCmd := exec.Command("launchctl", "bootout", launchctlDomain())
	if out, err := bootoutCmd.CombinedOutput(); err != nil {
		s := string(out)
		if !strings.Contains(s, "No such process") &&
			!strings.Contains(s, "Bootstrap failed") &&
			!strings.Contains(s, "No such file") &&
			!strings.Contains(s, "error = 3:") {
			// Non-fatal: log but continue.
			fmt.Fprintf(os.Stderr, "databricks-claude serve install: launchctl bootout (ignored): %s\n", strings.TrimSpace(s))
		}
	}

	// Load plist.
	bootstrapCmd := exec.Command("launchctl", "bootstrap", launchctlUserDomain(), plist)
	if out, err := bootstrapCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	// Kickstart (restart if already running).
	kickstartCmd := exec.Command("launchctl", "kickstart", "-k", launchctlDomain())
	if out, err := kickstartCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	// Check binary notarization. Non-fatal — warn only.
	spctlCmd := exec.Command("spctl", "--assess", "--type", "execute", opts.binPath)
	if err := spctlCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: binary not notarized — launchd may kill it. Run `xattr -dr com.apple.quarantine %s` or sign the binary.\n", opts.binPath)
	}

	return nil
}

// uninstallDaemon stops and removes the LaunchAgent.
func uninstallDaemon() error {
	// Bootout — tolerate "no such service".
	bootoutCmd := exec.Command("launchctl", "bootout", launchctlDomain())
	if out, err := bootoutCmd.CombinedOutput(); err != nil {
		s := string(out)
		if !strings.Contains(s, "No such process") &&
			!strings.Contains(s, "Bootstrap failed") &&
			!strings.Contains(s, "No such file") &&
			!strings.Contains(s, "error = 3:") {
			fmt.Fprintf(os.Stderr, "databricks-claude serve uninstall: launchctl bootout (ignored): %s\n", strings.TrimSpace(s))
		}
	}

	// Remove plist — tolerate not-exist.
	plist, err := plistPath()
	if err != nil {
		return fmt.Errorf("cannot determine plist path: %w", err)
	}
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove plist %q: %w", plist, err)
	}

	return nil
}

// daemonStatus returns the current registration/running/healthy state.
func daemonStatus(port int) (statusResult, error) {
	var r statusResult

	plist, err := plistPath()
	if err != nil {
		return r, fmt.Errorf("cannot determine plist path: %w", err)
	}
	r.ManifestPath = plist

	// Registered: plist file exists.
	if _, err := os.Stat(plist); err == nil {
		r.Registered = true
	}

	// Running: launchctl print exit 0 AND output contains "state = running".
	printCmd := exec.Command("launchctl", "print", launchctlDomain())
	out, err := printCmd.Output()
	if err == nil {
		s := string(out)
		if strings.Contains(s, "state = running") {
			r.Running = true
		}
		// Extract last exit code.
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "last exit code = ") {
				r.LastExitCode = strings.TrimPrefix(line, "last exit code = ")
			}
		}
		// Extract binary path from program arguments.
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "path = ") {
				r.BinaryPath = strings.TrimPrefix(line, "path = ")
			}
		}
	}

	// Healthy: probe /health endpoint.
	r.Healthy, r.HealthMode, r.Version, r.Profile = probeHealth(port)

	return r, nil
}
