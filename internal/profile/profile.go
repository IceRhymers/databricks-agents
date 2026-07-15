package profile

import "errors"

// Profile is the per-tool descriptor that a launcher (databricks-claude,
// databricks-codex, databricks-opencode) fills in with its own implementations
// of the three seams below. Because each launcher is its own `package main` and
// cannot be imported, the interfaces live here in internal/profile and the
// concrete implementations live in the launcher's own package (see
// cmd/databricks-claude/profile_claude.go for the claude wiring).
//
// There is deliberately NO EnvVars field: the env block written into
// settings.json is assembled at the call site (main.go) and handed to
// SettingsPatcher.Patch via PatchRequest.Env. Baking a static env map into the
// Profile would lose the per-run OTEL / model-routing values computed at launch.
type Profile struct {
	// Name is the stable identifier for the tool (e.g. "databricks-claude").
	Name string
	// ChildBinary is the downstream binary the wrapper launches (e.g. "claude").
	ChildBinary string
	// ConfigPath resolves the tool's settings file path (e.g.
	// ~/.claude/settings.json). Returns an error when the home dir is
	// unresolvable.
	ConfigPath func() (string, error)
	// GatewayPath is the AI Gateway URL suffix appended to the Databricks host
	// (e.g. "/ai-gateway/anthropic").
	GatewayPath string
	// PatchSettings mutates the tool's settings file to point at the proxy.
	PatchSettings SettingsPatcher
	// DaemonStrategy installs/uninstalls/queries the long-lived OS service.
	DaemonStrategy DaemonStrategy
	// HookInstaller manages the session-lifecycle hook entries.
	HookInstaller HookInstaller
}

// PatchRequest carries everything SettingsPatcher.Patch needs, in a neutral
// shape that never leaks settings.json / map[string]interface{} internals into
// the interface. Env is the fully-assembled env block (OTEL keys, model
// routing, Databricks headers) computed at the launch call site.
type PatchRequest struct {
	// PortFlag is the raw --port CLI value (0 if absent). When > 0 the resolved
	// port is persisted as sticky state.
	PortFlag int
	// ProfileName is the post-resolution Databricks profile ("DEFAULT" if none).
	ProfileName string
	// ProxyURL is written into ANTHROPIC_BASE_URL (or the tool's equivalent).
	ProxyURL string
	// Env is the additional env block merged into settings.json.
	Env map[string]string
}

// RestoreRequest is the neutral shape for SettingsPatcher.Restore. It exists
// only for interface-shape stability (#E) — there is nothing to restore today.
type RestoreRequest struct {
	// Port is the proxy port whose settings entry would be restored.
	Port int
}

// SettingsPatcher writes (and, in a future #E, would restore) the tool's
// settings file so it points at the local proxy.
type SettingsPatcher interface {
	// Patch mutates the settings file to route the tool through the proxy. For
	// the claude profile this is a byte-identical delegation to bootstrapSettings.
	Patch(PatchRequest) error
	// Restore is an explicit, currently UNWIRED no-op scoped to #E. There is no
	// settings-restore free function anywhere in this branch — exit teardown is
	// refcount-release + listener close only, and self-healing of dead localhost
	// entries happens on the next Patch (pruneStaleProxyEntries). Restore has
	// nothing to delegate to today; it is kept purely for interface-shape
	// stability so a future #E can wire real teardown without an interface break.
	Restore(RestoreRequest) error
}

// DaemonInstallRequest is a lossless, neutral mirror of the launcher's internal
// installOptions (all 8 fields). No plist / launchctl / installOptions type
// leaks across the interface, yet every field the OS manifest needs is carried.
type DaemonInstallRequest struct {
	// BinPath is the absolute path to the wrapper binary baked into the manifest.
	BinPath string
	// Port is the proxy port the daemon binds.
	Port int
	// Profile is the Databricks profile the daemon uses.
	Profile string
	// LogFile is the daemon's append-only log path.
	LogFile string
	// MetricsTable is the UC OTEL metrics table (empty = disabled).
	MetricsTable string
	// LogsTable is the UC OTEL logs table (empty = disabled).
	LogsTable string
	// TracesTable is the UC OTEL traces table (empty = disabled).
	TracesTable string
	// CLIPath is the absolute `databricks` CLI path pinned into the manifest
	// environment ($DATABRICKS_CLI). Load-bearing under service managers whose
	// PATH is minimal; empty falls back to runtime PATH resolution.
	CLIPath string
}

// DaemonStatus is a lossless, neutral mirror of the launcher's internal
// statusResult (all 11 fields), so `serve status` rendering never loses detail.
type DaemonStatus struct {
	// Registered is true when the service manager knows the unit/agent/task.
	Registered bool
	// Running is true when the service manager reports it as active.
	Running bool
	// Healthy is true when the /health probe succeeded.
	Healthy bool
	// HealthMode is the mode reported by /health (e.g. "daemon").
	HealthMode string
	// Version is the daemon version from /health.
	Version string
	// Profile is the Databricks profile from /health.
	Profile string
	// ManifestPath is the path to the OS service manifest.
	ManifestPath string
	// BinaryPath is the binary path baked into the manifest.
	BinaryPath string
	// LastExitCode is the last-exit code reported by the service manager.
	LastExitCode string
	// Failed is true when the service manager reports a failed/crash-loop state.
	Failed bool
	// FailureDetail is a short parseable failure summary (e.g. "result=exit-code").
	FailureDetail string
}

// DaemonStrategy installs, uninstalls, queries, and diagnoses the long-lived OS
// service for a tool. All types crossing this interface are neutral
// (DaemonInstallRequest / DaemonStatus / plain error), never plist/launchctl.
type DaemonStrategy interface {
	// Install writes and starts the OS service manifest. Implementations MAY
	// return ErrDaemonUnsupported when the platform has no daemon strategy; the
	// claude profile does NOT — it delegates to installDaemon, which returns a
	// plain (non-sentinel) error on the unsupported-platform `other` build.
	Install(DaemonInstallRequest) error
	// Uninstall stops and unregisters the OS service.
	Uninstall() error
	// Status queries the service manager and /health for the given port.
	Status(port int) (DaemonStatus, error)
	// Diagnostics returns a tail of service-manager logs for failure visibility.
	Diagnostics() (string, error)
}

// HookInstaller manages the session-lifecycle hook entries in the tool's
// settings file (SessionStart / SessionEnd for claude).
type HookInstaller interface {
	// Install merges the tool's session hooks into its settings file.
	Install() error
	// Uninstall removes the tool's session hooks from its settings file.
	Uninstall() error
}

// ErrDaemonUnsupported is a forward-looking sentinel a DaemonStrategy.Install
// implementation MAY return when the platform has no daemon support. It is
// UNUSED by the claude profile in #199 (claude delegates to installDaemon,
// whose `other` build returns a plain fmt.Errorf, not this sentinel). Provided
// so future profiles / callers that want to branch on "no daemon here" have a
// stable comparison target.
var ErrDaemonUnsupported = errors.New("daemon strategy not supported for this profile")
