package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// seedProxyURL is the live proxy URL the regression seeds are configured for.
const seedProxyURL = "http://127.0.0.1:49156"

// seededOpencodeJSON returns an opencode.json body that ALREADY has both
// managed providers wired for baseURL (databricks-proxy at baseURL+"/v1",
// databricks-gemini-proxy at baseURL+"/v1beta") with the apiKey/npm values
// NeedsConfig checks, plus a user `//` comment and deliberately hand-rolled
// formatting (tabs + blank lines) that a naive re-marshal would destroy.
func seededOpencodeJSON(baseURL string) string {
	return `{
	// KEEP ME: user's hand-written note that a re-marshal would delete.
	"theme": "opencode-dark",

	"provider": {
		"databricks-proxy": {
			"npm": "@ai-sdk/anthropic",
			"name": "Databricks AI Gateway",
			"options": {
				"baseURL": "` + baseURL + `/v1",
				"apiKey": "databricks-proxy"
			}
		},
		"databricks-gemini-proxy": {
			"npm": "@ai-sdk/google",
			"name": "Databricks Gemini",
			"options": {
				"baseURL": "` + baseURL + `/v1beta",
				"apiKey": "databricks-proxy"
			}
		}
	}
}
`
}

// writeSeedConfig writes body to <xdgDir>/opencode/opencode.json and returns
// the path. The parent directory is created so opencodeConfigDir() (driven by
// XDG_CONFIG_HOME) resolves to it.
func writeSeedConfig(t *testing.T, xdgDir, body string) string {
	t.Helper()
	cfgDir := filepath.Join(xdgDir, "opencode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode config dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "opencode.json")
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	return cfgPath
}

// TestOpencodePatch_NeedsConfigGatePreservesComments is the comment-preservation
// regression proving the NeedsConfig gate fires inside opencodeSettingsPatcher.
// Patch. core.Run calls Patch unconditionally on every launch; without the gate
// each launch would re-marshal opencode.json and destroy user comments/formatting
// even when the config already points at the right proxy.
//
// Part 1: a config that ALREADY has both managed providers correctly wired must
// be left byte-for-byte untouched by Patch (gate short-circuits before the
// comment-destroying marshal).
//
// Part 2: mutating the databricks-proxy baseURL to a stale value must make Patch
// rewrite the file (gate correctly detects drift), self-healing base_url back to
// the live proxy and — as a documented side effect of the surgical re-marshal —
// dropping the JSONC comment.
func TestOpencodePatch_NeedsConfigGatePreservesComments(t *testing.T) {
	// --- Part 1: already-configured → Patch is a no-op (bytes unchanged) ---
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	seed := seededOpencodeJSON(seedProxyURL)
	cfgPath := writeSeedConfig(t, dir, seed)

	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	// forceModel=false, empty proxyAPIKey → apiKey placeholder "databricks-proxy"
	// (the value the seed carries), so NeedsConfig sees zero drift.
	p := newOpencodePatcher("databricks-claude-opus-4-7", false, "")
	if err := p.Patch(profile.PatchRequest{ProxyURL: seedProxyURL}); err != nil {
		t.Fatalf("Patch (already-configured): %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after no-op patch: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("NeedsConfig gate did not fire: opencode.json was rewritten despite being already configured\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if !strings.Contains(string(after), "KEEP ME") {
		t.Errorf("user comment destroyed by a patch that should have short-circuited:\n%s", after)
	}

	// --- Part 2: stale baseURL → Patch rewrites (gate detects drift) ---
	dir2 := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir2)

	staleSeed := seededOpencodeJSON("http://127.0.0.1:1") // dead proxy port
	stalePath := writeSeedConfig(t, dir2, staleSeed)

	staleBefore, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatalf("read stale seed: %v", err)
	}

	p2 := newOpencodePatcher("databricks-claude-opus-4-7", false, "")
	if err := p2.Patch(profile.PatchRequest{ProxyURL: seedProxyURL}); err != nil {
		t.Fatalf("Patch (stale): %v", err)
	}

	staleAfter, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatalf("read after drift patch: %v", err)
	}
	if bytes.Equal(staleBefore, staleAfter) {
		t.Errorf("NeedsConfig gate failed to detect drift: stale opencode.json was not rewritten:\n%s", staleAfter)
	}
	if !strings.Contains(string(staleAfter), seedProxyURL+"/v1") {
		t.Errorf("drift patch did not self-heal baseURL to the live proxy:\n%s", staleAfter)
	}
	if strings.Contains(string(staleAfter), "http://127.0.0.1:1/v1") {
		t.Errorf("stale baseURL survived the drift rewrite:\n%s", staleAfter)
	}
}

// TestOpencodeConfig_CrossPathByteIdentical is the byte-parity golden: the SAME
// newOpencodePatcher inputs must produce byte-identical opencode.json regardless
// of which entrypoint drives the write. Wrapper mode (buildOpencodeLaunchPlan →
// core.Run) and the serve sibling (runServeSession) both construct their patcher
// via newOpencodePatcher and both call the identical opencodeSettingsPatcher.Patch
// writer, so identical constructor args + identical ProxyURL must yield identical
// bytes. This mirrors codex's TestCodexConfig_CrossPathByteIdentical intent.
func TestOpencodeConfig_CrossPathByteIdentical(t *testing.T) {
	const model = "databricks-claude-opus-4-7"
	const proxyURL = seedProxyURL

	// writeVia patches a fresh (empty) opencode config dir and returns the bytes.
	writeVia := func(xdgDir string) []byte {
		t.Setenv("XDG_CONFIG_HOME", xdgDir)
		// modelExplicit=true so the top-level "model" key is written
		// deterministically rather than depending on preserve-if-present.
		p := newOpencodePatcher(model, true, "")
		if err := p.Patch(profile.PatchRequest{ProxyURL: proxyURL}); err != nil {
			t.Fatalf("Patch: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(xdgDir, "opencode", "opencode.json"))
		if err != nil {
			t.Fatalf("read patched config: %v", err)
		}
		return b
	}

	// Two independent fresh config dirs, identical inputs.
	a := writeVia(t.TempDir())
	b := writeVia(t.TempDir())

	if !bytes.Equal(a, b) {
		t.Errorf("cross-path opencode.json is not byte-identical for identical inputs\n--- A ---\n%s\n--- B ---\n%s", a, b)
	}
	// Sanity: both must carry both managed providers at the resolved baseURLs.
	for _, want := range []string{
		proxyURL + "/v1",
		proxyURL + "/v1beta",
		"databricks-proxy/" + model,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("patched opencode.json missing %q:\n%s", want, a)
		}
	}
}
