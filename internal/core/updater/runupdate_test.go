package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// formatUpdate — pure; Result constructed directly, no server needed.
// Assertions are exact full-output equality (trailing newline included), since
// the literal-parity gate normalizes the newline representation away and can no
// longer catch a genuinely dropped "\n".
// ---------------------------------------------------------------------------

func TestFormatUpdate_NoUpdate(t *testing.T) {
	cfg := Config{BinaryName: "databricks-test", CurrentVersion: "1.0.0"}
	var buf bytes.Buffer

	code := formatUpdate(cfg, Result{UpdateAvailable: false}, &buf)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	want := "databricks-test v1.0.0 is already the latest version\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestFormatUpdate_Homebrew(t *testing.T) {
	cfg := Config{BinaryName: "databricks-test", CurrentVersion: "1.0.0"}
	var buf bytes.Buffer

	code := formatUpdate(cfg, Result{
		UpdateAvailable: true,
		LatestVersion:   "2.0.0",
		IsHomebrew:      true,
		ReleaseURL:      "https://github.com/test/repo/releases/tag/v2.0.0",
	}, &buf)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	want := "Update available: v2.0.0. Run: brew upgrade databricks-test\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestFormatUpdate_Download(t *testing.T) {
	cfg := Config{BinaryName: "databricks-test", CurrentVersion: "1.0.0"}
	var buf bytes.Buffer

	code := formatUpdate(cfg, Result{
		UpdateAvailable: true,
		LatestVersion:   "2.0.0",
		IsHomebrew:      false,
		ReleaseURL:      "https://github.com/test/repo/releases/tag/v2.0.0",
	}, &buf)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	want := "Update available: v2.0.0. Download from: https://github.com/test/repo/releases/tag/v2.0.0\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// RunUpdateCommand — integration; uses the rewriteTransport + http.DefaultClient
// swap pattern from updater_test.go.
// ---------------------------------------------------------------------------

// TestRunUpdateCommand_Disabled asserts the env kill-switch short-circuits before
// Check — a regression here would make it leak a network call.
func TestRunUpdateCommand_Disabled(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	t.Setenv("DATABRICKS_NO_UPDATE_CHECK", "1")

	var buf bytes.Buffer
	code := RunUpdateCommand(Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "1.0.0",
		BinaryName:     "databricks-test",
		CacheFile:      filepath.Join(t.TempDir(), "cache.json"),
		CacheTTL:       24 * time.Hour,
	}, &buf)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	want := "databricks-test: update check disabled via DATABRICKS_NO_UPDATE_CHECK\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("HTTP calls = %d, want 0 (kill-switch must short-circuit Check)", n)
	}
}

func TestRunUpdateCommand_CheckError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	var buf bytes.Buffer
	code := RunUpdateCommand(Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "1.0.0",
		BinaryName:     "databricks-test",
		CacheFile:      filepath.Join(t.TempDir(), "cache.json"),
		CacheTTL:       24 * time.Hour,
	}, &buf)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if got := buf.String(); !strings.HasPrefix(got, "databricks-test: update check failed:") {
		t.Errorf("output = %q, want prefix %q", got, "databricks-test: update check failed:")
	}
}

// TestRunUpdateCommand_SuccessPath drives a success path through the exported
// function the launchers actually call. Without this, the RunUpdateCommand ->
// formatUpdate wiring is unpinned: replacing the call with `return 0` — so
// `update` prints nothing at all — would leave the rest of the suite green.
func TestRunUpdateCommand_SuccessPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(githubRelease{
			TagName: "v2.0.0",
			HTMLURL: "https://github.com/test/repo/releases/tag/v2.0.0",
		})
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	var buf bytes.Buffer
	code := RunUpdateCommand(Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "1.0.0",
		BinaryName:     "databricks-test",
		CacheFile:      filepath.Join(t.TempDir(), "cache.json"),
		CacheTTL:       24 * time.Hour,
	}, &buf)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	// IsHomebrew is environment-dependent (it inspects os.Executable), so assert
	// on the branch-invariant prefix rather than pinning one of the two forms.
	if got := buf.String(); !strings.HasPrefix(got, "Update available: v2.0.0. ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("output = %q, want %q + a branch suffix + newline", got, "Update available: v2.0.0. ")
	}
}

// TestCheck_CacheSlugMismatch pins the #217 repoint actually taking effect for an
// existing user. A cache entry written by the old (abandoned) repo must not be
// served for the new one — otherwise `update` keeps recommending a downgrade onto
// dead code for up to CacheTTL after the upgrade.
func TestCheck_CacheSlugMismatch(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(githubRelease{
			TagName: "v1.2.0",
			HTMLURL: "https://github.com/IceRhymers/databricks-claude/releases/tag/v1.2.0",
		})
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	cacheFile := filepath.Join(t.TempDir(), "cache.json")

	// Seed a *fresh* entry from the abandoned standalone repo, exactly as a
	// pre-upgrade databricks-codex would have written it.
	data, _ := json.Marshal(cacheEntry{
		CheckedAt:     timeNow().UTC().Format(time.RFC3339),
		RepoSlug:      "IceRhymers/databricks-codex",
		LatestVersion: "2.0.0",
		ReleaseURL:    "https://github.com/IceRhymers/databricks-codex/releases/tag/v2.0.0",
	})
	os.WriteFile(cacheFile, data, 0o600)

	res, err := Check(context.Background(), Config{
		RepoSlug:       "IceRhymers/databricks-claude",
		CurrentVersion: "1.2.0",
		BinaryName:     "databricks-codex",
		CacheFile:      cacheFile,
		CacheTTL:       24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d, want 1 (a foreign-repo entry must be a miss)", calls.Load())
	}
	if res.LatestVersion != "1.2.0" {
		t.Errorf("LatestVersion = %q, want %q (served the abandoned repo's cached answer)", res.LatestVersion, "1.2.0")
	}
	if res.UpdateAvailable {
		t.Error("UpdateAvailable = true; the stale v2.0.0 downgrade recommendation survived")
	}
}

// TestCheck_CacheEntryPredatingRepoSlug covers the on-disk entries that exist in
// the wild today: written before the repo_slug field, they decode it as "" and
// must be refetched once rather than trusted.
func TestCheck_CacheEntryPredatingRepoSlug(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(githubRelease{TagName: "v1.2.0"})
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	cacheFile := filepath.Join(t.TempDir(), "cache.json")
	os.WriteFile(cacheFile, []byte(`{
  "checked_at": "`+timeNow().UTC().Format(time.RFC3339)+`",
  "latest_version": "2.0.0",
  "release_url": "https://github.com/IceRhymers/databricks-codex/releases/tag/v2.0.0",
  "asset_url": ""
}`), 0o600)

	res, err := Check(context.Background(), Config{
		RepoSlug:       "IceRhymers/databricks-claude",
		CurrentVersion: "1.2.0",
		BinaryName:     "databricks-codex",
		CacheFile:      cacheFile,
		CacheTTL:       24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d, want 1 (a field-less legacy entry must be a miss)", calls.Load())
	}
	if res.LatestVersion != "1.2.0" {
		t.Errorf("LatestVersion = %q, want %q", res.LatestVersion, "1.2.0")
	}
}
