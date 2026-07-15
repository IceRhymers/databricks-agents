package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core"
	"github.com/IceRhymers/databricks-agents/internal/core/completion"
	"github.com/IceRhymers/databricks-agents/internal/core/updater"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	// completion <shell> — must be the very first check, before any flag parsing,
	// auth, or state loading. Safe to call in the Homebrew install sandbox.
	if len(os.Args) >= 2 && os.Args[1] == "completion" {
		completion.Run(os.Args[2:], flagDefs, "databricks-codex", knownSubcommands...)
		os.Exit(0)
	}

	// hooks <subcommand> — handled before auth/state setup since
	// session-start is hot-path (called by every codex SessionStart) and
	// install/uninstall must work in environments where the proxy is not
	// yet configured. The dispatcher in hooks_cmd.go owns flag parsing.
	if len(os.Args) >= 2 && os.Args[1] == "hooks" {
		if err := runHooksCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "databricks-codex:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// `config` subcommand — persistent config editor (OTEL signals,
	// resolved-config diagnostic). Consolidates the 7 flags removed from the
	// root in #87 — the flags that mutate state for FUTURE runs rather than
	// affecting the current invocation. The transparent-proxy launcher path
	// below is intentionally flag-driven and bare; persistent state mutation
	// lives behind this tree.
	if len(os.Args) >= 2 && os.Args[1] == "config" {
		runConfigCommand(os.Args[2:])
		return
	}

	// `serve` subcommand — runs the proxy in headless mode. A session/headless
	// sibling entrypoint that does NOT route through core.Run (distinct
	// lifecycle: lifecycle wrap + idle timeout, no child). The dispatcher in
	// serve_codex.go owns flag parsing.
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		if err := runServeCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "databricks-codex:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// update — force-check for a newer release and print instructions.
	if len(os.Args) >= 2 && os.Args[1] == "update" {
		if os.Getenv("DATABRICKS_NO_UPDATE_CHECK") == "1" {
			fmt.Fprintln(os.Stderr, "databricks-codex: update check disabled via DATABRICKS_NO_UPDATE_CHECK")
			os.Exit(0)
		}
		cfg := buildUpdaterConfig()
		cfg.CacheTTL = 0 // force fresh check
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := updater.Check(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "databricks-codex: update check failed: %v\n", err)
			os.Exit(1)
		}
		if !r.UpdateAvailable {
			fmt.Fprintf(os.Stderr, "databricks-codex v%s is already the latest version\n", Version)
			os.Exit(0)
		}
		if r.IsHomebrew {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Run: brew upgrade databricks-codex\n", r.LatestVersion)
		} else {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Download from: %s\n", r.LatestVersion, r.ReleaseURL)
		}
		os.Exit(0)
	}

	a, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "databricks-codex:", err)
		os.Exit(1)
	}

	if a.ShowHelp {
		handleHelp()
		os.Exit(0)
	}

	if a.Version {
		fmt.Printf("databricks-codex %s\n", Version)
		os.Exit(0)
	}

	// --- Assemble the codex launch plan and hand off to the shared engine ---
	// buildCodexLaunchPlan owns all codex-specific pre-flight (logging,
	// profile/model resolution + state saves, auth, port resolution, TLS
	// validation, token seed, host discovery, gateway URL, OTEL resolution,
	// LookPath("codex") guard) and returns a neutral core.LaunchPlan plus the
	// field-bearing config.toml patcher. core.Run owns the generic proxy bind →
	// serve/watch → settings-patch → child-launch → refcount-teardown lifecycle.
	plan, patcher, err := buildCodexLaunchPlan(a)
	if err != nil {
		log.Fatalf("databricks-codex: %v", err)
	}
	os.Exit(core.Run(CodexProfile(patcher), plan, a.CodexArgs))
}

// Args holds all parsed databricks-codex flags plus the residual codex args.
//
// #89 removed the legacy --headless and --idle-timeout root flags. Their
// effect now lives behind the `serve` subcommand. The Headless and
// IdleTimeout fields stay on this struct because they are inputs to the serve
// launch path — set by buildServeArgs (serve_codex.go) when the user invokes
// `databricks-codex serve`. parseArgs never sets them; the transparent-wrapper
// path leaves both at zero and routes through core.Run instead.
type Args struct {
	Verbose       bool
	Version       bool
	ShowHelp      bool
	Upstream      string
	LogFile       string
	Profile       string
	ProxyAPIKey   string
	TLSCert       string
	TLSKey        string
	Model         string
	ModelSet      bool
	PortFlag      int
	Headless      bool          // set by buildServeArgs only (#89)
	IdleTimeout   time.Duration // set by buildServeArgs only (#89)
	NoUpdateCheck bool
	CodexArgs     []string
}

// parseArgs separates databricks-codex flags from codex flags. Recognises
// only the root-flag set declared on rootCommand (commands.go); the legacy
// --headless and --idle-timeout flags removed in #89 are intentionally NOT
// recognised here — they will fall through to CodexArgs and be forwarded to
// the wrapped codex binary. Users should migrate to `databricks-codex serve
// [--idle-timeout D]`.
func parseArgs(args []string) (*Args, error) {
	a := &Args{}

	// knownFlags is defined at package level in completion_flags.go,
	// derived from flagDefs so completions and parsing stay in sync.

	i := 0
	for i < len(args) {
		arg := args[i]

		// Explicit separator: everything after "--" goes to codex.
		if arg == "--" {
			a.CodexArgs = append(a.CodexArgs, args[i+1:]...)
			return a, nil
		}

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

		if strings.HasPrefix(arg, "--") {
			name := arg
			value := ""
			if eqIdx := strings.Index(arg, "="); eqIdx >= 0 {
				name = arg[:eqIdx]
				value = arg[eqIdx+1:]
			}

			if knownFlags[name] {
				switch name {
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
				case "--profile":
					if value != "" {
						a.Profile = value
					} else if i+1 < len(args) {
						i++
						a.Profile = args[i]
					}
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
				case "--model":
					if value != "" {
						a.Model = value
						a.ModelSet = true
					} else if i+1 < len(args) {
						i++
						a.Model = args[i]
						a.ModelSet = true
					}
				case "--verbose":
					a.Verbose = true
				case "--version":
					a.Version = true
				case "--help":
					a.ShowHelp = true
				case "--port":
					if value != "" {
						a.PortFlag, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						a.PortFlag, _ = strconv.Atoi(args[i])
					}
				case "--no-update-check":
					a.NoUpdateCheck = true
				default:
					// A name in knownFlags must have a corresponding case
					// above; this arm catches the case where rootCommand
					// declares a new flag but parseArgs hasn't been updated.
					// Loud failure beats silent passthrough — the bidirectional
					// parity test in main_test.go also detects this drift,
					// but a runtime check catches it for any caller path the
					// test doesn't exercise.
					return nil, fmt.Errorf("internal: %s is a known flag but parseArgs has no case for it", name)
				}
				i++
				continue
			}
		}

		// Not a known flag — pass through to codex.
		a.CodexArgs = append(a.CodexArgs, arg)
		i++
	}
	return a, nil
}

// handleHelp prints the databricks-codex help. It does NOT exec
// `codex --help` — appending the agent's help below the wrapper's made it
// impossible to tell which flags the wrapper owns vs which it forwards.
// Codex's own help is reachable via the existing `--` separator:
//
//	databricks-codex -- --help
func handleHelp() {
	fmt.Printf(`databricks-codex v%s — Databricks AI Gateway wrapper for OpenAI Codex CLI

Patches ~/.codex/config.toml and runs a local proxy so the Codex CLI
authenticates through a Databricks AI Gateway endpoint with live token refresh.

Usage:
  databricks-codex [databricks-codex flags] [codex flags] [codex args]

Databricks-Codex Flags:
  --profile string      Databricks CLI profile (saved for future sessions; default: env or "DEFAULT")
  --model string        Model name (saved for future sessions; default: "databricks-gpt-5-5")
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --proxy-api-key string    Require this API key on all proxy requests (default: disabled)
  --tls-cert string         Path to TLS certificate file (requires --tls-key)
  --tls-key string          Path to TLS private key file (requires --tls-cert)
  --port int                Fixed proxy port (default: 49154, saved to state)
  --no-update-check         Skip the automatic update check on startup
  --version             Print version and exit
  --help, -h            Show this help message

Subcommands:
  config                       Persistent config editor (otel enable/disable, show)
  completion <shell>           Generate shell completions (bash, zsh, fish)
  update                       Check for a newer release and print upgrade instructions
  hooks <subcommand>           Install/uninstall SessionStart hooks (install, uninstall, session-start)
  serve [flags]                Run the proxy in headless mode (consolidates the deleted root flags)

Passthrough to codex:
  Anything after a "--" separator is forwarded to the codex CLI unchanged.
  Examples:
    databricks-codex -- --help                # show codex's own help
    databricks-codex -- --model o3 -p "hi"    # run codex with extra flags
`, Version)
}

// buildUpdaterConfig returns the standard updater.Config for databricks-codex.
func buildUpdaterConfig() updater.Config {
	home, _ := os.UserHomeDir()
	return updater.Config{
		RepoSlug:       "IceRhymers/databricks-codex",
		CurrentVersion: Version,
		BinaryName:     "databricks-codex",
		CacheFile:      filepath.Join(home, ".codex", ".update-check.json"),
		CacheTTL:       24 * time.Hour,
	}
}

// handlePrintEnv prints resolved configuration with the token redacted.
// Redaction is applied unconditionally — never branch on token shape, since any
// branch leaks information about the token format.
func handlePrintEnv(databricksHost, openaiBaseURL, token, profile, model, otelMetricsTable, otelLogsTable string) {
	_ = token // intentionally unused: we never print the token
	redacted := "**** (redacted)"

	codexPath := "(not found)"
	if p, err := exec.LookPath("codex"); err == nil {
		codexPath = p
	}

	metricsLine := otelMetricsTable
	if metricsLine == "" {
		metricsLine = "(disabled)"
	}
	logsLine := otelLogsTable
	if logsLine == "" {
		logsLine = "(disabled)"
	}

	fmt.Printf(`databricks-codex configuration:
  Profile:             %s
  Model:               %s
  DATABRICKS_HOST:     %s
  OPENAI_BASE_URL:     %s
  Auth Token:          %s
  OTEL Metrics Table:  %s
  OTEL Logs Table:     %s
  Codex binary:        %s
`, profile, model, databricksHost, openaiBaseURL, redacted, metricsLine, logsLine, codexPath)
}

// defaultModel returns the built-in default model name used when nothing else
// (flag, env, saved state) is set. Centralised so tests can lock the default
// against silent drift.
func defaultModel() string { return "databricks-gpt-5-5" }

// resolveModel returns the model name using the resolution chain:
// --model flag → saved state → built-in default. The built-in default is
// the only value that changes when the project bumps its default model.
func resolveModel(flagValue string, savedValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if savedValue != "" {
		return savedValue
	}
	return defaultModel()
}

// resolveProfile returns the Databricks CLI profile using the resolution chain:
// --profile flag → saved state → "DEFAULT".
// The env var DATABRICKS_CONFIG_PROFILE is intentionally skipped; injected env
// vars would silently override the user's saved proxy profile.
func resolveProfile(flagValue string, savedValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if savedValue != "" {
		return savedValue
	}
	return "DEFAULT"
}

// resolveOtel reads the persistent state and returns the (otel, metrics,
// logs) tuple the regular session path uses to drive proxy + config.toml
// patching. Pure function over state — no flag input — because #87 removed
// the session-time OTEL flags. The persistent-config editor
// (`databricks-codex config otel enable/disable`) is the only writer; the
// session is a read-only consumer.
//
// Semantics:
//
//   - A signal is on iff the corresponding *Disabled bit is unset AND the
//     corresponding table name is non-empty in state.
//   - Returned table strings are empty when their signal is off (so the
//     proxy's tomlconfig.Patch removes the [otel] section when both are
//     empty rather than leaving stale exporter lines).
//   - OTel as a whole is on iff at least one signal is on.
func resolveOtel(saved persistentState) (otel bool, metricsTable string, logsTable string) {
	if !saved.OtelMetricsDisabled && saved.OtelMetricsTable != "" {
		metricsTable = saved.OtelMetricsTable
	}
	if !saved.OtelLogsDisabled && saved.OtelLogsTable != "" {
		logsTable = saved.OtelLogsTable
	}
	otel = metricsTable != "" || logsTable != ""
	return otel, metricsTable, logsTable
}

// deriveLogsTable derives the OTEL logs table name from a metrics table name.
// If the metrics table ends with "_otel_metrics", replace that suffix with
// "_otel_logs". Otherwise append "_otel_logs". Kept exported (within-package)
// so cli_config.go's resolver can reuse it for the `config otel enable
// --metrics-table` derivation.
func deriveLogsTable(metricsTable string) string {
	if metricsTable == "" {
		return ""
	}
	if strings.HasSuffix(metricsTable, "_otel_metrics") {
		return strings.TrimSuffix(metricsTable, "_otel_metrics") + "_otel_logs"
	}
	return metricsTable + "_otel_logs"
}
