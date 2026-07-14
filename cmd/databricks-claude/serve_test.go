package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/proxy"
)

// --- parseServeFlags tests ---

func TestParseServeFlags_Defaults(t *testing.T) {
	f := parseServeFlags(nil)
	if f.port != 0 {
		t.Errorf("port default: got %d, want 0", f.port)
	}
	if f.profile != "" {
		t.Errorf("profile default: got %q, want empty", f.profile)
	}
	if f.logFile != "" {
		t.Errorf("logFile default: got %q, want empty", f.logFile)
	}
	if f.verbose {
		t.Error("verbose default: got true, want false")
	}
	if f.metricsTableSet || f.logsTableSet || f.tracesTableSet {
		t.Error("tableSet flags default: expected all false")
	}
}

func TestParseServeFlags_Port(t *testing.T) {
	f := parseServeFlags([]string{"--port", "12345"})
	if f.port != 12345 {
		t.Errorf("got port %d, want 12345", f.port)
	}
}

func TestParseServeFlags_PortEqualForm(t *testing.T) {
	f := parseServeFlags([]string{"--port=49153"})
	if f.port != 49153 {
		t.Errorf("got port %d, want 49153", f.port)
	}
}

func TestParseServeFlags_LogFile(t *testing.T) {
	f := parseServeFlags([]string{"--log-file", "/tmp/d.log"})
	if f.logFile != "/tmp/d.log" {
		t.Errorf("got logFile %q, want /tmp/d.log", f.logFile)
	}
}

func TestParseServeFlags_LogFileEqualForm(t *testing.T) {
	f := parseServeFlags([]string{"--log-file=/var/log/d.log"})
	if f.logFile != "/var/log/d.log" {
		t.Errorf("got logFile %q, want /var/log/d.log", f.logFile)
	}
}

func TestParseServeFlags_Verbose(t *testing.T) {
	for _, arg := range []string{"--verbose", "-v"} {
		f := parseServeFlags([]string{arg})
		if !f.verbose {
			t.Errorf("verbose flag not set for arg %q", arg)
		}
	}
}

func TestParseServeFlags_Profile(t *testing.T) {
	f := parseServeFlags([]string{"--profile", "my-workspace"})
	if f.profile != "my-workspace" {
		t.Errorf("got profile %q, want my-workspace", f.profile)
	}
}

func TestParseServeFlags_ProfileEqualForm(t *testing.T) {
	f := parseServeFlags([]string{"--profile=prod"})
	if f.profile != "prod" {
		t.Errorf("got profile %q, want prod", f.profile)
	}
}

func TestParseServeFlags_OTELTables(t *testing.T) {
	f := parseServeFlags([]string{
		"--otel-metrics-table", "cat.schema.metrics",
		"--otel-logs-table", "cat.schema.logs",
		"--otel-traces-table=cat.schema.traces",
	})
	if f.metricsTable != "cat.schema.metrics" {
		t.Errorf("metricsTable: got %q, want cat.schema.metrics", f.metricsTable)
	}
	if !f.metricsTableSet {
		t.Error("metricsTableSet: expected true")
	}
	if f.logsTable != "cat.schema.logs" {
		t.Errorf("logsTable: got %q, want cat.schema.logs", f.logsTable)
	}
	if !f.logsTableSet {
		t.Error("logsTableSet: expected true")
	}
	if f.tracesTable != "cat.schema.traces" {
		t.Errorf("tracesTable: got %q, want cat.schema.traces", f.tracesTable)
	}
	if !f.tracesTableSet {
		t.Error("tracesTableSet: expected true")
	}
}

func TestParseServeFlags_Mixed(t *testing.T) {
	f := parseServeFlags([]string{
		"--port", "49153",
		"--profile", "dev",
		"--verbose",
		"--log-file=/tmp/d.log",
		"--otel-metrics-table", "main.t.metrics",
	})
	if f.port != 49153 {
		t.Errorf("port: got %d, want 49153", f.port)
	}
	if f.profile != "dev" {
		t.Errorf("profile: got %q, want dev", f.profile)
	}
	if !f.verbose {
		t.Error("verbose: expected true")
	}
	if f.logFile != "/tmp/d.log" {
		t.Errorf("logFile: got %q, want /tmp/d.log", f.logFile)
	}
	if f.metricsTable != "main.t.metrics" || !f.metricsTableSet {
		t.Errorf("metrics table: got %q set=%v", f.metricsTable, f.metricsTableSet)
	}
}

// --- openLogFile O_APPEND tests ---

// TestOpenLogFile_AppendsNotTruncates verifies that openLogFile opens with
// O_APPEND so restarts do not destroy prior log content.
func TestOpenLogFile_AppendsNotTruncates(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "daemon.log")

	// Write initial content.
	prior := "prior line\n"
	if err := os.WriteFile(path, []byte(prior), 0o600); err != nil {
		t.Fatalf("write prior: %v", err)
	}

	f, err := openLogFile(path)
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}
	defer f.Close()

	// Write a new line through the file handle.
	newLine := "new line\n"
	if _, err := f.WriteString(newLine); err != nil {
		t.Fatalf("write new line: %v", err)
	}
	f.Close()

	// Verify prior content is preserved.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readfile: %v", err)
	}
	if string(got) != prior+newLine {
		t.Errorf("file content = %q, want %q", string(got), prior+newLine)
	}
}

// TestOpenLogFile_CreatesMissing verifies that openLogFile creates the file if absent.
func TestOpenLogFile_CreatesMissing(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "new.log")

	f, err := openLogFile(path)
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}
	f.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

// --- resolveTableFromChain tests (OTEL table resolution matrix) ---

// noMDM is a stub that always returns empty (simulates no MDM config).
func noMDM(domain, key string) (string, error) { return "", nil }

// staticMDM returns value for the exact key, empty otherwise.
func staticMDM(key, value string) func(string, string) (string, error) {
	return func(_, k string) (string, error) {
		if k == key {
			return value, nil
		}
		return "", nil
	}
}

func TestResolveTableFromChain_FlagWins(t *testing.T) {
	got := resolveTableFromChain("flag-val", true, "state-val", "mdmKey", staticMDM("mdmKey", "mdm-val"))
	if got != "flag-val" {
		t.Errorf("got %q, want flag-val", got)
	}
}

func TestResolveTableFromChain_StateWinsWhenNoFlag(t *testing.T) {
	got := resolveTableFromChain("", false, "state-val", "mdmKey", staticMDM("mdmKey", "mdm-val"))
	if got != "state-val" {
		t.Errorf("got %q, want state-val", got)
	}
}

func TestResolveTableFromChain_MDMWinsWhenNoFlagOrState(t *testing.T) {
	got := resolveTableFromChain("", false, "", "otelMetricsTable", staticMDM("otelMetricsTable", "mdm-metrics"))
	if got != "mdm-metrics" {
		t.Errorf("got %q, want mdm-metrics", got)
	}
}

func TestResolveTableFromChain_EmptyWhenNothingSet(t *testing.T) {
	got := resolveTableFromChain("", false, "", "otelMetricsTable", noMDM)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestResolveTableFromChain_SentinelGuard verifies that an empty state value
// (the sentinel for "unset") falls through to MDM — regression guard on the
// resolver side.
func TestResolveTableFromChain_SentinelGuard(t *testing.T) {
	// stateVal="" must fall through to MDM, not return "".
	got := resolveTableFromChain("", false, "", "otelMetricsTable", staticMDM("otelMetricsTable", "mdm-from-sentinel-fallthrough"))
	if got != "mdm-from-sentinel-fallthrough" {
		t.Errorf("sentinel guard: got %q, want mdm-from-sentinel-fallthrough", got)
	}
}

// TestShouldPersistOTELTable exercises the writer-side sentinel guard
// directly. This is the real regression test for the footgun caught in
// databricks-claude PR #149 (state value shadowing MDM tier on subsequent
// runs). All three conditions of shouldPersistOTELTable must hold; if any
// future refactor drops one of them, this test fails.
func TestShouldPersistOTELTable(t *testing.T) {
	cases := []struct {
		name     string
		flagSet  bool
		resolved string
		stateVal string
		want     bool
		why      string
	}{
		{
			name: "explicit_new_value", flagSet: true, resolved: "main.x.metrics", stateVal: "",
			want: true, why: "fresh user input must persist",
		},
		{
			name: "explicit_overrides_existing", flagSet: true, resolved: "new.table", stateVal: "old.table",
			want: true, why: "explicit flag overwrites differing state",
		},
		{
			name: "explicit_same_as_state_no_write", flagSet: true, resolved: "same.table", stateVal: "same.table",
			want: false, why: "no-op write would touch state file unnecessarily",
		},
		{
			name: "flag_not_set_no_write", flagSet: false, resolved: "from.state.or.mdm", stateVal: "",
			want: false, why: "values resolved from state or MDM must NOT round-trip back to state",
		},
		{
			name: "flag_set_empty_resolved_no_write", flagSet: true, resolved: "", stateVal: "old.table",
			want: false, why: "empty is the unset sentinel — must never be persisted (PR #149 footgun)",
		},
		{
			name: "flag_set_empty_resolved_empty_state_no_write", flagSet: true, resolved: "", stateVal: "",
			want: false, why: "empty-to-empty is a no-op AND would write the sentinel",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldPersistOTELTable(c.flagSet, c.resolved, c.stateVal)
			if got != c.want {
				t.Errorf("shouldPersistOTELTable(flagSet=%v, resolved=%q, stateVal=%q): got %v, want %v — %s",
					c.flagSet, c.resolved, c.stateVal, got, c.want, c.why)
			}
		})
	}
}

// TestServe_StdoutSilence_HelpPath builds the real binary and invokes
// `databricks-claude serve --help`. Because `runServe` reassigns
// `os.Stdout = os.Stderr` BEFORE parsing flags, even the --help output (which
// is normally printed to stdout) lands on stderr. This is the smoke test for
// AC #2 / pre-mortem scenario 1: a transitive SDK call writing to stdout must
// not be able to corrupt the LaunchAgent stdout log. If a future refactor
// removes the belt-and-suspenders redirect, this test fails.
//
// We use --help (not actual daemon startup) because the latter requires real
// Databricks auth and would fail in CI; --help still exercises the redirect
// because it runs after line `os.Stdout = os.Stderr` and before `os.Exit(0)`.
func TestServe_StdoutSilence_HelpPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "databricks-claude")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "serve", "--help")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("serve --help: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
	}

	if stdout.Len() != 0 {
		t.Errorf("AC #2 violation: serve --help wrote %d bytes to stdout (expected 0); content:\n%s",
			stdout.Len(), stdout.String())
	}
	// Sanity check: the help text MUST land somewhere — stderr.
	if !strings.Contains(stderr.String(), "serve") {
		t.Errorf("help text not found on stderr — redirect may have eaten it entirely\nstderr=%q", stderr.String())
	}
}

// TestServe_SubSubcommandHelp_RoutesCorrectly verifies that
// `serve install --help`, `serve uninstall --help`, and `serve status --help`
// each reach their sub-subcommand-specific help function — NOT the parent
// `printServeHelp` via the --help short-circuit. Regression guard for the
// bug caught in PR #155 round-1 review: if `hasFlag(args, "--help")` fires
// before the sub-subcommand dispatch in `runServe`, the install/uninstall/
// status help is dead code on the user-facing path.
//
// **Sentinel choice matters.** The parent `printServeHelp` lists the three
// sub-subcommand names in its "Sub-subcommands" section and "Examples"
// section, so substrings like "serve install" appear in BOTH the parent help
// AND the sub-subcommand help — a test that asserts those would still pass
// under the regression. Each sentinel here is a line that appears ONLY in
// the corresponding sub-subcommand help body, verified empirically against
// `serve --help` output. If you adjust any of the help-text functions,
// re-verify the sentinels stay unique to their respective help.
func TestServe_SubSubcommandHelp_RoutesCorrectly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "databricks-claude")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	// Capture the parent `serve --help` body once so each sub-subcommand
	// assertion can also verify its sentinel is absent from the parent —
	// belt-and-suspenders against future help-text edits that accidentally
	// leak a sentinel into both places.
	parentCmd := exec.Command(bin, "serve", "--help")
	var parentOut, parentErr bytes.Buffer
	parentCmd.Stdout = &parentOut
	parentCmd.Stderr = &parentErr
	if err := parentCmd.Run(); err != nil {
		t.Fatalf("serve --help: %v\nstderr=%q", err, parentErr.String())
	}
	parentBody := parentOut.String() + parentErr.String()

	cases := []struct {
		subcmd   string
		sentinel string // a string that appears ONLY in this sub-subcommand's help, NOT in parent serve help
	}{
		{"install", "Service name: databricks-claude-daemon"},
		{"uninstall", `Tolerates "not installed" gracefully.`},
		{"status", "Registered — manifest/task/unit file exists"},
	}
	for _, c := range cases {
		t.Run(c.subcmd, func(t *testing.T) {
			// First confirm sentinel is genuinely unique — guards against
			// help-text drift silently breaking the regression test.
			if strings.Contains(parentBody, c.sentinel) {
				t.Fatalf("sentinel %q for %q help leaked into parent serve --help body; pick a sentinel that appears ONLY in the sub-subcommand help", c.sentinel, c.subcmd)
			}

			cmd := exec.Command(bin, "serve", c.subcmd, "--help")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("serve %s --help: %v\nstdout=%q\nstderr=%q", c.subcmd, err, stdout.String(), stderr.String())
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, c.sentinel) {
				t.Errorf("serve %s --help: output missing %q (sub-subcommand help did not route; parent serve help may be short-circuiting)\nstdout=%q\nstderr=%q",
					c.subcmd, c.sentinel, stdout.String(), stderr.String())
			}
		})
	}
}

// TestServe_Status_OutputGoesToStdout verifies that `serve status` writes
// its result to REAL stdout, not stderr. The long-lived daemon path in
// `runServe` reassigns `os.Stdout = os.Stderr` to protect the LaunchAgent
// stdout log, but that redirect must NOT apply to one-shot user-facing
// sub-subcommands. Regression guard for the bug caught in PR #155 round-1
// review: if sub-subcommand dispatch happens AFTER the stdout redirect,
// `serve status | grep Healthy` is broken in shipped binaries.
//
// `serve status` with no daemon registered returns an "all-no" result on
// the current platform and exits with a documented non-zero code. We assert
// the human-readable output lands on stdout regardless of the exit code.
func TestServe_Status_OutputGoesToStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "databricks-claude")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "serve", "status")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Don't fail on non-zero exit — `status` may exit non-zero when nothing
	// is registered. We care about the stream the OUTPUT landed on.
	_ = cmd.Run()

	if stdout.Len() == 0 {
		t.Errorf("serve status: expected human-readable output on stdout, got 0 bytes\nstderr=%q", stderr.String())
	}
	// Sanity: the output should mention the canonical service name.
	if !strings.Contains(stdout.String(), "databricks-claude-daemon") {
		t.Errorf("serve status stdout does not contain canonical service name; redirect may be misrouting\nstdout=%q\nstderr=%q",
			stdout.String(), stderr.String())
	}
}

// TestResolveTableFromChain_AllThreeSignals exercises the full resolution matrix
// for each of metrics, logs, and traces independently.
func TestResolveTableFromChain_AllThreeSignals(t *testing.T) {
	cases := []struct {
		name   string
		mdmKey string
		mdmVal string
	}{
		{"metrics", "otelMetricsTable", "mdm.metrics"},
		{"logs", "otelLogsTable", "mdm.logs"},
		{"traces", "otelTracesTable", "mdm.traces"},
	}
	for _, c := range cases {
		t.Run(c.name+"_mdm_tier", func(t *testing.T) {
			got := resolveTableFromChain("", false, "", c.mdmKey, staticMDM(c.mdmKey, c.mdmVal))
			if got != c.mdmVal {
				t.Errorf("%s MDM tier: got %q, want %q", c.name, got, c.mdmVal)
			}
		})
		t.Run(c.name+"_empty_when_unset", func(t *testing.T) {
			got := resolveTableFromChain("", false, "", c.mdmKey, noMDM)
			if got != "" {
				t.Errorf("%s empty case: got %q, want empty", c.name, got)
			}
		})
	}
}

// --- Port resolution tests ---

// TestServePortResolution verifies that resolvePort follows flag > state > 49153.
func TestServePortResolution(t *testing.T) {
	cases := []struct {
		name      string
		flagPort  int
		statePort int
		want      int
	}{
		{"flag wins", 12345, 54321, 12345},
		{"state wins when no flag", 0, 54321, 54321},
		{"default when neither", 0, 0, defaultPort},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := persistentState{Port: c.statePort}
			got := resolvePort(c.flagPort, st)
			if got != c.want {
				t.Errorf("resolvePort(%d, {Port:%d}) = %d, want %d",
					c.flagPort, c.statePort, got, c.want)
			}
		})
	}
}

// --- buildServeProxyConfig tests ---
//
// These tests lock in the WebSearch wiring on the daemon path. They exist
// because dropping `WebSearch:` from the proxy.Config struct literal — the
// exact regression that caused the original bug — must fail loudly here,
// not silently in production. Helper-only unit tests (resolution defaults)
// are not sufficient; these assert the wired output.

// captureStderr redirects os.Stderr for the duration of fn and returns the
// captured bytes. Used by TestBuildServeProxyConfig_WebSearchEnabledBogusBackend
// to assert the fail-soft error message lands on stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	defer func() {
		os.Stderr = orig
	}()

	fn()
	w.Close()
	<-done
	return buf.String()
}

func TestBuildServeProxyConfig_WebSearchDisabled(t *testing.T) {
	st := persistentState{}
	r := serveResolved{
		inferenceUpstream: "https://example.com/ai-gateway/anthropic",
		otelUpstream:      "https://example.com/api/2.0/otel",
		profile:           "test",
	}
	cfg := buildServeProxyConfig(st, r, serveModeDaemon)
	if cfg.WebSearch.Enabled {
		t.Error("WebSearch.Enabled: got true, want false")
	}
	if cfg.WebSearch.Backend != nil {
		t.Errorf("WebSearch.Backend: got %T, want nil", cfg.WebSearch.Backend)
	}
	if cfg.WebSearch.Robots != nil {
		t.Errorf("WebSearch.Robots: got %T, want nil", cfg.WebSearch.Robots)
	}
	if !cfg.Daemon {
		t.Error("Daemon: got false, want true")
	}
	if cfg.Profile != "test" {
		t.Errorf("Profile: got %q, want test", cfg.Profile)
	}
}

func TestBuildServeProxyConfig_WebSearchEnabledDefaults(t *testing.T) {
	st := persistentState{WithWebSearch: true}
	r := serveResolved{
		inferenceUpstream: "https://example.com/ai-gateway/anthropic",
		otelUpstream:      "https://example.com/api/2.0/otel",
		profile:           "test",
	}

	out := captureStderr(t, func() {
		_ = buildServeProxyConfig(st, r, serveModeDaemon)
	})
	if !strings.Contains(out, "--with-websearch is a workaround") {
		t.Errorf("expected workaround banner on stderr, got: %q", out)
	}

	var got *proxy.Config
	_ = captureStderr(t, func() { got = buildServeProxyConfig(st, r, serveModeDaemon) })
	if !got.WebSearch.Enabled {
		t.Error("WebSearch.Enabled: got false, want true")
	}
	if got.WebSearch.Backend == nil {
		t.Fatal("WebSearch.Backend: got nil, want non-nil")
	}
	if name := got.WebSearch.Backend.Name(); name != "duckduckgo" {
		t.Errorf("WebSearch.Backend.Name(): got %q, want duckduckgo", name)
	}
	if got.WebSearch.Robots == nil {
		t.Error("WebSearch.Robots: got nil, want non-nil")
	}
	if got.WebSearch.FetchBudget != 100*1024 {
		t.Errorf("WebSearch.FetchBudget: got %d, want %d", got.WebSearch.FetchBudget, 100*1024)
	}
}

func TestBuildServeProxyConfig_WebSearchEnabledExplicitBudget(t *testing.T) {
	st := persistentState{
		WithWebSearch:        true,
		WebSearchFetchBudget: 204800,
	}
	r := serveResolved{
		inferenceUpstream: "https://example.com/ai-gateway/anthropic",
		otelUpstream:      "https://example.com/api/2.0/otel",
	}
	_ = captureStderr(t, func() {
		c := buildServeProxyConfig(st, r, serveModeDaemon)
		if !c.WebSearch.Enabled {
			t.Error("WebSearch.Enabled: got false, want true")
		}
		if c.WebSearch.FetchBudget != 204800 {
			t.Errorf("WebSearch.FetchBudget: got %d, want 204800", c.WebSearch.FetchBudget)
		}
	})
}

func TestBuildServeProxyConfig_WebSearchEnabledBogusBackend(t *testing.T) {
	st := persistentState{
		WithWebSearch:    true,
		WebSearchBackend: "bogus-backend-that-does-not-exist",
	}
	r := serveResolved{
		inferenceUpstream: "https://example.com/ai-gateway/anthropic",
		otelUpstream:      "https://example.com/api/2.0/otel",
	}

	// Route log.Printf into a buffer so we can assert the dual-log signal.
	origOut := log.Writer()
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	stderr := captureStderr(t, func() {
		c := buildServeProxyConfig(st, r, serveModeDaemon)
		// Fail-soft: bad backend disables websearch but daemon stays up.
		if c.WebSearch.Enabled {
			t.Error("WebSearch.Enabled: got true, want false (fail-soft on bogus backend)")
		}
		if c.WebSearch.Backend != nil {
			t.Errorf("WebSearch.Backend: got %T, want nil after fail-soft", c.WebSearch.Backend)
		}
		if c.WebSearch.Robots != nil {
			t.Errorf("WebSearch.Robots: got %T, want nil after fail-soft", c.WebSearch.Robots)
		}
	})
	if !strings.Contains(stderr, "websearch backend build failed") {
		t.Errorf("stderr missing fail-soft error: %q", stderr)
	}
	if !strings.Contains(stderr, "websearch DISABLED") {
		t.Errorf("stderr missing DISABLED notice: %q", stderr)
	}
	if !strings.Contains(logBuf.String(), "websearch backend build failed") {
		t.Errorf("log.Printf missing fail-soft error: %q", logBuf.String())
	}
}

// --- #174 mode-dispatch tests ---

// TestDetermineServeMode is the load-bearing invariant test: bare `serve`
// (no mode flag, no sub-subcommand) AND `serve --session-mode --daemon`
// (both modes set) MUST be hard errors. This is the headline mitigation
// against the silent-degradation hazard at the hooks spawn site — a typo
// dropping --session-mode in pkg/headless.buildArgs would otherwise launch
// the daemon (wrong lifecycle, no /shutdown, broken hooks session-end).
func TestDetermineServeMode(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		want     serveMode
		wantErr  bool
		errPiece string
	}{
		{"session only", []string{"--session-mode"}, serveModeSession, false, ""},
		{"session via equals", []string{"--session-mode=true"}, serveModeSession, false, ""},
		{"daemon only", []string{"--daemon"}, serveModeDaemon, false, ""},
		{"daemon via equals", []string{"--daemon=true"}, serveModeDaemon, false, ""},
		{"neither — must be hard error", []string{"--port", "49153"}, serveModeUnset, true, "must specify"},
		{"both — must be hard error", []string{"--session-mode", "--daemon"}, serveModeUnset, true, "mutually exclusive"},
		{"help long path returns unset+nil", []string{"--help"}, serveModeUnset, false, ""},
		{"help short path returns unset+nil", []string{"-h"}, serveModeUnset, false, ""},
		{"help wins over missing mode", []string{"--port", "49153", "--help"}, serveModeUnset, false, ""},
		{"session + other flags", []string{"--session-mode", "--port", "49153", "--idle-timeout", "5m"}, serveModeSession, false, ""},
		{"daemon + otel tables", []string{"--daemon", "--otel-metrics-table", "cat.s.m"}, serveModeDaemon, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := determineServeMode(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Errorf("determineServeMode(%v): want error, got nil", tc.args)
					return
				}
				if tc.errPiece != "" && !strings.Contains(err.Error(), tc.errPiece) {
					t.Errorf("determineServeMode(%v): error %q does not contain %q", tc.args, err, tc.errPiece)
				}
				return
			}
			if err != nil {
				t.Errorf("determineServeMode(%v): unexpected error %v", tc.args, err)
				return
			}
			if got != tc.want {
				t.Errorf("determineServeMode(%v): got mode %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestServe_BareInvocation_HardError builds the binary and runs `serve` with
// no mode flag and no sub-subcommand. It MUST exit with code 2 and emit the
// "must specify" error to stderr. This is the user-facing manifestation of
// the required-explicit-mode invariant — the integration assertion behind
// TestDetermineServeMode.
func TestServe_BareInvocation_HardError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "databricks-claude")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "serve")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected non-zero exit, got success\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("expected exit code 2 for bare 'serve' (#174 invariant), got %d\nstderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must specify") {
		t.Errorf("stderr does not mention 'must specify' — error message regressed:\nstderr=%q", stderr.String())
	}
}

// --- #174 buildServeProxyConfig matrix ---
//
// Mode × every field. Locks the per-mode wiring so a future refactor that
// drops a field on either branch fails here loudly. Includes WebSearch as a
// regression net for #163.

func TestBuildServeProxyConfig_Matrix(t *testing.T) {
	st := persistentState{}
	r := serveResolved{
		profile:           "matrix-test",
		inferenceUpstream: "https://example.com/ai-gateway/anthropic",
		otelUpstream:      "https://example.com/api/2.0/otel",
		metricsTable:      "cat.s.m",
		logsTable:         "cat.s.l",
		tracesTable:       "cat.s.t",
		verbose:           true,
		apiKey:            "session-only-key",
		tlsCert:           "/cert.pem",
		tlsKey:            "/key.pem",
	}

	t.Run("session_mode", func(t *testing.T) {
		cfg := buildServeProxyConfig(st, r, serveModeSession)
		if cfg.Daemon {
			t.Error("session mode: cfg.Daemon = true, want false (lifecycle wrap registers /shutdown)")
		}
		if cfg.APIKey != "session-only-key" {
			t.Errorf("session mode: cfg.APIKey = %q, want %q (session mode wires APIKey)", cfg.APIKey, "session-only-key")
		}
		if cfg.TLSCertFile != "/cert.pem" {
			t.Errorf("session mode: cfg.TLSCertFile = %q, want /cert.pem", cfg.TLSCertFile)
		}
		if cfg.TLSKeyFile != "/key.pem" {
			t.Errorf("session mode: cfg.TLSKeyFile = %q, want /key.pem", cfg.TLSKeyFile)
		}
		if cfg.Profile != "matrix-test" {
			t.Errorf("session mode: cfg.Profile = %q, want matrix-test", cfg.Profile)
		}
		if cfg.InferenceUpstream != r.inferenceUpstream {
			t.Errorf("session mode: cfg.InferenceUpstream = %q, want %q", cfg.InferenceUpstream, r.inferenceUpstream)
		}
		if cfg.OTELUpstream != r.otelUpstream {
			t.Errorf("session mode: cfg.OTELUpstream = %q, want %q", cfg.OTELUpstream, r.otelUpstream)
		}
		if cfg.UCMetricsTable != "cat.s.m" || cfg.UCLogsTable != "cat.s.l" || cfg.UCTracesTable != "cat.s.t" {
			t.Errorf("session mode: UC tables wired wrong: metrics=%q logs=%q traces=%q", cfg.UCMetricsTable, cfg.UCLogsTable, cfg.UCTracesTable)
		}
		if !cfg.Verbose {
			t.Error("session mode: cfg.Verbose = false, want true")
		}
		if cfg.ToolName != "databricks-claude" {
			t.Errorf("session mode: cfg.ToolName = %q, want databricks-claude", cfg.ToolName)
		}
		// WebSearch field must be wired — regression net from #163.
		if cfg.WebSearch.Enabled {
			t.Error("session mode: cfg.WebSearch.Enabled = true with no state, want false")
		}
	})

	t.Run("daemon_mode", func(t *testing.T) {
		cfg := buildServeProxyConfig(st, r, serveModeDaemon)
		if !cfg.Daemon {
			t.Error("daemon mode: cfg.Daemon = false, want true")
		}
		// Daemon mode does NOT wire APIKey / TLS — the help template gates
		// those flags onto --session-mode and the daemon code path has
		// never accepted them.
		if cfg.APIKey != "" {
			t.Errorf("daemon mode: cfg.APIKey = %q, want empty (daemon does not accept --proxy-api-key)", cfg.APIKey)
		}
		if cfg.TLSCertFile != "" {
			t.Errorf("daemon mode: cfg.TLSCertFile = %q, want empty (daemon does not accept --tls-cert)", cfg.TLSCertFile)
		}
		if cfg.TLSKeyFile != "" {
			t.Errorf("daemon mode: cfg.TLSKeyFile = %q, want empty", cfg.TLSKeyFile)
		}
		if cfg.Profile != "matrix-test" {
			t.Errorf("daemon mode: cfg.Profile = %q, want matrix-test", cfg.Profile)
		}
		if cfg.InferenceUpstream != r.inferenceUpstream {
			t.Errorf("daemon mode: cfg.InferenceUpstream = %q, want %q", cfg.InferenceUpstream, r.inferenceUpstream)
		}
		if cfg.UCMetricsTable != "cat.s.m" || cfg.UCLogsTable != "cat.s.l" || cfg.UCTracesTable != "cat.s.t" {
			t.Errorf("daemon mode: UC tables wired wrong: metrics=%q logs=%q traces=%q", cfg.UCMetricsTable, cfg.UCLogsTable, cfg.UCTracesTable)
		}
		if !cfg.Verbose {
			t.Error("daemon mode: cfg.Verbose = false, want true")
		}
		if cfg.WebSearch.Enabled {
			t.Error("daemon mode: cfg.WebSearch.Enabled = true with no state, want false")
		}
	})

	// Bidirectional WebSearch positive case. The matrix above asserts
	// Enabled==false with empty state for BOTH modes — that branch alone
	// is tautological (deleting the factory's `Enabled: <wired>` line
	// still yields the zero value `false`). This sub-test populates state
	// with WithWebSearch=true and asserts the factory propagates it to
	// BOTH lifecycle policies. If a future refactor drops the WebSearch
	// wiring for one mode, this fails loudly — the regression net the
	// review for #180 flagged was missing.
	t.Run("websearch_enabled_both_modes", func(t *testing.T) {
		stOn := persistentState{
			WithWebSearch:        true,
			WebSearchBackend:     "duckduckgo",
			WebSearchFetchBudget: 7 * 1024,
		}
		for _, mode := range []serveMode{serveModeSession, serveModeDaemon} {
			name := "session"
			if mode == serveModeDaemon {
				name = "daemon"
			}
			cfg := buildServeProxyConfig(stOn, r, mode)
			if !cfg.WebSearch.Enabled {
				t.Errorf("%s mode: cfg.WebSearch.Enabled = false with state.WithWebSearch=true, want true (factory dropped the wiring for this mode)", name)
			}
			if cfg.WebSearch.Backend == nil {
				t.Errorf("%s mode: cfg.WebSearch.Backend = nil, want a concrete backend impl", name)
			}
			if cfg.WebSearch.FetchBudget != 7*1024 {
				t.Errorf("%s mode: cfg.WebSearch.FetchBudget = %d, want %d", name, cfg.WebSearch.FetchBudget, 7*1024)
			}
		}
	})
}

// TestParseServeFlags_SessionMode verifies that the --session-mode boolean
// flag and the new session-only flags (--idle-timeout, --proxy-api-key,
// --tls-cert, --tls-key, --upstream) parse correctly through parseServeFlags.
func TestParseServeFlags_SessionMode(t *testing.T) {
	f := parseServeFlags([]string{
		"--session-mode",
		"--idle-timeout", "5m",
		"--proxy-api-key", "key123",
		"--tls-cert", "/c.pem",
		"--tls-key", "/k.pem",
		"--upstream", "https://override.example.com",
	})
	if !f.sessionMode {
		t.Error("sessionMode: got false, want true")
	}
	if f.idleTimeout != 5*time.Minute {
		t.Errorf("idleTimeout: got %v, want 5m", f.idleTimeout)
	}
	if f.apiKey != "key123" {
		t.Errorf("apiKey: got %q, want key123", f.apiKey)
	}
	if f.tlsCert != "/c.pem" {
		t.Errorf("tlsCert: got %q, want /c.pem", f.tlsCert)
	}
	if f.tlsKey != "/k.pem" {
		t.Errorf("tlsKey: got %q, want /k.pem", f.tlsKey)
	}
	if f.upstream != "https://override.example.com" {
		t.Errorf("upstream: got %q, want https://override.example.com", f.upstream)
	}
}

// TestParseServeFlags_DaemonFlag verifies the --daemon boolean.
func TestParseServeFlags_DaemonFlag(t *testing.T) {
	f := parseServeFlags([]string{"--daemon"})
	if !f.daemon {
		t.Error("daemon: got false, want true")
	}
	if f.sessionMode {
		t.Error("sessionMode: got true, want false")
	}
}

// TestParseServeFlags_IdleTimeoutDefault verifies the default 30m.
func TestParseServeFlags_IdleTimeoutDefault(t *testing.T) {
	f := parseServeFlags([]string{"--session-mode"})
	if f.idleTimeout != 30*time.Minute {
		t.Errorf("default idleTimeout: got %v, want 30m", f.idleTimeout)
	}
	if f.idleTimeoutSet {
		t.Error("idleTimeoutSet: got true, want false (default not user-supplied)")
	}
}

// TestServe_SpawnInvocation_TargetsSessionMode is the load-bearing
// post-#174 contract: the spawn invocation that pkg/headless.buildArgs
// emits MUST target `serve --session-mode`, NOT the deleted --headless
// root flag. headlessEnsure (in hooks.go) is the only databricks-claude
// site that calls into pkg/headless, so we assert by replicating its
// EnsureCommand wiring directly.
func TestServe_SpawnInvocation_TargetsSessionMode(t *testing.T) {
	// Mirror what headlessEnsure passes — keep this expectation in lockstep
	// with hooks.go.
	want := []string{"serve", "--session-mode"}
	// Sanity grep: hooks.go's headlessEnsure body must reference exactly
	// this prefix. If a future refactor moves the wiring elsewhere, this
	// test serves as a "look here" trail rather than missing the change.
	src, err := os.ReadFile("hooks.go")
	if err != nil {
		t.Fatalf("read hooks.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `EnsureCommand: []string{"serve", "--session-mode"}`) {
		t.Errorf("hooks.go's headlessEnsure no longer wires EnsureCommand to %v — pkg/headless.buildArgs will spawn the wrong child", want)
	}
}
