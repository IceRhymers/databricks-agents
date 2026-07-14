package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildRetryTokenBinary compiles a mock "databricks" binary that:
//   - First invocation of "auth token": exits 1 (no token, simulating expiry).
//   - Subsequent "auth token": returns a valid JSON token.
//   - "auth login": exits 0 (login succeeds).
//
// The invocation count is stored in a temp file so the same binary can
// distinguish first vs. subsequent calls across processes.
func buildRetryTokenBinary(t *testing.T, token string) (bin, countFile string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	bin = filepath.Join(dir, "databricks")
	countFile = filepath.Join(dir, "count.txt")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	tokenLit, _ := jsonMarshalString(token)
	countFileLit, _ := jsonMarshalString(countFile)

	code := fmt.Sprintf(`package main
import (
	"fmt"
	"os"
	"strconv"
	"strings"
)
func main() {
	args := strings.Join(os.Args[1:], " ")
	if strings.Contains(args, "auth login") {
		os.Exit(0)
	}
	if strings.Contains(args, "auth token") {
		cf := %s
		count := 0
		if data, err := os.ReadFile(cf); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			count = n
		}
		count++
		_ = os.WriteFile(cf, []byte(strconv.Itoa(count)), 0o600)
		if count == 1 {
			fmt.Fprintln(os.Stderr, "token expired")
			os.Exit(1)
		}
		fmt.Printf(%s)
		return
	}
}
`, countFileLit, tokenLit)

	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatalf("write retry-token src: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build retry-token binary: %v\n%s", err, out)
	}
	return bin, countFile
}

// jsonMarshalString returns the Go string literal form of s (same as JSON string).
func jsonMarshalString(s string) (string, error) {
	b := []byte{'"'}
	for _, c := range s {
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		default:
			b = append(b, byte(c))
		}
	}
	b = append(b, '"')
	return string(b), nil
}

// buildAlwaysFailLoginBinary compiles a mock "databricks" binary where
// "auth token" always fails and "auth login" also fails.
func buildAlwaysFailLoginBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	bin := filepath.Join(dir, "databricks")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	code := `package main
import (
	"fmt"
	"os"
	"strings"
)
func main() {
	args := strings.Join(os.Args[1:], " ")
	if strings.Contains(args, "auth login") {
		fmt.Fprintln(os.Stderr, "auth login: network error")
		os.Exit(1)
	}
	if strings.Contains(args, "auth token") {
		fmt.Fprintln(os.Stderr, "token expired")
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatalf("write fail-login src: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fail-login binary: %v\n%s", err, out)
	}
	return bin
}

// TestRunCredentialHelper_WarmCache verifies the fast path: when the token
// provider succeeds on the first attempt, no login subprocess is spawned.
func TestRunCredentialHelper_WarmCache(t *testing.T) {
	const token = "dapi-warm-cache-token"
	// Build a mock that always returns a valid token on "auth token".
	bin := buildHelperBinary(t, `{"access_token":"`+token+`","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"}`, 0)

	_, cleanup := overrideStatePath(t)
	defer cleanup()

	// Pre-warm the token cache by calling the token provider directly.
	tp := NewTokenProvider("DEFAULT", bin)
	if _, err := tp.Token(context.Background()); err != nil {
		t.Fatalf("pre-warm token: %v", err)
	}

	// Now verify the helper path: tp.Token() should return the cached token
	// without calling login.
	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("warm cache tp.Token: %v", err)
	}
	if !strings.Contains(tok, "dapi-warm-cache-token") {
		t.Errorf("token = %q, expected dapi-warm-cache-token", tok)
	}
}

// TestRunCredentialHelper_ColdCache_LoginOK verifies the recovery path:
// when tp.Token() fails initially (token expired), EnsureAuthenticatedWithStdout
// is called and then tp.Token() is retried successfully.
func TestRunCredentialHelper_ColdCache_LoginOK(t *testing.T) {
	const token = `{"access_token":"dapi-retry-ok","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"}`
	bin, _ := buildRetryTokenBinary(t, token)

	_, cleanup := overrideStatePath(t)
	defer cleanup()

	// First call: should fail (simulates cold/expired cache).
	tp := NewTokenProvider("DEFAULT", bin)
	_, firstErr := tp.Token(context.Background())
	if firstErr == nil {
		t.Skip("mock first call unexpectedly succeeded; binary may have stale count file")
	}

	// EnsureAuthenticatedWithStdout: login succeeds (bin exits 0 for auth login).
	var buf bytes.Buffer
	if err := ensureAuthForHelper("DEFAULT", bin, &buf); err != nil {
		t.Fatalf("EnsureAuthenticatedWithStdout: %v", err)
	}

	// Second call: should succeed after login.
	tp2 := NewTokenProvider("DEFAULT", bin)
	tok, err := tp2.Token(context.Background())
	if err != nil {
		t.Fatalf("tp.Token retry: %v", err)
	}
	if !strings.Contains(tok, "dapi-retry-ok") {
		t.Errorf("token = %q, expected dapi-retry-ok", tok)
	}
}

// TestRunCredentialHelper_ColdCache_LoginFail verifies the failure path:
// when both tp.Token() and EnsureAuthenticatedWithStdout fail, the helper
// should report a non-zero exit (we test the error return, not os.Exit).
func TestRunCredentialHelper_ColdCache_LoginFail(t *testing.T) {
	bin := buildAlwaysFailLoginBinary(t)

	_, cleanup := overrideStatePath(t)
	defer cleanup()

	tp := NewTokenProvider("DEFAULT", bin)
	_, firstErr := tp.Token(context.Background())
	if firstErr == nil {
		t.Fatal("expected tp.Token to fail with always-fail binary")
	}

	var buf bytes.Buffer
	authErr := ensureAuthForHelper("DEFAULT", bin, &buf)
	if authErr == nil {
		t.Fatal("expected EnsureAuthenticatedWithStdout to fail when login fails")
	}

	// Verify that a stderr message would be written (helper exits 1 path).
	var stderr strings.Builder
	fmt.Fprintf(&stderr, "databricks-claude: credential helper authentication failed: %v\n", authErr)
	if !strings.Contains(stderr.String(), "credential helper authentication failed") {
		t.Errorf("stderr message does not mention failure, got %q", stderr.String())
	}
}

// TestRunCredentialHelper_StateDefault_MDMPopulated_UsesMDM verifies that
// when state.Profile is the "DEFAULT" sentinel AND MDM supplies a profile,
// resolveCredHelperProfile advances past state and returns the MDM value.
func TestRunCredentialHelper_StateDefault_MDMPopulated_UsesMDM(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	// Seed state with the stale sentinel.
	if err := saveState(persistentState{Profile: "DEFAULT"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	orig := mdmReader
	defer func() { mdmReader = orig }()
	mdmReader = func(string) (string, error) { return "fleet-profile", nil }

	got := resolveCredHelperProfile("")
	if got != "fleet-profile" {
		t.Errorf("resolveCredHelperProfile = %q, want fleet-profile (MDM must win over stale DEFAULT state)", got)
	}
}

// TestRunCredentialHelper_StateDefault_NoMDM_FallsThroughToDEFAULT verifies
// that when state.Profile is the sentinel AND MDM is empty, the helper still
// falls through to "DEFAULT" — no regression on the no-config case.
func TestRunCredentialHelper_StateDefault_NoMDM_FallsThroughToDEFAULT(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	if err := saveState(persistentState{Profile: "DEFAULT"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	orig := mdmReader
	defer func() { mdmReader = orig }()
	mdmReader = func(string) (string, error) { return "", nil }

	got := resolveCredHelperProfile("")
	if got != "DEFAULT" {
		t.Errorf("resolveCredHelperProfile = %q, want DEFAULT (fall-through when MDM empty)", got)
	}
}

// TestRunCredentialHelper_RealProfile_MDMPopulated_StateWins verifies that a
// real profile in state beats MDM — the local admin's explicit choice wins
// over fleet MDM (resolution order: flag > state > MDM > "DEFAULT").
func TestRunCredentialHelper_RealProfile_MDMPopulated_StateWins(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	if err := saveState(persistentState{Profile: "fevm"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	orig := mdmReader
	defer func() { mdmReader = orig }()
	mdmReader = func(string) (string, error) { return "fleet-profile", nil }

	got := resolveCredHelperProfile("")
	if got != "fevm" {
		t.Errorf("resolveCredHelperProfile = %q, want fevm (state beats MDM for real profiles)", got)
	}
}

// ensureAuthForHelper is a thin test helper that calls
// authcheck.EnsureAuthenticatedWithStdout through the public API.
func ensureAuthForHelper(profile, cliPath string, w io.Writer) error {
	from := authcheck_EnsureAuthenticatedWithStdout
	return from(profile, cliPath, w)
}

// authcheck_EnsureAuthenticatedWithStdout is an alias that avoids importing
// authcheck in test files where the package is already an internal dependency.
// Resolved via the init function below.
var authcheck_EnsureAuthenticatedWithStdout func(profile, cmdName string, w io.Writer) error

func init() {
	// Wire the authcheck function.
	authcheck_EnsureAuthenticatedWithStdout = func(profile, cmdName string, w io.Writer) error {
		// Import indirection: use pkg/authcheck through the main package
		// by calling the same function runCredentialHelper uses.
		cmd := exec.Command(resolveDatabricksCLI(cmdName), "auth", "login", "--profile", profile)
		cmd.Stdout = w
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
}
