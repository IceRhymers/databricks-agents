package main

import (
	"github.com/IceRhymers/databricks-agents/internal/cmd"
)

// rootCommand is the source-of-truth declaration for the databricks-codex
// CLI. It drives:
//   - parseArgs → knownFlags (the set of "--flag" names the binary owns;
//     anything else is forwarded transparently to the wrapped codex binary).
//   - flagDefs (in completion_flags.go) → the bash/zsh/fish completion
//     scripts (fed via pkg/completion).
//
// Adding a new root flag requires three edits:
//  1. Append a FlagDef to Flags (or Persistent for inherited flags) here.
//  2. Add a case to the switch in parseArgs (main.go) that wires the flag
//     into the Args struct.
//  3. Add the matching field to the Args struct.
//
// The parity tests in main_test.go (TestRootTreeFlagsAreParseRecognised,
// TestParseArgsCasesAreDeclaredInRootTree) fail loudly if step 1 and 2
// drift apart — the tree is the single source of truth.
//
// #86 migrated the root command onto the tree. #87 lifts the persistent-config
// editor (the legacy --otel*/--print-env root flags) onto a discoverable
// `config` subcommand; #88 lifts the hooks lifecycle flags
// (--install-hooks / --uninstall-hooks / --headless-ensure) onto a `hooks`
// subcommand. #89 lifts the remaining session-mode root flags (--headless,
// --idle-timeout) onto a `serve` subcommand. All three sets of legacy
// root-flag spellings are GONE in this revision. --profile and --port live
// on Persistent so subcommand inheritance works for the migrated children.
var rootCommand = cmd.Command{
	Name:  "databricks-codex",
	Short: "Databricks AI Gateway wrapper for OpenAI Codex CLI",

	// Persistent flags are inherited by every subcommand once those
	// commands migrate onto the tree (#89). Each migrated leaf
	// (today: config + hooks children) re-declares --profile / --port
	// locally where it consumes them — internal/cmd's parser does not
	// yet walk ancestors, so this is the same shape as databricks-claude's
	// configCommand leaves.
	Persistent: []cmd.FlagDef{
		{
			Name:        "profile",
			Description: "Databricks CLI profile (default: DEFAULT)",
			TakesArg:    true,
			Completer:   "__databricks_profiles",
			StateKey:    "profile",
			EnvVar:      "DATABRICKS_CONFIG_PROFILE",
			MDMKey:      "databricksProfile",
			Default:     "DEFAULT",
		},
		{
			Name:        "port",
			Description: "Proxy listen port (default: 49154)",
			TakesArg:    true,
			StateKey:    "port",
			Default:     "49154",
		},
	},

	// Order matches the legacy flagDefs slice so the bash/zsh/fish
	// completion output stays byte-identical with the pre-tree binary
	// for the flags that survived #87/#88/#89. The migrated OTEL/--print-env,
	// hooks-lifecycle, and session-mode flags are gone from BOTH this slice
	// and completion_flags.go's `order` — that is the breaking surface
	// change.
	Flags: []cmd.FlagDef{
		{Name: "verbose", Short: "v", Description: "Enable debug logging to stderr"},
		{Name: "version", Description: "Print version and exit"},
		{Name: "help", Short: "h", Description: "Show help message"},
		{Name: "model", Description: "Model to use (default: databricks-gpt-5-5)", TakesArg: true, StateKey: "model"},
		{Name: "upstream", Description: "Override upstream codex binary path", TakesArg: true, Completer: "__files"},
		{Name: "log-file", Description: "Write debug logs to file (combinable with --verbose)", TakesArg: true, Completer: "__files"},
		{Name: "proxy-api-key", Description: "Require this API key on all proxy requests", TakesArg: true},
		{Name: "tls-cert", Description: "TLS certificate file for the local proxy (requires --tls-key)", TakesArg: true, Completer: "__files"},
		{Name: "tls-key", Description: "TLS private key file for the local proxy (requires --tls-cert)", TakesArg: true, Completer: "__files"},
		{Name: "no-update-check", Description: "Skip the automatic update check on startup", EnvVar: "DATABRICKS_NO_UPDATE_CHECK"},
	},

	// Subcommands declared on the root. completion and update dispatch
	// from main.go directly. config (added in #87) carries its own
	// dispatcher in cli_config.go. hooks (added in #88) carries its own
	// dispatcher in hooks_cmd.go. serve (added in #89) carries its own
	// dispatcher in serve_cmd.go.
	Subcommands: []cmd.Command{
		completionCommand,
		updateCommand,
		configCommand,
		hooksCommand,
		serveCommand,
	},
}

// completionCommand declares the `completion` subcommand and its three
// shell-target children. Dispatch lives in main.go (calls completion.Run);
// the tree exists so shell completion can offer "completion <TAB>" → bash
// / zsh / fish, and so the help renderer has a node to describe.
var completionCommand = cmd.Command{
	Name:  "completion",
	Short: "Generate shell completion scripts (bash, zsh, fish)",
	Subcommands: []cmd.Command{
		{Name: "bash", Short: "Generate bash completion script"},
		{Name: "zsh", Short: "Generate zsh completion script"},
		{Name: "fish", Short: "Generate fish completion script"},
	},
}

// updateCommand declares the `update` subcommand. Dispatch lives in main.go
// (calls updater.Check + prints upgrade instructions).
var updateCommand = cmd.Command{
	Name:  "update",
	Short: "Check for a newer release and print upgrade instructions",
}

// configCommand declares the `config` subcommand tree introduced in #87.
// Consolidates the persistent-config root flags (--otel, --no-otel,
// --no-otel-metrics, --no-otel-logs, --otel-metrics-table,
// --otel-logs-table, --print-env) into a discoverable tree. Storage
// semantics — state-file table preferences preserved across `config otel
// disable`, ~/.codex/config.toml [otel] section *removal* (not just
// skip-the-write) when both signals are off — are unchanged; this is a
// pure surface reshape.
//
// Tree shape:
//
//	config
//	├── otel
//	│   ├── enable     [--metrics-table T] [--logs-table T]
//	│   └── disable    [--metrics] [--logs]      (no flags = both signals)
//	└── show           (was --print-env)
//
// Codex's surface is smaller than databricks-claude's: no `config write`
// (codex has no settings.json env block — config.toml is patched at
// session start by the proxy lifecycle, not by a one-shot bootstrap), no
// `config websearch` (codex has no local websearch fulfilment), no
// traces signal (codex's tomlconfig manages logs + metrics exporters
// only).
var configCommand = cmd.Command{
	Name:  "config",
	Short: "Persistent config editor (otel, show)",
	Long:  configHelpTemplate,
	Subcommands: []cmd.Command{
		{
			Name:  "otel",
			Short: "Toggle OpenTelemetry signals (enable|disable)",
			Long:  configOtelHelpTemplate,
			Subcommands: []cmd.Command{
				{
					Name:  "enable",
					Short: "Enable OTEL — persists table preferences to state for the next codex session",
					Long:  configOtelEnableHelpTemplate,
					Flags: []cmd.FlagDef{
						{Name: "metrics-table", Description: "Unity Catalog table for OTEL metrics (cat.schema.table)", TakesArg: true, StateKey: "otel_metrics_table"},
						{Name: "logs-table", Description: "Unity Catalog table for OTEL logs (cat.schema.table)", TakesArg: true, StateKey: "otel_logs_table"},
						{Name: "profile", Description: "Databricks CLI profile (default: state file > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
						{Name: "help", Short: "h", Description: "Show help message"},
					},
				},
				{
					Name:  "disable",
					Short: "Disable OTEL — clears the [otel] section from ~/.codex/config.toml on the next session start (state file preserved)",
					Long:  configOtelDisableHelpTemplate,
					Flags: []cmd.FlagDef{
						{Name: "metrics", Description: "Disable only OTEL metrics (other signals untouched)"},
						{Name: "logs", Description: "Disable only OTEL logs"},
						{Name: "help", Short: "h", Description: "Show help message"},
					},
				},
			},
		},
		{
			Name:  "show",
			Short: "Print resolved configuration (was --print-env)",
			Long:  configShowHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "profile", Description: "Databricks CLI profile (default: state file > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
	},
}

// hooksCommand declares the `hooks` subcommand tree introduced in #88.
// Consolidates the 3 hooks-lifecycle root flags (--install-hooks,
// --uninstall-hooks, --headless-ensure) under a discoverable subcommand.
// install/uninstall manage the SessionStart entries in ~/.codex/hooks.json;
// session-start is hook-invoked refcount-managed proxy lifecycle internal
// (formerly --headless-ensure).
//
// Tree shape:
//
//	hooks
//	├── install        [--profile P] [--port N]
//	├── uninstall
//	└── session-start  [--port N]   (hook-invoked internal)
//
// The hook-install logic (installHooks/uninstallHooks in hooks.go) and the
// proxy-ensure logic (headlessEnsure) are unchanged behaviorally — they
// move behind tree commands. The detector that matches "databricks-codex
// --headless"-prefixed entries continues to recognise hooks installed by
// the legacy flag spellings, so a re-install replaces them cleanly with
// the new "databricks-codex hooks session-start" command line.
var hooksCommand = cmd.Command{
	Name:  "hooks",
	Short: "Session-hook deployment mode: install/uninstall + lifecycle internals",
	Long:  hooksHelpTemplate,
	Subcommands: []cmd.Command{
		{
			Name:  "install",
			Short: "Install SessionStart hook into ~/.codex/hooks.json",
			Long:  hooksInstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "uninstall",
			Short: "Remove databricks-codex hooks from ~/.codex/hooks.json",
			Long:  hooksUninstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "session-start",
			Short: "Start proxy if not running (invoked by the SessionStart hook — internal)",
			Long:  hooksSessionStartHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "port", Description: "Proxy listen port (default: saved state > 49154)", TakesArg: true, StateKey: "port", Default: "49154"},
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
	},
}

// serveCommand declares the `serve` subcommand introduced in #89.
// Consolidates the legacy `--headless` and `--idle-timeout` root flags into
// a discoverable subcommand. Mirrors databricks-claude #174's `serve
// --session-mode` entrypoint with a deliberately smaller scope: codex has
// no daemon mode (no LaunchAgent/Service equivalent), so install/uninstall/
// status sub-subcommands are deferred.
//
// Tree shape:
//
//	serve   [--idle-timeout D] [--profile P] [--port N] [--model M]
//	        [--upstream U] [--log-file F] [--verbose|-v]
//	        [--proxy-api-key K] [--tls-cert C] [--tls-key K]
//	        [--no-update-check]
//
// Behavior is byte-identical with the deleted `databricks-codex --headless`
// path: the runner constructs the same Args struct that parseArgs used to,
// sets Headless=true, and dispatches into the shared runProxyMode launcher.
// The hook-spawn path (headlessEnsure → headless.Ensure) is updated to
// invoke `databricks-codex serve --port=N` instead of the removed
// `--headless --port=N` so the SessionStart hook keeps working.
var serveCommand = cmd.Command{
	Name:  "serve",
	Short: "Run the proxy in headless mode (consolidates --headless / --idle-timeout)",
	Long:  serveHelpTemplate,
	Flags: []cmd.FlagDef{
		{Name: "idle-timeout", Description: "Idle timeout (default: 30m; 0 disables; e.g. 30s, 5m, 1h)", TakesArg: true, Default: "30m"},
		{Name: "profile", Description: "Databricks CLI profile (default: state file > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
		{Name: "port", Description: "Proxy listen port (default: saved state > 49154)", TakesArg: true, StateKey: "port", Default: "49154"},
		{Name: "model", Description: "Model to use (saved for future sessions)", TakesArg: true, StateKey: "model"},
		{Name: "upstream", Description: "Override the AI Gateway URL (default: auto-discovered)", TakesArg: true},
		{Name: "log-file", Description: "Write debug logs to file (combinable with --verbose)", TakesArg: true, Completer: "__files"},
		{Name: "verbose", Short: "v", Description: "Enable debug logging to stderr"},
		{Name: "proxy-api-key", Description: "Require this API key on all proxy requests", TakesArg: true},
		{Name: "tls-cert", Description: "TLS certificate file for the local proxy (requires --tls-key)", TakesArg: true, Completer: "__files"},
		{Name: "tls-key", Description: "TLS private key file for the local proxy (requires --tls-cert)", TakesArg: true, Completer: "__files"},
		{Name: "no-update-check", Description: "Skip the automatic update check on startup", EnvVar: "DATABRICKS_NO_UPDATE_CHECK"},
		{Name: "help", Short: "h", Description: "Show help message"},
	},
}

const configHelpTemplate = `Usage: databricks-codex config <subcommand> [flags]

Persistent config editor. Mutates ~/.codex/.databricks-codex.json (state file)
to record table preferences that the proxy lifecycle reads at the next codex
session start. ~/.codex/config.toml is NOT touched by config.* commands —
config.toml is owned by the proxy lifecycle and is rewritten when codex
launches.

Subcommands:
  otel enable [flags]   Persist OTEL table preferences and (next session)
                        emit the [otel] section in config.toml.
  otel disable [flags]  Mark OTEL signals as off for the next session. The
                        proxy lifecycle removes the [otel] section from
                        config.toml on its next start. State-file table
                        preferences are PRESERVED so a future
                        'config otel enable' restores them.
  show [flags]          Print the resolved configuration (token redacted).
                        Read-only — no writes.

Run 'databricks-codex config <subcommand> --help' for per-subcommand flags.

Examples:
  # Enable OTEL with explicit metrics + logs tables:
  databricks-codex config otel enable \
    --metrics-table main.codex_telemetry.codex_otel_metrics \
    --logs-table   main.codex_telemetry.codex_otel_logs

  # Disable just metrics (logs still routed if previously enabled):
  databricks-codex config otel disable --metrics

  # Disable all signals:
  databricks-codex config otel disable

  # Diagnostic dump:
  databricks-codex config show

Exit codes:
  0   success
  1   write/discovery failure
  2   missing or unknown subcommand
`

const configOtelHelpTemplate = `Usage: databricks-codex config otel <enable|disable> [flags]

Toggle OpenTelemetry signal export for the wrapped codex session. Storage:
  - Tables (metrics/logs) are persisted to ~/.codex/.databricks-codex.json
  - The [otel] section in ~/.codex/config.toml is written or removed by the
    proxy lifecycle the next time codex starts (this command does not edit
    config.toml directly).

'config otel disable' marks signals off in the state file but PRESERVES the
table-name preferences so a subsequent 'config otel enable' can restore them
without re-typing.

Subcommands:
  enable    Persist OTEL table preferences (see 'config otel enable --help').
  disable   Mark OTEL signals off for the next session (see 'config otel disable --help').
`

const configOtelEnableHelpTemplate = `Usage: databricks-codex config otel enable [flags]

Enable OTEL — persists the resolved table names to ~/.codex/.databricks-codex.json
so the proxy lifecycle emits the [otel] section in ~/.codex/config.toml the
next time codex launches.

Resolution chain per signal: explicit flag > state file > derive (logs from
metrics) > unset. With no table flags and an empty state file, --metrics-table
defaults to 'main.codex_telemetry.codex_otel_metrics' (matching the legacy
'--otel' bare-toggle behavior).

Flags:
  --metrics-table string   Unity Catalog table for OTEL metrics (cat.schema.table)
  --logs-table string      Unity Catalog table for OTEL logs   (cat.schema.table)
  --profile string         Databricks CLI profile (default: state > DEFAULT)
  --help, -h               Show this help message

Examples:
  # Enable with custom metrics + logs tables:
  databricks-codex config otel enable \
    --metrics-table main.telemetry.codex_otel_metrics \
    --logs-table   main.telemetry.codex_otel_logs

  # Enable with default tables (logs derived from metrics):
  databricks-codex config otel enable
`

const configOtelDisableHelpTemplate = `Usage: databricks-codex config otel disable [flags]

Mark OTEL signals off in the state file. The proxy lifecycle clears the
[otel] section from ~/.codex/config.toml on its next start. State-file table
preferences are PRESERVED — a subsequent 'config otel enable' restores them.
With no flags, BOTH signals are disabled (equivalent to the legacy --no-otel).

Flags:
  --metrics    Disable only OTEL metrics (logs untouched)
  --logs       Disable only OTEL logs
  --help, -h   Show this help message

Examples:
  # Disable both signals:
  databricks-codex config otel disable

  # Disable just metrics:
  databricks-codex config otel disable --metrics

  # Disable just logs:
  databricks-codex config otel disable --logs
`

const configShowHelpTemplate = `Usage: databricks-codex config show [flags]

Print the resolved configuration (token redacted) and exit. Read-only —
zero writes to the state file or config.toml. Replaces the legacy
--print-env flag.

Resolves: profile, model, Databricks workspace host, AI Gateway URL,
auth token (redacted), the persisted OTEL UC tables, and the discovered
codex binary path.

Flags:
  --profile string   Databricks CLI profile (default: state > DEFAULT)
  --help, -h         Show this help message
`

const hooksHelpTemplate = `Usage: databricks-codex hooks <subcommand> [flags]

Session-hook deployment mode for the OpenAI Codex CLI. Installs hook
entries into ~/.codex/hooks.json that spin a refcount-managed proxy up on
SessionStart — making 'databricks-codex' auto-launch with every codex
session without a long-lived daemon.

Subcommands:
  install        Install the SessionStart hook into ~/.codex/hooks.json
                 (idempotent). Also flips [features] hooks = true in
                 ~/.codex/config.toml so codex actually reads the file.
  uninstall      Remove databricks-codex hooks from
                 ~/.codex/hooks.json. Tolerates "not installed".
  session-start  Hook-invoked internal: starts the proxy if it isn't
                 already running. Called by the SessionStart hook JSON
                 written by 'hooks install'. Not intended to be invoked
                 directly.

Run 'databricks-codex hooks <subcommand> --help' for per-subcommand flags.

Examples:
  # First-time install on a developer machine:
  databricks-codex hooks install

  # Remove hooks (e.g. when switching back to the manual proxy mode):
  databricks-codex hooks uninstall

Exit codes:
  0   success
  1   write/discovery failure
  2   missing or unknown subcommand
`

const hooksInstallHelpTemplate = `Usage: databricks-codex hooks install [flags]

Install the SessionStart hook into ~/.codex/hooks.json so that every
codex session auto-launches a refcount-managed databricks-codex proxy
in the background. Idempotent — safe to re-run after upgrades.

Also ensures [features] hooks = true in ~/.codex/config.toml; without
that flag codex does not read hooks.json at all.

Generated hook JSON:
  SessionStart → "databricks-codex hooks session-start"

Flags:
  --help, -h    Show this help message
`

const hooksUninstallHelpTemplate = `Usage: databricks-codex hooks uninstall [flags]

Remove databricks-codex hook entries from ~/.codex/hooks.json. Other
user-authored hooks survive byte-identical. Also removes the
[features] hooks = true line databricks-codex installed; legacy
codex_hooks = true (if present) is left untouched.

If hooks.json doesn't exist, this is a no-op.

Flags:
  --help, -h    Show this help message
`

const hooksSessionStartHelpTemplate = `Usage: databricks-codex hooks session-start [flags]

Hook-invoked internal: probes the local proxy on the configured port
and, if absent, starts a detached headless databricks-codex process
that exits via idle timeout. Replaces the legacy --headless-ensure
flag. Not intended for direct invocation — the SessionStart hook
JSON written by 'hooks install' calls this command.

MUST remain fast and fail-fast: no interactive auth flow. If the user
hasn't run 'databricks auth login' yet, this command exits 0 silently
so the codex session is not blocked on a hook timeout.

Internally spawns 'databricks-codex serve --port=N' (the #89 replacement
for the deleted '--headless' root flag).

Flags:
  --port int    Override saved port (default: saved state > 49154)
  --help, -h    Show this help message
`

const serveHelpTemplate = `Usage: databricks-codex serve [flags]

Run the proxy in headless mode without launching codex. Replaces the
legacy '--headless' and '--idle-timeout' root flags (#89) with a
discoverable subcommand. Behavior is byte-identical with the removed
flags — the hooks SessionStart entry and IDE extensions can call this
directly to bring the proxy up and read PROXY_URL=... from stdout.

Use this when:
  - An IDE extension needs the proxy running but doesn't want a child
    codex process. Read PROXY_URL=... from stdout, point your client
    at it, then SIGTERM (or POST /shutdown) when done.
  - Bringing up the proxy from a SessionStart hook. 'hooks install'
    wires this for you under the hood.

The proxy exits when:
  - SIGINT or SIGTERM is received.
  - POST /shutdown is hit on the proxy URL.
  - --idle-timeout elapses with zero in-flight requests (default 30m;
    0 disables idle shutdown).

Flags:
  --idle-timeout duration  Idle timeout (default 30m, 0 disables; e.g. 30s, 5m, 1h)
  --profile string         Databricks CLI profile (default: state > DEFAULT)
  --port int               Proxy listen port (default: state > 49154)
  --model string           Model name (saved for future sessions)
  --upstream string        Override the AI Gateway URL (default: auto-discovered)
  --log-file string        Write debug logs to file (combinable with --verbose)
  --verbose, -v            Enable debug logging to stderr
  --proxy-api-key string   Require this API key on all proxy requests
  --tls-cert string        TLS certificate file (requires --tls-key)
  --tls-key string         TLS private key file (requires --tls-cert)
  --no-update-check        Skip the automatic update check on startup
  --help, -h               Show this help message

Examples:
  # Start the proxy with the default 30-minute idle timeout:
  databricks-codex serve

  # Start with a 5-minute idle timeout (test/CI scenarios):
  databricks-codex serve --idle-timeout 5m

  # Start with idle shutdown disabled (long-running IDE sessions):
  databricks-codex serve --idle-timeout 0
`
