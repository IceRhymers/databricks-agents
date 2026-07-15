package main

import (
	"os/exec"

	"github.com/IceRhymers/databricks-agents/internal/core/childproc"
)

// ForwardSignals sets up SIGINT/SIGTERM forwarding from the parent to cmd's
// process. The returned cancel function stops the forwarding goroutine.
func ForwardSignals(cmd *exec.Cmd) (cancel func()) {
	return childproc.ForwardSignals(cmd)
}
