package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
	"github.com/IceRhymers/databricks-agents/internal/core/cli"
	"github.com/IceRhymers/databricks-agents/pkg/health"
	"github.com/IceRhymers/databricks-agents/pkg/mdmprofile"
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
	// cliPath is the absolute path to the `databricks` CLI baked into the
	// service manifest via $DATABRICKS_CLI. Service managers (systemd user
	// units, launchd, schtasks) inherit a minimal PATH that often misses
	// non-standard install locations (e.g. Linuxbrew at
	// /home/linuxbrew/.linuxbrew/bin/databricks), so we resolve once at
	// install time and pin the absolute path in the manifest environment.
	// Empty when the CLI was not found at install time — the daemon will
	// fall back to PATH lookup + cli.FallbackCLIDirs.
	cliPath string
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
	// Failed is set when the service-manager reports the unit/agent/task as
	// failed (systemd "failed"/non-zero Result, launchd non-zero last exit,
	// or schtasks "Could not start"). When true, the human-friendly status
	// renderer emits `Running: no (failed, ...)` so a crash-loop is visible
	// instead of being masked by a momentarily-true `state = running` read.
	Failed bool
	// FailureDetail is a short, parseable summary of the failure (e.g.
	// `result=exit-code` on systemd or empty when no extra detail is
	// available beyond LastExitCode).
	FailureDetail string
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
//
// runServe only routes here when args[0] is one of those three keywords, so
// the bare "no args / --help" branch the legacy printServeInstallRootHelp
// covered is unreachable in production. Kept as a defensive fallback that
// renders the parent serveCommand help so a future caller change can't
// silently regress to "exit 0 with no output".
func runServeInstall(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_ = cmd.Render(os.Stderr, serveCommand, nil)
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

// installCommand returns the cmd.Command node for `serve install`. Single
// call site today (parseInstallFlags / runInstall help check) but factored
// out so future edits to serveCommand.Subcommands ordering can't drift the
// lookup.
func installCommand() cmd.Command {
	if c := serveCommand.Subcommand("install"); c != nil {
		return *c
	}
	return cmd.Command{}
}

func uninstallCommand() cmd.Command {
	if c := serveCommand.Subcommand("uninstall"); c != nil {
		return *c
	}
	return cmd.Command{}
}

func statusCommand() cmd.Command {
	if c := serveCommand.Subcommand("status"); c != nil {
		return *c
	}
	return cmd.Command{}
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
	// skipAuthCheck bypasses the install-time pre-auth probe. Used by CI,
	// MDM fleet init scripts, and any context where the install command
	// shouldn't fail just because the workspace has no cached token yet.
	skipAuthCheck bool
}

// parseInstallFlags maps installCommand().Parse(args) into the typed
// installFlags struct. The flag set is the single source of truth declared
// on serveCommand.Subcommands["install"] in commands.go (#171).
func parseInstallFlags(args []string) installFlags {
	r, _ := installCommand().Parse(args)
	var f installFlags
	if v, ok := r.Strings["port"]; ok {
		f.port, _ = strconv.Atoi(v)
	}
	f.profile = r.Strings["profile"]
	f.logFile = r.Strings["log-file"]
	f.metricsTable = r.Strings["otel-metrics-table"]
	f.metricsTableSet = r.Set["otel-metrics-table"]
	f.logsTable = r.Strings["otel-logs-table"]
	f.logsTableSet = r.Set["otel-logs-table"]
	f.tracesTable = r.Strings["otel-traces-table"]
	f.tracesTableSet = r.Set["otel-traces-table"]
	f.skipAuthCheck = r.Bools["skip-auth-check"]
	return f
}

// isInteractiveStdin returns true when stdin appears to be a real tty. False
// when stdin is a pipe, redirected file, or otherwise non-character-device —
// the case we care about catching is `databricks-claude serve install` invoked
// from an MDM init script or systemd unit, where any interactive prompt would
// hang forever or fail outright.
//
// Windows note: os.ModeCharDevice semantics differ on Windows; we conservatively
// treat Windows stdin as non-interactive so install must be paired with
// --skip-auth-check when running under schtasks. Manual install from cmd.exe
// works because the user can pre-run `databricks auth login` then re-run install.
func isInteractiveStdin() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runInstall(args []string) {
	r, _ := installCommand().Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stderr, installCommand(), nil)
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

	// Resolve `databricks` CLI absolute path to bake into the service
	// manifest's environment. The service manager's PATH is minimal (e.g.
	// systemd --user PATH defaults to /usr/local/bin:/usr/local/sbin:/usr/
	// bin:/usr/sbin which doesn't include Linuxbrew or ~/.local/bin); pinning
	// $DATABRICKS_CLI ensures the daemon finds the same CLI the install ran.
	cliPath := cli.ResolveDatabricksCLI(st.DatabricksCLIPath)
	cliPathResolved := ""
	if _, err := os.Stat(cliPath); err == nil && filepath.IsAbs(cliPath) {
		// ResolveDatabricksCLI returns an absolute path in every tier that
		// actually located the binary ($DATABRICKS_CLI override, PATH hit
		// via exec.LookPath, fallback-dir scan). It only returns the
		// verbatim string "databricks" when every tier missed — that's
		// neither stat-able nor absolute, so it correctly leaves
		// cliPathResolved empty (and the daemon falls back to the same
		// resolver chain at runtime).
		cliPathResolved = cliPath
	}

	// Install-time pre-auth gate.
	//
	// The daemon path itself is now non-interactive: runServe calls
	// IsAuthenticated only, no browser fallback. That means the only
	// reliable moment to seed a token is right here, before any unit file
	// is written, while we still have a human and a tty.
	//
	// Behavior matrix:
	//   --skip-auth-check       → bypass entirely (CI / MDM init scripts)
	//   tty + unauthed          → prompt via `databricks auth login`
	//   tty + authed            → no-op (fast path)
	//   non-tty + unauthed      → abort with canonical error before write
	//   non-tty + authed        → no-op
	//   CLI not found + unauthed→ propagate IsAuthenticated=false → abort
	//                             (cliPath empty; auth fails identically)
	if !f.skipAuthCheck {
		interactive := isInteractiveStdin()
		if err := authcheck.EnsureOrCheck(profile, cliPathResolved, interactive); err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: serve install: %v\n", err)
			os.Exit(1)
		}
	}

	opts := installOptions{
		binPath:      binPath,
		port:         port,
		profile:      profile,
		logFile:      logFile,
		metricsTable: resolvedMetrics,
		logsTable:    resolvedLogs,
		tracesTable:  resolvedTraces,
		cliPath:      cliPathResolved,
	}

	if err := installDaemon(opts); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude serve install: %v\n", err)
		os.Exit(1)
	}

	// Post-install /health probe with 10s deadline. Service managers report
	// "started" before the process is actually serving; without this probe,
	// a crash-loop would silently pass install and only show up later in
	// `serve status` or when the next Claude Code session tried to connect.
	healthy := waitForHealth(port, 10*time.Second)
	if healthy {
		fmt.Fprintf(os.Stderr, "databricks-claude: daemon installed and healthy at 127.0.0.1:%d\n", port)
	} else {
		fmt.Fprintln(os.Stderr, "databricks-claude: serve install: post-install probe timed out after 10s; daemon may still be starting — see diagnostics below")
		if tail, _ := diagnosticsTail(); tail != "" {
			fmt.Fprintln(os.Stderr, "--- daemon diagnostics ---")
			fmt.Fprintln(os.Stderr, tail)
			fmt.Fprintln(os.Stderr, "--- end diagnostics ---")
		}
	}

	fmt.Fprintf(os.Stderr, "  Service: %s\n", daemonServiceName)
	fmt.Fprintf(os.Stderr, "  Binary:  %s\n", binPath)
	if cliPathResolved != "" {
		fmt.Fprintf(os.Stderr, "  CLI:     %s\n", cliPathResolved)
	}
	fmt.Fprintf(os.Stderr, "  Profile: %s\n", profile)
	fmt.Fprintf(os.Stderr, "  Port:    %d\n", port)
	fmt.Fprintf(os.Stderr, "  Log:     %s\n", logFile)
	fmt.Fprintf(os.Stderr, "\nRun 'databricks-claude serve status' to verify.\n")
}

// waitForHealth polls 127.0.0.1:<port>/health every 500ms until it returns
// healthy or the deadline elapses. Returns true on success.
func waitForHealth(port int, deadline time.Duration) bool {
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if _, ok := health.ProxyMode(port, "http"); ok {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func runUninstall(args []string) {
	r, _ := uninstallCommand().Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stderr, uninstallCommand(), nil)
		os.Exit(0)
	}

	if err := uninstallDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude serve uninstall: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "databricks-claude: daemon stopped and unregistered\n")
}

func runStatus(args []string) {
	r, _ := statusCommand().Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stderr, statusCommand(), nil)
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

	// Running rendering — if the service-manager reports a failed state,
	// surface that explicitly so a crash-loop can't be masked by a
	// momentarily-true `state = running` read. The Linux daemonStatus sets
	// Failed when systemd `is-failed` succeeds or Result != "success"; the
	// darwin daemonStatus sets it when launchctl reports a non-zero last
	// exit code. Either way we want Running=no with the failure detail.
	runStr := boolStr(r.Running)
	if r.Failed {
		parts := []string{"failed"}
		if r.FailureDetail != "" {
			parts = append(parts, r.FailureDetail)
		}
		if r.LastExitCode != "" && r.LastExitCode != "0" {
			parts = append(parts, "last-exit="+r.LastExitCode)
		}
		runStr = "no (" + strings.Join(parts, ", ") + ")"
	}
	fmt.Printf("Running:    %s\n", runStr)

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

// Help for `serve install`, `serve uninstall`, `serve status` is rendered
// via cmd.Render against the corresponding tree node in serveCommand
// (commands.go). The four hand-rolled printServeInstall*/printServeStatus*
// /printServeUninstallHelp functions that previously lived here were
// deleted in #171.
