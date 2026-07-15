package portbind

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestBind_FreePort verifies that Bind on a free port returns isOwner=true.
func TestBind_FreePort(t *testing.T) {
	// Find a free port by briefly binding :0, reading the port, then closing.
	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := tmp.Addr().(*net.TCPAddr).Port
	tmp.Close()

	ln, isOwner, err := Bind("test-tool", port)
	if err != nil {
		t.Fatalf("Bind returned error: %v", err)
	}
	defer ln.Close()

	if !isOwner {
		t.Error("expected isOwner=true for free port")
	}
	gotPort := ln.Addr().(*net.TCPAddr).Port
	if gotPort != port {
		t.Errorf("got port %d, want %d", gotPort, port)
	}
}

// TestBind_SameTool verifies that when the port is occupied by the same tool's
// proxy (health check matches), Bind returns nil listener and isOwner=false.
func TestBind_SameTool(t *testing.T) {
	toolName := "databricks-claude"

	// Start a mock HTTP server that responds to /health with our tool name.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{
			Tool:    toolName,
			Version: "0.5.0",
			PID:     12345,
		})
	})
	srv := httptest.NewUnstartedServer(mux)
	// We need to start on a known port. Use a temporary listener to find one.
	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := tmp.Addr().(*net.TCPAddr).Port
	tmp.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("failed to listen on port %d: %v", port, err)
	}
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	gotLn, isOwner, err := Bind(toolName, port)
	if err != nil {
		t.Fatalf("Bind returned error: %v", err)
	}
	if gotLn != nil {
		gotLn.Close()
		t.Error("expected nil listener when joining existing proxy")
	}
	if isOwner {
		t.Error("expected isOwner=false when joining existing proxy")
	}
}

// TestBind_UnrelatedProcess verifies that when the port is occupied by an
// unrelated process (no HTTP health endpoint), Bind falls back to an
// ephemeral port and returns isOwner=true.
func TestBind_UnrelatedProcess(t *testing.T) {
	// Occupy a port with a raw TCP listener (no HTTP server).
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	ln, isOwner, err := Bind("databricks-claude", port)
	if err != nil {
		t.Fatalf("Bind returned error: %v", err)
	}
	defer ln.Close()

	if !isOwner {
		t.Error("expected isOwner=true for ephemeral fallback")
	}
	gotPort := ln.Addr().(*net.TCPAddr).Port
	if gotPort == port {
		t.Errorf("expected fallback to different port, got same port %d", gotPort)
	}
}

// TestBind_DifferentTool verifies that when the port is occupied by a
// different tool's proxy, Bind falls back to an ephemeral port.
func TestBind_DifferentTool(t *testing.T) {
	// Start a mock HTTP server responding as a different tool.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{
			Tool:    "other-tool",
			Version: "1.0.0",
			PID:     99999,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Extract port from test server URL.
	addr := srv.Listener.Addr().(*net.TCPAddr)
	port := addr.Port

	ln, isOwner, err := Bind("databricks-claude", port)
	if err != nil {
		t.Fatalf("Bind returned error: %v", err)
	}
	defer ln.Close()

	if !isOwner {
		t.Error("expected isOwner=true for ephemeral fallback")
	}
	_ = strings.Contains // suppress unused import if needed
	gotPort := ln.Addr().(*net.TCPAddr).Port
	if gotPort == port {
		t.Errorf("expected fallback to different port, got same port %d", gotPort)
	}
}
