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
