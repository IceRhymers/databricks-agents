package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceRhymers/databricks-claude/pkg/refcount"
)

// TestHeadlessEnsure_ManagedSessionSkips verifies that headlessEnsure returns
// immediately when DATABRICKS_CLAUDE_MANAGED=1 is set, without attempting any
// network connection. If the guard were absent, calling with port 99999 would
// fatalf when the proxy cannot be started.
func TestHeadlessEnsure_ManagedSessionSkips(t *testing.T) {
	t.Setenv("DATABRICKS_CLAUDE_MANAGED", "1")
	// Port 99999 has nothing listening. Without the guard this would fatalf.
	headlessEnsure(99999)
}

// TestHeadlessRelease_ManagedSessionSkips verifies that headlessRelease returns
// immediately when DATABRICKS_CLAUDE_MANAGED=1 is set, without making any HTTP
// call. If the guard were absent, calling with port 99999 would log a connection
// refused error (not fatal, but observable). The guard prevents even that attempt.
func TestHeadlessRelease_ManagedSessionSkips(t *testing.T) {
	t.Setenv("DATABRICKS_CLAUDE_MANAGED", "1")
	// Port 99999 has nothing listening; the guard must prevent any HTTP attempt.
	headlessRelease(99999)
}

// TestHeadlessEnsure_AcquiresRefcount verifies that headlessEnsure increments
// the refcount file before checking proxy health. It starts a test HTTP server
// that answers GET /health with 200 so headlessEnsure returns after the health
// check without trying to spawn a new proxy.
func TestHeadlessEnsure_AcquiresRefcount(t *testing.T) {
	// Start a minimal health server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Extract the port from the test server address.
	addr := srv.Listener.Addr().String()
	var port int
	fmt.Sscanf(addr[len(addr)-len(addr)+len("127.0.0.1:"):], "%d", &port)
	// Use net package-free port extraction: scan from the last ':'.
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			fmt.Sscanf(addr[i+1:], "%d", &port)
			break
		}
	}

	// Ensure the refcount file doesn't exist before the call.
	rcPath := refcount.PathForPort(".databricks-claude-sessions", port)
	os.Remove(rcPath)
	t.Cleanup(func() { os.Remove(rcPath) })

	headlessEnsure(port)

	// Read and parse the refcount file.
	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("refcount file not found after headlessEnsure: %v", err)
	}
	type refcountFile struct {
		Count int `json:"count"`
	}
	var rc refcountFile
	if err := json.Unmarshal(data, &rc); err != nil {
		t.Fatalf("unmarshal refcount file: %v", err)
	}
	if rc.Count != 1 {
		t.Errorf("refcount = %d, want 1", rc.Count)
	}
}

// TestRefcountPathForPort verifies that the refcount file path is constructed
// correctly from the port number via the shared pkg/refcount function.
func TestRefcountPathForPort(t *testing.T) {
	want := filepath.Join(os.TempDir(), ".databricks-claude-sessions-12345")
	got := refcount.PathForPort(".databricks-claude-sessions", 12345)
	if got != want {
		t.Errorf("PathForPort(..., 12345) = %q, want %q", got, want)
	}
}

// TestHeadlessRelease_DaemonSkipsShutdown verifies that when the proxy is
// running in daemon mode, headlessRelease does not POST /shutdown.
func TestHeadlessRelease_DaemonSkipsShutdown(t *testing.T) {
	shutdownCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"daemon":true}`)
		case "/shutdown":
			shutdownCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	headlessRelease(port)

	if shutdownCalled {
		t.Error("/shutdown was called in daemon mode; headlessRelease must be a no-op when daemon is running")
	}
}

// TestHeadlessRelease_EphemeralPostsShutdown verifies that when the proxy is
// running in ephemeral mode, headlessRelease POSTs /shutdown as expected.
func TestHeadlessRelease_EphemeralPostsShutdown(t *testing.T) {
	shutdownCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"daemon":false}`)
		case "/shutdown":
			if r.Method == http.MethodPost {
				shutdownCalled = true
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	headlessRelease(port)

	if !shutdownCalled {
		t.Error("/shutdown was not called in ephemeral mode; headlessRelease must POST /shutdown for non-daemon proxy")
	}
}
