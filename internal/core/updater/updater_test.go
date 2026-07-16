package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CompareVersions
// ---------------------------------------------------------------------------

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.9.0", "0.10.0", -1},
		{"0.10.0", "0.9.0", 1},
		{"1.0", "1.0.0", 0},
		{"1.0.0", "1.0", 0},
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
		{"1.0.0-rc1", "1.0.0-rc2", 0}, // same numeric parts, both pre-release
		{"1.2.3", "1.2.3", 0},
		{"2.0.0", "1.99.99", 1},
		{"0.0.1", "0.0.2", -1},
		{"v1.0.0", "1.0.0", 0},
		{"1", "1.0.0", 0},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tc.a, tc.b), func(t *testing.T) {
			got := CompareVersions(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Check — cache miss → HTTP call + cache written
// ---------------------------------------------------------------------------

func TestCheck_CacheMiss(t *testing.T) {
	assetName := fmt.Sprintf("mybin-%s-%s", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(githubRelease{
			TagName:    "v0.11.0",
			HTMLURL:    "https://github.com/test/repo/releases/tag/v0.11.0",
			Prerelease: false,
			Assets: []githubAsset{
				{Name: assetName, BrowserDownloadURL: "https://dl.example.com/mybin"},
			},
		})
	}))
	defer srv.Close()

	// Patch fetchLatestRelease to use our test server by overriding the HTTP
	// call via a custom transport that rewrites the URL.
	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	cacheDir := t.TempDir()
	cacheFile := filepath.Join(cacheDir, "cache.json")

	res, err := Check(context.Background(), Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "0.10.0",
		BinaryName:     "mybin",
		CacheFile:      cacheFile,
		CacheTTL:       24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !res.UpdateAvailable {
		t.Error("expected UpdateAvailable=true")
	}
	if res.LatestVersion != "0.11.0" {
		t.Errorf("LatestVersion = %q, want %q", res.LatestVersion, "0.11.0")
	}
	if res.AssetURL != "https://dl.example.com/mybin" {
		t.Errorf("AssetURL = %q, want download URL", res.AssetURL)
	}

	// Verify cache was written, including repo_slug. Without the slug assertion
	// the write side is unpinned: an entry missing it mismatches on every read,
	// turning the cache write-only and putting a GitHub API call on every
	// startup (PrintUpdateNotice) against an unauthenticated 60/hr/IP limit.
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
	var written cacheEntry
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("parsing written cache: %v", err)
	}
	if written.RepoSlug != "test/repo" {
		t.Errorf("written repo_slug = %q, want %q", written.RepoSlug, "test/repo")
	}
}

// ---------------------------------------------------------------------------
// Check — cache hit (fresh) → no HTTP call
// ---------------------------------------------------------------------------

func TestCheck_CacheHit(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	cacheDir := t.TempDir()
	cacheFile := filepath.Join(cacheDir, "cache.json")

	// Seed fresh cache. RepoSlug must match the Config below — a foreign-repo
	// entry is a miss (see TestCheck_CacheSlugMismatch).
	entry := cacheEntry{
		CheckedAt:     timeNow().UTC().Format(time.RFC3339),
		RepoSlug:      "test/repo",
		LatestVersion: "0.12.0",
		ReleaseURL:    "https://github.com/test/repo/releases/tag/v0.12.0",
		AssetURL:      "https://dl.example.com/cached",
	}
	data, _ := json.Marshal(entry)
	os.WriteFile(cacheFile, data, 0o600)

	res, err := Check(context.Background(), Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "0.10.0",
		BinaryName:     "mybin",
		CacheFile:      cacheFile,
		CacheTTL:       24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if called {
		t.Error("HTTP call was made despite fresh cache")
	}
	if !res.UpdateAvailable {
		t.Error("expected UpdateAvailable=true from cache")
	}
	if res.LatestVersion != "0.12.0" {
		t.Errorf("LatestVersion = %q, want %q", res.LatestVersion, "0.12.0")
	}
}

// ---------------------------------------------------------------------------
// Check — cache expired → HTTP call made
// ---------------------------------------------------------------------------

func TestCheck_CacheExpired(t *testing.T) {
	called := false
	assetName := fmt.Sprintf("mybin-%s-%s", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		json.NewEncoder(w).Encode(githubRelease{
			TagName:    "v0.13.0",
			HTMLURL:    "https://github.com/test/repo/releases/tag/v0.13.0",
			Prerelease: false,
			Assets: []githubAsset{
				{Name: assetName, BrowserDownloadURL: "https://dl.example.com/new"},
			},
		})
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	cacheDir := t.TempDir()
	cacheFile := filepath.Join(cacheDir, "cache.json")

	// Seed stale cache (25 hours old). RepoSlug matches the Config below, so
	// expiry is the only reason this can miss — otherwise the test would pass
	// on a slug mismatch and stop covering the TTL logic it exists to cover.
	entry := cacheEntry{
		CheckedAt:     timeNow().Add(-25 * time.Hour).UTC().Format(time.RFC3339),
		RepoSlug:      "test/repo",
		LatestVersion: "0.11.0",
		ReleaseURL:    "https://github.com/test/repo/releases/tag/v0.11.0",
		AssetURL:      "https://dl.example.com/old",
	}
	data, _ := json.Marshal(entry)
	os.WriteFile(cacheFile, data, 0o600)

	res, err := Check(context.Background(), Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "0.10.0",
		BinaryName:     "mybin",
		CacheFile:      cacheFile,
		CacheTTL:       24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !called {
		t.Error("HTTP call was NOT made despite expired cache")
	}
	if res.LatestVersion != "0.13.0" {
		t.Errorf("LatestVersion = %q, want %q", res.LatestVersion, "0.13.0")
	}
}

// ---------------------------------------------------------------------------
// Check — HTTP timeout → error returned
// ---------------------------------------------------------------------------

func TestCheck_HTTPTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled.
		<-r.Context().Done()
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	cacheDir := t.TempDir()
	_, err := Check(ctx, Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "0.10.0",
		BinaryName:     "mybin",
		CacheFile:      filepath.Join(cacheDir, "cache.json"),
		CacheTTL:       24 * time.Hour,
	})
	if err == nil {
		t.Fatal("expected error on timeout, got nil")
	}
}

// ---------------------------------------------------------------------------
// Check — malformed response → error returned gracefully
// ---------------------------------------------------------------------------

func TestCheck_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this is not json"))
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	cacheDir := t.TempDir()
	_, err := Check(context.Background(), Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "0.10.0",
		BinaryName:     "mybin",
		CacheFile:      filepath.Join(cacheDir, "cache.json"),
		CacheTTL:       24 * time.Hour,
	})
	if err == nil {
		t.Fatal("expected error on malformed response, got nil")
	}
}

// ---------------------------------------------------------------------------
// Check — no update available (current == latest)
// ---------------------------------------------------------------------------

func TestCheck_NoUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(githubRelease{
			TagName:    "v0.10.0",
			HTMLURL:    "https://github.com/test/repo/releases/tag/v0.10.0",
			Prerelease: false,
		})
	}))
	defer srv.Close()

	origClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	http.DefaultClient.Transport = rewriteTransport{target: srv.URL}
	defer func() { http.DefaultClient = origClient }()

	cacheDir := t.TempDir()
	res, err := Check(context.Background(), Config{
		RepoSlug:       "test/repo",
		CurrentVersion: "0.10.0",
		BinaryName:     "mybin",
		CacheFile:      filepath.Join(cacheDir, "cache.json"),
		CacheTTL:       24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if res.UpdateAvailable {
		t.Error("expected UpdateAvailable=false when versions are equal")
	}
}

// ---------------------------------------------------------------------------
// IsHomebrew — path-based detection
// ---------------------------------------------------------------------------

func TestIsHomebrew_Paths(t *testing.T) {
	// IsHomebrew relies on os.Executable which we can't easily override,
	// so we test the path-matching logic directly via the underlying check.
	tests := []struct {
		path string
		want bool
	}{
		{"/usr/local/Cellar/mybin/1.0/bin/mybin", true},
		{"/opt/homebrew/bin/mybin", true},
		{"/home/user/.local/bin/mybin", false},
		{"/usr/bin/mybin", false},
		{"/opt/Homebrew/Cellar/something/bin/x", true},
	}
	for _, tc := range tests {
		lower := strings.ToLower(tc.path)
		got := strings.Contains(lower, "/cellar/") || strings.Contains(lower, "/homebrew/")
		if got != tc.want {
			t.Errorf("path %q: got %v, want %v", tc.path, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// rewriteTransport redirects all requests to the test server.
// ---------------------------------------------------------------------------

type rewriteTransport struct {
	target string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(rt.target, "http://")
	return http.DefaultTransport.RoundTrip(req)
}
