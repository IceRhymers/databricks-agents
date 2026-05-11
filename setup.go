package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
)

// setupExecCommand is the exec.Command factory used by runSetupCommand.
// Overridable in tests to inject a mock databricks CLI binary.
var setupExecCommand = exec.Command

// runSetupCommand implements the `databricks-claude setup` subcommand.
//
// Flags:
//
//	--profile NAME   Databricks CLI profile to authenticate (default: state > "DEFAULT")
//	--host URL       Forwarded verbatim to `databricks auth login --host`
//	--force          Always re-run auth login, even if already authenticated
//
// Behaviour (idempotent):
//  1. Resolve profile via flag → state file → "DEFAULT".
//  2. Persist profile to state file.
//  3. If already authenticated and !force: print success and exit 0.
//  4. Exec `databricks auth login --profile X [--host Y]` with attached
//     stdin/stdout/stderr (interactive browser OAuth flow).
//  5. Re-check authentication. Exit 0 on success, 1 on failure.
func runSetupCommand(args []string) {
	if hasFlag(args, "--help") || hasFlag(args, "-h") {
		printSetupHelp()
		os.Exit(0)
	}

	profile := extractProfileFlag(args)
	host := extractSetupHostFlag(args)
	force := hasFlag(args, "--force")

	state := loadState()
	resolved := profile
	if resolved == "" && state.Profile != "" {
		resolved = state.Profile
	}
	if resolved == "" {
		resolved = "DEFAULT"
	}

	// Persist resolved profile so subsequent databricks-claude invocations
	// on this machine pick up the correct workspace.
	if state.Profile != resolved {
		state.Profile = resolved
		if err := saveState(state); err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: setup: failed to persist profile: %v\n", err)
			os.Exit(1)
		}
	}

	cliPath := resolveDatabricksCLI(state.DatabricksCLIPath)

	if !force && authcheck.IsAuthenticated(resolved, state.DatabricksCLIPath) {
		fmt.Fprintf(os.Stderr, "Already authenticated for profile %q\n", resolved)
		os.Exit(0)
	}

	// Build the auth login command.
	loginArgs := []string{"auth", "login", "--profile", resolved}
	if host != "" {
		loginArgs = append(loginArgs, "--host", host)
	}
	cmd := setupExecCommand(cliPath, loginArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude: setup: auth login failed: %v\n", err)
		os.Exit(1)
	}

	if !authcheck.IsAuthenticated(resolved, state.DatabricksCLIPath) {
		fmt.Fprintf(os.Stderr, "databricks-claude: setup: still not authenticated for profile %q after login\n", resolved)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Setup complete for profile %q\n", resolved)
	os.Exit(0)
}

// extractSetupHostFlag scans args for --host / --host=value.
func extractSetupHostFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--host" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--host=") {
			return strings.TrimPrefix(a, "--host=")
		}
	}
	return ""
}

// printSetupHelp prints usage for the `setup` subcommand to stderr.
func printSetupHelp() {
	fmt.Fprint(os.Stderr, `Usage: databricks-claude setup [flags]

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
`)
}
