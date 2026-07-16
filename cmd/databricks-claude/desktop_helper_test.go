package main

import (
	"bytes"
	"fmt"
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

// seedHelperState points the credential helper's state file at a mock CLI so
// credentialHelperToken's internal loadState() picks it up. Returns the cleanup.
//
// Overrides HOME as well: credentialHelperToken calls helperDebugLog, which
// resolves its log dir from os.UserHomeDir() and is ungated, so without this the
// suite appends to the developer's real ~/.cache/databricks-claude (or
// ~/Library/Logs on darwin) — including token prefixes. Harmless with today's
// hardcoded literals, but a test that ever seeds a real token would leak its
// prefix to disk.
func seedHelperState(t *testing.T, cliPath string) func() {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	_, cleanup := overrideStatePath(t)
	if err := saveState(persistentState{Profile: "DEFAULT", DatabricksCLIPath: cliPath}); err != nil {
		cleanup()
		t.Fatalf("seed state: %v", err)
	}
	return cleanup
}

// TestRunCredentialHelper_WarmCache verifies the fast path: when the token
// provider succeeds on the first attempt, no login subprocess is spawned.
//
// Calls the real credentialHelperToken seam (#218). Before the seam existed
// this test re-implemented the helper's sequence and pinned nothing about it.
func TestRunCredentialHelper_WarmCache(t *testing.T) {
	const token = "dapi-warm-cache-token"
	// Build a mock that always returns a valid token on "auth token".
	bin := buildHelperBinary(t, `{"access_token":"`+token+`","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"}`, 0)

	defer seedHelperState(t, bin)()

	var loginOut bytes.Buffer
	tok, err := credentialHelperToken("DEFAULT", &loginOut)
	if err != nil {
		t.Fatalf("credentialHelperToken: %v", err)
	}
	if tok != token {
		t.Errorf("token = %q, want %q", tok, token)
	}
	// Fast path: no login subprocess, so nothing reaches loginOut.
	if loginOut.Len() != 0 {
		t.Errorf("warm cache spawned login; loginOut = %q", loginOut.String())
	}
}

// TestRunCredentialHelper_ColdCache_LoginOK verifies the recovery path:
// when the first token fetch fails (expired), the seam invokes
// EnsureAuthenticatedWithStdout and retries successfully.
//
// Calls the real seam (#218), so the retry orchestration itself is now pinned —
// previously the test drove each step by hand and could not have caught the
// helper wiring them up wrongly.
func TestRunCredentialHelper_ColdCache_LoginOK(t *testing.T) {
	const token = `{"access_token":"dapi-retry-ok","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"}`
	bin, _ := buildRetryTokenBinary(t, token)

	defer seedHelperState(t, bin)()

	var loginOut bytes.Buffer
	tok, err := credentialHelperToken("DEFAULT", &loginOut)
	if err != nil {
		t.Fatalf("credentialHelperToken: %v", err)
	}
	if tok != "dapi-retry-ok" {
		t.Errorf("token = %q, want %q", tok, "dapi-retry-ok")
	}
}

// TestRunCredentialHelper_ColdCache_LoginFail verifies the failure path: when
// both the token fetch and the re-auth fail, the seam returns the error the
// caller turns into "databricks-claude: <err>\n" + exit 1.
//
// #218: this test previously asserted a stderr string it had just formatted
// itself — a tautology that could not fail — because runCredentialHelper had no
// error return to assert against, despite the comment claiming otherwise. The
// seam gives it one, so it now asserts what its name promises.
func TestRunCredentialHelper_ColdCache_LoginFail(t *testing.T) {
	bin := buildAlwaysFailLoginBinary(t)

	defer seedHelperState(t, bin)()

	var loginOut bytes.Buffer
	tok, err := credentialHelperToken("DEFAULT", &loginOut)
	if err == nil {
		t.Fatalf("expected error when login fails; got token %q", tok)
	}
	if tok != "" {
		t.Errorf("token = %q, want empty on failure", tok)
	}
	// Pins the caller's stderr contract: "databricks-claude: " + err + "\n"
	// must reproduce today's byte-for-byte message.
	if !strings.Contains(err.Error(), "credential helper authentication failed") {
		t.Errorf("err = %q, want it to mention authentication failure", err)
	}
	if got := fmt.Sprintf("databricks-claude: %v\n", err); !strings.HasPrefix(got, "databricks-claude: credential helper authentication failed: ") {
		t.Errorf("caller stderr line = %q", got)
	}
}

// TestCredentialHelper_HonorsStateDatabricksCLIPath is AC#6′ (#218): it pins
// the credential helper's use of state.DatabricksCLIPath across the migration
// of desktop_config.go:330 from the positional NewTokenProvider(profile, cmdName)
// to the keyed dbxauth.Config{Profile, CLIPath}.
//
// HERMETICITY IS LOAD-BEARING — do not remove either Setenv. cli.ResolveDatabricksCLI
// consults $DATABRICKS_CLI *before* MDM and LookPath, so with it unset in the
// ambient environment a Profile/CLIPath key-swap silently resolves a real CLI and
// this test would PASS while the helper is broken. With both cleared, a swap makes
// the CLI unresolvable and the test fails — which is the entire point of the pin.
func TestCredentialHelper_HonorsStateDatabricksCLIPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The mock is reached via an absolute state.DatabricksCLIPath, which
		// cli.ResolveDatabricksCLI short-circuits, so this does not actually
		// depend on the unix-only permission-bit tiers. Skipped only because
		// CI is linux-only and the Windows path is unverified.
		t.Skip("windows path unverified: CI is linux-only")
	}
	const token = "dapi-from-state-cli-path"
	bin := buildHelperBinary(t, `{"access_token":"`+token+`","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"}`, 0)

	defer seedHelperState(t, bin)()

	// Hermetic: the mock is reachable ONLY via state.DatabricksCLIPath.
	t.Setenv("PATH", "")
	t.Setenv("DATABRICKS_CLI", "")

	var loginOut bytes.Buffer
	tok, err := credentialHelperToken("DEFAULT", &loginOut)
	if err != nil {
		t.Fatalf("credentialHelperToken: %v", err)
	}
	if tok != token {
		t.Errorf("token = %q, want %q", tok, token)
	}
	// Desktop reads stdout verbatim: the token must carry no trailing newline
	// and no surrounding whitespace.
	if tok != strings.TrimSpace(tok) {
		t.Errorf("token has surrounding whitespace: %q", tok)
	}
	if strings.HasSuffix(tok, "\n") {
		t.Errorf("token has trailing newline: %q", tok)
	}
	// The bare-token contract: login output must never reach stdout.
	if loginOut.Len() != 0 {
		t.Errorf("unexpected login output: %q", loginOut.String())
	}
}

// TestRunCredentialHelper_StateDefault_MDMPopulated_UsesMDM verifies that
// when state.Profile is the "DEFAULT" sentinel AND MDM supplies a profile,
// resolveCredHelperProfile advances past state and returns the MDM value.
func TestRunCredentialHelper_StateDefault_MDMPopulated_UsesMDM(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // resolveCredHelperProfile → helperDebugLog writes under $HOME
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
	t.Setenv("HOME", t.TempDir()) // resolveCredHelperProfile → helperDebugLog writes under $HOME
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
	t.Setenv("HOME", t.TempDir()) // resolveCredHelperProfile → helperDebugLog writes under $HOME
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

// #218: the ensureAuthForHelper / authcheck_EnsureAuthenticatedWithStdout /
// init() chain that lived here is deleted. It claimed to call "the same function
// runCredentialHelper uses" but re-implemented login by shelling out itself and
// never called authcheck at all — so the tests that used it could not have caught
// a change in the helper's real auth wiring. The credentialHelperToken seam calls
// the real authcheck.EnsureAuthenticatedWithStdout, so the indirection is dead.
