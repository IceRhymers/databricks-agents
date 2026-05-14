package main

import (
	"github.com/IceRhymers/databricks-claude/internal/cmd"
)

// rootCommand is the source-of-truth declaration for the databricks-claude
// CLI. It drives:
//   - parseArgs → knownFlags (the set of "--flag" names the binary owns;
//     anything else is forwarded transparently to the wrapped claude binary).
//   - handleHelp → the help body (rendered from rootCommand.Long).
//   - completion <shell> → the bash/zsh/fish completion scripts (fed via
//     pkg/completion using rootCommand.CompletionFlags() and
//     rootCommand.CompletionSubcommands()).
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
// #170 migrates only the root command. serve / setup / desktop continue
// to use hand-rolled flag.FlagSet parsers in their own files; their
// migration onto Subcommands is tracked in #171. --profile and --port
// are already declared as Persistent so subcommand inheritance works
// out of the box once those migrations land.
var rootCommand = cmd.Command{
	Name:  "databricks-claude",
	Short: "Databricks AI Gateway proxy for Claude Code",
	Long:  rootHelpTemplate,

	// Persistent flags are inherited by every subcommand once those
	// commands migrate onto the tree (#171). For now, declaring them
	// here is a no-op for serve/setup/desktop (which have their own
	// FlagSets) but ensures the tree is shaped correctly for the
	// follow-up. Both flags also feed the resolution chain in main.go
	// today; the StateKey/EnvVar/Default fields below document that
	// behavior so #171/#172 can derive the chain from this declaration.
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
			Description: "Proxy listen port (default: 49153)",
			TakesArg:    true,
			StateKey:    "port",
			Default:     "49153",
		},
	},

	// Order matches the legacy flagDefs slice so the bash/zsh/fish
	// completion output stays byte-identical with the pre-tree binary.
	// The few flags that previously ordered "profile" first now have
	// it pulled into Persistent (which renders first in AllFlags), so
	// "profile" still leads the completion script.
	Flags: []cmd.FlagDef{
		{Name: "verbose", Short: "v", Description: "Enable debug logging to stderr"},
		{Name: "version", Description: "Print version and exit"},
		{Name: "help", Short: "h", Description: "Show help message"},
		{Name: "print-env", Description: "Print resolved configuration (token redacted) and exit"},
		{Name: "otel", Description: "Enable OpenTelemetry logs/metrics export"},
		{Name: "no-otel", Description: "Disable OpenTelemetry (clears all signal keys + telemetry toggle)"},
		{Name: "no-otel-metrics", Description: "Clear persisted OTel metrics keys from settings.json"},
		{Name: "no-otel-logs", Description: "Clear persisted OTel logs keys from settings.json"},
		{Name: "no-otel-traces", Description: "Clear persisted OTel traces keys from settings.json"},
		{Name: "otel-metrics-table", Description: "Unity Catalog table for OTel metrics (cat.schema.table)", TakesArg: true,
			StateKey: "otel_metrics_table"},
		{Name: "otel-logs-table", Description: "Unity Catalog table for OTel logs (cat.schema.table)", TakesArg: true,
			StateKey: "otel_logs_table"},
		{Name: "otel-traces", Description: "Enable OpenTelemetry traces export (Claude Code beta)"},
		{Name: "otel-traces-table", Description: "Unity Catalog table for OTel traces (cat.schema.table)", TakesArg: true,
			StateKey: "otel_traces_table"},
		{Name: "upstream", Description: "Override upstream claude binary path", TakesArg: true, Completer: "__files"},
		{Name: "log-file", Description: "Write debug logs to file (combinable with --verbose)", TakesArg: true, Completer: "__files"},
		{Name: "proxy-api-key", Description: "Require this API key on all proxy requests", TakesArg: true},
		{Name: "tls-cert", Description: "TLS certificate file for the local proxy (requires --tls-key)", TakesArg: true, Completer: "__files"},
		{Name: "tls-key", Description: "TLS private key file for the local proxy (requires --tls-cert)", TakesArg: true, Completer: "__files"},
		{Name: "headless", Description: "Start proxy without launching claude (for IDE extensions or hooks)"},
		{Name: "write-claude-config", Description: "Write first-run settings.json env block and exit (no proxy, no port bind)"},
		{Name: "idle-timeout", Description: "Idle timeout for headless mode (default: 30m; 0 disables; use e.g. 30s, 5m, 1h)", TakesArg: true,
			Default: "30m"},
		{Name: "install-hooks", Description: "Install SessionStart/Stop hooks into ~/.claude/settings.json"},
		{Name: "uninstall-hooks", Description: "Remove databricks-claude hooks from ~/.claude/settings.json"},
		{Name: "headless-ensure", Description: "Start proxy if not running — called by the SessionStart hook"},
		{Name: "headless-release", Description: "Decrement proxy refcount — called by the Stop hook"},
		{Name: "no-update-check", Description: "Skip the automatic update check on startup",
			EnvVar: "DATABRICKS_NO_UPDATE_CHECK"},
		{Name: "with-websearch", Description: "Locally fulfill Anthropic web_search/web_fetch tools (workaround for FMAPI gap)",
			StateKey: "with_websearch"},
		{Name: "websearch-backend", Description: "Web search backend (duckduckgo|none)", TakesArg: true,
			StateKey: "websearch_backend", Default: "duckduckgo"},
		{Name: "websearch-fetch-budget", Description: "Per-fetch byte budget for --with-websearch (default 102400)", TakesArg: true,
			StateKey: "websearch_fetch_budget", Default: "102400"},
		// NOTE: --daemon and --daemon-fake-key are *desktop generate-config*
		// flags and are deliberately not declared here. They were previously
		// in the root flag set so completion would offer them, but completion
		// is position-1-only today (it can't tell whether the cursor is inside
		// `desktop`), so listing them at the root just polluted the root
		// completion namespace. The desktop subcommand has its own scanner
		// (extractDaemonFlag / extractDaemonFakeKeyFlag) so dropping them
		// here doesn't change parsing for `desktop generate-config --daemon
		// --daemon-fake-key=…`. They will land back as desktop-scoped flags
		// when `desktop` migrates onto the tree (#171).
	},

	// Subcommands carry their own flags + Long help bodies so:
	//   - parseSubcommand call sites in serve.go / setup.go / desktop_config.go
	//     can drive their typed-struct mappers off the tree (single source of
	//     truth for what flags each subcommand accepts).
	//   - cmd.Render(serveCommand, …) etc. replaces the deleted printXxxHelp
	//     functions while keeping byte-equivalent help output.
	//   - pkg/completion's nested-completion path (added in #171) walks these
	//     children to offer e.g. `serve install --<TAB>` correctly.
	//
	// Run is still nil on every node — main() / runServe() / runDesktopCommand()
	// keep their existing dispatch shape; the tree is the source of truth for
	// flags and help, not for execution. Issue #171 deliberately scopes itself
	// to the flag/help/completion plumbing.
	Subcommands: []cmd.Command{
		{Name: "completion", Short: "Generate shell completion scripts (bash, zsh, fish)"},
		{Name: "update", Short: "Check for a newer release and print upgrade instructions"},
		desktopCommand,
		setupCommand,
		serveCommand,
	},
}

// desktopCommand declares the `desktop` subcommand's flag set and verbatim
// help body. The flags are the union of every extract*Flag scanner that
// runDesktopCommand previously called by hand. They are NOT split per-action
// (generate-config / credential-helper / generate-trust-profile) because the
// hand-rolled scanners weren't either — runDesktopCommand passes args[1:]
// (everything after the action keyword) into each scanner, so the scanner's
// implicit "known flags" set was the union across actions. Splitting per
// action would change which flags trip "unknown" detection — out of scope
// for #171.
var desktopCommand = cmd.Command{
	Name:  "desktop",
	Short: "Claude Desktop integration (generate-config, credential-helper)",
	Long:  desktopHelpTemplate,
	Flags: []cmd.FlagDef{
		{Name: "profile", Description: "Databricks CLI profile (default: state file > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
		{Name: "output", Description: "generate-config: single output path (format inferred from extension)", TakesArg: true, Completer: "__files"},
		{Name: "binary-path", Description: "generate-config: credential-helper path embedded in the generated config", TakesArg: true, Completer: "__files"},
		{Name: "databricks-cli-path", Description: "generate-config: pin absolute path of the 'databricks' CLI", TakesArg: true, Completer: "__files", StateKey: "databricks_cli_path"},
		{Name: "cert", Description: "generate-trust-profile: PEM-encoded x509 certificate path", TakesArg: true, Completer: "__files"},
		{Name: "for-pkg", Description: "generate-config: bake the canonical .pkg install path into the config (darwin only)"},
		{Name: "daemon", Description: "generate-config: emit daemon-mode artifacts pointing at a local 'databricks-claude serve' daemon"},
		{Name: "port", Description: "generate-config --daemon: daemon port for gatewayBaseUrl (default: state file > 49153)", TakesArg: true, StateKey: "port", Default: "49153"},
		{Name: "daemon-fake-key", Description: "generate-config --daemon: static API key embedded in artifacts (localhost gate)", TakesArg: true},
		{Name: "otel", Description: "generate-config --daemon: also emit otlpEndpoint pointing at the daemon (Cowork OTEL routing)"},
		{Name: "help", Short: "h", Description: "Show help message"},
	},
}

// setupCommand declares the `setup` subcommand's flags + help body.
var setupCommand = cmd.Command{
	Name:  "setup",
	Short: "Idempotent auth bootstrap for fleet init scripts",
	Long:  setupHelpTemplate,
	Flags: []cmd.FlagDef{
		{Name: "profile", Description: "Databricks CLI profile to bootstrap (default: saved state > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
		{Name: "host", Description: "Workspace URL forwarded to 'databricks auth login --host'", TakesArg: true},
		{Name: "force", Description: "Always re-run 'databricks auth login' even when already authenticated"},
		{Name: "help", Short: "h", Description: "Show help message"},
	},
}

// serveCommand declares the `serve` subcommand's flags + help body, AND its
// own Subcommands (install / uninstall / status). --daemon and
// --daemon-fake-key are NOT here — they belong on `desktop` (issue-#171
// requirement: those are desktop-scoped, not serve-scoped).
var serveCommand = cmd.Command{
	Name:  "serve",
	Short: "Long-lived daemon; sub-subcommands: install, uninstall, status",
	Long:  serveHelpTemplate,
	Flags: []cmd.FlagDef{
		{Name: "port", Description: "Proxy listen port (default: 49153)", TakesArg: true, StateKey: "port", Default: "49153"},
		{Name: "profile", Description: "Databricks CLI profile (flag > saved state > MDM > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
		{Name: "log-file", Description: "Append to this file instead of discarding logs (O_APPEND)", TakesArg: true, Completer: "__files"},
		{Name: "verbose", Short: "v", Description: "Also write debug logs to stderr"},
		{Name: "otel-metrics-table", Description: "Unity Catalog table for OTEL metrics (flag > state > MDM > empty)", TakesArg: true, StateKey: "otel_metrics_table", MDMKey: "otelMetricsTable"},
		{Name: "otel-logs-table", Description: "Unity Catalog table for OTEL logs (flag > state > MDM > empty)", TakesArg: true, StateKey: "otel_logs_table", MDMKey: "otelLogsTable"},
		{Name: "otel-traces-table", Description: "Unity Catalog table for OTEL traces (flag > state > MDM > empty)", TakesArg: true, StateKey: "otel_traces_table", MDMKey: "otelTracesTable"},
		{Name: "help", Short: "h", Description: "Show help message"},
	},
	Subcommands: []cmd.Command{
		{
			Name:  "install",
			Short: "Register and start the daemon as a per-user OS service",
			Long:  serveInstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "port", Description: "Proxy listen port (default: saved state > 49153)", TakesArg: true, StateKey: "port", Default: "49153"},
				{Name: "profile", Description: "Databricks CLI profile (flag > saved state > MDM > DEFAULT)", TakesArg: true, Completer: "__databricks_profiles", StateKey: "profile", MDMKey: "databricksProfile", Default: "DEFAULT"},
				{Name: "log-file", Description: "Log file path (default: per-OS default)", TakesArg: true, Completer: "__files"},
				{Name: "otel-metrics-table", Description: "UC table for OTEL metrics (flag > state > MDM > empty)", TakesArg: true, StateKey: "otel_metrics_table", MDMKey: "otelMetricsTable"},
				{Name: "otel-logs-table", Description: "UC table for OTEL logs (flag > state > MDM > empty)", TakesArg: true, StateKey: "otel_logs_table", MDMKey: "otelLogsTable"},
				{Name: "otel-traces-table", Description: "UC table for OTEL traces (flag > state > MDM > empty)", TakesArg: true, StateKey: "otel_traces_table", MDMKey: "otelTracesTable"},
				{Name: "skip-auth-check", Description: "Skip the install-time auth probe (CI / non-tty contexts)"},
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "uninstall",
			Short: "Stop and remove the daemon OS service registration",
			Long:  serveUninstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "status",
			Short: "Report Registered / Running / Healthy in one shot",
			Long:  serveStatusHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
	},
}

// rootHelpTemplate is the verbatim help body rendered by handleHelp(). The
// "{{Version}}" placeholder is substituted by cmd.Render at print time.
//
// This template is hand-formatted (two distinct flag-column widths matching
// the legacy printf) to preserve byte-for-byte equivalence with the pre-tree
// help output. When subcommands migrate onto the tree (#171) they can use
// the programmatic renderer (cmd.Render with empty Long) for a uniform layout
// at the cost of touching every line; today the tree is still the single
// source of truth — every flag in the table also has a FlagDef above, the
// parity tests in main_test.go enforce that, and the completion scripts
// derive from those FlagDefs (not from this template).
const rootHelpTemplate = `databricks-claude v{{Version}} — Databricks AI Gateway proxy for Claude Code

Transparently proxies the Claude Code CLI with Databricks AI Gateway authentication
injected via environment variables.

Usage:
  databricks-claude [databricks-claude flags] [claude flags] [claude args]

Databricks-Claude Flags:
  --profile string      Databricks config profile (saved to ~/.claude/.databricks-claude.json)
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --print-env           Print resolved configuration and exit (token redacted)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --otel                       Enable OpenTelemetry metrics + logs export
  --otel-metrics-table string  Unity Catalog table for OTEL metrics
  --otel-logs-table string     Unity Catalog table for OTEL logs (derived from metrics table if omitted)
  --otel-traces                Enable OpenTelemetry traces export (Claude Code beta;
                               requires --otel-traces-table; can be used standalone)
  --otel-traces-table string   Unity Catalog table for OTEL traces (cat.schema.table)
  --no-otel                    Clear persisted OTEL keys and disable OTEL for future sessions
  --no-otel-metrics            Clear persisted OTEL metrics keys (other signals untouched)
  --no-otel-logs               Clear persisted OTEL logs keys (other signals untouched)
  --no-otel-traces             Clear persisted OTEL traces keys (other signals untouched)
  --proxy-api-key string       Require Bearer token auth on all proxy requests
  --tls-cert string            Path to TLS certificate file (requires --tls-key)
  --tls-key string             Path to TLS private key file (requires --tls-cert)
  --port int                   Fixed proxy port (default: 49153, saved to state)
  --headless                   Start proxy without launching claude (for IDE extensions)
  --write-claude-config        Write first-run settings.json env block (proxy URL, model
                               routing, custom headers) and exit — no proxy startup, no
                               port binding, no child process. Designed for MDM / fleet
                               init scripts and as a cleaner alternative to the
                               '--headless then Ctrl+C' workaround. Idempotent.
                               Accepts --profile, --port, and OTEL table flags.
  --headless-ensure            Start proxy if not running (called by SessionStart hook)
  --headless-release           Decrement proxy refcount (called by Stop hook)
  --idle-timeout duration      Idle timeout for headless mode (default 30m, 0 disables; use e.g. 30s, 5m, 1h)
  --install-hooks              Install SessionStart/SessionEnd hooks AND perform first-run
                               env setup (idempotent). Accepts --profile and --port to
                               persist them; no prior databricks-claude invocation needed.
  --uninstall-hooks            Remove databricks-claude hooks from ~/.claude/settings.json
  --no-update-check            Skip the automatic update check on startup
  --with-websearch             Enable local fulfillment of Anthropic web_search/web_fetch
                               tools (workaround until Databricks FMAPI ships native
                               support; saved to state). Default: disabled.
  --websearch-backend string   Search backend when --with-websearch is enabled.
                               Values: duckduckgo (default, zero config), none
  --websearch-fetch-budget int Max bytes returned per web_fetch call (default 102400)
  --version                    Print version and exit
  --help, -h                   Show this help message

Subcommands:
  completion <shell>           Generate shell completions (bash, zsh, fish)
  update                       Check for a newer release and print upgrade instructions
  desktop <action>             Claude Desktop integration. Run 'databricks-claude desktop'
                               for actions (generate-config, credential-helper) and flags.
  setup [flags]                Idempotent auth bootstrap. Persists the profile to state and
                               runs 'databricks auth login' when not authenticated. Designed
                               for fleet init scripts and per-user login agents.
                               Run 'databricks-claude setup --help' for flags.
  serve [install|uninstall|status|flags]
                               Long-lived daemon serving Claude Code and Claude
                               Desktop with persistent Databricks OAuth. Owns
                               OAuth refresh; exposes inference + OTLP on
                               127.0.0.1. No refcount, no /shutdown, append-only
                               logging. A third deployment mode alongside the
                               per-session CLI wrapper and SessionStart hooks.
                               Sub-subcommands register the daemon as a per-user
                               OS service (LaunchAgent/schtasks/systemd --user).
                               Run 'databricks-claude serve --help' for flags.
                               Run 'databricks-claude serve install --help' etc.

Example Unity Catalog table setup (run in a Databricks SQL warehouse):

  CREATE TABLE main.claude_telemetry.claude_otel_metrics (
    ... -- see https://docs.databricks.com/aws/en/ai-gateway/coding-agent-integration-beta
  ) USING DELTA TBLPROPERTIES ('otel.schemaVersion' = 'v1');

  CREATE TABLE main.claude_telemetry.claude_otel_logs (
    ... -- see https://docs.databricks.com/aws/en/ai-gateway/coding-agent-integration-beta
  ) USING DELTA TBLPROPERTIES ('otel.schemaVersion' = 'v1');

Passthrough to claude:
  Anything after a "--" separator is forwarded to the claude CLI unchanged.
  Examples:
    databricks-claude -- --help                # show claude's own help
    databricks-claude -- --model opus -p "hi"  # run claude with extra flags
`

// serveHelpTemplate is the verbatim body of the deleted printServeHelp(),
// preserved here so cmd.Render(serveCommand, …) emits byte-identical output.
const serveHelpTemplate = `Usage: databricks-claude serve [flags]
       databricks-claude serve <install|uninstall|status> [flags]

Long-lived daemon that serves Claude Code and Claude Desktop with persistent
Databricks OAuth. A third deployment mode alongside the per-session CLI wrapper
(databricks-claude claude ...) and SessionStart hooks — useful when you want a
single OAuth-refreshing proxy that survives across sessions.

Owns Databricks OAuth refresh and exposes inference + OTLP on 127.0.0.1.
Distinguished from --headless mode by: no session refcount, no /shutdown
route, append-only logging, and daemon:true in /health so hooks can detect
and no-op.

Designed for LaunchAgent or systemd service deployment, where a plist or
unit file invokes 'databricks-claude serve' once and keeps it running.
Configure your client to point at the daemon:
  Claude Desktop: via MDM, set gatewayBaseUrl: http://127.0.0.1:<port>.
  Claude Code:    edit ~/.claude/settings.json once to set
                  ANTHROPIC_BASE_URL=http://127.0.0.1:<port> in the env block.
The daemon does NOT mutate settings.json itself — it stays outside the
per-tool lifecycle by design.

Sub-subcommands (OS service management):
  install    Register and start the daemon as a per-user OS service.
             Uses: launchctl (macOS), schtasks (Windows), systemctl --user (Linux).
             Run 'databricks-claude serve install --help' for flags.
  uninstall  Stop and remove the daemon OS service registration.
             Run 'databricks-claude serve uninstall --help' for flags.
  status     Report Registered / Running / Healthy in one shot.
             Run 'databricks-claude serve status --help' for flags.

Flags (for the daemon itself, not sub-subcommands):
  --port int                   Proxy listen port (default: 49153). The daemon
                               binds this port exclusively — MDM-baked
                               gatewayBaseUrl is a fixed URL and cannot follow
                               a fallback port.
  --profile string             Databricks config profile (default: saved
                               state > MDM databricksProfile key > "DEFAULT")
  --log-file string            Append to this file instead of discarding logs.
                               Safe for log rotation (O_APPEND). Restarts
                               preserve prior content (not O_TRUNC).
  --verbose, -v                Also write debug logs to stderr (combinable
                               with --log-file)
  --otel-metrics-table string  Unity Catalog table for OTEL metrics
                               (cat.schema.table). Resolution: flag > saved
                               state > MDM otelMetricsTable key > empty.
                               Empty = no X-Databricks-UC-Table-Name header;
                               Databricks ingest rejects the request (visible,
                               actionable failure — not silent).
  --otel-logs-table string     Unity Catalog table for OTEL logs (same chain)
  --otel-traces-table string   Unity Catalog table for OTEL traces (same chain)
  --help, -h                   Show this help message

MDM keys (domain: com.icerhymers.databricks-claude):
  databricksProfile   Databricks CLI profile name
  otelMetricsTable    UC table for OTEL metrics
  otelLogsTable       UC table for OTEL logs
  otelTracesTable     UC table for OTEL traces

Note: --otel / --no-otel* flags are NOT supported for serve. Those flags
mutate ~/.claude/settings.json to configure Claude Code's OTLP emission.
In daemon mode, Claude Desktop reads OTLP config from MDM, not from any
wrapper-mutated file. Omit otlpEndpoint from the MDM profile to disable OTLP.

Endpoints:
  GET /health   Returns {"tool":"databricks-claude","daemon":true,"version":"...",
                         "profile":"...","token_valid_until":"..."}
  POST /shutdown  Not registered — returns 404. Stop the daemon via SIGTERM
                  (e.g. launchctl stop or systemctl stop).

Examples:
  # Minimal daemon on default port:
  databricks-claude serve

  # Register as an OS service and start:
  databricks-claude serve install
  databricks-claude serve install --profile databricks-ai-inference --port 49153

  # Check service status:
  databricks-claude serve status

  # Remove OS service registration:
  databricks-claude serve uninstall

  # With explicit profile, port, and log file:
  databricks-claude serve \
    --profile databricks-ai-inference \
    --port 49153 \
    --log-file /var/log/databricks-claude/daemon.log

  # With OTEL table routing:
  databricks-claude serve \
    --otel-metrics-table main.claude_telemetry.claude_otel_metrics \
    --otel-logs-table main.claude_telemetry.claude_otel_logs

Exit codes:
  0   Clean shutdown on SIGINT/SIGTERM
  1   Startup failure (auth, port collision, host discovery)
`

// serveInstallHelpTemplate is the verbatim body of the deleted
// printServeInstallHelp().
const serveInstallHelpTemplate = `Usage: databricks-claude serve install [flags]

Register and start 'databricks-claude serve' as a per-user OS service using
native OS primitives (launchctl on macOS, schtasks on Windows, systemctl --user
on Linux). No sudo required — runs in the current user's session only.

The binary path is resolved via os.Executable() at install time and baked into
the manifest. After a binary upgrade, re-run 'serve install' to refresh the path.

Service name: databricks-claude-daemon

Flags:
  --port int                   Proxy listen port (default: saved state > 49153)
  --profile string             Databricks config profile
                               (flag > saved state > MDM > "DEFAULT")
  --log-file string            Log file path (default: per-OS default)
  --otel-metrics-table string  UC table for OTEL metrics (flag > state > MDM > empty)
  --otel-logs-table string     UC table for OTEL logs   (flag > state > MDM > empty)
  --otel-traces-table string   UC table for OTEL traces (flag > state > MDM > empty)
  --skip-auth-check            Skip the install-time auth probe. Required when
                               running from CI / MDM init / any non-tty context
                               where 'databricks auth login' cannot prompt.
                               Daemon will fail to start until auth is seeded
                               separately via 'databricks auth login --profile'.
  --help, -h                   Show this help message

Install-time auth: by default, 'serve install' verifies that the resolved
profile has a valid Databricks token before writing any service-manager
manifest. When stdin is a tty, an unauthenticated profile triggers the
interactive 'databricks auth login' flow. When stdin is not a tty, the
install aborts with an actionable error instead of writing a guaranteed-
broken unit. Use --skip-auth-check to bypass this gate.

Windows note: stdin is conservatively treated as non-interactive on this
platform regardless of how 'serve install' was invoked (cmd.exe interactive
sessions included), because os.ModeCharDevice semantics differ on Windows
and the typical deployment is schtasks-driven. Run 'databricks auth login
--profile <name>' yourself before 'serve install', or pass --skip-auth-check
to defer auth seeding until later.

macOS note: if the binary is unsigned, a Gatekeeper warning is printed but
the install proceeds. Run 'xattr -dr com.apple.quarantine <binary>' or sign
the binary to suppress the warning.
`

// serveUninstallHelpTemplate is the verbatim body of the deleted
// printServeUninstallHelp().
const serveUninstallHelpTemplate = `Usage: databricks-claude serve uninstall

Stop and remove the 'databricks-claude-daemon' OS service registration.
Tolerates "not installed" gracefully.

Flags:
  --help, -h   Show this help message
`

// serveStatusHelpTemplate is the verbatim body of the deleted
// printServeStatusHelp().
const serveStatusHelpTemplate = `Usage: databricks-claude serve status

Report the current state of the 'databricks-claude-daemon' OS service:
  Registered — manifest/task/unit file exists
  Running    — OS service manager reports the service as active
  Healthy    — /health endpoint responds with daemon:true

Flags:
  --help, -h   Show this help message
`

// setupHelpTemplate is the verbatim body of the deleted printSetupHelp().
const setupHelpTemplate = `Usage: databricks-claude setup [flags]

Idempotent auth bootstrap for the active Databricks CLI profile. Designed for
fleet init scripts and per-user login agents — safe to re-run on every login.

Behaviour:
  1. Resolve profile (flag > saved state > "DEFAULT") and persist it to
     ~/.claude/.databricks-claude.json so subsequent databricks-claude
     invocations (including the Desktop credential helper) pick it up.
  2. If already authenticated for that profile, print a success line and
     exit 0 without spawning a browser. Use --force to override.
  3. Otherwise exec "databricks auth login --profile X [--host Y]" with
     attached stdin/stdout/stderr (interactive browser OAuth flow).
  4. Re-check authentication. Exit 0 on success, non-zero on failure.

Flags:
  --profile NAME    Databricks CLI profile to bootstrap (default: saved
                    state > "DEFAULT")
  --host URL        Databricks workspace URL, forwarded verbatim to
                    "databricks auth login --host" (only used on first
                    login for a profile; subsequent runs reuse the host
                    cached in ~/.databrickscfg)
  --force           Always re-run "databricks auth login" even when already
                    authenticated (use when switching workspaces or after
                    revoking tokens)
  --help, -h        Show this help message

Examples:
  # First-time bootstrap on a new endpoint (fleet init script):
  databricks-claude setup \
    --profile databricks-ai-inference \
    --host https://my-ai-workspace.cloud.databricks.com

  # Idempotent re-run (no-op when authed) — safe in a LaunchAgent:
  databricks-claude setup --profile databricks-ai-inference

  # Force a re-login (switched workspaces, or revoked the old token):
  databricks-claude setup --profile databricks-ai-inference --force

Exit codes:
  0   already authenticated, or login succeeded
  1   state write failed, or auth login failed, or still unauthenticated
      after login
`

// desktopHelpTemplate is the verbatim body of the deleted printDesktopHelp().
const desktopHelpTemplate = `Usage: databricks-claude desktop <action> [flags]

Set up Claude Desktop's third-party-inference integration with Databricks.

Actions:
  generate-config     Write Claude Desktop configuration artifacts. Without
                      --output, writes three files into the current directory.
                      All three encode the same Databricks gateway and
                      credential-helper defaults:
                        databricks-claude-desktop.mobileconfig (install on macOS)
                        databricks-claude-desktop.reg          (install on Windows)
                        databricks-claude-desktop.json         (editable source —
                                                                import into Claude
                                                                Desktop developer
                                                                mode to customize
                                                                further, then
                                                                re-export for MDM)
  credential-helper   Print a fresh Databricks token to stdout — the same code
                      path Claude Desktop's inferenceCredentialHelper invokes
                      via the databricks-claude-credential-helper symlink.
                      Useful for scripting and debug.
  generate-trust-profile
                      Emit a Configuration Profile (.mobileconfig) that
                      establishes the .pkg signing certificate as a trusted
                      root for code-signing on managed Macs. Pair with the
                      signed .pkg in your MDM rollout so Gatekeeper accepts
                      the installer without per-device prompts.

Flags:
  --profile string              Databricks CLI profile (default: state file > DEFAULT)
  --output string               Single output path for generate-config; format
                                inferred from .mobileconfig/.reg/.json extension
                                or host OS. Also the output path for
                                generate-trust-profile (default:
                                dist/databricks-claude-trust.mobileconfig).
  --binary-path string          generate-config: credential-helper path embedded in
                                the generated config (default: derived from the
                                running binary). Use this for MDM rollouts so one
                                config works on every endpoint.
  --databricks-cli-path string  generate-config: pin the absolute path of the
                                'databricks' CLI used by the credential helper.
                                Persisted to ~/.claude/.databricks-claude.json.
  --cert string                 generate-trust-profile: path to a PEM-encoded
                                x509 certificate (the .pkg signing cert) to
                                wrap as a trusted root.
  --daemon                      generate-config: emit daemon-mode artifacts pointing
                                at a local 'databricks-claude serve' daemon instead
                                of the Databricks AI Gateway. Default: helper-mode.
  --port int                    generate-config --daemon: daemon port for
                                gatewayBaseUrl (default: state file > 49153).
  --daemon-fake-key string      generate-config --daemon: static API key embedded
                                in artifacts (localhost gate, not a real credential).
                                Default: built-in constant with a banner warning.

Examples:
  # First-time setup on your Mac.
  databricks-claude desktop generate-config --profile myws

  # MDM rollout — bake fleet-wide paths into one config.
  databricks-claude desktop generate-config --profile myws \
    --binary-path /usr/local/bin/databricks-claude-credential-helper \
    --databricks-cli-path /usr/local/bin/databricks

  # Print a token directly (debug; equivalent to invoking the helper symlink).
  databricks-claude desktop credential-helper --profile myws

  # Emit a code-signing trust profile for MDM (pairs with a signed .pkg).
  databricks-claude desktop generate-trust-profile \
    --cert ./codesign-cert.pem \
    --output dist/databricks-claude-trust.mobileconfig
`
