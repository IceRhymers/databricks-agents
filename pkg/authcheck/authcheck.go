package authcheck

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/cli"
)

// Overridable for testing.
var execCommand = exec.Command
var execCommandContext = exec.CommandContext

// IsAuthenticated returns true if a valid token can be fetched for the given
// Databricks profile without triggering an interactive login.
// cmdName is the Databricks CLI binary name or path; pass "" to use the default ("databricks").
func IsAuthenticated(profile, cmdName string) bool {
	resolved := cli.ResolveDatabricksCLI(cmdName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := execCommandContext(ctx, resolved, "auth", "token", "--profile", profile).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "access_token")
}

// EnsureAuthenticatedWithStdout verifies the user has a valid token for the
// profile. If not authenticated, it runs "<cmdName> auth login --profile
// <profile>" interactively; the login subprocess's stdout is written to w
// (e.g. os.Stderr or a bytes.Buffer) rather than the caller's stdout.
// This lets callers that own their stdout for another protocol (e.g. the
// credential helper emitting a bare token) capture or suppress the login
// subprocess's output without leaking it.
// cmdName is the Databricks CLI binary name or path; pass "" to use the default.
func EnsureAuthenticatedWithStdout(profile, cmdName string, w io.Writer) error {
	if IsAuthenticated(profile, cmdName) {
		return nil
	}
	resolved := cli.ResolveDatabricksCLI(cmdName)
	fmt.Fprintf(os.Stderr, "databricks: not authenticated for profile %q, opening browser login...\n", profile)
	cmd := execCommand(resolved, "auth", "login", "--profile", profile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("databricks auth login failed: %w", err)
	}
	if !IsAuthenticated(profile, cmdName) {
		return fmt.Errorf("still not authenticated for profile %q after login attempt", profile)
	}
	return nil
}

// EnsureAuthenticated verifies the user has a valid token for the profile.
// It is a thin shim over EnsureAuthenticatedWithStdout that routes the login
// subprocess stdout to os.Stdout — the same behaviour as before this variant
// was introduced, preserving backward compatibility for all existing callers.
// cmdName is the Databricks CLI binary name or path; pass "" to use the default.
func EnsureAuthenticated(profile, cmdName string) error {
	return EnsureAuthenticatedWithStdout(profile, cmdName, os.Stdout)
}

// EnsureOrCheck verifies auth for profile. If interactive is true and the CLI
// lacks a valid token, prompts the user via `databricks auth login`. If
// interactive is false, returns a non-nil error instead of prompting — this is
// the mode `serve install` and other daemon-adjacent call sites use when stdin
// is not a tty: they fail loudly at the boundary rather than launching a
// browser flow that has nowhere to land.
// cmdName is the resolved Databricks CLI binary name or path; pass "" to use
// the default ("databricks").
func EnsureOrCheck(profile, cmdName string, interactive bool) error {
	if IsAuthenticated(profile, cmdName) {
		return nil
	}
	if !interactive {
		return fmt.Errorf("profile %q is not authenticated and stdin is not a tty (run `databricks auth login --profile %s` first, or pass --skip-auth-check to defer)", profile, profile)
	}
	return EnsureAuthenticated(profile, cmdName)
}
