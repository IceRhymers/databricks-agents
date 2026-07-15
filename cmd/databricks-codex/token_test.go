package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// buildHelperBinary compiles a small helper binary that prints a fixed JSON response
// and exits with a given code. Returns the path to the binary.
func buildHelperBinary(t *testing.T, jsonPayload string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()

	src := filepath.Join(dir, "main.go")
	bin := filepath.Join(dir, "helper")
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

	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	return bin
}

// buildSlowBinary compiles a binary that sleeps for a long time before responding.
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
	time.Sleep(30 * time.Second)
}
`
	if err := os.WriteFile(src, []byte(code), 0600); err != nil {
		t.Fatalf("write slow src: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build slow: %v\n%s", err, out)
	}
	return bin
}

func validTokenJSON(token, expiry string) string {
	return fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer","expiry":%q}`, token, expiry)
}

func futureExpiry() string {
	return time.Now().Add(60 * time.Minute).Format(time.RFC3339)
}

// TestTokenProvider_FreshToken: subprocess returns valid JSON -> token is cached.
func TestTokenProvider_FreshToken(t *testing.T) {
	bin := buildHelperBinary(t, validTokenJSON("tok-fresh", futureExpiry()), 0)
	tp := NewTokenProvider(bin, "")

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "tok-fresh" {
		t.Errorf("got token %q, want %q", tok, "tok-fresh")
	}
	if tp.CachedToken() != "tok-fresh" {
		t.Error("token not cached after fresh fetch")
	}
}

// TestTokenProvider_CacheHit: second call within expiry window skips subprocess.
func TestTokenProvider_CacheHit(t *testing.T) {
	bin := buildHelperBinary(t, validTokenJSON("tok-cached", futureExpiry()), 0)
	tp := NewTokenProvider(bin, "")

	if _, err := tp.Token(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if tok != "tok-cached" {
		t.Errorf("got %q, want cached token", tok)
	}
}

// TestTokenProvider_RefreshNearExpiry: token within 5 min of expiry triggers refresh.
func TestTokenProvider_RefreshNearExpiry(t *testing.T) {
	bin := buildHelperBinary(t, validTokenJSON("tok-refreshed", futureExpiry()), 0)
	tp := NewTokenProvider(bin, "")
	tp.SetCache("tok-old", time.Now().Add(3*time.Minute)) // within 5-minute buffer

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "tok-refreshed" {
		t.Errorf("expected refresh; got %q", tok)
	}
}

// TestTokenProvider_FallbackOnError: subprocess fails -> last cached token returned.
func TestTokenProvider_FallbackOnError(t *testing.T) {
	failBin := buildHelperBinary(t, "", 1)
	tp := NewTokenProvider(failBin, "")
	tp.SetCache("tok-last-good", time.Now().Add(-1*time.Minute))

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error with cached fallback: %v", err)
	}
	if tok != "tok-last-good" {
		t.Errorf("got %q, want last-good cached token", tok)
	}
}

// TestTokenProvider_NoCachedTokenError: first call fails with no cache -> returns error.
func TestTokenProvider_NoCachedTokenError(t *testing.T) {
	failBin := buildHelperBinary(t, "", 1)
	tp := NewTokenProvider(failBin, "")

	_, err := tp.Token(context.Background())
	if err == nil {
		t.Fatal("expected error on first-call failure with no cache, got nil")
	}
}

// TestTokenProvider_SubprocessTimeout: slow subprocess doesn't block forever.
func TestTokenProvider_SubprocessTimeout(t *testing.T) {
	slowBin := buildSlowBinary(t)
	tp := NewTokenProvider(slowBin, "")

	start := time.Now()
	_, err := tp.Token(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 15*time.Second {
		t.Errorf("token fetch took %v, expected timeout within 15s", elapsed)
	}
}

// TestParseTokenResponse_RFC3339: parses RFC3339 expiry correctly.
func TestParseTokenResponse_RFC3339(t *testing.T) {
	expiry := time.Now().Add(1 * time.Hour).UTC().Round(time.Second)
	payload := []byte(fmt.Sprintf(`{"access_token":"tok","token_type":"Bearer","expiry":%q}`,
		expiry.Format(time.RFC3339)))

	resp, err := parseTokenResponse(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.expiryTime().UTC().Round(time.Second)
	if !got.Equal(expiry) {
		t.Errorf("expiry: got %v, want %v", got, expiry)
	}
}

// TestParseTokenResponse_UnixTimestamp: parses Unix timestamp expiry.
func TestParseTokenResponse_UnixTimestamp(t *testing.T) {
	expiry := time.Now().Add(1 * time.Hour).UTC().Round(time.Second)
	unixStr := strconv.FormatInt(expiry.Unix(), 10)
	payload := []byte(fmt.Sprintf(`{"access_token":"tok","token_type":"Bearer","expiry":%q}`, unixStr))

	resp, err := parseTokenResponse(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.expiryTime().UTC().Round(time.Second)
	if !got.Equal(expiry) {
		t.Errorf("expiry: got %v, want %v", got, expiry)
	}
}

// TestParseTokenResponse_MissingExpiry: defaults to ~55-minute expiry.
func TestParseTokenResponse_MissingExpiry(t *testing.T) {
	payload := []byte(`{"access_token":"tok","token_type":"Bearer"}`)
	resp, err := parseTokenResponse(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.expiryTime()
	lower := time.Now().Add(54 * time.Minute)
	upper := time.Now().Add(56 * time.Minute)
	if got.Before(lower) || got.After(upper) {
		t.Errorf("default expiry %v not in [54m, 56m] from now", got)
	}
}

// TestParseTokenResponse_MalformedJSON: returns error on bad input.
func TestParseTokenResponse_MalformedJSON(t *testing.T) {
	_, err := parseTokenResponse([]byte("not json"))
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}

// TestParseTokenResponse_EmptyToken: returns error on empty access_token.
func TestParseTokenResponse_EmptyToken(t *testing.T) {
	_, err := parseTokenResponse([]byte(`{"access_token":"","token_type":"Bearer"}`))
	if err == nil {
		t.Fatal("expected error on empty access_token, got nil")
	}
}

// buildAuthEnvBinary builds a helper binary that prints the given JSON and exits with exitCode.
func buildAuthEnvBinary(t *testing.T, jsonPayload string, exitCode int) string {
	return buildHelperBinary(t, jsonPayload, exitCode)
}

// TestDiscoverHost_Success: mock command returns valid JSON -> host extracted.
func TestDiscoverHost_Success(t *testing.T) {
	payload := `{"env":{"DATABRICKS_HOST":"https://dbc-abc123.cloud.databricks.com"}}`
	bin := buildAuthEnvBinary(t, payload, 0)

	host, err := DiscoverHost(bin, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://dbc-abc123.cloud.databricks.com"
	if host != want {
		t.Errorf("got %q, want %q", host, want)
	}
}

// TestDiscoverHost_MissingHost: JSON missing DATABRICKS_HOST -> error.
func TestDiscoverHost_MissingHost(t *testing.T) {
	payload := `{"env":{"DATABRICKS_TOKEN":"some-token"}}`
	bin := buildAuthEnvBinary(t, payload, 0)

	_, err := DiscoverHost(bin, "")
	if err == nil {
		t.Fatal("expected error when DATABRICKS_HOST missing, got nil")
	}
}

// TestDiscoverHost_CommandFails: command exits non-zero -> error.
func TestDiscoverHost_CommandFails(t *testing.T) {
	bin := buildAuthEnvBinary(t, "", 1)

	_, err := DiscoverHost(bin, "")
	if err == nil {
		t.Fatal("expected error when command fails, got nil")
	}
}

// TestConstructGatewayURL: verifies the host-relative AI Gateway URL format.
func TestConstructGatewayURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{
			name: "plain host",
			host: "https://dbc-abc123.cloud.databricks.com",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/openai/v1",
		},
		{
			name: "host with trailing slash is trimmed",
			host: "https://dbc-abc123.cloud.databricks.com/",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/openai/v1",
		},
		{
			name: "host with multiple trailing slashes is trimmed",
			host: "https://dbc-abc123.cloud.databricks.com///",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/openai/v1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ConstructGatewayURL(tc.host)
			if got != tc.want {
				t.Errorf("ConstructGatewayURL(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}
