package portbind

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Bind tries to bind the fixed port for the given tool.
// If the port is already in use by the same tool (health check matches),
// returns isOwner=false (caller should skip proxy startup).
// If port is busy but health check fails (different process), falls back to :0.
// Returns the listener and whether this caller is the proxy owner.
func Bind(toolName string, port int) (net.Listener, bool, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, true, nil // we own the port
	}

	// Port is busy — check if it's our proxy
	if isOurProxy(toolName, port) {
		return nil, false, nil // join existing proxy
	}

	// Collision with unrelated process — fall back to ephemeral
	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, false, fmt.Errorf("portbind: all ports unavailable: %w", err)
	}
	return ln, true, nil
}

// ListenerPort extracts the port from a net.Listener, falling back to the
// given fallback if the listener is nil (e.g., non-owner case).
func ListenerPort(ln net.Listener, fallback int) int {
	if ln == nil {
		return fallback
	}
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return fallback
}

// HealthResponse is the response from GET /health.
type HealthResponse struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

// isOurProxy checks if the port is owned by a proxy for the given tool.
func isOurProxy(toolName string, port int) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var hr HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		return false
	}
	return hr.Tool == toolName
}
