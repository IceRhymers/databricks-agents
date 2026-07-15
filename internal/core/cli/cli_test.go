package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsExecutableFile_Executable(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "exec-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsExecutableFile(f.Name()) {
		t.Errorf("expected %q to be reported as executable", f.Name())
	}
}

func TestIsExecutableFile_NonExecutable(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "noexec-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsExecutableFile(f.Name()) {
		t.Errorf("expected %q to not be executable", f.Name())
	}
}

func TestIsExecutableFile_Missing(t *testing.T) {
	if IsExecutableFile("/nonexistent/path/to/binary") {
		t.Error("expected false for missing file")
	}
}

func TestIsExecutableFile_Directory(t *testing.T) {
	dir := t.TempDir()
	if IsExecutableFile(dir) {
		t.Errorf("expected false for directory %q", dir)
	}
}

func TestResolveDatabricksCLI_EmptyDefaultsToDatabricks(t *testing.T) {
	// When the binary isn't found, it returns the default name unchanged.
	result := ResolveDatabricksCLI("")
	if result == "" {
		t.Error("expected non-empty result for empty cmdName")
	}
	// Should either resolve to an absolute path or fall back to "databricks".
	if filepath.Base(result) != "databricks" && !filepath.IsAbs(result) {
		t.Errorf("unexpected result for empty cmdName: %q", result)
	}
}

func TestResolveDatabricksCLI_AbsolutePassthrough(t *testing.T) {
	abs := "/usr/local/bin/databricks"
	if got := ResolveDatabricksCLI(abs); got != abs {
		t.Errorf("expected absolute path returned unchanged, got %q", got)
	}
}

func TestResolveDatabricksCLI_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-databricks")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABRICKS_CLI", fake)
	if got := ResolveDatabricksCLI("databricks"); got != fake {
		t.Errorf("expected DATABRICKS_CLI override %q, got %q", fake, got)
	}
}

func TestResolveDatabricksCLI_EnvOverrideNotExecutable(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "not-exec")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABRICKS_CLI", fake)
	// Should NOT use the override since it's not executable; falls through to LookPath/fallback.
	got := ResolveDatabricksCLI("databricks")
	if got == fake {
		t.Errorf("should not use non-executable DATABRICKS_CLI override, but got %q", got)
	}
}

func TestResolveDatabricksCLI_FallbackDir(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "mybinary")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := FallbackCLIDirs
	FallbackCLIDirs = []string{dir}
	defer func() { FallbackCLIDirs = orig }()

	// Clear DATABRICKS_CLI so it falls through to fallback dirs.
	t.Setenv("DATABRICKS_CLI", "")

	got := ResolveDatabricksCLI("mybinary")
	if got != fake {
		t.Errorf("expected fallback dir resolution to %q, got %q", fake, got)
	}
}

// makeExec creates an executable file at path and returns the path.
func makeExec(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("makeExec: %v", err)
	}
	return path
}

// withMDMReader temporarily replaces the package-level mdmReader for a test.
func withMDMReader(t *testing.T, r MDMReader) {
	t.Helper()
	orig := mdmReader
	t.Cleanup(func() { mdmReader = orig })
	mdmReader = r
}

// isolatePATH points $PATH at an empty directory for the duration of the test.
//
// ResolveDatabricksCLI consults exec.LookPath (tier 4) before scanning
// FallbackCLIDirs (tier 5). A developer machine with a real `databricks` on
// PATH therefore resolves via tier 4 and never reaches the fallback tier the
// test is asserting on, while CI (no Databricks CLI installed) does. Any test
// that exercises the fallback tier with the real binary name must neutralize
// PATH first, or it passes in CI and fails locally.
func isolatePATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestResolveDatabricksCLI_MDM_ExecutablePath(t *testing.T) {
	dir := t.TempDir()
	mdmBin := makeExec(t, filepath.Join(dir, "databricks-mdm"))

	withMDMReader(t, func(_, _ string) (string, error) { return mdmBin, nil })
	t.Setenv("DATABRICKS_CLI", "") // ensure env tier doesn't fire

	got := ResolveDatabricksCLI("databricks")
	if got != mdmBin {
		t.Errorf("ResolveDatabricksCLI MDM executable: got %q, want %q", got, mdmBin)
	}
}

func TestResolveDatabricksCLI_MDM_NonExecutable_FallsThrough(t *testing.T) {
	dir := t.TempDir()
	// Non-executable file in MDM; an executable in fallback dir.
	nonExec := filepath.Join(dir, "databricks-notexec")
	if err := os.WriteFile(nonExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallbackBin := makeExec(t, filepath.Join(dir, "databricks"))

	withMDMReader(t, func(_, _ string) (string, error) { return nonExec, nil })
	t.Setenv("DATABRICKS_CLI", "")
	isolatePATH(t)

	orig := FallbackCLIDirs
	FallbackCLIDirs = []string{dir}
	defer func() { FallbackCLIDirs = orig }()

	got := ResolveDatabricksCLI("databricks")
	if got == nonExec {
		t.Errorf("ResolveDatabricksCLI should not return non-executable MDM path %q", nonExec)
	}
	if got != fallbackBin {
		t.Errorf("ResolveDatabricksCLI MDM non-executable: got %q, want fallback %q", got, fallbackBin)
	}
}

func TestResolveDatabricksCLI_MDM_Unset_NoRegression(t *testing.T) {
	dir := t.TempDir()
	fallbackBin := makeExec(t, filepath.Join(dir, "databricks"))

	withMDMReader(t, func(_, _ string) (string, error) { return "", nil })
	t.Setenv("DATABRICKS_CLI", "")
	isolatePATH(t)

	orig := FallbackCLIDirs
	FallbackCLIDirs = []string{dir}
	defer func() { FallbackCLIDirs = orig }()

	got := ResolveDatabricksCLI("databricks")
	if got != fallbackBin {
		t.Errorf("ResolveDatabricksCLI MDM unset: got %q, want fallback %q", got, fallbackBin)
	}
}

func TestResolveDatabricksCLI_AbsoluteBeforeMDM(t *testing.T) {
	// An absolute cmdName (state.DatabricksCLIPath) must be returned before
	// the MDM tier is consulted.
	dir := t.TempDir()
	stateBin := makeExec(t, filepath.Join(dir, "databricks-state"))
	mdmCalled := false
	withMDMReader(t, func(_, _ string) (string, error) {
		mdmCalled = true
		return filepath.Join(dir, "databricks-mdm"), nil
	})

	got := ResolveDatabricksCLI(stateBin)
	if got != stateBin {
		t.Errorf("absolute path should be returned unchanged, got %q", got)
	}
	if mdmCalled {
		t.Error("MDM reader must not be called when cmdName is absolute")
	}
}

func TestResolveDatabricksCLI_EnvBeforeMDM(t *testing.T) {
	dir := t.TempDir()
	envBin := makeExec(t, filepath.Join(dir, "databricks-env"))
	mdmCalled := false

	t.Setenv("DATABRICKS_CLI", envBin)
	withMDMReader(t, func(_, _ string) (string, error) {
		mdmCalled = true
		return filepath.Join(dir, "databricks-mdm"), nil
	})

	got := ResolveDatabricksCLI("databricks")
	if got != envBin {
		t.Errorf("$DATABRICKS_CLI should win over MDM, got %q want %q", got, envBin)
	}
	if mdmCalled {
		t.Error("MDM reader must not be called when $DATABRICKS_CLI is set and executable")
	}
}
