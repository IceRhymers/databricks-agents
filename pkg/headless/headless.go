// Package headless provides the headless-ensure logic shared across
// databricks-claude, databricks-codex, and databricks-opencode.
package headless

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/health"
	"github.com/IceRhymers/databricks-claude/pkg/refcount"
)

// Config holds the parameters for Ensure.
type Config struct {
	// Port is the proxy port to check/start.
	Port int

	// Scheme is "http" or "https" for health checks. Typically "http" for
	// the headless-ensure path since TLS is not yet negotiated.
	Scheme string

	// ManagedEnvVar is the environment variable that, when set to "1",
	// causes Ensure to skip (e.g. "DATABRICKS_CLAUDE_MANAGED").
	ManagedEnvVar string

	// BinaryPath is the path to the binary to launch in headless mode.
	// If empty, os.Executable() is used.
	BinaryPath string

	// LogPrefix is the string used in log messages (e.g. "databricks-claude").
	LogPrefix string

	// RefcountPath, when non-empty, causes Ensure to acquire/release the
	// refcount around the operation (claude behavior). When empty, refcount
	// is skipped (codex/opencode behavior).
	RefcountPath string

	// TLSCert is the path to the TLS certificate file.
	// When non-empty, --tls-cert is forwarded to the subprocess.
	TLSCert string

	// TLSKey is the path to the TLS key file.
	// When non-empty, --tls-key is forwarded to the subprocess.
	TLSKey string
}

// Ensure checks whether the proxy is healthy on the given port.
// If not, it starts a detached headless proxy and polls until ready (max 10s).
// Returns an error if the proxy cannot be started or does not become healthy.
func Ensure(cfg Config) error {
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	if cfg.ManagedEnvVar != "" && os.Getenv(cfg.ManagedEnvVar) == "1" {
		log.Printf("%s: --headless-ensure: skipped (managed session)", cfg.LogPrefix)
		return nil
	}

	// Short-circuit before touching refcount: when the serve daemon is running it
	// manages its own lifecycle — this hook must be a no-op to avoid phantom
	// refcount entries that would prevent the daemon from running indefinitely.
	if mode, _ := health.ProxyMode(cfg.Port, cfg.Scheme); mode == "daemon" {
		log.Printf("%s: --headless-ensure: managed by daemon, hook is no-op", cfg.LogPrefix)
		return nil
	}

	// Acquire refcount FIRST so every ensure/release pair is symmetric.
	if cfg.RefcountPath != "" {
		if err := refcount.Acquire(cfg.RefcountPath); err != nil {
			log.Printf("%s: --headless-ensure: refcount acquire warning: %v", cfg.LogPrefix, err)
		}
	}

	if health.ProxyHealthy(cfg.Port, cfg.Scheme) {
		return nil // already running, refcount incremented
	}

	self := cfg.BinaryPath
	if self == "" {
		var err error
		self, err = os.Executable()
		if err != nil {
			if cfg.RefcountPath != "" {
				refcount.Release(cfg.RefcountPath) // undo acquire on failure
			}
			return fmt.Errorf("%s: --headless-ensure: cannot find self: %v", cfg.LogPrefix, err)
		}
	}

	cmd := exec.Command(self, buildArgs(cfg)...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		if cfg.RefcountPath != "" {
			refcount.Release(cfg.RefcountPath) // undo acquire on failure
		}
		return fmt.Errorf("%s: --headless-ensure: failed to start proxy: %v", cfg.LogPrefix, err)
	}
	if err := cmd.Process.Release(); err != nil {
		log.Printf("%s: --headless-ensure: release warning: %v", cfg.LogPrefix, err)
	}

	// Poll until healthy or timeout.
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if health.ProxyHealthy(cfg.Port, cfg.Scheme) {
			return nil
		}
	}
	if cfg.RefcountPath != "" {
		refcount.Release(cfg.RefcountPath) // undo acquire on failure
	}
	return fmt.Errorf("%s: --headless-ensure: proxy did not become healthy within 10s", cfg.LogPrefix)
}

// buildArgs constructs the CLI arguments for the detached proxy subprocess.
func buildArgs(cfg Config) []string {
	args := []string{"--headless", fmt.Sprintf("--port=%d", cfg.Port)}
	if cfg.TLSCert != "" {
		args = append(args, "--tls-cert="+cfg.TLSCert)
	}
	if cfg.TLSKey != "" {
		args = append(args, "--tls-key="+cfg.TLSKey)
	}
	return args
}
