package dbxauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildHelperBinary compiles a small helper binary that prints a fixed JSON
// response and exits with a given code. Returns the path to the binary.
// Per CLAUDE.md, exec.Command is never mocked — real binaries are compiled.
func buildHelperBinary(t *testing.T, jsonPayload string, exitCode int) string {
	t.Helper()
	return buildBinary(t, "helper", jsonPayload, exitCode)
}

// buildAuthEnvBinary compiles a mock for "databricks auth env --output json".
func buildAuthEnvBinary(t *testing.T, jsonPayload string, exitCode int) string {
	t.Helper()
	return buildBinary(t, "authenv", jsonPayload, exitCode)
}

func buildBinary(t *testing.T, name, jsonPayload string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()

	src := filepath.Join(dir, "main.go")
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	payloadLiteral, _ := json.Marshal(jsonPayload)

	code := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Print(%s)
	os.Exit(%d)
}
`, string(payloadLiteral), exitCode)

	if err := os.WriteFile(src, []byte(code), 0600); err != nil {
		t.Fatalf("write helper src: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	return bin
}

// buildSlowBinary compiles a binary that sleeps well past the fetch timeout.
func buildSlowBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	src := filepath.Join(dir, "main.go")
	bin := filepath.Join(dir, "slow")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	code := `package main

import "time"

func main() {
	time.Sleep(60 * time.Second)
}
`
	if err := os.WriteFile(src, []byte(code), 0600); err != nil {
		t.Fatalf("write slow src: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build slow: %v\n%s", err, out)
	}
	return bin
}

// buildArgRecorderBinary compiles a mock that records the argv it received to
// argFile, then prints a valid token. Lets a test assert what was passed to the
// Databricks CLI without mocking exec.Command.
func buildArgRecorderBinary(t *testing.T, argFile string) string {
	t.Helper()
	dir := t.TempDir()

	src := filepath.Join(dir, "main.go")
	bin := filepath.Join(dir, "recorder")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	argLiteral, _ := json.Marshal(argFile)
	code := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	os.WriteFile(%s, []byte(strings.Join(os.Args[1:], " ")), 0600)
	fmt.Print(`+"`"+`{"access_token":"tok","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"}`+"`"+`)
}
`, string(argLiteral))

	if err := os.WriteFile(src, []byte(code), 0600); err != nil {
		t.Fatalf("write recorder src: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build recorder: %v\n%s", err, out)
	}
	return bin
}

func futureExpiry() string { return time.Now().Add(30 * time.Minute).Format(time.RFC3339) }

func validTokenJSON(tok, expiry string) string {
	return `{"access_token":"` + tok + `","token_type":"Bearer","expiry":"` + expiry + `"}`
}

// --- The bugfix (#218) -------------------------------------------------------
//
// These are the point of the PR. Before #218, codex and opencode exec'd the bare
// CLI name and ignored $DATABRICKS_CLI entirely — so under launchd/systemd's
// minimal PATH their token fetch failed while their authcheck path (which does
// resolve) succeeded. Captured against pre-change codex:
//
//	RED: databricks auth token failed: exec: "databricks": executable file not
//	     found in $PATH
//
// HERMETICITY IS LOAD-BEARING. The pre-#218 tests passed an ABSOLUTE helper path
// as cmdName, which cli.ResolveDatabricksCLI short-circuits (cli.go:67-69) — so
// they never reached the $DATABRICKS_CLI tier and would have stayed green even if
// the fix were dropped. That is why the bug survived. CLIPath:"" + PATH:"" is the
// only shape that reproduces the real-world failure.

func TestFetchToken_HonorsDATABRICKS_CLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		// cli.IsExecutableFile gates the $DATABRICKS_CLI tier on unix permission
		// bits (mode&0o111). Windows files carry none, so that tier can never
		// fire there and this probe has nothing to assert.
		t.Skip("$DATABRICKS_CLI tier is unix-only: gated on permission bits")
	}
	bin := buildHelperBinary(t, validTokenJSON("tok-env", futureExpiry()), 0)
	t.Setenv("DATABRICKS_CLI", bin)
	t.Setenv("PATH", "") // a bare "databricks" must not resolve

	// CLIPath "" is the real-world shape: every launcher call site but the
	// credential helper leaves it empty.
	tp := NewProvider(Config{Profile: "DEFAULT"})
	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "tok-env" {
		t.Errorf("token = %q, want %q", tok, "tok-env")
	}
}

func TestDiscoverHost_HonorsDATABRICKS_CLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Same as above: the $DATABRICKS_CLI tier is gated on unix permission bits.
		t.Skip("$DATABRICKS_CLI tier is unix-only: gated on permission bits")
	}
	bin := buildAuthEnvBinary(t, `{"env":{"DATABRICKS_HOST":"https://env.example.com"}}`, 0)
	t.Setenv("DATABRICKS_CLI", bin)
	t.Setenv("PATH", "")

	host, err := DiscoverHost(Config{Profile: "DEFAULT"})
	if err != nil {
		t.Fatalf("DiscoverHost: %v", err)
	}
	if host != "https://env.example.com" {
		t.Errorf("host = %q, want %q", host, "https://env.example.com")
	}
}

// TestFetchToken_FallbackDirScan covers the third resolution tier codex and
// opencode also gain: no PATH, no $DATABRICKS_CLI, binary in ~/.local/bin.
func TestFetchToken_FallbackDirScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fallback dir scan is unix-shaped")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := buildHelperBinary(t, validTokenJSON("tok-fallback", futureExpiry()), 0)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read helper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "databricks"), data, 0o755); err != nil {
		t.Fatalf("install helper: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("DATABRICKS_CLI", "")

	tp := NewProvider(Config{Profile: "DEFAULT"})
	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "tok-fallback" {
		t.Errorf("token = %q, want %q", tok, "tok-fallback")
	}
}

// --- Config defaults ---------------------------------------------------------

func TestConfig_ProfileDefaultsToDEFAULT(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args.txt")
	bin := buildArgRecorderBinary(t, argFile)

	// Zero-value Profile must resolve to "DEFAULT".
	tp := NewProvider(Config{CLIPath: bin})
	if _, err := tp.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}

	got, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if want := "auth token --profile DEFAULT"; string(got) != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestConfig_ExplicitProfileIsPassedThrough(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args.txt")
	bin := buildArgRecorderBinary(t, argFile)

	tp := NewProvider(Config{Profile: "myprofile", CLIPath: bin})
	if _, err := tp.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}

	got, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	// Guards the pre-#218 divergence: a Profile/CLIPath swap would land the CLI
	// path here instead of the profile name.
	if want := "auth token --profile myprofile"; string(got) != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// --- TokenProvider -----------------------------------------------------------

func TestTokenProvider_FreshToken(t *testing.T) {
	bin := buildHelperBinary(t, validTokenJSON("dapi-fresh", futureExpiry()), 0)
	tp := NewProvider(Config{Profile: "DEFAULT", CLIPath: bin})

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "dapi-fresh" {
		t.Errorf("token = %q, want %q", tok, "dapi-fresh")
	}
}

func TestTokenProvider_CacheHit(t *testing.T) {
	bin := buildHelperBinary(t, validTokenJSON("dapi-cached", futureExpiry()), 0)
	tp := NewProvider(Config{Profile: "DEFAULT", CLIPath: bin})

	first, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	// Removing the binary proves the second call is served from cache.
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove bin: %v", err)
	}
	second, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if first != second {
		t.Errorf("cache miss: %q != %q", first, second)
	}
}

func TestTokenProvider_RefreshNearExpiry(t *testing.T) {
	// Inside the 5-minute refresh buffer, so a re-fetch is forced.
	near := time.Now().Add(2 * time.Minute).Format(time.RFC3339)
	bin := buildHelperBinary(t, validTokenJSON("dapi-near", near), 0)
	tp := NewProvider(Config{Profile: "DEFAULT", CLIPath: bin})

	if _, err := tp.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	// A near-expiry token must not be served from cache: with the binary gone
	// the forced refresh fails, and the cached token is returned as fallback.
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove bin: %v", err)
	}
	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token fallback: %v", err)
	}
	if tok != "dapi-near" {
		t.Errorf("token = %q, want the cached %q", tok, "dapi-near")
	}
}

func TestTokenProvider_FallbackOnError(t *testing.T) {
	bin := buildHelperBinary(t, validTokenJSON("dapi-good", futureExpiry()), 0)
	tp := NewProvider(Config{Profile: "DEFAULT", CLIPath: bin})
	if _, err := tp.Token(context.Background()); err != nil {
		t.Fatalf("seed Token: %v", err)
	}

	// Replace with a failing binary; the cached token must still be served.
	failing := buildHelperBinary(t, "", 1)
	data, err := os.ReadFile(failing)
	if err != nil {
		t.Fatalf("read failing: %v", err)
	}
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatalf("overwrite bin: %v", err)
	}

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("expected fallback to cached token, got %v", err)
	}
	if tok != "dapi-good" {
		t.Errorf("token = %q, want %q", tok, "dapi-good")
	}
}

func TestTokenProvider_NoCachedTokenError(t *testing.T) {
	bin := buildHelperBinary(t, "", 1)
	tp := NewProvider(Config{Profile: "DEFAULT", CLIPath: bin})

	if _, err := tp.Token(context.Background()); err == nil {
		t.Fatal("expected error with no cached token and a failing CLI")
	}
}

func TestTokenProvider_SubprocessTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess timeout test is slow")
	}
	bin := buildSlowBinary(t)
	tp := NewProvider(Config{Profile: "DEFAULT", CLIPath: bin})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if _, err := tp.Token(ctx); err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- parseTokenResponse ------------------------------------------------------

func TestParseTokenResponse_RFC3339(t *testing.T) {
	want := time.Now().Add(42 * time.Minute).Truncate(time.Second)
	resp, err := parseTokenResponse([]byte(validTokenJSON("tok", want.Format(time.RFC3339))))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !resp.expiryTime().Equal(want) {
		t.Errorf("expiry = %v, want %v", resp.expiryTime(), want)
	}
}

func TestParseTokenResponse_UnixTimestamp(t *testing.T) {
	secs := time.Now().Add(time.Hour).Unix()
	resp, err := parseTokenResponse([]byte(validTokenJSON("tok", strconv.FormatInt(secs, 10))))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := resp.expiryTime().Unix(); got != secs {
		t.Errorf("expiry = %d, want %d", got, secs)
	}
}

func TestParseTokenResponse_MissingExpiry(t *testing.T) {
	resp, err := parseTokenResponse([]byte(`{"access_token":"tok","token_type":"Bearer"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Conservative 55-minute default.
	got := time.Until(resp.expiryTime())
	if got < 54*time.Minute || got > 56*time.Minute {
		t.Errorf("expiry in %v, want ~55m", got)
	}
}

func TestParseTokenResponse_MalformedJSON(t *testing.T) {
	if _, err := parseTokenResponse([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseTokenResponse_EmptyToken(t *testing.T) {
	if _, err := parseTokenResponse([]byte(`{"access_token":"","token_type":"Bearer"}`)); err == nil {
		t.Fatal("expected error for empty access_token")
	}
}

// --- DiscoverHost ------------------------------------------------------------

func TestDiscoverHost_Success(t *testing.T) {
	bin := buildAuthEnvBinary(t, `{"env":{"DATABRICKS_HOST":"https://ok.example.com"}}`, 0)

	host, err := DiscoverHost(Config{Profile: "DEFAULT", CLIPath: bin})
	if err != nil {
		t.Fatalf("DiscoverHost: %v", err)
	}
	if host != "https://ok.example.com" {
		t.Errorf("host = %q", host)
	}
}

func TestDiscoverHost_MissingHost(t *testing.T) {
	bin := buildAuthEnvBinary(t, `{"env":{"SOMETHING_ELSE":"x"}}`, 0)

	if _, err := DiscoverHost(Config{Profile: "DEFAULT", CLIPath: bin}); err == nil {
		t.Fatal("expected error when DATABRICKS_HOST is absent")
	}
}

func TestDiscoverHost_CommandFails(t *testing.T) {
	bin := buildAuthEnvBinary(t, "", 1)

	if _, err := DiscoverHost(Config{Profile: "DEFAULT", CLIPath: bin}); err == nil {
		t.Fatal("expected error when the CLI exits non-zero")
	}
}

// --- GatewayURL --------------------------------------------------------------

// TestGatewayURL pins the join for every path the three launchers own, with and
// without a trailing slash on the host. Values must be byte-identical to the
// pre-#218 per-launcher ConstructGatewayURL implementations.
func TestGatewayURL(t *testing.T) {
	const (
		anthropic = "/ai-gateway/anthropic"     // claude + opencode
		openai    = "/ai-gateway/openai/v1"     // codex
		gemini    = "/ai-gateway/gemini/v1beta" // opencode's second upstream
	)
	tests := []struct {
		name string
		host string
		path string
		want string
	}{
		{"claude no trailing slash", "https://h.example.com", anthropic, "https://h.example.com/ai-gateway/anthropic"},
		{"claude trailing slash", "https://h.example.com/", anthropic, "https://h.example.com/ai-gateway/anthropic"},
		{"claude many trailing slashes", "https://h.example.com///", anthropic, "https://h.example.com/ai-gateway/anthropic"},
		{"codex no trailing slash", "https://h.example.com", openai, "https://h.example.com/ai-gateway/openai/v1"},
		{"codex trailing slash", "https://h.example.com/", openai, "https://h.example.com/ai-gateway/openai/v1"},
		{"opencode gemini no trailing slash", "https://h.example.com", gemini, "https://h.example.com/ai-gateway/gemini/v1beta"},
		{"opencode gemini trailing slash", "https://h.example.com/", gemini, "https://h.example.com/ai-gateway/gemini/v1beta"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := GatewayURL(tc.host, tc.path); got != tc.want {
				t.Errorf("GatewayURL(%q, %q) = %q, want %q", tc.host, tc.path, got, tc.want)
			}
		})
	}
}

// TestGatewayURL_MatchesLegacyImplementation pins GatewayURL against the exact
// expression the three launchers used before #218.
func TestGatewayURL_MatchesLegacyImplementation(t *testing.T) {
	legacy := func(host, path string) string { return strings.TrimRight(host, "/") + path }
	hosts := []string{"https://h.example.com", "https://h.example.com/", "https://h.example.com//", ""}
	paths := []string{"/ai-gateway/anthropic", "/ai-gateway/openai/v1", "/ai-gateway/gemini/v1beta"}
	for _, h := range hosts {
		for _, p := range paths {
			if got, want := GatewayURL(h, p), legacy(h, p); got != want {
				t.Errorf("GatewayURL(%q, %q) = %q, legacy = %q", h, p, got, want)
			}
		}
	}
}
