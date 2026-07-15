//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// delegate spawns the sibling binary with inherited stdio and the parent's
// environment, forwards interrupt/termination signals, and propagates the
// child's exit code. Windows has no execve, so — unlike the unix path — this
// is a spawn-and-wait, matching the fidelity the sibling launchers already
// accept via internal/core/childproc. argv[0] is the resolved sibling path so
// the child sees the same argv[0] basename as a direct invocation.
func delegate(path string, args []string) int {
	cmd := exec.Command(path, args...)
	cmd.Args = append([]string{path}, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "databricks: exec %s: %v\n", path, err)
		return 1
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range sigCh {
			_ = cmd.Process.Signal(sig)
		}
	}()

	err := cmd.Wait()
	signal.Stop(sigCh)
	close(sigCh)

	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "databricks: %v\n", err)
	return 1
}
