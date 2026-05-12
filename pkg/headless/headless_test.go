package headless

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/IceRhymers/databricks-claude/pkg/refcount"
)

func TestEnsure_ManagedSessionSkips(t *testing.T) {
	t.Setenv("DATABRICKS_CLAUDE_MANAGED", "1")
	// Port 99999 has nothing listening. Without the guard this would fatalf.
	if err := Ensure(Config{
		Port:          99999,
		ManagedEnvVar: "DATABRICKS_CLAUDE_MANAGED",
		LogPrefix:     "test",
	}); err != nil {
		t.Fatalf("expected no error for managed session skip, got: %v", err)
	}
}

func TestEnsure_AlreadyHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port

	// Should return immediately without trying to start a new proxy.
	if err := Ensure(Config{
		Port:      port,
		Scheme:    "http",
		LogPrefix: "test",
	}); err != nil {
		t.Fatalf("expected no error when proxy is already healthy, got: %v", err)
	}
}

// TestEnsure_BadBinaryReturnsError verifies that when the configured binary
// does not exist, Ensure returns an error instead of calling log.Fatalf.
func TestEnsure_BadBinaryReturnsError(t *testing.T) {
	// Use a port with nothing listening so health check fails and Ensure tries
	// to launch the binary.
	err := Ensure(Config{
		Port:       0, // port 0 will not have a healthy proxy
		Scheme:     "http",
		BinaryPath: "/nonexistent/path/to/binary-that-does-not-exist",
		LogPrefix:  "test",
	})
	if err == nil {
		t.Fatal("expected non-nil error when binary does not exist, got nil")
	}
}

// TestEnsure_DaemonModeNoOp verifies that when the proxy answers /health with
// daemon:true, Ensure returns nil immediately without acquiring the refcount.
func TestEnsure_DaemonModeNoOp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"daemon":true}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	rcPath := refcount.PathForPort(".databricks-claude-sessions-headlesstest", port)
	os.Remove(rcPath)
	t.Cleanup(func() { os.Remove(rcPath) })

	if err := Ensure(Config{
		Port:         port,
		Scheme:       "http",
		LogPrefix:    "test",
		RefcountPath: rcPath,
	}); err != nil {
		t.Fatalf("Ensure in daemon mode returned error: %v", err)
	}

	if _, err := os.Stat(rcPath); !os.IsNotExist(err) {
		t.Error("refcount file was created in daemon mode; Ensure must not touch refcount when daemon is running")
	}
}

// TestEnsure_EphemeralAcquiresRefcount verifies that when the proxy answers
// /health with daemon:false, Ensure follows the normal path and acquires the
// refcount before returning.
func TestEnsure_EphemeralAcquiresRefcount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"daemon":false}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	rcPath := refcount.PathForPort(".databricks-claude-sessions-headlesstest", port)
	os.Remove(rcPath)
	t.Cleanup(func() { os.Remove(rcPath) })

	if err := Ensure(Config{
		Port:         port,
		Scheme:       "http",
		LogPrefix:    "test",
		RefcountPath: rcPath,
	}); err != nil {
		t.Fatalf("Ensure in ephemeral mode returned error: %v", err)
	}

	if _, err := os.Stat(rcPath); os.IsNotExist(err) {
		t.Error("refcount file was not created in ephemeral mode; Ensure must acquire refcount for non-daemon proxy")
	}
}

func TestBuildArgs_NoTLS(t *testing.T) {
	args := buildArgs(Config{Port: 8080})
	expected := []string{"--headless", "--port=8080"}
	if !reflect.DeepEqual(args, expected) {
		t.Errorf("expected %v, got %v", expected, args)
	}
}

func TestBuildArgs_WithTLSCert(t *testing.T) {
	args := buildArgs(Config{Port: 8080, TLSCert: "/path/to/cert.pem"})
	expected := []string{"--headless", "--port=8080", "--tls-cert=/path/to/cert.pem"}
	if !reflect.DeepEqual(args, expected) {
		t.Errorf("expected %v, got %v", expected, args)
	}
}

func TestBuildArgs_WithTLSKey(t *testing.T) {
	args := buildArgs(Config{Port: 8080, TLSKey: "/path/to/key.pem"})
	expected := []string{"--headless", "--port=8080", "--tls-key=/path/to/key.pem"}
	if !reflect.DeepEqual(args, expected) {
		t.Errorf("expected %v, got %v", expected, args)
	}
}

func TestBuildArgs_WithTLSCertAndKey(t *testing.T) {
	args := buildArgs(Config{
		Port:    8443,
		TLSCert: "/cert.pem",
		TLSKey:  "/key.pem",
	})
	expected := []string{"--headless", "--port=8443", "--tls-cert=/cert.pem", "--tls-key=/key.pem"}
	if !reflect.DeepEqual(args, expected) {
		t.Errorf("expected %v, got %v", expected, args)
	}
}
