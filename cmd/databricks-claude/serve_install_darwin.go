//go:build darwin

package main

import (
	"bytes"
	"encoding/xml"
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
  </array>{{if .CLIPath}}
  <key>EnvironmentVariables</key>
  <dict>
    <key>DATABRICKS_CLI</key><string>{{escapeXML .CLIPath}}</string>
  </dict>{{end}}
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
	// CLIPath, when non-empty, is rendered into an EnvironmentVariables
	// dict block as DATABRICKS_CLI=<abs-path>. launchd inherits a minimal
	// PATH (essentially /usr/bin:/bin:/usr/sbin:/sbin) that often misses
	// brew installs at /opt/homebrew/bin or /usr/local/bin/databricks
	// symlinks; pinning the absolute path avoids "databricks: command not
	// found" failures in the daemon under launchd's restricted PATH.
	CLIPath string
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
		CLIPath:  opts.cliPath,
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
	// We also extract the last exit code to detect crash-loops: a unit that
	// has Restart=on-failure semantics under launchd (KeepAlive +
	// SuccessfulExit=false) can briefly show state=running between respawns
	// while its actual previous exit code is non-zero. Treating last-exit
	// non-zero as Failed=true gives the symmetric status accuracy fix for
	// the Linux crash-loop masking bug.
	printCmd := exec.Command("launchctl", "print", launchctlDomain())
	out, err := printCmd.Output()
	if err == nil {
		s := string(out)
		if strings.Contains(s, "state = running") {
			r.Running = true
		}
		// Extract last exit code. A clean exit ("0") or a sentinel
		// ("(never exited)") is intentionally not surfaced — we only
		// flag last-exit when it's actionable evidence of a crash-loop.
		// One consequence: printStatusResult's `Last exit: 0` line is
		// unreachable on darwin, which is fine — successful exits
		// belong in the "Running: yes" rendering, not as a footnote.
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "last exit code = ") {
				code := strings.TrimPrefix(line, "last exit code = ")
				if code != "" && code != "0" && code != "(never exited)" {
					r.LastExitCode = code
					r.Failed = true
					r.Running = false
				}
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

// diagnosticsTail returns recent launchd state + stderr log tail for the
// daemon to aid post-install probe failure diagnosis. Returns ("", nil) when
// neither source produced useful output — callers treat empty as "no
// diagnostics available" and continue.
//
// SECURITY: `launchctl print` dumps the agent's configured environment
// variables. We only ever set DATABRICKS_CLI (a path, not a secret); if a
// future change ever adds a token or any other secret to the plist's
// EnvironmentVariables dict, it will be surfaced verbatim here and end up in
// any CI/MDM log capture. NEVER bake secrets into plist EnvironmentVariables
// — use the token cache + the credential helper instead.
func diagnosticsTail() (string, error) {
	var parts []string

	// launchctl print is the single richest source of launchd state info
	// (last exit, state machine, recent spawns); take its full output.
	printCmd := exec.Command("launchctl", "print", launchctlDomain())
	if out, err := printCmd.Output(); err == nil && len(out) > 0 {
		parts = append(parts, "launchctl print:\n"+strings.TrimSpace(string(out)))
	}

	// Also tail the daemon's stderr log file. Parse the plist properly
	// (encoding/xml + plist's key/value pairing) rather than substring
	// scraping — a future plist key whose *value* contained the string
	// "StandardErrorPath" would otherwise yield the wrong path.
	if plist, err := plistPath(); err == nil {
		if logPath := readPlistStringKey(plist, "StandardErrorPath"); logPath != "" {
			if logData, err := os.ReadFile(logPath); err == nil {
				lines := strings.Split(strings.TrimRight(string(logData), "\n"), "\n")
				start := len(lines) - 50
				if start < 0 {
					start = 0
				}
				parts = append(parts, "stderr log tail ("+logPath+"):\n"+strings.Join(lines[start:], "\n"))
			}
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

// readPlistStringKey reads the value associated with key from an Apple
// property-list XML file at path. Returns "" if the file can't be read,
// isn't valid plist XML, or the key isn't present at the top-level <dict>.
// Only handles the <key>NAME</key><string>VALUE</string> case — sufficient
// for the LaunchAgent fields we emit. Nested dicts and array values are
// not supported (we don't emit any).
func readPlistStringKey(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	var pendingKey string
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "dict" {
				depth++
				continue
			}
			// Only consume key/string pairs at the top-level dict to
			// avoid matching nested-dict entries (e.g. KeepAlive's
			// SuccessfulExit) with the same name.
			if depth != 1 {
				continue
			}
			switch t.Name.Local {
			case "key":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					pendingKey = s
				}
			case "string":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil && pendingKey == key {
					return strings.TrimSpace(s)
				}
				pendingKey = ""
			default:
				// Any non-string value flushes the pending key.
				pendingKey = ""
			}
		case xml.EndElement:
			if t.Name.Local == "dict" {
				depth--
			}
		}
	}
}
