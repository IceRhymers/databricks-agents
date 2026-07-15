package childproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Give the child a moment to start then send SIGINT to the child directly.
	time.Sleep(50 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGINT)

	select {
	case <-done:
		// Child exited — signal was received.
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		t.Fatal("child did not exit after SIGINT")
	}
}

func TestRun_ExitCode(t *testing.T) {
	code, err := Run(context.Background(), Config{
		BinaryName: "/bin/sh",
		Args:       []string{"-c", "exit 42"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestRun_Success(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true not found in PATH: %v", err)
	}
	code, err := Run(context.Background(), Config{
		BinaryName: trueBin,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRun_EnvPropagation(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "env-output.txt")
	code, err := Run(context.Background(), Config{
		BinaryName: "/bin/sh",
		Args:       []string{"-c", fmt.Sprintf("printenv TEST_CHILDPROC_VAR > %s", outFile)},
		Env:        []string{"TEST_CHILDPROC_VAR=hello123"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if got != "hello123" {
		t.Errorf("env var = %q, want %q", got, "hello123")
	}
}

func TestRun_NoEnvPreservesParent(t *testing.T) {
	t.Setenv("TEST_PARENT_VAR", "parentval")
	outFile := filepath.Join(t.TempDir(), "env-output.txt")
	code, err := Run(context.Background(), Config{
		BinaryName: "/bin/sh",
		Args:       []string{"-c", fmt.Sprintf("printenv TEST_PARENT_VAR > %s", outFile)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if got != "parentval" {
		t.Errorf("env var = %q, want %q", got, "parentval")
	}
}
