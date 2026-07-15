//go:build !windows

package core

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy"
	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// stubToken is a no-op proxy.TokenSource; Run never triggers a request against
// it in these tests (the child exits immediately), so the value is irrelevant.
type stubToken struct{}

func (stubToken) Token(ctx context.Context) (string, error) { return "test-token", nil }

// capturePatcher records the PatchRequest handed to it so tests can assert what
// core.Run assembled and forwarded.
type capturePatcher struct {
	calls int
	last  profile.PatchRequest
}

func (c *capturePatcher) Patch(r profile.PatchRequest) error {
	c.calls++
	c.last = r
	return nil
}
func (c *capturePatcher) Restore(profile.RestoreRequest) error { return nil }

// testProfile builds a minimal profile.Profile whose child is /bin/sh. Run only
// consults ChildBinary and PatchSettings; the daemon/hook seams are unused.
func testProfile(p *capturePatcher) profile.Profile {
	return profile.Profile{
		Name:          "databricks-test",
		ChildBinary:   "/bin/sh",
		PatchSettings: p,
	}
}

// basePlan returns a LaunchPlan with valid (never-contacted) upstreams and
// update checks disabled. Callers override Port/BuildEnv/ManagedEnvVar/ToolName.
func basePlan(t *testing.T, port int, buildEnv func(string) map[string]string) LaunchPlan {
	return LaunchPlan{
		InferenceUpstream: "http://127.0.0.1:9/",
		OTELUpstream:      "http://127.0.0.1:9/",
		TokenProvider:     stubToken{},
		Port:              port,
		ProfileName:       "DEFAULT",
		Version:           "test",
		ToolName:          "databricks-test",
		RefcountPrefix:    ".dbtest-" + strings.ReplaceAll(t.Name(), "/", "_") + "-sessions",
		NoUpdateCheck:     true,
		BuildEnv:          buildEnv,
	}
}

// exitArgs runs `sh -c 'exit N'`.
func exitArgs(code int) []string { return []string{"-c", fmt.Sprintf("exit %d", code)} }

// TestRun_ExitCodePropagation: Run returns the child's exit code.
func TestRun_ExitCodePropagation(t *testing.T) {
	p := &capturePatcher{}
	got := Run(testProfile(p), basePlan(t, 0, nil), exitArgs(7))
	if got != 7 {
		t.Errorf("Run exit code = %d, want 7", got)
	}
}

// TestRun_PatchReceivesBuildEnvOutputVerbatim: the map returned by BuildEnv is
// passed to Patch unmodified, and BuildEnv is called with the same proxyURL
// that Patch receives. This is the env-parity guard — the merge/hand-off must
// not drop or mutate keys.
func TestRun_PatchReceivesBuildEnvOutputVerbatim(t *testing.T) {
	p := &capturePatcher{}
	var gotProxyURL string
	buildEnv := func(proxyURL string) map[string]string {
		gotProxyURL = proxyURL
		return map[string]string{
			"KEY_A":             "value-a",
			"OTEL_ENDPOINT_URL": proxyURL + "/otel/v1/metrics",
		}
	}
	Run(testProfile(p), basePlan(t, 0, buildEnv), exitArgs(0))

	if p.calls != 1 {
		t.Fatalf("Patch called %d times, want 1", p.calls)
	}
	if gotProxyURL == "" {
		t.Fatal("BuildEnv was not called with a proxyURL")
	}
	if p.last.ProxyURL != gotProxyURL {
		t.Errorf("Patch.ProxyURL = %q, BuildEnv saw %q; must match", p.last.ProxyURL, gotProxyURL)
	}
	wantEnv := map[string]string{
		"KEY_A":             "value-a",
		"OTEL_ENDPOINT_URL": gotProxyURL + "/otel/v1/metrics",
	}
	if len(p.last.Env) != len(wantEnv) {
		t.Fatalf("Patch.Env = %v, want %v", p.last.Env, wantEnv)
	}
	for k, v := range wantEnv {
		if p.last.Env[k] != v {
			t.Errorf("Patch.Env[%q] = %q, want %q", k, p.last.Env[k], v)
		}
	}
}

// TestRun_BuildEnvGetsBoundPortNotRequested: when the requested port is taken by
// an unrelated process, Run binds an ephemeral fallback port and must call
// BuildEnv (and Patch) with the ACTUAL bound port — not the requested one. This
// is the single subtle regression the extraction could introduce.
func TestRun_BuildEnvGetsBoundPortNotRequested(t *testing.T) {
	// Occupy a port with a plain (non-proxy) listener so /health fails and
	// portbind falls back to an ephemeral port.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listener: %v", err)
	}
	defer occupied.Close()
	requestedPort := occupied.Addr().(*net.TCPAddr).Port

	p := &capturePatcher{}
	var seen string
	buildEnv := func(proxyURL string) map[string]string {
		seen = proxyURL
		return map[string]string{"URL": proxyURL}
	}
	Run(testProfile(p), basePlan(t, requestedPort, buildEnv), exitArgs(0))

	wantSuffix := fmt.Sprintf(":%d", requestedPort)
	if strings.HasSuffix(seen, wantSuffix) {
		t.Errorf("BuildEnv proxyURL %q used the REQUESTED port %d; expected the ephemeral fallback", seen, requestedPort)
	}
	if p.last.ProxyURL != seen {
		t.Errorf("Patch.ProxyURL %q != BuildEnv proxyURL %q", p.last.ProxyURL, seen)
	}
	// Sanity: the URL must carry some 127.0.0.1 port.
	if !strings.HasPrefix(seen, "http://127.0.0.1:") {
		t.Errorf("proxyURL = %q, want http://127.0.0.1:<port>", seen)
	}
}

// TestRun_NonOwnerUsesRequestedPortAndNoPanic: when another live proxy already
// owns the port (isOwner=false, listener nil), Run must use the requested port
// in proxyURL, take the watcher branch, and NOT panic closing a nil listener on
// teardown.
func TestRun_NonOwnerUsesRequestedPortAndNoPanic(t *testing.T) {
	// Stand up a matching proxy (same ToolName) serving /health so portbind's
	// isOurProxy check succeeds and Run joins as a non-owner.
	handler, err := proxy.NewServer(&proxy.Config{
		InferenceUpstream: "http://127.0.0.1:9/",
		OTELUpstream:      "http://127.0.0.1:9/",
		TokenSource:       stubToken{},
		ToolName:          "databricks-test",
		Version:           "test",
	})
	if err != nil {
		t.Fatalf("proxy.NewServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go http.Serve(ln, handler)
	ownerPort := ln.Addr().(*net.TCPAddr).Port

	p := &capturePatcher{}
	var seen string
	buildEnv := func(proxyURL string) map[string]string {
		seen = proxyURL
		return map[string]string{"URL": proxyURL}
	}
	got := Run(testProfile(p), basePlan(t, ownerPort, buildEnv), exitArgs(0))

	if got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d", ownerPort)
	if seen != wantURL {
		t.Errorf("non-owner proxyURL = %q, want %q (the joined owner's port)", seen, wantURL)
	}
}

// TestRun_NilBuildEnvYieldsEmptyEnv: a nil BuildEnv results in a non-nil empty
// env map handed to Patch (never nil).
func TestRun_NilBuildEnvYieldsEmptyEnv(t *testing.T) {
	p := &capturePatcher{}
	Run(testProfile(p), basePlan(t, 0, nil), exitArgs(0))
	if p.calls != 1 {
		t.Fatalf("Patch called %d times, want 1", p.calls)
	}
	if p.last.Env == nil {
		t.Error("Patch.Env is nil, want non-nil empty map")
	}
	if len(p.last.Env) != 0 {
		t.Errorf("Patch.Env = %v, want empty", p.last.Env)
	}
}

// TestRun_ManagedEnvVarReachesChild: the ManagedEnvVar marker is injected into
// the child's environment (and its absence is observable).
func TestRun_ManagedEnvVarReachesChild(t *testing.T) {
	check := []string{"-c", `[ "$DBTEST_MANAGED" = "1" ] && exit 0 || exit 3`}

	t.Run("present", func(t *testing.T) {
		p := &capturePatcher{}
		plan := basePlan(t, 0, nil)
		plan.ManagedEnvVar = "DBTEST_MANAGED=1"
		if got := Run(testProfile(p), plan, check); got != 0 {
			t.Errorf("exit = %d, want 0 (marker should be in child env)", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		p := &capturePatcher{}
		plan := basePlan(t, 0, nil) // ManagedEnvVar == ""
		if got := Run(testProfile(p), plan, check); got != 3 {
			t.Errorf("exit = %d, want 3 (marker should be absent)", got)
		}
	})
}

// TestRun_PatchForwardsPortFlagAndProfile: Run forwards the plan's PortFlag and
// ProfileName into the PatchRequest unchanged.
func TestRun_PatchForwardsPortFlagAndProfile(t *testing.T) {
	p := &capturePatcher{}
	plan := basePlan(t, 0, nil)
	plan.PortFlag = 49999
	plan.ProfileName = "myprofile"
	Run(testProfile(p), plan, exitArgs(0))
	if p.last.PortFlag != 49999 {
		t.Errorf("Patch.PortFlag = %d, want 49999", p.last.PortFlag)
	}
	if p.last.ProfileName != "myprofile" {
		t.Errorf("Patch.ProfileName = %q, want myprofile", p.last.ProfileName)
	}
}

// TestCore_NoClaudeCoupledImports guards the seam: the internal/core launch
// engine must not import Claude-only packages, so codex/opencode can reuse it.
func TestCore_NoClaudeCoupledImports(t *testing.T) {
	forbidden := []string{
		"pkg/modeldiscovery",
		"pkg/websearch",
		"pkg/mdmprofile",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		astFile, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range astFile.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.Contains(path, bad) {
					t.Errorf("%s imports Claude-coupled package %q — internal/core must stay tool-agnostic", f, path)
				}
			}
		}
	}
}
