package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core"
	"github.com/IceRhymers/databricks-agents/internal/core/cli"
	"github.com/IceRhymers/databricks-agents/internal/core/completion"
	"github.com/IceRhymers/databricks-agents/internal/core/updater"
	"github.com/IceRhymers/databricks-agents/pkg/mdmprofile"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// Args holds all parsed command-line arguments for databricks-claude.
//
// #172 removed the 14 "persistent config editor" fields (PrintEnv, OTEL,
// OTELMetricsTable*, OTELLogsTable*, OTELTraces, OTELTracesTable*, NoOTEL*,
// WriteClaudeConfig, WithWebSearch*, WebSearchBackend*, WebSearchFetchBudget*).
// Their mutation paths now live under the `config` subcommand tree.
//
// #173 removed the 4 hooks-lifecycle fields (InstallHooks, UninstallHooks,
// HeadlessEnsure, HeadlessRelease). Their entrypoints now live under the
// `hooks` subcommand tree (install / uninstall / session-start / session-end).
//
// #174 removed Headless and IdleTimeout. The session-scoped standalone proxy
// now lives behind `serve --session-mode` (with --idle-timeout colocated as
// a `serve` flag).
type Args struct {
	Profile       string
	Verbose       bool
	Version       bool
	ShowHelp      bool
	Upstream      string
	LogFile       string
	ProxyAPIKey   string
	TLSCert       string
	TLSKey        string
	Port          int
	NoUpdateCheck bool
	ClaudeArgs    []string
}

func main() {
	// completion <shell> — must be the very first check, before any flag parsing,
	// auth, or state loading. Safe to call in the Homebrew install sandbox.
	if len(os.Args) >= 2 && os.Args[1] == "completion" {
		completion.Run(os.Args[2:], flagDefs, "databricks-claude", knownSubcommands...)
		os.Exit(0)
	}

	// update — force-check for a newer release and print instructions.
	if len(os.Args) >= 2 && os.Args[1] == "update" {
		os.Exit(updater.RunUpdateCommand(buildUpdaterConfig(), os.Stderr))
	}

	// Wire the MDM reader so ResolveDatabricksCLI can consult the MDM-managed
	// databricksCliPath key. Hoisted ABOVE the early-exit branches so all
	// entry points (credential-helper alias, `desktop`, `setup`, and the
	// normal proxy flow) see the real reader before any code path can call
	// ResolveDatabricksCLI. The logger remains helper-specific (wired inside
	// runCredentialHelper) since only the helper has a debug-log surface.
	cli.SetMDMReader(mdmprofile.ReadKey)

	// `serve` subcommand — long-lived daemon for MDM-deployed Claude Desktop
	// deployments. Owns Databricks OAuth refresh; exposes inference + OTLP on
	// 127.0.0.1. Distinguished from --headless: no refcount, no /shutdown route,
	// append-only logging, daemon:true in /health.
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		runServe(os.Args[2:])
		return
	}

	// argv[0] alias `databricks-claude-credential-helper` — Desktop's
	// mobileconfig accepts only a path with no arguments; install methods
	// drop a symlink at this name pointing at the main binary so Desktop's
	// inferenceCredentialHelper can target a stable path.
	if isCredentialHelperBinaryName(os.Args[0]) {
		runCredentialHelper(extractProfileFlag(os.Args[1:]))
		return
	}

	// `desktop` subcommand — Claude Desktop integration setup. Encapsulates
	// `generate-config` and an explicit `credential-helper` action so these
	// flags don't pollute the root flag namespace.
	if len(os.Args) >= 2 && os.Args[1] == "desktop" {
		runDesktopCommand(os.Args[2:])
		return
	}

	// `setup` subcommand — idempotent auth bootstrap for fleet init scripts.
	// Resolves + persists the profile, then runs `databricks auth login` when
	// not already authenticated (or always, with --force).
	if len(os.Args) >= 2 && os.Args[1] == "setup" {
		runSetupCommand(os.Args[2:])
		return
	}

	// `config` subcommand — persistent config editor (OTEL signals, websearch,
	// settings.json env block, resolved-config diagnostic). Consolidates the
	// 14 flags removed from the root in #172 — the flags that mutate state /
	// settings.json for FUTURE runs rather than affecting the current
	// invocation. The transparent-proxy launcher path below is intentionally
	// flag-driven and bare; persistent state mutation lives behind this tree.
	if len(os.Args) >= 2 && os.Args[1] == "config" {
		runConfigCommand(os.Args[2:])
		return
	}

	// `hooks` subcommand — session-hook deployment mode. install/uninstall
	// manage the SessionStart/SessionEnd entries in ~/.claude/settings.json;
	// session-start/session-end are the hook-invoked refcount-managed proxy
	// lifecycle internals (formerly --headless-ensure / --headless-release
	// before #173 consolidated them off the root flag namespace).
	if len(os.Args) >= 2 && os.Args[1] == "hooks" {
		runHooksCommand(os.Args[2:])
		return
	}

	// `doctor` subcommand — non-interactive model-routing diagnostic. Runs
	// model discovery, diffs the discovered per-family models against the pins
	// in ~/.claude/settings.json, prints the delta, and rewrites settings.json
	// ONLY under --fix (through bootstrapSettings). The sanctioned recovery
	// path for the hook/daemon flow that can't prompt.
	if len(os.Args) >= 2 && os.Args[1] == "doctor" {
		runDoctor(os.Args[2:])
		return
	}

	// Parse databricks-claude flags, passing everything else through to claude.
	// Usage: databricks-claude [databricks-claude-flags] [--] [claude-args...]
	// Unknown flags are forwarded to claude automatically.
	// Tip: anything after "--" is forwarded to claude verbatim — e.g.
	// "databricks-claude -- --help" shows claude's own help, and
	// "databricks-claude -- completion" passes "completion" to claude.
	a, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "databricks-claude:", err)
		os.Exit(1)
	}

	if a.ShowHelp {
		handleHelp()
		os.Exit(0)
	}

	if a.Version {
		fmt.Printf("databricks-claude %s\n", Version)
		os.Exit(0)
	}

	// --- Assemble the claude launch plan and hand off to the shared engine ---
	// buildClaudeLaunchPlan owns all claude-specific pre-flight (logging,
	// settings read, profile/auth, upstream discovery, token seed, OTEL table
	// migration, TLS validation, websearch) and returns a neutral
	// core.LaunchPlan. core.Run owns the generic proxy bind → serve/watch →
	// settings-patch → child-launch → refcount-teardown lifecycle.
	plan, err := buildClaudeLaunchPlan(a)
	if err != nil {
		log.Fatalf("databricks-claude: %v", err)
	}
	os.Exit(core.Run(ClaudeProfile(), plan, a.ClaudeArgs))
}

// envBlock returns the "env" sub-map from a settings document, or an empty map.
func envBlock(doc map[string]interface{}) map[string]interface{} {
	if env, ok := doc["env"]; ok {
		if m, ok := env.(map[string]interface{}); ok {
			return m
		}
	}
	return map[string]interface{}{}
}

// parseArgs separates databricks-claude flags from claude flags.
// databricks-claude owns: --profile, --port, --verbose/-v, --version, --help,
// --upstream, --log-file, --proxy-api-key, --tls-cert, --tls-key,
// --no-update-check.
// Everything else (including unknown flags like --debug) passes through to claude.
// An explicit "--" separator is supported but not required.
//
// #172 removed the 14 "persistent config editor" flags — they live behind
// the `config` subcommand tree now (config otel enable|disable, config
// websearch enable|disable, config write, config show). Old flags are
// removed, NOT aliased: `databricks-claude --otel` now passes `--otel`
// through to claude as an unknown flag.
//
// #173 removed the 4 hooks-lifecycle flags (--install-hooks,
// --uninstall-hooks, --headless-ensure, --headless-release) — they live
// behind the `hooks` subcommand tree now (hooks install / uninstall /
// session-start / session-end). Same "removed, not aliased" semantics.
//
// #174 removed --headless and --idle-timeout — they live behind
// `serve --session-mode` now. Same "removed, not aliased" semantics.
func parseArgs(args []string) (*Args, error) {
	a := &Args{}

	// knownFlags is defined at package level in completion_flags.go,
	// derived from flagDefs so completions and parsing stay in sync.

	i := 0
	for i < len(args) {
		arg := args[i]

		// Explicit separator: everything after "--" goes to claude.
		if arg == "--" {
			a.ClaudeArgs = append(a.ClaudeArgs, args[i+1:]...)
			return a, nil
		}

		// Special case: -h is a short flag for --help, -v for --verbose.
		if arg == "-h" {
			a.ShowHelp = true
			i++
			continue
		}
		if arg == "-v" {
			a.Verbose = true
			i++
			continue
		}

		// Check if it's a known databricks-claude flag.
		if strings.HasPrefix(arg, "--") {
			// Handle --flag=value form.
			name := arg
			value := ""
			if eqIdx := strings.Index(arg, "="); eqIdx >= 0 {
				name = arg[:eqIdx]
				value = arg[eqIdx+1:]
			}

			if knownFlags[name] {
				switch name {
				case "--profile":
					if value != "" {
						a.Profile = value
					} else if i+1 < len(args) {
						i++
						a.Profile = args[i]
					}
				case "--upstream":
					if value != "" {
						a.Upstream = value
					} else if i+1 < len(args) {
						i++
						a.Upstream = args[i]
					}
				case "--log-file":
					if value != "" {
						a.LogFile = value
					} else if i+1 < len(args) {
						i++
						a.LogFile = args[i]
					}
				case "--verbose":
					a.Verbose = true
				case "--version":
					a.Version = true
				case "--help":
					a.ShowHelp = true
				case "--proxy-api-key":
					if value != "" {
						a.ProxyAPIKey = value
					} else if i+1 < len(args) {
						i++
						a.ProxyAPIKey = args[i]
					}
				case "--tls-cert":
					if value != "" {
						a.TLSCert = value
					} else if i+1 < len(args) {
						i++
						a.TLSCert = args[i]
					}
				case "--tls-key":
					if value != "" {
						a.TLSKey = value
					} else if i+1 < len(args) {
						i++
						a.TLSKey = args[i]
					}
				case "--port":
					if value != "" {
						a.Port, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						a.Port, _ = strconv.Atoi(args[i])
					}
				case "--no-update-check":
					a.NoUpdateCheck = true
				default:
					// name is in knownFlags (derived from the rootCommand
					// tree) but no case above handles it. This can only
					// happen if commands.go declares a flag parseArgs was
					// never taught to parse — fail loudly instead of
					// silently swallowing it. Guards the "every tree flag
					// is parsed" half of #170's parity contract.
					return nil, fmt.Errorf("internal: %s is a known flag but parseArgs has no case for it (drift between commands.go and parseArgs)", name)
				}
				i++
				continue
			}
		}

		// Not a known databricks-claude flag — pass through to claude.
		a.ClaudeArgs = append(a.ClaudeArgs, arg)
		i++
	}
	return a, nil
}

// handleHelp prints the databricks-claude help message by rendering the
// root command-tree node from internal/cmd. The actual help body lives in
// commands.go (rootHelpTemplate, attached as rootCommand.Long); this
// function is just a thin wrapper around cmd.Render that injects the
// build-time Version. Only the wrapper's own flags and subcommands are
// documented; claude's CLI help is reachable via the `--` passthrough
// escape hatch (databricks-claude -- --help).
func handleHelp() {
	if err := cmd.Render(os.Stdout, rootCommand, map[string]string{"Version": Version}); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude: failed to render help: %v\n", err)
	}
}

// defaultModelRouting is the OFFLINE FALLBACK used when no ModelSet has been
// discovered/persisted (fresh install, offline, empty grants). Demoted from
// the former hardcoded map — deliberately NOT deleted: the launch path, where
// discovery is forbidden, must always have something legal to write.
//
// Model names are versioned and drift as Databricks ships new models;
// bumping them here is the right place.
func defaultModelRouting() ModelRouting {
	return ModelRouting{
		Opus:   "databricks-claude-opus-4-7",
		Sonnet: "databricks-claude-sonnet-4-6",
		Haiku:  "databricks-claude-haiku-4-5",
	}
}

// launchModelRouting returns the persisted ModelRouting if present, else the
// demoted default; blank per-family fields fall back to the default. NEVER
// performs network I/O — safe to call on the launch hot path.
func launchModelRouting(s persistentState) ModelRouting {
	def := defaultModelRouting()
	if s.Models == nil {
		return def
	}
	m := *s.Models
	if m.Opus == "" {
		m.Opus = def.Opus
	}
	if m.Sonnet == "" {
		m.Sonnet = def.Sonnet
	}
	if m.Haiku == "" {
		m.Haiku = def.Haiku
	}
	return m
}

// databricksFullSetupEnv returns the Databricks-specific env keys written
// during a "full setup" bootstrap (first-run, --write-claude-config, etc.).
// These cover model routing, the coding-agent-mode custom header, and the
// experimental-betas opt-out. Kept as a single source of truth so the
// --write-claude-config flag and the normal startup flow can never drift —
// regressions deleting any key fail the integration tests that assert
// against this map.
//
// A family's model key is OMITTED when its FQN is empty, so a discovery-time
// writer never emits an empty or wrongly-substituted model for an unresolved
// family (no silent mis-route). Launch callers pass launchModelRouting(...)
// which is always fully populated, so all six keys are present on the launch
// path.
func databricksFullSetupEnv(m ModelRouting) map[string]string {
	env := map[string]string{
		"ANTHROPIC_CUSTOM_HEADERS":               "x-databricks-use-coding-agent-mode: true",
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
	}
	if m.Opus != "" {
		env["ANTHROPIC_MODEL"] = m.Opus
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = m.Opus
	}
	if m.Sonnet != "" {
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = m.Sonnet
	}
	if m.Haiku != "" {
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = m.Haiku
	}
	return env
}

// buildUpdaterConfig returns the standard updater.Config for databricks-claude.
func buildUpdaterConfig() updater.Config {
	home, _ := os.UserHomeDir()
	return updater.Config{
		RepoSlug:       "IceRhymers/databricks-claude",
		CurrentVersion: Version,
		BinaryName:     "databricks-claude",
		CacheFile:      filepath.Join(home, ".claude", ".update-check.json"),
		CacheTTL:       24 * time.Hour,
	}
}

// handlePrintEnv prints resolved configuration with the token redacted.
func handlePrintEnv(profile, databricksHost, anthropicBaseURL, token, upstreamBinary string, otelEnabled bool, otelMetricsTable, otelLogsTable, otelTracesTable string) {
	// Redact token.
	redacted := "**** (redacted)"
	if strings.HasPrefix(token, "dapi-") {
		redacted = "dapi-***"
	}

	// Resolve upstream binary path for display.
	binaryPath := upstreamBinary
	if binaryPath == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			binaryPath = p
		} else {
			binaryPath = "(not found)"
		}
	}

	fmt.Printf(`databricks-claude configuration:
  Profile:              %s
  DATABRICKS_HOST:      %s
  ANTHROPIC_BASE_URL:   %s
  ANTHROPIC_AUTH_TOKEN: %s
  Upstream binary:      %s
  OTEL enabled:         %v
`, profile, databricksHost, anthropicBaseURL, redacted, binaryPath, otelEnabled)

	if otelEnabled {
		if otelMetricsTable != "" {
			fmt.Printf(`  OTEL metrics table:   %s
  OTEL metric interval: 10000ms
`, otelMetricsTable)
		}
		if otelLogsTable != "" {
			fmt.Printf(`  OTEL logs table:      %s
  OTEL logs interval:   5000ms
`, otelLogsTable)
		}
		if otelTracesTable != "" {
			fmt.Printf(`  OTEL traces table:    %s
  OTEL traces interval: 5000ms
`, otelTracesTable)
		}
	}
}

// deriveLogsTable derives the OTEL logs table name from the metrics table name.
// If the metrics table ends with "_otel_metrics", replace that suffix with "_otel_logs".
// Otherwise, append "_otel_logs" as a sibling.
func deriveLogsTable(metricsTable string) string {
	if strings.HasSuffix(metricsTable, "_otel_metrics") {
		return strings.TrimSuffix(metricsTable, "_otel_metrics") + "_otel_logs"
	}
	return metricsTable + "_otel_logs"
}
