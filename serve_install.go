package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/IceRhymers/databricks-claude/pkg/health"
	"github.com/IceRhymers/databricks-claude/pkg/mdmprofile"
)

const daemonServiceName = "databricks-claude-daemon"

// installOptions holds all parameters needed to write an OS service manifest.
type installOptions struct {
	binPath      string
	port         int
	profile      string
	logFile      string
	metricsTable string
	logsTable    string
	tracesTable  string
}

// statusResult carries what daemonStatus() discovered on the current OS.
type statusResult struct {
	Registered   bool
	Running      bool
	Healthy      bool
	HealthMode   string
	Version      string
	Profile      string
	ManifestPath string
	BinaryPath   string // binary path baked into the manifest
	LastExitCode string
}

// defaultLogFile returns the per-OS default log file path for the daemon.
func defaultLogFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", daemonServiceName, "serve.log"), nil
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(localAppData, daemonServiceName, "serve.log"), nil
	default: // linux and others
		return filepath.Join(home, ".local", "state", daemonServiceName, "serve.log"), nil
	}
}

// runServeInstall dispatches serve install/uninstall/status sub-subcommands.
func runServeInstall(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printServeInstallRootHelp()
		os.Exit(0)
	}

	subcmd := args[0]
	rest := args[1:]

	switch subcmd {
	case "install":
		runInstall(rest)
	case "uninstall":
		runUninstall(rest)
	case "status":
		runStatus(rest)
	default:
		fmt.Fprintf(os.Stderr, "databricks-claude serve: unknown sub-subcommand %q\n", subcmd)
		fmt.Fprintln(os.Stderr, "Run 'databricks-claude serve install --help' for usage.")
		os.Exit(1)
	}
}

// installFlags holds the raw parsed flags for 'serve install'.
type installFlags struct {
	port            int
	profile         string
	logFile         string
	metricsTable    string
	logsTable       string
	tracesTable     string
	metricsTableSet bool
	logsTableSet    bool
	tracesTableSet  bool
}

// parseInstallFlags parses the args slice for 'serve install' flags.
func parseInstallFlags(args []string) installFlags {
	var f installFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case arg == "--port":
			f.port, _ = strconv.Atoi(next())
		case strings.HasPrefix(arg, "--port="):
			f.port, _ = strconv.Atoi(strings.TrimPrefix(arg, "--port="))
		case arg == "--profile":
			f.profile = next()
		case strings.HasPrefix(arg, "--profile="):
			f.profile = strings.TrimPrefix(arg, "--profile=")
		case arg == "--log-file":
			f.logFile = next()
		case strings.HasPrefix(arg, "--log-file="):
			f.logFile = strings.TrimPrefix(arg, "--log-file=")
		case arg == "--otel-metrics-table":
			f.metricsTable = next()
			f.metricsTableSet = true
		case strings.HasPrefix(arg, "--otel-metrics-table="):
			f.metricsTable = strings.TrimPrefix(arg, "--otel-metrics-table=")
			f.metricsTableSet = true
		case arg == "--otel-logs-table":
			f.logsTable = next()
			f.logsTableSet = true
		case strings.HasPrefix(arg, "--otel-logs-table="):
			f.logsTable = strings.TrimPrefix(arg, "--otel-logs-table=")
			f.logsTableSet = true
		case arg == "--otel-traces-table":
			f.tracesTable = next()
			f.tracesTableSet = true
		case strings.HasPrefix(arg, "--otel-traces-table="):
			f.tracesTable = strings.TrimPrefix(arg, "--otel-traces-table=")
			f.tracesTableSet = true
		}
	}
	return f
}

func runInstall(args []string) {
	if hasFlag(args, "--help") || hasFlag(args, "-h") {
		printServeInstallHelp()
		os.Exit(0)
	}

	f := parseInstallFlags(args)

	// Resolve port: flag → state → default.
	st := loadState()
	port := resolvePort(f.port, st)

	// Resolve profile: flag → state → MDM → "DEFAULT".
	profile := f.profile
	if profile == "" && st.Profile != "" {
		profile = st.Profile
	}
	if profile == "" {
		if v, err := mdmprofile.ReadKey(mdmDomain, "databricksProfile"); err == nil && v != "" {
			profile = v
		}
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	// Resolve OTEL tables: flag → state → MDM → empty.
	resolvedMetrics := resolveTableFromChain(f.metricsTable, f.metricsTableSet, st.OtelMetricsTable, "otelMetricsTable", mdmprofile.ReadKey)
	resolvedLogs := resolveTableFromChain(f.logsTable, f.logsTableSet, st.OtelLogsTable, "otelLogsTable", mdmprofile.ReadKey)
	resolvedTraces := resolveTableFromChain(f.tracesTable, f.tracesTableSet, st.OtelTracesTable, "otelTracesTable", mdmprofile.ReadKey)

	// Resolve log file: flag → per-OS default.
	logFile := f.logFile
	if logFile == "" {
		var err error
		logFile, err = defaultLogFile()
		if err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude serve install: cannot determine default log path: %v\n", err)
			os.Exit(1)
		}
	}

	// Resolve binary path.
	binPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude serve install: cannot determine executable path: %v\n", err)
		os.Exit(1)
	}
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude serve install: cannot resolve symlinks for %q: %v\n", binPath, err)
		os.Exit(1)
	}

	opts := installOptions{
		binPath:      binPath,
		port:         port,
		profile:      profile,
		logFile:      logFile,
		metricsTable: resolvedMetrics,
		logsTable:    resolvedLogs,
		tracesTable:  resolvedTraces,
	}

	if err := installDaemon(opts); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude serve install: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "databricks-claude: daemon installed and started\n")
	fmt.Fprintf(os.Stderr, "  Service: %s\n", daemonServiceName)
	fmt.Fprintf(os.Stderr, "  Binary:  %s\n", binPath)
	fmt.Fprintf(os.Stderr, "  Profile: %s\n", profile)
	fmt.Fprintf(os.Stderr, "  Port:    %d\n", port)
	fmt.Fprintf(os.Stderr, "  Log:     %s\n", logFile)
	fmt.Fprintf(os.Stderr, "\nRun 'databricks-claude serve status' to verify.\n")
}

func runUninstall(args []string) {
	if hasFlag(args, "--help") || hasFlag(args, "-h") {
		printServeUninstallHelp()
		os.Exit(0)
	}

	if err := uninstallDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude serve uninstall: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "databricks-claude: daemon stopped and unregistered\n")
}

func runStatus(args []string) {
	if hasFlag(args, "--help") || hasFlag(args, "-h") {
		printServeStatusHelp()
		os.Exit(0)
	}

	// Resolve port for health check.
	st := loadState()
	port := resolvePort(0, st)

	result, err := daemonStatus(port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude serve status: %v\n", err)
		os.Exit(1)
	}

	printStatusResult(result)
}

// printStatusResult prints a human-readable status report to stdout.
func printStatusResult(r statusResult) {
	boolStr := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}

	fmt.Printf("Service:    %s\n", daemonServiceName)
	fmt.Printf("Registered: %s\n", boolStr(r.Registered))
	fmt.Printf("Running:    %s\n", boolStr(r.Running))

	healthStr := boolStr(r.Healthy)
	if r.Healthy && r.HealthMode != "" {
		extras := []string{}
		if r.HealthMode != "" {
			extras = append(extras, "mode="+r.HealthMode)
		}
		if r.Profile != "" {
			extras = append(extras, "profile="+r.Profile)
		}
		if r.Version != "" {
			extras = append(extras, "version="+r.Version)
		}
		if len(extras) > 0 {
			healthStr = "yes (" + strings.Join(extras, ", ") + ")"
		}
	}
	fmt.Printf("Healthy:    %s\n", healthStr)

	if r.ManifestPath != "" {
		fmt.Printf("Manifest:   %s\n", r.ManifestPath)
	}
	if r.BinaryPath != "" {
		fmt.Printf("Binary:     %s\n", r.BinaryPath)
	}
	if r.LastExitCode != "" && r.LastExitCode != "0" {
		fmt.Printf("LastExit:   %s\n", r.LastExitCode)
	}

	// Warn when the manifest binary path doesn't match the current binary.
	if r.BinaryPath != "" {
		cur, err := os.Executable()
		if err == nil {
			cur, _ = filepath.EvalSymlinks(cur)
		}
		if cur != "" && r.BinaryPath != cur {
			fmt.Printf("WARNING: manifest binary path mismatch — re-run 'serve install' after upgrade\n")
			fmt.Printf("  manifest: %s\n", r.BinaryPath)
			fmt.Printf("  current:  %s\n", cur)
		}
	}
}

// probeHealth calls pkg/health.ProxyMode and returns a partial statusResult
// filled with Healthy, HealthMode, Version, and Profile from the /health endpoint.
func probeHealth(port int) (healthy bool, mode, version, profile string) {
	m, h := health.ProxyMode(port, "http")
	if !h {
		return false, "", "", ""
	}
	return true, m, "", ""
}

// printServeInstallRootHelp prints the top-level help for serve install/uninstall/status.
func printServeInstallRootHelp() {
	fmt.Fprint(os.Stderr, `Usage: databricks-claude serve <sub-subcommand> [flags]

Sub-subcommands:
  install    Register and start the daemon as a per-user OS service
  uninstall  Stop and remove the daemon OS service registration
  status     Report Registered / Running / Healthy in one shot

Run 'databricks-claude serve <sub-subcommand> --help' for sub-subcommand flags.
`)
}

// printServeInstallHelp prints usage for 'serve install'.
func printServeInstallHelp() {
	fmt.Fprint(os.Stderr, `Usage: databricks-claude serve install [flags]

Register and start 'databricks-claude serve' as a per-user OS service using
native OS primitives (launchctl on macOS, schtasks on Windows, systemctl --user
on Linux). No sudo required — runs in the current user's session only.

The binary path is resolved via os.Executable() at install time and baked into
the manifest. After a binary upgrade, re-run 'serve install' to refresh the path.

Service name: databricks-claude-daemon

Flags:
  --port int                   Proxy listen port (default: saved state > 49153)
  --profile string             Databricks config profile
                               (flag > saved state > MDM > "DEFAULT")
  --log-file string            Log file path (default: per-OS default)
  --otel-metrics-table string  UC table for OTEL metrics (flag > state > MDM > empty)
  --otel-logs-table string     UC table for OTEL logs   (flag > state > MDM > empty)
  --otel-traces-table string   UC table for OTEL traces (flag > state > MDM > empty)
  --help, -h                   Show this help message

macOS note: if the binary is unsigned, a Gatekeeper warning is printed but
the install proceeds. Run 'xattr -dr com.apple.quarantine <binary>' or sign
the binary to suppress the warning.
`)
}

// printServeUninstallHelp prints usage for 'serve uninstall'.
func printServeUninstallHelp() {
	fmt.Fprint(os.Stderr, `Usage: databricks-claude serve uninstall

Stop and remove the 'databricks-claude-daemon' OS service registration.
Tolerates "not installed" gracefully.

Flags:
  --help, -h   Show this help message
`)
}

// printServeStatusHelp prints usage for 'serve status'.
func printServeStatusHelp() {
	fmt.Fprint(os.Stderr, `Usage: databricks-claude serve status

Report the current state of the 'databricks-claude-daemon' OS service:
  Registered — manifest/task/unit file exists
  Running    — OS service manager reports the service as active
  Healthy    — /health endpoint responds with daemon:true

Flags:
  --help, -h   Show this help message
`)
}
