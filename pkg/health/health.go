// Package health provides proxy health-check utilities shared across
// databricks-claude, databricks-codex, and databricks-opencode.
package health

import (
	"crypto/tls"
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
