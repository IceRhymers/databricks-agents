package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
