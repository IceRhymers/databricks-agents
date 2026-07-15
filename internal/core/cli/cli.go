// Package cli provides helpers for locating the Databricks CLI binary.
package cli

import (
	"os"
	"os/exec"
	"path/filepath"
)

// FallbackCLIDirs lists install locations to probe when the CLI binary is not
// on PATH. Order matters: most-likely first. GUI-launched subprocesses (e.g.
// Claude Desktop invoking the credential helper) inherit launchd's minimal
// PATH (/usr/bin:/bin:/usr/sbin:/sbin), which omits all of these.
var FallbackCLIDirs = []string{
	"/usr/local/bin",
	"/opt/homebrew/bin",
	"/opt/homebrew/sbin",
	".local/bin", // resolved against $HOME
	"go/bin",     // resolved against $HOME
	"bin",        // resolved against $HOME
}

// MDMReader is a function that reads a string value from MDM managed
// preferences. Takes a domain and key, returns the value or empty string.
type MDMReader func(domain, key string) (string, error)

// mdmReader is the package-level injection point. Default is a no-op so
// pkg/cli remains independently importable without a hard dep on
// pkg/mdmprofile. main.go wires the real mdmprofile.ReadKey at startup.
var mdmReader MDMReader = func(_, _ string) (string, error) { return "", nil }

// SetMDMReader replaces the package-level MDM reader. Call once at startup
// with mdmprofile.ReadKey; no-op if r is nil.
func SetMDMReader(r MDMReader) {
	if r != nil {
		mdmReader = r
	}
}

// mdmLogger is called when the MDM tier fires during CLI resolution.
// Default is a no-op; wire helperDebugLog via SetMDMLogger to surface
// resolution steps in the credential-helper log.
var mdmLogger func(format string, args ...any) = func(string, ...any) {}

// SetMDMLogger replaces the package-level MDM logger. No-op if logger is nil.
func SetMDMLogger(logger func(format string, args ...any)) {
	if logger != nil {
		mdmLogger = logger
	}
}

// ResolveDatabricksCLI returns an executable path for cmdName.
// Lookup order:
//  1. Absolute or path-qualified cmdName → returned unchanged (back-compat for tests).
//  2. $DATABRICKS_CLI env override, if set and executable.
//  3. MDM managed preference (com.icerhymers.databricks-claude / databricksCliPath),
//     if set and executable. Admin-pinned value beats accidental PATH discovery.
//  4. exec.LookPath(cmdName), which honors the inherited PATH.
//  5. A scan of common install dirs (FallbackCLIDirs).
//
// If none match, cmdName is returned unchanged so the eventual exec error
// surfaces with its original message.
func ResolveDatabricksCLI(cmdName string) string {
	if cmdName == "" {
		cmdName = "databricks"
	}
	if filepath.IsAbs(cmdName) || filepath.Base(cmdName) != cmdName {
		return cmdName
	}
	if override := os.Getenv("DATABRICKS_CLI"); override != "" {
		if IsExecutableFile(override) {
			return override
		}
	}
	// MDM tier — admin-pinned path from com.icerhymers.databricks-claude domain.
	if mdmPath, err := mdmReader("com.icerhymers.databricks-claude", "databricksCliPath"); err == nil && mdmPath != "" {
		if IsExecutableFile(mdmPath) {
			mdmLogger("MDM databricksCliPath=%q used", mdmPath)
			return mdmPath
		}
		mdmLogger("MDM databricksCliPath=%q not executable, falling through", mdmPath)
	}
	if p, err := exec.LookPath(cmdName); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, dir := range FallbackCLIDirs {
		if !filepath.IsAbs(dir) {
			if home == "" {
				continue
			}
			dir = filepath.Join(home, dir)
		}
		candidate := filepath.Join(dir, cmdName)
		if IsExecutableFile(candidate) {
			return candidate
		}
	}
	return cmdName
}

// IsExecutableFile reports whether path refers to an executable file.
func IsExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}
