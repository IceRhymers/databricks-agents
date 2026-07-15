// Package lifecycle provides an HTTP handler wrapper that adds /shutdown and
// /health endpoints with idle-timeout support. Shared across databricks-claude,
// databricks-codex, and databricks-opencode.
package lifecycle

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
)

// Config holds the parameters for WrapWithLifecycle.
type Config struct {
	// Inner is the upstream handler to delegate non-lifecycle requests to.
	Inner http.Handler

	// RefcountPath is the path to the refcount file. When empty, /shutdown
	// always closes DoneCh (opencode behavior). When set, /shutdown
	// decrements the refcount and only closes DoneCh when the count reaches
	// zero and IsOwner is true (claude/codex behavior).
	RefcountPath string

	// IsOwner indicates whether this process owns the listener.
	IsOwner bool

	// PromoteCh, when non-nil, is closed by the caller to signal that this
	// process has taken over as the primary proxy owner (health-watcher
	// takeover). After promotion, /shutdown behaves as if IsOwner were true.
	PromoteCh <-chan struct{}

	// IdleTimeout is the duration after which the proxy shuts down if no
	// requests are received. Zero disables the idle timer.
	IdleTimeout time.Duration

	// APIKey, when non-empty, requires Bearer token auth on /shutdown.
	APIKey string

	// DoneCh is closed when shutdown is triggered (by /shutdown or idle timeout).
	DoneCh chan struct{}

	// LogPrefix is the string used in log messages (e.g. "databricks-claude").
	LogPrefix string
}

// shutdownResponse is the JSON body returned by POST /shutdown.
type shutdownResponse struct {
	Remaining int  `json:"remaining"`
	Exiting   bool `json:"exiting"`
}

// WrapWithLifecycle wraps the inner handler with:
//   - POST /shutdown: decrements refcount (if configured) and conditionally shuts down
//   - Activity tracking: resets the idle timer on every proxied request
//
// It returns the wrapped handler. DoneCh is closed when shutdown is triggered
// (either via /shutdown or idle timeout). The caller selects on DoneCh to
// begin cleanup.
func WrapWithLifecycle(cfg Config) http.Handler {
	var shutdownOnce sync.Once
	triggerShutdown := func() {
		shutdownOnce.Do(func() { close(cfg.DoneCh) })
	}

	// isOwner returns true if this process was the original owner or has been
	// promoted to owner via PromoteCh (health-watcher takeover).
	isOwner := func() bool {
		if cfg.IsOwner {
			return true
		}
		if cfg.PromoteCh == nil {
			return false
		}
		select {
		case <-cfg.PromoteCh:
			return true
		default:
			return false
		}
	}

	// Idle timer: fires once after IdleTimeout of inactivity.
	// Reset on every proxied request. time.AfterFunc is goroutine-safe.
	var idleTimer *time.Timer
	if cfg.IdleTimeout > 0 {
		idleTimer = time.AfterFunc(cfg.IdleTimeout, func() {
			log.Printf("%s: idle timeout (%s), shutting down", cfg.LogPrefix, cfg.IdleTimeout)
			triggerShutdown()
		})
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Enforce API key if configured (matches requireAPIKey in pkg/proxy).
		if cfg.APIKey != "" {
			if r.Header.Get("Authorization") != "Bearer "+cfg.APIKey {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		if cfg.RefcountPath == "" {
			// No refcount — always shut down (opencode behavior).
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(shutdownResponse{Remaining: 0, Exiting: true})
			if idleTimer != nil {
				idleTimer.Stop()
			}
			triggerShutdown()
			return
		}

		remaining, err := refcount.Release(cfg.RefcountPath)
		if err != nil {
			log.Printf("%s: shutdown refcount release error: %v", cfg.LogPrefix, err)
		}
		exiting := remaining == 0 && isOwner()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(shutdownResponse{Remaining: remaining, Exiting: exiting})
		if exiting {
			// Stop idle timer to avoid double-shutdown.
			if idleTimer != nil {
				idleTimer.Stop()
			}
			triggerShutdown()
		}
	})

	// All other routes: reset idle timer, then delegate to inner handler.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if idleTimer != nil {
			idleTimer.Reset(cfg.IdleTimeout)
		}
		cfg.Inner.ServeHTTP(w, r)
	})

	return mux
}
