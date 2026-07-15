package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
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
	r, _ := setupCommand.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stderr, setupCommand, nil)
		os.Exit(0)
	}

	profile := r.Strings["profile"]
	host := r.Strings["host"]
	force := r.Bools["force"]

	state := loadState()
	resolved := profile
	if resolved == "" && state.Profile != "" {
		resolved = state.Profile
	}
	if resolved == "" {
		resolved = "DEFAULT"
	}

	// Persist resolved profile so subsequent databricks-claude invocations
	// on this machine pick up the correct workspace. Skip "DEFAULT" — it is a
	// sentinel for "fall through the chain", not a real profile choice
	// (mirrors ensureconfig.go:42).
	if resolved != "" && resolved != "DEFAULT" && state.Profile != resolved {
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

// extractSetupHostFlag scans args for --host / --host=value. Kept after #171
// because setup_test.go still drives it; runSetupCommand itself no longer
// uses it (it walks the tree-driven parse path via setupCommand.Parse).
func extractSetupHostFlag(args []string) string {
	r, _ := setupCommand.Parse(args)
	return r.Strings["host"]
}

// Help for `setup` is rendered via cmd.Render against setupCommand
// (commands.go). The hand-rolled printSetupHelp was deleted in #171.
