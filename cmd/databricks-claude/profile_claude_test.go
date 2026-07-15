package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// Compile-time conformance for the three claude implementations.
var (
	_ profile.SettingsPatcher = claudeSettingsPatcher{}
	_ profile.DaemonStrategy  = claudeDaemon{}
	_ profile.HookInstaller   = claudeHooks{}
)

// TestClaudeProfile_Fields asserts the constructed Profile carries the expected
// scalar fields and non-nil interface implementations.
func TestClaudeProfile_Fields(t *testing.T) {
	p := ClaudeProfile()
	if p.Name != ProfileName {
		t.Errorf("Name = %q, want %q", p.Name, ProfileName)
	}
	if p.Name != "databricks-claude" {
		t.Errorf("ProfileName = %q, want %q", p.Name, "databricks-claude")
	}
	if p.ChildBinary != "claude" {
		t.Errorf("ChildBinary = %q, want %q", p.ChildBinary, "claude")
	}
	if p.GatewayPath != "/ai-gateway/anthropic" {
		t.Errorf("GatewayPath = %q, want %q", p.GatewayPath, "/ai-gateway/anthropic")
	}
	if p.ConfigPath == nil {
		t.Error("ConfigPath must be non-nil")
	}
	if p.PatchSettings == nil {
		t.Error("PatchSettings must be non-nil")
	}
	if p.DaemonStrategy == nil {
		t.Error("DaemonStrategy must be non-nil")
	}
	if p.HookInstaller == nil {
		t.Error("HookInstaller must be non-nil")
	}
}

// TestDaemonInstallRequest_MirrorsInstallOptions asserts the neutral
// DaemonInstallRequest is a lossless mirror of the internal installOptions: the
// same field count and the same case-normalized field-name set. installOptions
// fields are unexported lowercase (binPath), DaemonInstallRequest fields are
// exported (BinPath); normalization is a simple lowercase.
func TestDaemonInstallRequest_MirrorsInstallOptions(t *testing.T) {
	reqT := reflect.TypeOf(profile.DaemonInstallRequest{})
	optT := reflect.TypeOf(installOptions{})

	if reqT.NumField() != optT.NumField() {
		t.Fatalf("field count mismatch: DaemonInstallRequest=%d installOptions=%d",
			reqT.NumField(), optT.NumField())
	}
	if reqT.NumField() != 8 {
		t.Fatalf("DaemonInstallRequest field count = %d, want 8", reqT.NumField())
	}

	reqNames := normalizedFieldSet(reqT)
	optNames := normalizedFieldSet(optT)
	if !reflect.DeepEqual(reqNames, optNames) {
		t.Errorf("case-normalized field sets differ:\n  DaemonInstallRequest=%v\n  installOptions=%v",
			reqNames, optNames)
	}
}

// TestToInstallOptions_MapsAll8Fields pushes a DaemonInstallRequest with a
// distinct value per field through toInstallOptions and asserts each landed in
// the correct installOptions field. Unlike the field-set completeness check
// above, this gates the actual per-field assignments, so a same-typed cross-wire
// (e.g. cliPath↔logFile — the load-bearing minimal-PATH daemon fix — or a swap
// among metricsTable/logsTable/tracesTable) fails loudly instead of passing
// silently.
func TestToInstallOptions_MapsAll8Fields(t *testing.T) {
	req := profile.DaemonInstallRequest{
		BinPath:      "sentinel-binpath",
		Port:         54321,
		Profile:      "sentinel-profile",
		LogFile:      "sentinel-logfile",
		MetricsTable: "sentinel-metrics",
		LogsTable:    "sentinel-logs",
		TracesTable:  "sentinel-traces",
		CLIPath:      "sentinel-clipath",
	}
	opt := toInstallOptions(req)
	if opt.binPath != req.BinPath {
		t.Errorf("binPath = %q, want %q", opt.binPath, req.BinPath)
	}
	if opt.port != req.Port {
		t.Errorf("port = %d, want %d", opt.port, req.Port)
	}
	if opt.profile != req.Profile {
		t.Errorf("profile = %q, want %q", opt.profile, req.Profile)
	}
	if opt.logFile != req.LogFile {
		t.Errorf("logFile = %q, want %q", opt.logFile, req.LogFile)
	}
	if opt.metricsTable != req.MetricsTable {
		t.Errorf("metricsTable = %q, want %q", opt.metricsTable, req.MetricsTable)
	}
	if opt.logsTable != req.LogsTable {
		t.Errorf("logsTable = %q, want %q", opt.logsTable, req.LogsTable)
	}
	if opt.tracesTable != req.TracesTable {
		t.Errorf("tracesTable = %q, want %q", opt.tracesTable, req.TracesTable)
	}
	if opt.cliPath != req.CLIPath {
		t.Errorf("cliPath = %q, want %q", opt.cliPath, req.CLIPath)
	}
}

// TestToDaemonStatus_MapsAll11Fields fills a statusResult with distinct sentinel
// values, maps it through toDaemonStatus, and asserts every DaemonStatus field
// equals the same-named statusResult field and is non-zero (nothing dropped or
// cross-wired).
func TestToDaemonStatus_MapsAll11Fields(t *testing.T) {
	src := statusResult{
		Registered:    true,
		Running:       true,
		Healthy:       true,
		HealthMode:    "HealthMode",
		Version:       "Version",
		Profile:       "Profile",
		ManifestPath:  "ManifestPath",
		BinaryPath:    "BinaryPath",
		LastExitCode:  "LastExitCode",
		Failed:        true,
		FailureDetail: "FailureDetail",
	}

	got := toDaemonStatus(src)

	gv := reflect.ValueOf(got)
	gt := gv.Type()
	sv := reflect.ValueOf(src)

	if gt.NumField() != 11 {
		t.Fatalf("DaemonStatus field count = %d, want 11", gt.NumField())
	}
	if reflect.TypeOf(src).NumField() != 11 {
		t.Fatalf("statusResult field count = %d, want 11", reflect.TypeOf(src).NumField())
	}

	for i := 0; i < gt.NumField(); i++ {
		name := gt.Field(i).Name
		sf := sv.FieldByName(name)
		if !sf.IsValid() {
			t.Errorf("DaemonStatus field %q has no statusResult counterpart", name)
			continue
		}
		if gv.Field(i).IsZero() {
			t.Errorf("DaemonStatus field %q was dropped (zero after mapping)", name)
		}
		if gv.Field(i).Interface() != sf.Interface() {
			t.Errorf("DaemonStatus field %q = %v, want %v (from statusResult)",
				name, gv.Field(i).Interface(), sf.Interface())
		}
	}
}

// TestFromDaemonStatus_RoundTrip verifies the reverse mapping used by runStatus
// is a lossless inverse of toDaemonStatus.
func TestFromDaemonStatus_RoundTrip(t *testing.T) {
	// Distinct bool values (not all-true) so a bool↔bool cross-wire in either
	// direction of the mapping breaks struct equality instead of round-tripping
	// silently — printStatusResult reads Failed, so a Failed↔Registered swap
	// would misrender `serve status`.
	src := statusResult{
		Registered:    true,
		Running:       false,
		Healthy:       true,
		HealthMode:    "mode",
		Version:       "v1",
		Profile:       "prof",
		ManifestPath:  "/manifest",
		BinaryPath:    "/bin",
		LastExitCode:  "0",
		Failed:        false,
		FailureDetail: "detail",
	}
	if got := fromDaemonStatus(toDaemonStatus(src)); got != src {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, src)
	}
}

// normalizedFieldSet returns the set of struct field names lowercased, so an
// exported field (BinPath) and its unexported counterpart (binPath) compare
// equal.
func normalizedFieldSet(t reflect.Type) map[string]bool {
	set := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		set[strings.ToLower(t.Field(i).Name)] = true
	}
	return set
}
