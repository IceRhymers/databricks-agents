package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
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
		completion.Run(os.Args[2:], flagDefs, "databricks-opencode", knownSubcommands...)
		os.Exit(0)
	}

	// `config` subcommand — persistent-config editor. Today this is just
	// `config show` (the lifted --print-env diagnostic). Routed before parseArgs
	// so positional dispatch doesn't fight the transparent-passthrough behaviour.
	if len(os.Args) >= 2 && os.Args[1] == "config" {
		runConfigCommand(os.Args[2:])
		return
	}

	// `hooks` subcommand — opencode plugin lifecycle. Replaces the removed
	// --install-hooks / --uninstall-hooks / --headless-ensure root flags.
	if len(os.Args) >= 2 && os.Args[1] == "hooks" {
		runHooksCommand(os.Args[2:])
		return
	}

	// `serve` subcommand — start the proxy without launching opencode. A
	// session/headless sibling entrypoint that does NOT route through core.Run
	// (distinct lifecycle: lifecycle wrap + idle timeout, no child). The
	// dispatcher in serve_opencode.go parses its own flags then runs the proxy.
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		runServeCommand(os.Args[2:])
		return
	}

	// update — force-check for a newer release and print instructions.
	if len(os.Args) >= 2 && os.Args[1] == "update" {
		if os.Getenv("DATABRICKS_NO_UPDATE_CHECK") == "1" {
			fmt.Fprintln(os.Stderr, "databricks-opencode: update check disabled via DATABRICKS_NO_UPDATE_CHECK")
			os.Exit(0)
		}
		cfg := buildUpdaterConfig()
		cfg.CacheTTL = 0 // force fresh check
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := updater.Check(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "databricks-opencode: update check failed: %v\n", err)
			os.Exit(1)
		}
		if !r.UpdateAvailable {
			fmt.Fprintf(os.Stderr, "databricks-opencode v%s is already the latest version\n", Version)
			os.Exit(0)
		}
		if r.IsHomebrew {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Run: brew upgrade databricks-opencode\n", r.LatestVersion)
		} else {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Download from: %s\n", r.LatestVersion, r.ReleaseURL)
		}
		os.Exit(0)
	}

	a, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "databricks-opencode:", err)
		os.Exit(1)
	}

	if a.ShowHelp {
		handleHelp()
		os.Exit(0)
	}

	if a.Version {
		fmt.Printf("databricks-opencode %s\n", Version)
		os.Exit(0)
	}

	// --- Assemble the opencode launch plan and hand off to the shared engine ---
	// buildOpencodeLaunchPlan owns all opencode-specific pre-flight (logging,
	// profile/model resolution + state saves, auth, port resolution, TLS
	// validation, token seed, host discovery, gateway URLs, LookPath("opencode")
	// guard) and returns a neutral core.LaunchPlan plus the field-bearing
	// opencode.json patcher. core.Run owns the generic proxy bind → serve/watch →
	// settings-patch → child-launch → refcount-teardown lifecycle.
	plan, patcher, err := buildOpencodeLaunchPlan(a)
	if err != nil {
		log.Fatalf("databricks-opencode: %v", err)
	}
	os.Exit(core.Run(OpencodeProfile(patcher), plan, a.OpencodeArgs))
}

// Args holds all parsed databricks-opencode flags plus the residual opencode args.
//
// Headless and IdleTimeout are NOT populated by parseArgs — the --headless /
// --idle-timeout root flags were removed and replaced by the `serve`
// subcommand. Both fields remain on the struct because the serve dispatcher
// (serve_opencode.go) synthesises an Args with Headless=true and IdleTimeout
// populated from `serve --idle-timeout` before running the serve session.
type Args struct {
	Verbose       bool
	Version       bool
	ShowHelp      bool
	Model         string
	Upstream      string
	LogFile       string
	Profile       string
	ProxyAPIKey   string
	TLSCert       string
	TLSKey        string
	Port          int
	Headless      bool          // populated only by the `serve` dispatcher
	IdleTimeout   time.Duration // populated only by the `serve` dispatcher
	NoUpdateCheck bool
	OpencodeArgs  []string
}

// parseArgs separates databricks-opencode flags from opencode flags.
//
// --headless and --idle-timeout are NOT recognised at the root — they live
// under the `serve` subcommand. parseArgs leaves Args.Headless false and
// Args.IdleTimeout zero; the `serve` dispatcher populates them. Anything that
// looks like --headless / --idle-timeout at the root falls through to opencode
// (the transparent-passthrough behaviour the wrapper applies to unknown flags).
func parseArgs(args []string) (*Args, error) {
	a := &Args{}

	// knownFlags is defined at package level in completion_flags.go,
	// derived from flagDefs so completions and parsing stay in sync.

	i := 0
	for i < len(args) {
		arg := args[i]

		// Explicit separator: everything after "--" goes to opencode.
		if arg == "--" {
			a.OpencodeArgs = append(a.OpencodeArgs, args[i+1:]...)
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
				case "--model":
					if value != "" {
						a.Model = value
					} else if i+1 < len(args) {
						i++
						a.Model = args[i]
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
				case "--port":
					if value != "" {
						a.Port, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						a.Port, _ = strconv.Atoi(args[i])
					}
				case "--verbose":
					a.Verbose = true
				case "--version":
					a.Version = true
				case "--help":
					a.ShowHelp = true
				case "--no-update-check":
					a.NoUpdateCheck = true
				default:
					return nil, fmt.Errorf("internal: %s is a known flag but parseArgs has no case for it", name)
				}
				i++
				continue
			}
		}

		// Not a known flag — pass through to opencode.
		a.OpencodeArgs = append(a.OpencodeArgs, arg)
		i++
	}
	return a, nil
}

// handleHelp renders the databricks-opencode help body from the rootCommand
// registry (commands.go) so the help body, flag set, and completion scripts
// share one source of truth. The wrapper does not append `opencode --help` —
// users who want opencode's own help can run `databricks-opencode -- --help`.
func handleHelp() {
	if err := cmd.Render(os.Stdout, rootCommand, map[string]string{"Version": Version}); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-opencode: failed to render help: %v\n", err)
	}
}

// buildUpdaterConfig returns the standard updater.Config for databricks-opencode.
func buildUpdaterConfig() updater.Config {
	cacheDir, _ := opencodeConfigDir()
	return updater.Config{
		RepoSlug:       "IceRhymers/databricks-opencode",
		CurrentVersion: Version,
		BinaryName:     "databricks-opencode",
		CacheFile:      cacheDir + "/.update-check.json",
		CacheTTL:       24 * time.Hour,
	}
}

// handlePrintEnv prints resolved configuration with the token redacted.
// Redaction is applied unconditionally — never branch on token shape, since any
// branch leaks information about the token format.
func handlePrintEnv(databricksHost, openaiBaseURL, token, profile, model string) {
	_ = token // intentionally never printed; always redacted to a fixed sentinel
	redacted := "**** (redacted)"

	opencodePath := "(not found)"
	if p, err := exec.LookPath("opencode"); err == nil {
		opencodePath = p
	}

	fmt.Printf(`databricks-opencode configuration:
  Profile:           %s
  Model:             %s
  DATABRICKS_HOST:   %s
  ANTHROPIC_BASE_URL: %s
  Auth Token:         %s
  OpenCode binary:    %s
`, profile, model, databricksHost, openaiBaseURL, redacted, opencodePath)
}

// defaultModel returns the built-in default model name used when nothing else
// (flag, saved state) is set. Centralised so tests can lock the default against
// silent drift.
func defaultModel() string { return "databricks-claude-opus-4-7" }

// resolveModel returns the model name using the resolution chain:
// --model flag → saved state → built-in default.
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
