package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/codex/tomlconfig"
	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// Golden config.toml outputs for the top-level default-provider shape (#230):
// the proxy is registered as the root model_provider (NOT a named profile
// selector), so bare `codex` via hooks/daemon routes through it and Codex
// >=0.134 does not reject a root `profile` selector. These pins guard the
// observable config.toml wire shape: root model + model_provider, the
// model_providers.databricks-proxy block, and the [otel] section when set.
const (
	goldenProxyURL = "http://127.0.0.1:49154"

	// Fresh install, OTEL off, explicit model.
	goldenFreshNoOTEL = "\nmodel = \"databricks-gpt-5-5\"\n" +
		"model_provider = \"databricks-proxy\"\n\n" +
		"[model_providers.databricks-proxy]\n" +
		"name = \"Databricks Proxy\"\n" +
		"base_url = \"http://127.0.0.1:49154\"\n" +
		"api_key = \"databricks-proxy\"\n" +
		"wire_api = \"responses\"\n"

	// Fresh install, OTEL on (both metrics + logs endpoints).
	goldenFreshOTEL = "\nmodel = \"databricks-gpt-5-5\"\n" +
		"model_provider = \"databricks-proxy\"\n\n" +
		"[model_providers.databricks-proxy]\n" +
		"name = \"Databricks Proxy\"\n" +
		"base_url = \"http://127.0.0.1:49154\"\n" +
		"api_key = \"databricks-proxy\"\n" +
		"wire_api = \"responses\"\n\n" +
		"[otel]\n" +
		"environment = \"production\"\n" +
		"exporter = { otlp-http = { endpoint = \"http://127.0.0.1:49154/otel/v1/logs\", protocol = \"binary\" } }\n" +
		"metrics_exporter = { otlp-http = { endpoint = \"http://127.0.0.1:49154/otel/v1/metrics\", protocol = \"binary\" } }\n"
)

// newCodexPatcherAt builds the field-bearing patcher pointed at a test cfgPath,
// mirroring the production newCodexPatcher (which targets the default
// ~/.codex/config.toml) but injecting a temp-file path so goldens are hermetic.
func newCodexPatcherAt(cfgPath, model string, modelExplicit bool, metricsTable, logsTable string, otelEnabled bool) codexSettingsPatcher {
	return codexSettingsPatcher{
		mgr:              tomlconfig.NewManager(cfgPath),
		model:            model,
		modelExplicit:    modelExplicit,
		otelMetricsTable: metricsTable,
		otelLogsTable:    logsTable,
		otelEnabled:      otelEnabled,
	}
}

// TestCodexPatch_GoldenFreshNoOTEL asserts a fresh-install patch (no prior
// config.toml, OTEL off) produces the pinned config.toml byte-for-byte.
func TestCodexPatch_GoldenFreshNoOTEL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	p := newCodexPatcherAt(cfgPath, "databricks-gpt-5-5", true, "", "", false)
	if err := p.Patch(profile.PatchRequest{ProxyURL: goldenProxyURL}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	got := readFile(t, cfgPath)
	if got != goldenFreshNoOTEL {
		t.Errorf("config.toml not byte-identical to golden\n--- got ---\n%q\n--- want ---\n%q", got, goldenFreshNoOTEL)
	}
}

// TestCodexPatch_GoldenFreshOTEL asserts the OTEL-on patch emits the [otel]
// section (both exporter + metrics_exporter) byte-for-byte.
func TestCodexPatch_GoldenFreshOTEL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	p := newCodexPatcherAt(cfgPath, "databricks-gpt-5-5", true,
		"main.codex_telemetry.codex_otel_metrics",
		"main.codex_telemetry.codex_otel_logs", true)
	if err := p.Patch(profile.PatchRequest{ProxyURL: goldenProxyURL}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	got := readFile(t, cfgPath)
	if got != goldenFreshOTEL {
		t.Errorf("config.toml not byte-identical to golden\n--- got ---\n%q\n--- want ---\n%q", got, goldenFreshOTEL)
	}
}

// TestCodexPatch_StaleBaseURLSelfHeal asserts a dead prior proxy's base_url is
// replaced idempotently on the next patch: patching over a config that already
// points at a stale, dead port heals base_url to the live proxy URL, and a
// second identical patch is a no-op (byte-stable).
func TestCodexPatch_StaleBaseURLSelfHeal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	stale := "model = \"databricks-gpt-5-5\"\n" +
		"model_provider = \"databricks-proxy\"\n\n" +
		"[model_providers.databricks-proxy]\n" +
		"name = \"Databricks Proxy\"\n" +
		"base_url = \"http://127.0.0.1:59998\"\n" +
		"api_key = \"databricks-proxy\"\n" +
		"wire_api = \"responses\"\n"
	if err := os.WriteFile(cfgPath, []byte(stale), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A fresh Manager per patch, as the launch path constructs.
	patchOnce := func() string {
		p := newCodexPatcherAt(cfgPath, "databricks-gpt-5-5", true, "", "", false)
		if err := p.Patch(profile.PatchRequest{ProxyURL: goldenProxyURL}); err != nil {
			t.Fatalf("Patch: %v", err)
		}
		return readFile(t, cfgPath)
	}

	first := patchOnce()
	if strings.Contains(first, "59998") {
		t.Errorf("stale base_url 59998 was not self-healed:\n%s", first)
	}
	if !strings.Contains(first, `base_url = "http://127.0.0.1:49154"`) {
		t.Errorf("live base_url not written after self-heal:\n%s", first)
	}

	// Idempotency: patching again with the same live URL must not drift.
	second := patchOnce()
	if first != second {
		t.Errorf("re-patch is not idempotent:\n--- first ---\n%q\n--- second ---\n%q", first, second)
	}
}

// TestCodexPatch_OTELEraseParity asserts the config.toml-level erase semantics
// the two-store model promises: enabling OTEL emits the [otel] section,
// disabling it (otelEnabled=false → empty endpoints → tomlconfig.removeSection)
// erases the section, and re-enabling restores it. This is the codex analog of
// claude's OTEL-section-removal invariant.
func TestCodexPatch_OTELEraseParity(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	patch := func(otelEnabled bool) string {
		p := newCodexPatcherAt(cfgPath, "databricks-gpt-5-5", true,
			"main.codex_telemetry.codex_otel_metrics",
			"main.codex_telemetry.codex_otel_logs", otelEnabled)
		if err := p.Patch(profile.PatchRequest{ProxyURL: goldenProxyURL}); err != nil {
			t.Fatalf("Patch: %v", err)
		}
		return readFile(t, cfgPath)
	}

	// 1. Enable → [otel] present.
	enabled := patch(true)
	if !strings.Contains(enabled, "[otel]") {
		t.Fatalf("expected [otel] section after enable:\n%s", enabled)
	}

	// 2. Disable → [otel] erased entirely (not just stale exporter lines).
	disabled := patch(false)
	if strings.Contains(disabled, "[otel]") {
		t.Errorf("expected [otel] section erased after disable:\n%s", disabled)
	}
	if strings.Contains(disabled, "otel/v1/") {
		t.Errorf("expected all OTEL exporter endpoints erased after disable:\n%s", disabled)
	}
	// The rest of config.toml (provider block) must survive the erase.
	if !strings.Contains(disabled, `base_url = "http://127.0.0.1:49154"`) {
		t.Errorf("provider block must survive [otel] erase:\n%s", disabled)
	}

	// 3. Re-enable → [otel] section restored with both exporter endpoints.
	// (Whole-file bytes differ from the very first enable because the first
	// enable APPENDS the sections onto an empty file — emitting inter-section
	// blank lines — while every subsequent patch REPLACES the existing blocks;
	// that blank-line handling is documented surgical-patcher behavior. The
	// parity claim is that the section round-trips, and that steady-state
	// re-patches are idempotent.)
	reEnabled := patch(true)
	if !strings.Contains(reEnabled, "[otel]") {
		t.Errorf("re-enable did not restore the [otel] section:\n%s", reEnabled)
	}
	if !strings.Contains(reEnabled, `endpoint = "http://127.0.0.1:49154/otel/v1/logs"`) ||
		!strings.Contains(reEnabled, `endpoint = "http://127.0.0.1:49154/otel/v1/metrics"`) {
		t.Errorf("re-enabled [otel] missing exporter endpoints:\n%s", reEnabled)
	}
	// The provider block still survives the disable→re-enable round trip.
	if !strings.Contains(reEnabled, `base_url = "http://127.0.0.1:49154"`) {
		t.Errorf("provider block lost across OTEL round trip:\n%s", reEnabled)
	}
}

// TestCodexConfig_CrossPathByteIdentical is the F2 guarantee: wrapper mode
// (core.Run drives PatchSettings.Patch with a fully-populated PatchRequest —
// PortFlag/ProfileName/Env set) and the serve entrypoint (serve_codex.go builds
// profile2Request, only ProxyURL set) must emit byte-identical config.toml for
// identical resolved inputs. Codex's patcher reads ONLY req.ProxyURL, so the
// extra wrapper-side fields must not perturb the output.
func TestCodexConfig_CrossPathByteIdentical(t *testing.T) {
	const model = "databricks-gpt-5-5"
	const metrics = "main.codex_telemetry.codex_otel_metrics"
	const logs = "main.codex_telemetry.codex_otel_logs"

	writeVia := func(req profile.PatchRequest) string {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		p := newCodexPatcherAt(cfgPath, model, true, metrics, logs, true)
		if err := p.Patch(req); err != nil {
			t.Fatalf("Patch: %v", err)
		}
		return readFile(t, cfgPath)
	}

	// Wrapper path: core.Run populates all PatchRequest fields.
	wrapper := writeVia(profile.PatchRequest{
		PortFlag:    49154,
		ProfileName: "aidev",
		ProxyURL:    goldenProxyURL,
		Env:         map[string]string{"IGNORED": "by-codex"},
	})

	// Serve path: the real profile2Request helper (only ProxyURL).
	serve := writeVia(profile2Request(goldenProxyURL))

	if wrapper != serve {
		t.Errorf("wrapper-mode and serve-mode config.toml differ (F2 byte-parity broken)\n--- wrapper ---\n%q\n--- serve ---\n%q", wrapper, serve)
	}
	// And both must equal the OTEL golden.
	if wrapper != goldenFreshOTEL {
		t.Errorf("cross-path output drifted from OTEL golden\n--- got ---\n%q\n--- want ---\n%q", wrapper, goldenFreshOTEL)
	}
}

// --- shared helpers ---

// inTempCodexHome runs fn with HOME and statePath redirected to a fresh temp
// dir, restoring both before returning. tomlconfig.NewManager("") and
// codexHooksPath() both resolve under HOME, so this isolates the whole codex
// dotfile tree.
func inTempCodexHome(t *testing.T, fn func(home string)) {
	t.Helper()
	dir := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	origState := statePath
	statePath = func() string { return filepath.Join(dir, ".codex", ".databricks-codex.json") }
	defer func() {
		if hadHome {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
		statePath = origState
	}()
	fn(dir)
}

// readCodexConfig reads ~/.codex/config.toml under the given home.
func readCodexConfig(t *testing.T, home string) string {
	t.Helper()
	return readFile(t, filepath.Join(home, ".codex", "config.toml"))
}

// readFile reads a file as a string, failing the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
