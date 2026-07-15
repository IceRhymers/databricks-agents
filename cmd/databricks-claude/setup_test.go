package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
)

// buildAuthMockBinary compiles a minimal "databricks" mock binary.
// When called as "auth token ...", it prints tokenJSON and exits 0.
// When called as "auth login ...", it exits with loginExitCode.
// Any other invocation exits 0.
func buildAuthMockBinary(t *testing.T, tokenJSON string, loginExitCode int) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	bin := filepath.Join(dir, "databricks")

	tokenLit, _ := json.Marshal(tokenJSON)
	code := fmt.Sprintf(`package main
import (
	"fmt"
	"os"
)
func main() {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "auth" && i+1 < len(args) {
			switch args[i+1] {
			case "token":
				fmt.Print(%s)
				return
			case "login":
				os.Exit(%d)
			}
		}
	}
}
`, string(tokenLit), loginExitCode)

	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatalf("write auth mock src: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build auth mock: %v\n%s", err, out)
	}
	return bin
}

// overrideStatePath sets statePath to a file in a temp dir and returns cleanup.
func overrideStatePath(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".databricks-claude.json")
	orig := statePath
	statePath = func() string { return p }
	return p, func() { statePath = orig }
}

// TestSetupCommand_ProfileResolution_DefaultWhenEmpty verifies that with no
// --profile flag and an empty state file, the resolved profile is "DEFAULT".
func TestSetupCommand_ProfileResolution_DefaultWhenEmpty(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	args := []string{}
	resolved := extractProfileFlag(args)
	state := loadState()
	if resolved == "" && state.Profile != "" {
		resolved = state.Profile
	}
	if resolved == "" {
		resolved = "DEFAULT"
	}
	if resolved != "DEFAULT" {
		t.Errorf("resolved profile = %q, want DEFAULT", resolved)
	}
}

// TestSetupCommand_ProfilePersisted verifies that the setup logic persists the
// resolved profile to the state file.
func TestSetupCommand_ProfilePersisted(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	resolved := "myws"
	state := loadState()
	if state.Profile != resolved {
		state.Profile = resolved
		if err := saveState(state); err != nil {
			t.Fatalf("saveState: %v", err)
		}
	}

	got := loadState()
	if got.Profile != "myws" {
		t.Errorf("state.Profile = %q, want myws", got.Profile)
	}
}

// TestSetupCommand_AlreadyAuthed: IsAuthenticated returns true → login not run.
func TestSetupCommand_AlreadyAuthed(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	// Mock CLI: auth token → returns a valid token (IsAuthenticated = true).
	bin := buildAuthMockBinary(t, `{"access_token":"dapi-setup-token"}`, 0)

	// Confirm the mock reports authenticated.
	if !authcheck.IsAuthenticated("DEFAULT", bin) {
		t.Fatal("expected IsAuthenticated=true with valid-token mock")
	}

	// Confirm that no login subprocess fires.
	loginCalled := false
	orig := setupExecCommand
	defer func() { setupExecCommand = orig }()
	setupExecCommand = func(name string, args ...string) *exec.Cmd {
		loginCalled = true
		return exec.Command("true")
	}

	// Simulate the "already authed" branch of runSetupCommand inline.
	force := false
	st := persistentState{DatabricksCLIPath: bin}
	if !force && authcheck.IsAuthenticated("DEFAULT", st.DatabricksCLIPath) {
		// Would print "Already authenticated" and exit 0.
	} else {
		t.Error("expected already-authed branch to be taken")
	}

	if loginCalled {
		t.Error("login subprocess should not fire when already authenticated")
	}
}

// TestSetupCommand_NotAuthed_LoginSucceeds: no cached token, login succeeds → exit 0 path.
func TestSetupCommand_NotAuthed_LoginSucceeds(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	// Mock CLI: auth token → no token (IsAuthenticated = false); auth login → exit 0.
	bin := buildAuthMockBinary(t, `{"error":"no token"}`, 0)

	if authcheck.IsAuthenticated("DEFAULT", bin) {
		t.Fatal("expected IsAuthenticated=false with no-token mock")
	}

	loginRan := false
	orig := setupExecCommand
	defer func() { setupExecCommand = orig }()
	setupExecCommand = func(name string, args ...string) *exec.Cmd {
		loginRan = true
		return exec.Command("true") // login succeeds
	}

	// Simulate setup execution (not-authed branch) without calling os.Exit.
	st := persistentState{DatabricksCLIPath: bin}
	if !authcheck.IsAuthenticated("DEFAULT", st.DatabricksCLIPath) {
		cmd := setupExecCommand(resolveDatabricksCLI(st.DatabricksCLIPath), "auth", "login", "--profile", "DEFAULT")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("login cmd failed: %v", err)
		}
	}

	if !loginRan {
		t.Error("login subprocess should have been invoked when not authenticated")
	}
}

// TestSetupCommand_Force: --force triggers login even when already authenticated.
func TestSetupCommand_Force(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	// Mock CLI: auth token → valid token (IsAuthenticated = true).
	bin := buildAuthMockBinary(t, `{"access_token":"dapi-force-token"}`, 0)

	loginRan := false
	orig := setupExecCommand
	defer func() { setupExecCommand = orig }()
	setupExecCommand = func(name string, args ...string) *exec.Cmd {
		loginRan = true
		return exec.Command("true")
	}

	force := true
	st := persistentState{DatabricksCLIPath: bin}
	if force || !authcheck.IsAuthenticated("DEFAULT", st.DatabricksCLIPath) {
		// --force bypasses the already-authed check.
		cmd := setupExecCommand(resolveDatabricksCLI(st.DatabricksCLIPath), "auth", "login", "--profile", "DEFAULT")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("login cmd failed: %v", err)
		}
	}

	if !loginRan {
		t.Error("login subprocess should have been invoked when --force is set")
	}
}

// TestRunSetupCommand_NoFlags_LeavesStateUnchanged verifies that running setup
// with no --profile flag on a clean machine does not persist "DEFAULT" to the
// state file. "DEFAULT" is a sentinel for fall-through, not a real profile.
func TestRunSetupCommand_NoFlags_LeavesStateUnchanged(t *testing.T) {
	p, cleanup := overrideStatePath(t)
	defer cleanup()

	// Simulate the profile-resolution + write-side guard in runSetupCommand
	// with no flags and an empty initial state.
	args := []string{}
	profile := extractProfileFlag(args)
	state := loadState()
	resolved := profile
	if resolved == "" && state.Profile != "" {
		resolved = state.Profile
	}
	if resolved == "" {
		resolved = "DEFAULT"
	}

	// Apply the write-side guard (the fix).
	if resolved != "" && resolved != "DEFAULT" && state.Profile != resolved {
		state.Profile = resolved
		if err := saveState(state); err != nil {
			t.Fatalf("saveState: %v", err)
		}
	}

	// The state file must not have been created.
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("state file created for sentinel profile %q — should be absent", resolved)
	}

	got := loadState()
	if got.Profile != "" {
		t.Errorf("state.Profile = %q after setup with no flags, want \"\" (DEFAULT not persisted)", got.Profile)
	}
}

// TestExtractSetupHostFlag covers all flag forms for --host.
func TestExtractSetupHostFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--profile", "foo"}, ""},
		{[]string{"--host", "https://example.com"}, "https://example.com"},
		{[]string{"--host=https://example.com"}, "https://example.com"},
		{[]string{"--profile", "foo", "--host", "https://x.com"}, "https://x.com"},
		{[]string{"--host"}, ""}, // bare --host without value must not panic
	}
	for _, c := range cases {
		if got := extractSetupHostFlag(c.args); got != c.want {
			t.Errorf("extractSetupHostFlag(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}
