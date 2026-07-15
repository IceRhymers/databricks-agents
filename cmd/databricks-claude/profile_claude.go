package main

import (
	"os"
	"path/filepath"

	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// ProfileName is the stable identifier for the databricks-claude profile. Live
// wiring references ClaudeProfile() by this typed constructor rather than a
// string Registry.Lookup, so the compiler proves the seam is connected.
const ProfileName = "databricks-claude"

// claudeSettingsPath resolves ~/.claude/settings.json. Extracted from main.go's
// inline path logic so the SettingsPatcher/HookInstaller impls share one
// definition of "the claude settings file".
func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// claudeSettingsPatcher implements profile.SettingsPatcher for claude by
// delegating to the existing bootstrapSettings free function. Patch MUST remain
// a pure one-line delegation so settings.json bytes stay identical to the
// pre-refactor path (byte-identical-save invariant).
type claudeSettingsPatcher struct{}

func (claudeSettingsPatcher) Patch(req profile.PatchRequest) error {
	return bootstrapSettings(req.PortFlag, req.ProfileName, req.ProxyURL, req.Env)
}

// Restore is an explicit unwired no-op scoped to #E: there is no settings
// restore free function in this branch to delegate to. Kept for interface-shape
// stability only.
func (claudeSettingsPatcher) Restore(profile.RestoreRequest) error {
	return nil
}

// claudeDaemon implements profile.DaemonStrategy for claude by delegating to the
// OS-specific installDaemon / uninstallDaemon / daemonStatus / diagnosticsTail
// free functions. It never returns profile.ErrDaemonUnsupported — the
// unsupported-platform `other` build's installDaemon returns a plain error.
type claudeDaemon struct{}

func (claudeDaemon) Install(req profile.DaemonInstallRequest) error {
	return installDaemon(toInstallOptions(req))
}

// toInstallOptions maps the neutral profile.DaemonInstallRequest (all 8 fields)
// into the launcher-internal installOptions. Extracted from Install so the
// per-field assignments are unit-testable by value — a same-typed cross-wire
// (e.g. cliPath↔logFile, the load-bearing minimal-PATH daemon fix, or a swap
// among the OTEL table fields) is invisible to a field-set completeness check
// and needs a value-level gate.
func toInstallOptions(req profile.DaemonInstallRequest) installOptions {
	return installOptions{
		binPath:      req.BinPath,
		port:         req.Port,
		profile:      req.Profile,
		logFile:      req.LogFile,
		metricsTable: req.MetricsTable,
		logsTable:    req.LogsTable,
		tracesTable:  req.TracesTable,
		cliPath:      req.CLIPath,
	}
}

func (claudeDaemon) Uninstall() error {
	return uninstallDaemon()
}

func (claudeDaemon) Status(port int) (profile.DaemonStatus, error) {
	r, err := daemonStatus(port)
	if err != nil {
		return profile.DaemonStatus{}, err
	}
	return toDaemonStatus(r), nil
}

func (claudeDaemon) Diagnostics() (string, error) {
	return diagnosticsTail()
}

// toDaemonStatus maps the launcher-internal statusResult (all 11 fields) into
// the neutral profile.DaemonStatus. Every field is carried — the
// mapping-completeness test asserts none is dropped.
func toDaemonStatus(r statusResult) profile.DaemonStatus {
	return profile.DaemonStatus{
		Registered:    r.Registered,
		Running:       r.Running,
		Healthy:       r.Healthy,
		HealthMode:    r.HealthMode,
		Version:       r.Version,
		Profile:       r.Profile,
		ManifestPath:  r.ManifestPath,
		BinaryPath:    r.BinaryPath,
		LastExitCode:  r.LastExitCode,
		Failed:        r.Failed,
		FailureDetail: r.FailureDetail,
	}
}

// fromDaemonStatus is the inverse of toDaemonStatus: it maps the neutral
// profile.DaemonStatus returned across the DaemonStrategy interface back into
// the launcher-internal statusResult that printStatusResult renders. Kept
// symmetric with toDaemonStatus so both directions carry all 11 fields.
func fromDaemonStatus(s profile.DaemonStatus) statusResult {
	return statusResult{
		Registered:    s.Registered,
		Running:       s.Running,
		Healthy:       s.Healthy,
		HealthMode:    s.HealthMode,
		Version:       s.Version,
		Profile:       s.Profile,
		ManifestPath:  s.ManifestPath,
		BinaryPath:    s.BinaryPath,
		LastExitCode:  s.LastExitCode,
		Failed:        s.Failed,
		FailureDetail: s.FailureDetail,
	}
}

// claudeHooks implements profile.HookInstaller for claude by delegating to the
// installHooks / uninstallHooks free functions against ~/.claude/settings.json.
// It does NOT touch bootstrapSettings — the first-run bootstrap in hooks_cmd.go
// is a separate concern outside the HookInstaller seam.
type claudeHooks struct{}

func (claudeHooks) Install() error {
	path, err := claudeSettingsPath()
	if err != nil {
		return err
	}
	return installHooks(path)
}

func (claudeHooks) Uninstall() error {
	path, err := claudeSettingsPath()
	if err != nil {
		return err
	}
	return uninstallHooks(path)
}

// Compile-time conformance assertions.
var (
	_ profile.SettingsPatcher = claudeSettingsPatcher{}
	_ profile.DaemonStrategy  = claudeDaemon{}
	_ profile.HookInstaller   = claudeHooks{}
)

// ClaudeProfile constructs the databricks-claude profile.Profile. Built in
// package main (not internal/profile) so Profile construction never drags
// pkg/modeldiscovery or other launcher-only deps into internal/profile.
func ClaudeProfile() profile.Profile {
	return profile.Profile{
		Name:           ProfileName,
		ChildBinary:    "claude",
		ConfigPath:     claudeSettingsPath,
		GatewayPath:    "/ai-gateway/anthropic",
		PatchSettings:  claudeSettingsPatcher{},
		DaemonStrategy: claudeDaemon{},
		HookInstaller:  claudeHooks{},
	}
}
