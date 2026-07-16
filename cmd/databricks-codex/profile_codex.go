package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/IceRhymers/databricks-agents/internal/codex/tomlconfig"
	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// ProfileName is the stable identifier for the databricks-codex profile. Live
// wiring references CodexProfile() by this typed constructor rather than a
// string Registry.Lookup, so the compiler proves the seam is connected.
const ProfileName = "databricks-codex"

// codexConfigPath resolves ~/.codex/config.toml. Unlike claude (whose settings
// file is a JSON env block), codex's "settings file" is the TOML config the
// tomlconfig.Manager surgically patches. core.Run never reads Profile.ConfigPath
// (it drives patching through PatchSettings), so this is informational metadata
// mirroring the claudeSettingsPath seam.
func codexConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// codexSettingsPatcher implements profile.SettingsPatcher for codex. Unlike
// claude — which writes an env block into settings.json — codex has NO
// settings.json env block; its per-session configuration is the surgical
// patch of ~/.codex/config.toml performed by tomlconfig.Manager. That is why
// the LaunchPlan sets BuildEnv = nil and this patcher carries the launch-time
// static values (model, OTEL tables) as fields (Option A): only base_url +
// the OTEL exporter endpoints are proxyURL-dependent, and those are computed
// from req.ProxyURL here at patch time.
//
// The zero value is a valid profile.SettingsPatcher (satisfies the compile
// assertion and the Registry's API-shape usage); Patch lazily constructs a
// default Manager when mgr is nil so the zero value never nil-derefs. The
// launch path always supplies a field-bearing value via buildCodexLaunchPlan.
type codexSettingsPatcher struct {
	mgr              *tomlconfig.Manager
	model            string
	modelExplicit    bool
	otelMetricsTable string
	otelLogsTable    string
	otelEnabled      bool
}

// newCodexPatcher is the single constructor for a field-bearing
// codexSettingsPatcher. Both config.toml writers — wrapper-mode
// (buildCodexLaunchPlan → core.Run) and the serve sibling (runServeSession) —
// build their patcher HERE so their fields cannot drift apart; that is the
// structural half of the F2 byte-parity guarantee (TestCodexConfig_CrossPathByteIdentical
// is the behavioral half). Every call site passes a fresh tomlconfig.NewManager("").
func newCodexPatcher(model string, modelExplicit bool, otelMetricsTable, otelLogsTable string, otelEnabled bool) codexSettingsPatcher {
	return codexSettingsPatcher{
		mgr:              tomlconfig.NewManager(""),
		model:            model,
		modelExplicit:    modelExplicit,
		otelMetricsTable: otelMetricsTable,
		otelLogsTable:    otelLogsTable,
		otelEnabled:      otelEnabled,
	}
}

// Patch surgically rewrites ~/.codex/config.toml so codex authenticates through
// the local proxy. It computes base_url + the OTEL exporter endpoints from
// req.ProxyURL and delegates to tomlconfig.Patch — the SAME writer the serve
// sibling calls (F2), so both entrypoints emit byte-identical config.toml for
// identical inputs. The OTEL endpoint gating mirrors the pre-unification inline
// EnsureConfig: an endpoint is emitted only when OTEL is enabled AND its table
// is non-empty (an empty endpoint makes tomlconfig remove the [otel] section
// rather than leave stale exporter lines).
//
// Patch is invoked exactly once per process (once by core.Run in wrapper mode,
// once by runServeSession in serve mode) and never concurrently, so the
// in-process sync.Mutex the pre-unification ConfigManager.EnsureConfig held is
// not needed here.
func (p codexSettingsPatcher) Patch(req profile.PatchRequest) error {
	mgr := p.mgr
	if mgr == nil {
		mgr = tomlconfig.NewManager("")
	}

	otelLogsEndpoint := ""
	otelMetricsEndpoint := ""
	if p.otelEnabled {
		if p.otelLogsTable != "" {
			otelLogsEndpoint = req.ProxyURL + "/otel/v1/logs"
		}
		if p.otelMetricsTable != "" {
			otelMetricsEndpoint = req.ProxyURL + "/otel/v1/metrics"
		}
	}

	if err := mgr.Patch(tomlconfig.PatchConfig{
		ProxyURL:            req.ProxyURL,
		Model:               p.model,
		ModelExplicit:       p.modelExplicit,
		OTELLogsEndpoint:    otelLogsEndpoint,
		OTELMetricsEndpoint: otelMetricsEndpoint,
	}); err != nil {
		return err
	}

	// Clean up any stale backup from pre-v0.6.0 crash recovery (parity with
	// the pre-unification ConfigManager.EnsureConfig side effect).
	os.Remove(mgr.ConfigPath() + ".databricks-codex-backup")

	log.Printf("databricks-codex: ensured config.toml (proxy: %s)", req.ProxyURL)
	return nil
}

// Restore is an explicit unwired no-op scoped to #E, matching the claude
// profile: there is no config.toml restore free function to delegate to on
// this branch. Kept for interface-shape stability only.
func (codexSettingsPatcher) Restore(profile.RestoreRequest) error {
	return nil
}

// codexDaemon implements profile.DaemonStrategy for codex as an INERT
// API-shape conformance. Codex has no long-lived OS service: its `serve`
// subcommand is a session/headless leaf with no install/uninstall/status
// (no LaunchAgent/systemd/schtasks). These methods are never invoked at
// runtime; they exist only so codex fills the same Profile slots as claude.
// Install/Uninstall return the ErrDaemonUnsupported sentinel; Status and
// Diagnostics return zero values.
type codexDaemon struct{}

func (codexDaemon) Install(profile.DaemonInstallRequest) error {
	return profile.ErrDaemonUnsupported
}

func (codexDaemon) Uninstall() error {
	return profile.ErrDaemonUnsupported
}

func (codexDaemon) Status(int) (profile.DaemonStatus, error) {
	return profile.DaemonStatus{}, nil
}

func (codexDaemon) Diagnostics() (string, error) {
	return "", nil
}

// codexHooks implements profile.HookInstaller for codex by delegating to the
// installHooks / uninstallHooks free functions against ~/.codex/hooks.json.
// It carries the config.toml [features] hooks=true side effect (and its
// documented TOCTOU) forward unchanged from the standalone repo.
type codexHooks struct{}

func codexHooksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "hooks.json"), nil
}

func (codexHooks) Install() error {
	path, err := codexHooksPath()
	if err != nil {
		return err
	}
	return installHooks(path)
}

func (codexHooks) Uninstall() error {
	path, err := codexHooksPath()
	if err != nil {
		return err
	}
	return uninstallHooks(path)
}

// Compile-time conformance assertions. The zero-value patcher satisfies the
// interface so Registry / tests can register a representative Profile.
var (
	_ profile.SettingsPatcher = codexSettingsPatcher{}
	_ profile.DaemonStrategy  = codexDaemon{}
	_ profile.HookInstaller   = codexHooks{}
)

// CodexProfile constructs the databricks-codex profile.Profile. It is a
// one-arg factory (Option A): the launch path passes the field-bearing
// codexSettingsPatcher built by buildCodexLaunchPlan; Registry/tests pass a
// zero-value codexSettingsPatcher{}. Built in package main (not
// internal/profile) so Profile construction never drags tomlconfig or other
// launcher-only deps into internal/profile.
func CodexProfile(patcher profile.SettingsPatcher) profile.Profile {
	return profile.Profile{
		Name:           ProfileName,
		ChildBinary:    "codex",
		ConfigPath:     codexConfigPath,
		GatewayPath:    gatewayPath,
		PatchSettings:  patcher,
		DaemonStrategy: codexDaemon{},
		HookInstaller:  codexHooks{},
	}
}
