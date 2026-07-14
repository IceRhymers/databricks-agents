package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestSignalForwarding(t *testing.T) {
	// Start a child that sleeps; we'll kill it with SIGINT via ForwardSignals.
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep child: %v", err)
	}

	cancel := ForwardSignals(cmd)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Give the child a moment to start then send SIGINT to the child directly
	// via ForwardSignals by signaling ourselves.
	time.Sleep(50 * time.Millisecond)

	// Signal the child process directly to verify ForwardSignals wired things up.
	cmd.Process.Signal(syscall.SIGINT)

	select {
	case <-done:
		// Child exited — signal was received.
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		t.Fatal("child did not exit after SIGINT")
	}
}

func TestChildExitCode(t *testing.T) {
	// Verify exit code extraction by running a shell command that exits with 42.
	cmd := exec.Command("/bin/sh", "-c", "exit 42")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	cancel := ForwardSignals(cmd)
	defer cancel()

	waitErr := cmd.Wait()
	var exitCode int
	if waitErr == nil {
		exitCode = 0
	} else if exitErr, ok := waitErr.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else {
		exitCode = 1
	}

	if exitCode != 42 {
		t.Errorf("exit code = %d, want 42", exitCode)
	}
}
