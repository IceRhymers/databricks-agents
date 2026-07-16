package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config holds the parameters for an update check.
type Config struct {
	RepoSlug       string        // e.g. "IceRhymers/databricks-claude"
	CurrentVersion string        // e.g. "0.10.1" (no "v" prefix)
	BinaryName     string        // e.g. "databricks-claude"
	CacheFile      string        // caller-provided full path
	CacheTTL       time.Duration // default 24h
}

// Result holds the outcome of an update check.
type Result struct {
	UpdateAvailable bool
	LatestVersion   string
	IsHomebrew      bool
	ReleaseURL      string
	AssetURL        string
}

// cacheEntry is the on-disk JSON schema for the cache file.
//
// RepoSlug records which repository the entry was fetched from. A cached answer
// is only valid for the repo that produced it, so Check treats a slug mismatch
// as a miss. Entries written before this field existed decode it as "" and are
// therefore refetched once — which is the point: #217 repointed codex's and
// opencode's RepoSlug away from their abandoned standalone repos, and without
// this the stale entry would keep recommending a downgrade onto dead code for
// up to CacheTTL after the upgrade.
type cacheEntry struct {
	CheckedAt     string `json:"checked_at"`
	RepoSlug      string `json:"repo_slug"`
	LatestVersion string `json:"latest_version"`
	ReleaseURL    string `json:"release_url"`
	AssetURL      string `json:"asset_url"`
}

// githubRelease is a subset of the GitHub release API response.
type githubRelease struct {
	TagName    string        `json:"tag_name"`
	HTMLURL    string        `json:"html_url"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

// githubAsset is a subset of a GitHub release asset.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Overridable for testing.
var timeNow = time.Now

// Check queries GitHub for the latest release and returns whether an update
// is available. Results are cached to CacheFile with CacheTTL freshness.
func Check(ctx context.Context, cfg Config) (Result, error) {
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 24 * time.Hour
	}

	// Try reading cache first. An entry from a different repo is a miss, not a hit.
	if entry, err := readCache(cfg.CacheFile); err == nil && entry.RepoSlug == cfg.RepoSlug {
		checkedAt, parseErr := time.Parse(time.RFC3339, entry.CheckedAt)
		if parseErr == nil && timeNow().Before(checkedAt.Add(cfg.CacheTTL)) {
			return buildResult(cfg.CurrentVersion, entry.LatestVersion, entry.ReleaseURL, entry.AssetURL), nil
		}
	}

	// Cache miss or expired — fetch from GitHub.
	rel, err := fetchLatestRelease(ctx, cfg.RepoSlug)
	if err != nil {
		return Result{}, fmt.Errorf("fetching latest release: %w", err)
	}

	if rel.Prerelease {
		return Result{}, nil
	}

	latestVersion := strings.TrimPrefix(rel.TagName, "v")
	assetURL := matchAsset(rel.Assets, cfg.BinaryName)

	// Write cache (best-effort; errors returned to caller).
	if err := writeCache(cfg.CacheFile, cacheEntry{
		CheckedAt:     timeNow().UTC().Format(time.RFC3339),
		RepoSlug:      cfg.RepoSlug,
		LatestVersion: latestVersion,
		ReleaseURL:    rel.HTMLURL,
		AssetURL:      assetURL,
	}); err != nil {
		return Result{}, fmt.Errorf("writing update cache: %w", err)
	}

	return buildResult(cfg.CurrentVersion, latestVersion, rel.HTMLURL, assetURL), nil
}

// buildResult constructs a Result, setting UpdateAvailable based on version comparison.
func buildResult(currentVersion, latestVersion, releaseURL, assetURL string) Result {
	return Result{
		UpdateAvailable: CompareVersions(currentVersion, latestVersion) == -1,
		LatestVersion:   latestVersion,
		IsHomebrew:      IsHomebrew(),
		ReleaseURL:      releaseURL,
		AssetURL:        assetURL,
	}
}

// IsHomebrew returns true if the running binary is managed by Homebrew.
func IsHomebrew() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	lower := strings.ToLower(resolved)
	return strings.Contains(lower, "/cellar/") || strings.Contains(lower, "/homebrew/")
}

// CompareVersions compares two semver-like version strings numerically.
// Returns -1 if a < b, 0 if a == b, +1 if a > b.
// Pre-release suffixes (anything after "-") make a version sort before
// the same version without a suffix.
func CompareVersions(a, b string) int {
	aParts, aPre := splitVersion(a)
	bParts, bPre := splitVersion(b)

	// Pad to equal length.
	for len(aParts) < len(bParts) {
		aParts = append(aParts, 0)
	}
	for len(bParts) < len(aParts) {
		bParts = append(bParts, 0)
	}

	for i := range aParts {
		if aParts[i] < bParts[i] {
			return -1
		}
		if aParts[i] > bParts[i] {
			return 1
		}
	}

	// Equal numeric parts — pre-release < release.
	if aPre && !bPre {
		return -1
	}
	if !aPre && bPre {
		return 1
	}
	return 0
}

// splitVersion parses "1.2.3-rc1" into ([1,2,3], true).
func splitVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(v, "v")
	pre := false
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		v = v[:idx]
		pre = true
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		nums = append(nums, n)
	}
	return nums, pre
}

// fetchLatestRelease calls the GitHub API for the latest release.
func fetchLatestRelease(ctx context.Context, slug string) (githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return githubRelease{}, fmt.Errorf("decoding response: %w", err)
	}
	return rel, nil
}

// matchAsset finds the asset URL matching the current OS/arch.
func matchAsset(assets []githubAsset, binaryName string) string {
	target := fmt.Sprintf("%s-%s-%s", binaryName, runtime.GOOS, runtime.GOARCH)
	for _, a := range assets {
		if a.Name == target {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// readCache reads and parses the cache file.
func readCache(path string) (cacheEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cacheEntry{}, fmt.Errorf("parsing cache: %w", err)
	}
	return entry, nil
}

// PrintUpdateNotice checks for a newer release and prints a one-line notice
// to stderr. The 2-second timeout ensures cold misses don't delay startup.
// Uses cfg.BinaryName for the log prefix and output message.
func PrintUpdateNotice(cfg Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := Check(ctx, cfg)
	if err != nil {
		log.Printf("%s: update check: %v", cfg.BinaryName, err)
		return
	}
	if !r.UpdateAvailable {
		return
	}
	if r.IsHomebrew {
		fmt.Fprintf(os.Stderr, "%s: update available (v%s). Run: brew upgrade %s\n", cfg.BinaryName, r.LatestVersion, cfg.BinaryName)
	} else {
		fmt.Fprintf(os.Stderr, "%s: update available (v%s). Run: %s update\n", cfg.BinaryName, r.LatestVersion, cfg.BinaryName)
	}
}

// RunUpdateCommand implements the shared `update` subcommand: a forced check that
// prints upgrade instructions to w and returns the process exit code.
func RunUpdateCommand(cfg Config, w io.Writer) int {
	if os.Getenv("DATABRICKS_NO_UPDATE_CHECK") == "1" {
		fmt.Fprintf(w, "%s: update check disabled via DATABRICKS_NO_UPDATE_CHECK\n", cfg.BinaryName)
		return 0
	}
	// TODO(#223): this is a no-op — Check reinterprets CacheTTL==0 as 24h, so
	// `update` reads a cached result rather than forcing a fresh check. Preserved
	// verbatim from the launchers; fixing it is a behavior change, not a consolidation.
	cfg.CacheTTL = 0
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := Check(ctx, cfg)
	if err != nil {
		fmt.Fprintf(w, "%s: update check failed: %v\n", cfg.BinaryName, err)
		return 1
	}
	return formatUpdate(cfg, r, w)
}

// formatUpdate renders a completed check. Pure: no I/O beyond w, no clock, no env.
// Uses cfg.BinaryName for the display name and the Homebrew formula name, mirroring
// PrintUpdateNotice.
func formatUpdate(cfg Config, r Result, w io.Writer) int {
	if !r.UpdateAvailable {
		fmt.Fprintf(w, "%s v%s is already the latest version\n", cfg.BinaryName, cfg.CurrentVersion)
		return 0
	}
	if r.IsHomebrew {
		fmt.Fprintf(w, "Update available: v%s. Run: brew upgrade %s\n", r.LatestVersion, cfg.BinaryName)
	} else {
		fmt.Fprintf(w, "Update available: v%s. Download from: %s\n", r.LatestVersion, r.ReleaseURL)
	}
	return 0
}

// writeCache writes the cache file atomically (temp + rename).
func writeCache(path string, entry cacheEntry) error {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".update-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
