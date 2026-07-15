package health

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyHealthy_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if !ProxyHealthy(port, "http") {
		t.Error("expected ProxyHealthy to return true for healthy server")
	}
}

func TestProxyHealthy_Unhealthy(t *testing.T) {
	// Use a port with nothing listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if ProxyHealthy(port, "http") {
		t.Error("expected ProxyHealthy to return false for closed port")
	}
}

func TestProxyHealthy_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if ProxyHealthy(port, "http") {
		t.Error("expected ProxyHealthy to return false for 503 response")
	}
}

func TestProxyHealthy_SchemeMismatch(t *testing.T) {
	// Plain-HTTP server should not be detected as healthy when scheme is "https".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if ProxyHealthy(port, "https") {
		t.Error("expected ProxyHealthy to return false for HTTP server checked with https scheme")
	}
}

func TestProxyMode_DaemonTrue(t *testing.T) {
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
	mode, healthy := ProxyMode(port, "http")
	if mode != "daemon" || !healthy {
		t.Errorf("ProxyMode = (%q, %v), want (\"daemon\", true)", mode, healthy)
	}
}

func TestProxyMode_DaemonFalse(t *testing.T) {
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
	mode, healthy := ProxyMode(port, "http")
	if mode != "ephemeral" || !healthy {
		t.Errorf("ProxyMode = (%q, %v), want (\"ephemeral\", true)", mode, healthy)
	}
}

func TestProxyMode_DaemonMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"tool":"databricks-claude","version":"1.0.0"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	mode, healthy := ProxyMode(port, "http")
	if mode != "ephemeral" || !healthy {
		t.Errorf("ProxyMode = (%q, %v), want (\"ephemeral\", true)", mode, healthy)
	}
}

func TestProxyMode_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "not json {{{")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	mode, healthy := ProxyMode(port, "http")
	if mode != "ephemeral" || !healthy {
		t.Errorf("ProxyMode = (%q, %v), want (\"ephemeral\", true)", mode, healthy)
	}
}

func TestProxyMode_Unreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	mode, healthy := ProxyMode(port, "http")
	if mode != "" || healthy {
		t.Errorf("ProxyMode = (%q, %v), want (\"\", false)", mode, healthy)
	}
}
