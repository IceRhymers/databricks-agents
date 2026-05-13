package authcheck

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// fakeCommandContext returns a *exec.Cmd that does nothing successfully
// and whose Output() returns the given data.
func fakeCommandContext(output string, fail bool) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if fail {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "echo", output)
	}
}

func fakeCommand(output string, fail bool) func(name string, args ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		if fail {
			return exec.Command("false")
		}
		return exec.Command("echo", output)
	}
}

// TestIsAuthenticated_FakeCmdNameReturnsFalse verifies that when the CLI binary
// does not exist, IsAuthenticated returns false cleanly (no panic or fatal).
func TestIsAuthenticated_FakeCmdNameReturnsFalse(t *testing.T) {
	// Use the real execCommandContext with a binary that cannot be found.
	// pkg/cli.ResolveDatabricksCLI will return the name unchanged when not found,
	// and exec will fail with a "not found" error — IsAuthenticated must return false.
	result := IsAuthenticated("DEFAULT", "/nonexistent/path/to/fake-databricks-binary")
	if result {
		t.Error("expected IsAuthenticated to return false for nonexistent binary, got true")
	}
}

func TestIsAuthenticated_Success(t *testing.T) {
	origCtx := execCommandContext
	defer func() { execCommandContext = origCtx }()

	execCommandContext = fakeCommandContext(`{"access_token":"dapi-xxx","token_type":"Bearer"}`, false)

	if !IsAuthenticated("DEFAULT", "") {
		t.Error("expected IsAuthenticated to return true when access_token is present")
	}
}

func TestIsAuthenticated_NoToken(t *testing.T) {
	origCtx := execCommandContext
	defer func() { execCommandContext = origCtx }()

	execCommandContext = fakeCommandContext(`{"error":"no token"}`, false)

	if IsAuthenticated("DEFAULT", "") {
		t.Error("expected IsAuthenticated to return false when access_token is absent")
	}
}

func TestIsAuthenticated_CommandFails(t *testing.T) {
	origCtx := execCommandContext
	defer func() { execCommandContext = origCtx }()

	execCommandContext = fakeCommandContext("", true)

	if IsAuthenticated("DEFAULT", "") {
		t.Error("expected IsAuthenticated to return false when command fails")
	}
}

func TestEnsureAuthenticated_AlreadyAuthed(t *testing.T) {
	origCtx := execCommandContext
	defer func() { execCommandContext = origCtx }()

	execCommandContext = fakeCommandContext(`{"access_token":"dapi-xxx"}`, false)

	if err := EnsureAuthenticated("DEFAULT", ""); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestEnsureAuthenticated_LoginFails(t *testing.T) {
	origCtx := execCommandContext
	origCmd := execCommand
	defer func() {
		execCommandContext = origCtx
		execCommand = origCmd
	}()

	// IsAuthenticated returns false
	execCommandContext = fakeCommandContext("", true)
	// login command fails
	execCommand = fakeCommand("", true)

	err := EnsureAuthenticated("DEFAULT", "")
	if err == nil {
		t.Error("expected error when login fails")
	}
}

func TestEnsureAuthenticated_LoginSucceeds(t *testing.T) {
	origCtx := execCommandContext
	origCmd := execCommand
	defer func() {
		execCommandContext = origCtx
		execCommand = origCmd
	}()

	callCount := 0
	// First call: not authenticated. Second call (after login): authenticated.
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "echo", fmt.Sprintf(`{"access_token":"dapi-xxx"}`))
	}
	// login succeeds
	execCommand = fakeCommand("login ok", false)

	if err := EnsureAuthenticated("DEFAULT", ""); err != nil {
		t.Errorf("expected no error after successful login, got: %v", err)
	}
}

// TestEnsureAuthenticatedWithStdout_StdoutCaptured verifies that the login
// subprocess's stdout is written to the supplied writer, not leaked to
// os.Stdout. This is the critical property that keeps Desktop's bare-token
// contract intact when the credential helper calls EnsureAuthenticatedWithStdout.
func TestEnsureAuthenticatedWithStdout_StdoutCaptured(t *testing.T) {
	origCtx := execCommandContext
	origCmd := execCommand
	defer func() {
		execCommandContext = origCtx
		execCommand = origCmd
	}()

	callCount := 0
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			// IsAuthenticated → not authed
			return exec.CommandContext(ctx, "false")
		}
		// post-login IsAuthenticated → authed
		return exec.CommandContext(ctx, "echo", `{"access_token":"dapi-xxx"}`)
	}
	// Login subprocess writes a noisy banner to stdout.
	execCommand = fakeCommand("noisy-login-banner", false)

	var buf bytes.Buffer
	if err := EnsureAuthenticatedWithStdout("DEFAULT", "", &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The banner must appear in buf, NOT have leaked elsewhere.
	if !strings.Contains(buf.String(), "noisy-login-banner") {
		t.Errorf("login stdout not captured in buf; buf=%q", buf.String())
	}
}

// TestEnsureAuthenticatedWithStdout_AlreadyAuthed confirms the fast-path:
// when already authenticated, no login subprocess is spawned and the writer
// receives nothing.
func TestEnsureAuthenticatedWithStdout_AlreadyAuthed(t *testing.T) {
	origCtx := execCommandContext
	origCmd := execCommand
	defer func() {
		execCommandContext = origCtx
		execCommand = origCmd
	}()

	execCommandContext = fakeCommandContext(`{"access_token":"dapi-xxx"}`, false)
	// execCommand should never be called; make it fail loudly if it is.
	loginCalled := false
	execCommand = func(name string, args ...string) *exec.Cmd {
		loginCalled = true
		return exec.Command("false")
	}

	var buf bytes.Buffer
	if err := EnsureAuthenticatedWithStdout("DEFAULT", "", &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loginCalled {
		t.Error("login subprocess should not be spawned when already authenticated")
	}
	if buf.Len() != 0 {
		t.Errorf("buffer should be empty when already authed; got %q", buf.String())
	}
}

// TestEnsureOrCheck_AuthedInteractive verifies that when the profile is
// already authenticated, EnsureOrCheck succeeds without spawning a login
// subprocess regardless of the interactive flag.
func TestEnsureOrCheck_AuthedInteractive(t *testing.T) {
	origCtx := execCommandContext
	origCmd := execCommand
	defer func() {
		execCommandContext = origCtx
		execCommand = origCmd
	}()

	execCommandContext = fakeCommandContext(`{"access_token":"dapi-xxx"}`, false)
	loginCalled := false
	execCommand = func(name string, args ...string) *exec.Cmd {
		loginCalled = true
		return exec.Command("false")
	}

	if err := EnsureOrCheck("DEFAULT", "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loginCalled {
		t.Error("login subprocess should not be spawned when already authed")
	}
}

// TestEnsureOrCheck_AuthedNonInteractive: same fast-path under non-interactive.
func TestEnsureOrCheck_AuthedNonInteractive(t *testing.T) {
	origCtx := execCommandContext
	defer func() { execCommandContext = origCtx }()

	execCommandContext = fakeCommandContext(`{"access_token":"dapi-xxx"}`, false)

	if err := EnsureOrCheck("DEFAULT", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEnsureOrCheck_UnauthedInteractive verifies that interactive mode falls
// through to a real `databricks auth login` invocation when the token check
// fails.
func TestEnsureOrCheck_UnauthedInteractive(t *testing.T) {
	origCtx := execCommandContext
	origCmd := execCommand
	defer func() {
		execCommandContext = origCtx
		execCommand = origCmd
	}()

	// EnsureOrCheck → IsAuthenticated (false) → EnsureAuthenticated →
	// EnsureAuthenticatedWithStdout → IsAuthenticated (false) → login →
	// IsAuthenticated (true). Three IsAuthenticated calls total.
	callCount := 0
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount < 3 {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "echo", `{"access_token":"dapi-xxx"}`)
	}
	loginCalled := false
	execCommand = func(name string, args ...string) *exec.Cmd {
		loginCalled = true
		return exec.Command("echo", "login ok")
	}

	if err := EnsureOrCheck("DEFAULT", "", true); err != nil {
		t.Fatalf("expected success after interactive login, got: %v", err)
	}
	if !loginCalled {
		t.Error("interactive mode should spawn `databricks auth login` when unauthed")
	}
}

// TestEnsureOrCheck_UnauthedNonInteractive: the daemon-safety property — when
// stdin is not a tty, do NOT prompt; return an actionable error instead.
func TestEnsureOrCheck_UnauthedNonInteractive(t *testing.T) {
	origCtx := execCommandContext
	origCmd := execCommand
	defer func() {
		execCommandContext = origCtx
		execCommand = origCmd
	}()

	execCommandContext = fakeCommandContext("", true)
	loginCalled := false
	execCommand = func(name string, args ...string) *exec.Cmd {
		loginCalled = true
		return exec.Command("false")
	}

	err := EnsureOrCheck("DEFAULT", "", false)
	if err == nil {
		t.Fatal("expected error in non-interactive mode when unauthed")
	}
	if loginCalled {
		t.Error("non-interactive mode must NOT spawn `databricks auth login`")
	}
	// Error message should mention the actionable next step.
	msg := err.Error()
	if !strings.Contains(msg, "not authenticated") {
		t.Errorf("error message missing 'not authenticated': %q", msg)
	}
	if !strings.Contains(msg, "databricks auth login") {
		t.Errorf("error message missing remediation hint: %q", msg)
	}
}

// TestEnsureOrCheck_CLINotFound verifies that a missing CLI binary is reported
// as not-authenticated (IsAuthenticated returns false → non-interactive path
// returns an error). This guards against the install path silently treating a
// missing CLI as "no auth needed".
func TestEnsureOrCheck_CLINotFound(t *testing.T) {
	// Use real exec (no stub) against a definitely-missing binary so the
	// underlying `databricks auth token` invocation truly fails.
	err := EnsureOrCheck("DEFAULT", "/nonexistent/path/to/fake-databricks-binary", false)
	if err == nil {
		t.Error("expected error when CLI binary is missing in non-interactive mode")
	}
}
