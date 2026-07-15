package childproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Config holds parameters for running a child process.
type Config struct {
	BinaryName string
	Args       []string
	Env        []string // Additional env vars appended to the parent environment.
}

// Run starts a child process with the supplied config and waits for it to
// exit, returning the exit code.
func Run(ctx context.Context, cfg Config) (int, error) {
	cmd := exec.CommandContext(ctx, cfg.BinaryName, cfg.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}

	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start %s: %w", cfg.BinaryName, err)
	}

	cancel := ForwardSignals(cmd)
	defer cancel()

	err := cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, fmt.Errorf("wait %s: %w", cfg.BinaryName, err)
}

// ForwardSignals sets up SIGINT/SIGTERM forwarding from the parent to cmd's
// process. The returned cancel function stops the forwarding goroutine.
func ForwardSignals(cmd *exec.Cmd) (cancel func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case sig := <-ch:
				if cmd.Process != nil {
					cmd.Process.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		close(done)
	}
}
