package main

import (
	"log"

	"github.com/IceRhymers/databricks-agents/internal/opencode/jsonconfig"
	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// ProfileName is the stable identifier for the databricks-opencode profile.
// Live wiring references OpencodeProfile() by this typed constructor rather
// than a string Registry.Lookup, so the compiler proves the seam is connected.
const ProfileName = "databricks-opencode"

// opencodeConfigPath resolves <opencode-config-dir>/opencode.json. Unlike
// claude (whose settings file is a JSON env block), opencode's "settings file"
// is the JSON config the jsonconfig.Config surgically patches. core.Run never
// reads Profile.ConfigPath (it drives patching through PatchSettings), so this
// is informational metadata mirroring the codexConfigPath seam.
func opencodeConfigPath() (string, error) {
	dir, err := opencodeConfigDir()
	if err != nil {
		return "", err
	}
	return dir + "/opencode.json", nil
}

// opencodeSettingsPatcher implements profile.SettingsPatcher for opencode.
// Unlike claude — which writes an env block into settings.json — opencode has
// NO settings.json env block; its per-session configuration is the surgical
// patch of opencode.json performed by jsonconfig.Config. That is why the
// LaunchPlan sets BuildEnv = nil and this patcher carries the launch-time
// static values (model, modelExplicit, apiKey) as fields (Option A): only the
// baseURL is proxyURL-dependent, and it is computed from req.ProxyURL here at
// patch time.
//
// The zero value is a valid profile.SettingsPatcher (satisfies the compile
// assertion and the Registry's API-shape usage); Patch resolves the config
// dir at patch time and no-ops through the NeedsConfig gate. The launch path
// always supplies a field-bearing value via newOpencodePatcher.
type opencodeSettingsPatcher struct {
	model         string
	modelExplicit bool
	apiKey        string
}

// newOpencodePatcher is the single constructor for a field-bearing
// opencodeSettingsPatcher. Both opencode.json writers — wrapper-mode
// (buildOpencodeLaunchPlan → core.Run) and the serve sibling
// (runServeSession) — build their patcher HERE so their fields cannot drift
// apart. The apiKey placeholder is resolved once, in the constructor, so both
// entrypoints emit byte-identical opencode.json for identical inputs: use the
// explicit --proxy-api-key when set, else the fixed "databricks-proxy"
// placeholder (the proxy rewrites auth headers with a live Databricks token —
// the value here just needs to be non-empty for the @ai-sdk providers).
func newOpencodePatcher(model string, modelExplicit bool, proxyAPIKey string) opencodeSettingsPatcher {
	apiKey := proxyAPIKey
	if apiKey == "" {
		apiKey = "databricks-proxy"
	}
	return opencodeSettingsPatcher{
		model:         model,
		modelExplicit: modelExplicit,
		apiKey:        apiKey,
	}
}

// Patch surgically rewrites opencode.json so opencode authenticates through the
// local proxy. It computes the config dir, then gates on NeedsConfig — the
// idempotent check the pre-unification EnsureConfig performed inline. core.Run
// calls Patch unconditionally once the port binds, so the NeedsConfig gate MUST
// live here; without it every launch would reformat/de-comment the user's
// opencode.json even when it already points at the right proxy. When a rewrite
// is needed it delegates to jsonconfig.Config.Patch — the SAME writer the serve
// sibling calls, so both entrypoints emit byte-identical opencode.json for
// identical inputs.
func (p opencodeSettingsPatcher) Patch(req profile.PatchRequest) error {
	dir, err := opencodeConfigDir()
	if err != nil {
		return err
	}
	cfg := jsonconfig.New(dir)
	if !cfg.NeedsConfig(req.ProxyURL) {
		log.Printf("databricks-opencode: opencode.json already configured for %s", req.ProxyURL)
		return nil
	}
	log.Printf("databricks-opencode: opencode.json for %s", req.ProxyURL)
	return cfg.Patch(req.ProxyURL, p.model, p.apiKey, p.modelExplicit)
}

// Restore is an explicit no-op: opencode's config is a persistent
// patch-and-leave-it file (no backup, no restore), matching the codex profile.
// Kept for interface-shape stability only.
func (opencodeSettingsPatcher) Restore(profile.RestoreRequest) error {
	return nil
}

// opencodeDaemon implements profile.DaemonStrategy for opencode as an INERT
// API-shape conformance. opencode has no long-lived OS service: its `serve`
// subcommand is a session/headless leaf with no install/uninstall/status
// (no LaunchAgent/systemd/schtasks). These methods are never invoked at
// runtime; they exist only so opencode fills the same Profile slots as claude.
// Install/Uninstall return the ErrDaemonUnsupported sentinel; Status and
// Diagnostics return zero values.
type opencodeDaemon struct{}

func (opencodeDaemon) Install(profile.DaemonInstallRequest) error {
	return profile.ErrDaemonUnsupported
}

func (opencodeDaemon) Uninstall() error {
	return profile.ErrDaemonUnsupported
}

func (opencodeDaemon) Status(int) (profile.DaemonStatus, error) {
	return profile.DaemonStatus{}, nil
}

func (opencodeDaemon) Diagnostics() (string, error) {
	return "", nil
}

// opencodeHooks implements profile.HookInstaller for opencode by delegating to
// the installHooks / uninstallHooks free functions. Unlike codex, opencode's
// installHooks takes NO path argument — it resolves the opencode config dir
// itself (the JS plugin lives under <config-dir>/plugins/databricks-proxy/).
type opencodeHooks struct{}

func (opencodeHooks) Install() error {
	return installHooks()
}

func (opencodeHooks) Uninstall() error {
	return uninstallHooks()
}

// Compile-time conformance assertions. The zero-value patcher satisfies the
// interface so Registry / tests can register a representative Profile.
var (
	_ profile.SettingsPatcher = opencodeSettingsPatcher{}
	_ profile.DaemonStrategy  = opencodeDaemon{}
	_ profile.HookInstaller   = opencodeHooks{}
)

// OpencodeProfile constructs the databricks-opencode profile.Profile. It is a
// one-arg factory (Option A): the launch path passes the field-bearing
// opencodeSettingsPatcher built by buildOpencodeLaunchPlan; Registry/tests pass
// a zero-value opencodeSettingsPatcher{}. Built in package main (not
// internal/profile) so Profile construction never drags jsonconfig or other
// launcher-only deps into internal/profile.
func OpencodeProfile(patcher profile.SettingsPatcher) profile.Profile {
	return profile.Profile{
		Name:           ProfileName,
		ChildBinary:    "opencode",
		ConfigPath:     opencodeConfigPath,
		GatewayPath:    "/ai-gateway/anthropic",
		PatchSettings:  patcher,
		DaemonStrategy: opencodeDaemon{},
		HookInstaller:  opencodeHooks{},
	}
}
