package main

import (
	"context"
	"os/exec"

	"github.com/IceRhymers/databricks-claude/pkg/childproc"
)

// RunChild starts claude as a child process with the supplied arguments and
// waits for it to exit, returning the exit code.
func RunChild(ctx context.Context, claudeArgs []string) (int, error) {
	return childproc.Run(ctx, childproc.Config{
		BinaryName: "claude",
		Args:       claudeArgs,
		Env:        []string{"DATABRICKS_CLAUDE_MANAGED=1"},
	})
}

// ForwardSignals sets up SIGINT/SIGTERM forwarding from the parent to cmd's
// process. The returned cancel function stops the forwarding goroutine.
func ForwardSignals(cmd *exec.Cmd) (cancel func()) {
	return childproc.ForwardSignals(cmd)
}
