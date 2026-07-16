package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- resolveBinary unit tests -----------------------------------------------

func TestResolveBinary_PathFallback(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, binaryFileName("databricks-claude"))
	writeExecutable(t, stub)
	// os.Executable() (the test binary) has no co-located sibling, so
	// resolution must fall through to PATH.
	t.Setenv("PATH", dir)

	got, err := resolveBinary("databricks-claude")
	if err != nil {
		t.Fatalf("resolveBinary via PATH: %v", err)
	}
	if got != stub {
		// exec.LookPath may return the same path; compare basenames to be robust.
		if filepath.Base(got) != filepath.Base(stub) {
			t.Errorf("resolveBinary = %q, want %q", got, stub)
		}
	}
}

func TestResolveBinary_NotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir, nothing to find
	if _, err := resolveBinary("databricks-nonexistent"); err == nil {
		t.Fatal("resolveBinary: expected error for missing binary, got nil")
	}
}

// --- delegation fidelity (integration: build multiplexer + stub sibling) ----

func TestDelegate_Fidelity(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	if runtime.GOOS == "windows" {
		// syscall.Exec fidelity is unix-specific; the windows spawn path is
		// covered structurally by build. Skip the exec-replace assertion here.
		t.Skip("fidelity test targets the unix syscall.Exec path")
	}
	dir := t.TempDir()

	// Build the multiplexer (current package) into dir.
	mux := filepath.Join(dir, "databricks-agents")
	build(t, mux, ".")

	// Build a stub sibling that echoes argv[0]/args/env and exits with $DBX_EXIT.
	stubSrc := filepath.Join(dir, "stub.go")
	if err := os.WriteFile(stubSrc, []byte(stubProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	build(t, filepath.Join(dir, "databricks-claude"), stubSrc)

	cmd := exec.Command(mux, "claude", "foo", "--", "bar")
	cmd.Env = append(os.Environ(), "DBX_MARKER=hello", "DBX_EXIT=7")
	out, err := cmd.CombinedOutput()

	// Exit code 7 must propagate.
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running multiplexer: %v\noutput:\n%s", err, out)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7 (propagated from sibling)", code)
	}

	got := string(out)
	// argv[0] basename must be the resolved sibling name (byte-identity).
	if !strings.Contains(got, "ARGV0=databricks-claude") {
		t.Errorf("argv[0] not the sibling basename; output:\n%s", got)
	}
	// Remaining args forwarded verbatim, including the `--` passthrough.
	if !strings.Contains(got, "ARGS=[foo -- bar]") {
		t.Errorf("args not forwarded verbatim; output:\n%s", got)
	}
	// Parent environment reached the child.
	if !strings.Contains(got, "MARKER=hello") {
		t.Errorf("env not propagated; output:\n%s", got)
	}
}

// --- helpers ----------------------------------------------------------------

func build(t *testing.T, out, src string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, src)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", src, err, b)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	content := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		content = "@echo off\r\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

const stubProgram = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	fmt.Printf("ARGV0=%s\n", filepath.Base(os.Args[0]))
	fmt.Printf("ARGS=%v\n", os.Args[1:])
	fmt.Printf("MARKER=%s\n", os.Getenv("DBX_MARKER"))
	if c := os.Getenv("DBX_EXIT"); c != "" {
		n, _ := strconv.Atoi(c)
		os.Exit(n)
	}
}
`
