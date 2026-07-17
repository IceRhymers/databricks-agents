package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core"
	"github.com/IceRhymers/databricks-agents/internal/core/cli"
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
		os.Exit(updater.RunUpdateCommand(buildUpdaterConfig(), os.Stderr))
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
// newParseSpec builds the cli.Spec that maps databricks-opencode's root flags
// to their destination fields on a. knownFlags (completion_flags.go, derived
// from flagDefs) is the authoritative known-vs-passthrough gate; the binding
// table below must cover it exactly — TestBindingsCoverKnownFlags enforces that
// structurally (superseding the now-dormant grep-based
// TestParseArgsCasesAreDeclaredInRootTree).
func newParseSpec(a *Args) cli.Spec {
	return cli.Spec{
		Known:      knownFlags,
		Shorthands: map[string]string{"-h": "--help", "-v": "--verbose"},
		Residual:   &a.OpencodeArgs,
		Bindings: map[string]cli.Binding{
			"--profile":         {Str: &a.Profile},
			"--upstream":        {Str: &a.Upstream},
			"--log-file":        {Str: &a.LogFile},
			"--proxy-api-key":   {Str: &a.ProxyAPIKey},
			"--tls-cert":        {Str: &a.TLSCert},
			"--tls-key":         {Str: &a.TLSKey},
			"--model":           {Str: &a.Model},
			"--port":            {Int: &a.Port},
			"--verbose":         {Bool: &a.Verbose},
			"--version":         {Bool: &a.Version},
			"--help":            {Bool: &a.ShowHelp},
			"--no-update-check": {Bool: &a.NoUpdateCheck},
		},
	}
}

func parseArgs(args []string) (*Args, error) {
	a := &Args{}
	if err := cli.ParseFlags(args, newParseSpec(a)); err != nil {
		return nil, err
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
		RepoSlug:       "IceRhymers/databricks-claude",
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
