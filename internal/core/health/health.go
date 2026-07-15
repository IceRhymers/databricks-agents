// Package health provides proxy health-check utilities shared across
// databricks-claude, databricks-codex, and databricks-opencode.
package health

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ProxyHealthy checks whether the proxy on the given port is responding.
// scheme should be "http" or "https". For https, TLS certificate verification
// is skipped (the proxy uses a self-signed cert on localhost).
func ProxyHealthy(port int, scheme string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if scheme == "https" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	resp, err := client.Get(fmt.Sprintf("%s://127.0.0.1:%d/health", scheme, port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ProxyMode probes the /health endpoint and reports the proxy's deployment mode.
// Returns "daemon" when the proxy is up and running as a long-lived daemon (serve
// subcommand), "ephemeral" when it is up but not in daemon mode, or "" with
// healthy=false when the endpoint is unreachable or returns non-200.
//
// Callers should short-circuit hook logic when mode == "daemon": the daemon
// manages its own lifecycle and neither refcount acquisition nor /shutdown POSTs
// are appropriate.
func ProxyMode(port int, scheme string) (mode string, healthy bool) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if scheme == "https" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	resp, err := client.Get(fmt.Sprintf("%s://127.0.0.1:%d/health", scheme, port))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var body struct {
		Daemon bool `json:"daemon"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// 200 but unparseable body — treat as ephemeral (not a daemon).
		return "ephemeral", true
	}
	if body.Daemon {
		return "daemon", true
	}
	return "ephemeral", true
}
