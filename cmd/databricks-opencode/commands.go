package main

import (
	"github.com/IceRhymers/databricks-agents/internal/cmd"
)

// rootCommand is the source-of-truth declaration for the databricks-opencode
// CLI. It drives:
//   - parseArgs → knownFlags (the set of "--flag" names the binary owns;
//     anything else is forwarded transparently to the wrapped opencode binary).
//   - handleHelp → the help body (rendered from rootCommand.Long).
//   - completion <shell> → the bash/zsh/fish completion scripts (fed via
//     internal/core/completion using rootCommand.CompletionFlags()).
//
// Adding a new root flag requires three edits:
//  1. Append a FlagDef to Flags (or Persistent for inherited flags) here.
//  2. Add a case to the switch in parseArgs (main.go) that wires the flag
//     into the Args struct.
//  3. Add the matching field to the Args struct.
var rootCommand = cmd.Command{
	Name:  "databricks-opencode",
	Short: "Databricks AI Gateway wrapper for OpenCode CLI",
	Long:  rootHelpTemplate,

	// Persistent flags are inherited by every subcommand once those
	// commands migrate onto the tree.
	Persistent: []cmd.FlagDef{
		{
			Name:        "profile",
			Description: "Databricks CLI profile (default: DEFAULT)",
			TakesArg:    true,
			Completer:   "__databricks_profiles",
			StateKey:    "profile",
			MDMKey:      "databricksProfile",
			Default:     "DEFAULT",
		},
		{
			Name:        "port",
			Description: "Proxy listen port (default: 49156)",
			TakesArg:    true,
			StateKey:    "port",
			Default:     "49156",
		},
	},

	// Order matches the legacy flagDefs slice so the bash/zsh/fish
	// completion output stays byte-identical with the pre-tree binary.
	// "profile" is now under Persistent (which renders first in AllFlags),
	// matching its position-1 spot in the legacy completion output.
	Flags: []cmd.FlagDef{
		{Name: "verbose", Short: "v", Description: "Enable debug logging to stderr"},
		{Name: "version", Description: "Print version and exit"},
		{Name: "help", Short: "h", Description: "Show help message"},
		{Name: "model", Description: "Model to use (default: databricks-claude-opus-4-7)", TakesArg: true},
		{Name: "upstream", Description: "Override upstream opencode binary path", TakesArg: true, Completer: "__files"},
		{Name: "log-file", Description: "Write debug logs to file (combinable with --verbose)", TakesArg: true, Completer: "__files"},
		{Name: "proxy-api-key", Description: "Require this API key on all proxy requests", TakesArg: true},
		{Name: "tls-cert", Description: "TLS certificate file for the local proxy (requires --tls-key)", TakesArg: true, Completer: "__files"},
		{Name: "tls-key", Description: "TLS private key file for the local proxy (requires --tls-cert)", TakesArg: true, Completer: "__files"},
		{Name: "no-update-check", Description: "Skip the automatic update check on startup", EnvVar: "DATABRICKS_NO_UPDATE_CHECK"},
	},

	// Subcommands carry their own flags + Long help bodies so handleHelp
	// renders subcommand-aware help and completion scripts surface child
	// names. completion / update are leaf commands with their dispatch
	// still in main(); config has a `show` child.
	Subcommands: []cmd.Command{
		{Name: "completion", Short: "Generate shell completion scripts (bash, zsh, fish)"},
		{Name: "update", Short: "Check for a newer release and print upgrade instructions"},
		configCommand,
		hooksCommand,
		serveCommand,
	},
}

// configCommand declares the `config` subcommand tree. It has one child —
// `show` — that lifts the legacy --print-env diagnostic dump under a
// discoverable subcommand.
//
// Tree shape:
//
//	config
//	└── show           (was --print-env)
//
// `show` re-declares --profile and --port locally (instead of relying on
// root Persistent inheritance) because subcommand parsing still uses each
// command's flat AllFlags slice.
var configCommand = cmd.Command{
	Name:  "config",
	Short: "Persistent config editor (show)",
	Long:  configHelpTemplate,
	Subcommands: []cmd.Command{
		{
			Name:  "show",
			Short: "Print resolved configuration (was --print-env)",
			Long:  configShowHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "profile", Description: "Databricks CLI profile (default: state file > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
				{Name: "port", Description: "Proxy port for the displayed ANTHROPIC_BASE_URL", TakesArg: true, StateKey: "port", Default: "49156"},
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
	},
}

// rootHelpTemplate is the verbatim help body rendered by handleHelp(). The
// "{{Version}}" placeholder is substituted by cmd.Render at print time.
const rootHelpTemplate = `databricks-opencode v{{Version}} — Databricks AI Gateway wrapper for OpenCode CLI

Patches the opencode config (opencode.json) and runs a local proxy so the OpenCode CLI
authenticates through a Databricks AI Gateway endpoint with live token refresh.

Usage:
  databricks-opencode [databricks-opencode flags] [opencode flags] [opencode args]

Databricks-OpenCode Flags:
  --profile string      Databricks CLI profile (saved for future sessions; default: env or "DEFAULT")
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --model string        Model to use (default: "databricks-claude-opus-4-7")
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --proxy-api-key string    Require this API key on all proxy requests (default: disabled)
  --tls-cert string         Path to TLS certificate file (requires --tls-key)
  --tls-key string          Path to TLS private key file (requires --tls-cert)
  --port int                Local proxy port (default: 49156, saved for future sessions)
  --no-update-check            Skip the automatic update check on startup
  --version             Print version and exit
  --help, -h            Show this help message

Subcommands:
  completion <shell>           Generate shell completions (bash, zsh, fish)
  update                       Check for a newer release and print upgrade instructions
  config <subcommand>          Persistent config editor.
                                 config show                     Print resolved config
                                                                 (replaces the removed
                                                                 root diagnostic flag)
                               Run 'databricks-opencode config --help' for details.
  hooks <subcommand>           OpenCode plugin lifecycle.
                                 hooks install                   Install opencode plugin
                                 hooks uninstall                 Remove opencode plugin
                                 hooks session-start             Plugin-invoked internal
                                                                 (replaces removed root flag)
                               Run 'databricks-opencode hooks --help' for details.
  serve [flags]                Start the proxy without launching opencode
                               (for IDE extensions or hooks). Bare number on
                               the idle-timeout flag = minutes; 0 disables.
                               Run 'databricks-opencode serve --help' for details.

Passthrough to opencode:
  Anything after a "--" separator is forwarded to the opencode CLI unchanged.
  Examples:
    databricks-opencode -- --help              # show opencode's own help
    databricks-opencode -- --model o3 -p "hi"  # run opencode with extra flags
`

const configHelpTemplate = `Usage: databricks-opencode config <subcommand> [flags]

Persistent config editor. Read-only diagnostics today; future sub-issues may
grow this tree to cover settings.json mutations. The legacy --print-env root
flag has been replaced by 'config show'.

Subcommands:
  show [flags]              Print resolved configuration (token redacted).
                            Read-only — no writes.

Run 'databricks-opencode config <subcommand> --help' for per-subcommand flags.

Examples:
  # Diagnostic dump:
  databricks-opencode config show

  # Override profile for the dump (does not persist):
  databricks-opencode config show --profile my-workspace

Exit codes:
  0   success
  1   discovery / auth failure
  2   missing or unknown subcommand
`

const configShowHelpTemplate = `Usage: databricks-opencode config show [flags]

Print the resolved configuration (token redacted) and exit. Read-only —
zero writes to opencode.json or the state file. Replaces the legacy
--print-env flag.

Resolves: profile, model, Databricks workspace host, AI Gateway URL,
ANTHROPIC_AUTH_TOKEN (redacted), and the upstream opencode binary path.

Flags:
  --profile string   Databricks CLI profile (default: state > DEFAULT)
  --port int         Port used to display the proxy URL (default: state > 49156)
  --help, -h         Show this help message

Example output:

  databricks-opencode configuration:
    Profile:           DEFAULT
    Model:             databricks-claude-opus-4-7
    DATABRICKS_HOST:   https://adb-...azuredatabricks.net
    ANTHROPIC_BASE_URL: https://adb-.../ai-gateway/anthropic
    Auth Token:         **** (redacted)
    OpenCode binary:    /usr/local/bin/opencode
`

// hooksCommand declares the `hooks` subcommand tree. Consolidates the 3
// hooks-lifecycle root flags (--install-hooks, --uninstall-hooks,
// --headless-ensure) under a discoverable subcommand. install/uninstall
// manage the opencode plugin file at
// <opencode-config-dir>/plugins/databricks-proxy/index.js; session-start is
// the plugin-invoked refcount-free proxy lifecycle internal (formerly
// --headless-ensure).
//
// Tree shape:
//
//	hooks
//	├── install        [--profile P] [--port N]
//	├── uninstall
//	└── session-start  [--port N]   (plugin-invoked internal)
//
// Unlike databricks-claude, OpenCode has no SessionEnd hook event — the
// proxy shuts itself down on its idle timeout. So there is no
// `hooks session-end` counterpart.
var hooksCommand = cmd.Command{
	Name:  "hooks",
	Short: "OpenCode plugin lifecycle: install/uninstall + session-start internal",
	Long:  hooksHelpTemplate,
	Subcommands: []cmd.Command{
		{
			Name:  "install",
			Short: "Install the opencode plugin for automatic proxy lifecycle",
			Long:  hooksInstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "profile", Description: "Databricks CLI profile to persist (default: DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
				{Name: "port", Description: "Proxy listen port to persist (default: 49156)", TakesArg: true, StateKey: "port", Default: "49156"},
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "uninstall",
			Short: "Remove the databricks-opencode plugin from opencode",
			Long:  hooksUninstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "session-start",
			Short: "Start proxy if not running (invoked by the opencode plugin — internal)",
			Long:  hooksSessionStartHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "port", Description: "Proxy listen port (default: saved state > 49156)", TakesArg: true, StateKey: "port", Default: "49156"},
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
	},
}

const hooksHelpTemplate = `Usage: databricks-opencode hooks <subcommand> [flags]

OpenCode plugin lifecycle. Installs an opencode plugin at
<opencode-config-dir>/plugins/databricks-proxy/index.js that spins the
local proxy up on session start — making 'databricks-opencode' auto-launch
with every opencode session without a long-lived daemon.

Subcommands:
  install        Write the opencode plugin and register it in
                 opencode.json. Idempotent — safe to re-run after
                 upgrades.
  uninstall      Remove the databricks-opencode plugin file and config
                 entry. Tolerates "not installed".
  session-start  Plugin-invoked internal: start the proxy if it isn't
                 already running. Called by the opencode plugin written
                 by 'hooks install'. Not intended to be invoked directly.

Run 'databricks-opencode hooks <subcommand> --help' for per-subcommand flags.

Examples:
  # First-time install on a developer machine:
  databricks-opencode hooks install

  # Remove plugin (e.g. when switching to a different proxy management mode):
  databricks-opencode hooks uninstall

Exit codes:
  0   success
  1   write/discovery failure
  2   missing or unknown subcommand
`

const hooksInstallHelpTemplate = `Usage: databricks-opencode hooks install [flags]

Install the opencode plugin so every OpenCode session auto-starts the
local proxy on session init. Writes the plugin to:
  <opencode-config-dir>/plugins/databricks-proxy/index.js
and registers it in opencode.json. Idempotent — safe to re-run after
upgrades or after switching install methods (Homebrew ↔ go install).

Generated plugin invocation:
  $` + "`" + `<wrapper> hooks session-start` + "`" + `

Flags:
  --profile string   Databricks CLI profile to persist (default: DEFAULT)
  --port int         Proxy listen port to persist (default: 49156)
  --help, -h         Show this help message

Examples:
  # First-time install on a developer machine:
  databricks-opencode hooks install

  # Re-install after upgrade (idempotent):
  databricks-opencode hooks install
`

const hooksUninstallHelpTemplate = `Usage: databricks-opencode hooks uninstall

Remove the databricks-opencode plugin file and its entry from
opencode.json. Tolerates "not installed" — safe to run when no plugin is
present. Other plugins in your opencode plugins directory are untouched.

Flags:
  --help, -h   Show this help message
`

const hooksSessionStartHelpTemplate = `Usage: databricks-opencode hooks session-start [flags]

Plugin-invoked internal: start the local proxy if not already running.
Called by the opencode plugin file written by 'hooks install'. Not
intended to be invoked directly by end users.

Replaces the legacy --headless-ensure root flag.

Flags:
  --port int   Proxy listen port (default: saved state > 49156)
  --help, -h   Show this help message
`

// serveCommand declares the `serve` subcommand. It consolidates the legacy
// --headless and --idle-timeout root flags into a discoverable subcommand.
// Same deliberately smaller scope as the sibling launchers: no daemon mode and
// no install/uninstall/status — just the session-scoped proxy lifecycle the
// removed --headless flag drove.
//
// Tree shape:
//
//	serve   [--profile P] [--port N] [--upstream URL] [--model M]
//	        [--proxy-api-key K] [--tls-cert C] [--tls-key K]
//	        [--log-file F] [--verbose|-v] [--no-update-check]
//	        [--idle-timeout <dur>]
//
// --idle-timeout grammar: default 30m, `0 = disabled`, PLUS the "bare number =
// minutes" shape (`--idle-timeout 5` ≡ `--idle-timeout 5m`).
var serveCommand = cmd.Command{
	Name:  "serve",
	Short: "Start the proxy without launching opencode (for IDE extensions or hooks)",
	Long:  serveHelpTemplate,
	Flags: []cmd.FlagDef{
		{Name: "profile", Description: "Databricks CLI profile (default: state file > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
		{Name: "port", Description: "Proxy listen port (default: state > 49156)", TakesArg: true, StateKey: "port", Default: "49156"},
		{Name: "upstream", Description: "Override the AI Gateway URL (default: auto-discovered)", TakesArg: true, Completer: "__files"},
		{Name: "model", Description: "Model to use (default: databricks-claude-opus-4-7)", TakesArg: true},
		{Name: "proxy-api-key", Description: "Require this API key on all proxy requests", TakesArg: true},
		{Name: "tls-cert", Description: "TLS certificate file for the local proxy (requires --tls-key)", TakesArg: true, Completer: "__files"},
		{Name: "tls-key", Description: "TLS private key file for the local proxy (requires --tls-cert)", TakesArg: true, Completer: "__files"},
		{Name: "log-file", Description: "Write debug logs to file (combinable with --verbose)", TakesArg: true, Completer: "__files"},
		{Name: "verbose", Short: "v", Description: "Enable debug logging to stderr"},
		{Name: "no-update-check", Description: "Skip the automatic update check on startup", EnvVar: "DATABRICKS_NO_UPDATE_CHECK"},
		{Name: "idle-timeout", Description: "Idle timeout (default 30m; 0 disables; bare number = minutes)", TakesArg: true},
		{Name: "help", Short: "h", Description: "Show help message"},
	},
}

const serveHelpTemplate = `Usage: databricks-opencode serve [flags]

Start the local Databricks proxy without launching opencode. Intended for
IDE extensions, the opencode plugin (see 'hooks session-start'), and any
host that wants to drive the proxy lifecycle externally.

Replaces the removed --headless and --idle-timeout root flags. Behavior:
  - Discovers the workspace host and constructs the AI Gateway URL.
  - Binds 127.0.0.1:<port> (or joins an existing healthy proxy on that port).
  - Patches ~/.config/opencode/opencode.json to point at the local proxy.
  - Prints "PROXY_URL=<scheme>://127.0.0.1:<port>" on stdout.
  - Blocks until POST /shutdown, the idle timeout fires, or SIGINT/SIGTERM.

Flags:
  --profile string         Databricks CLI profile (default: state > DEFAULT)
  --port int               Proxy listen port (default: state > 49156)
  --upstream string        Override the AI Gateway URL (default: auto-discovered)
  --model string           Model to use (default: databricks-claude-opus-4-7)
  --proxy-api-key string   Require this API key on all proxy requests
  --tls-cert string        TLS certificate file (requires --tls-key)
  --tls-key string         TLS private key file (requires --tls-cert)
  --log-file string        Write debug logs to a file
  --verbose, -v            Enable debug logging to stderr
  --no-update-check        Skip the automatic update check on startup
  --idle-timeout duration  Idle timeout (default 30m; 0 disables; bare number = minutes)
  --help, -h               Show this help message

--idle-timeout examples:
  --idle-timeout 5m   five minutes
  --idle-timeout 5    five minutes (bare number = minutes)
  --idle-timeout 1h   one hour
  --idle-timeout 0    idle timeout disabled

Migration note: the legacy root flags --headless and --idle-timeout have
been removed. Replace 'databricks-opencode --headless' with
'databricks-opencode serve' (idle-timeout follows as a serve flag).
`
