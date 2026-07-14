//go:build !darwin && !windows && !linux

package main

import (
	"fmt"
	"runtime"
)

func installDaemon(_ installOptions) error {
	return fmt.Errorf("serve install: unsupported platform %q; macOS, Linux, and Windows only", runtime.GOOS)
}

func uninstallDaemon() error {
	return fmt.Errorf("serve install: unsupported platform %q; macOS, Linux, and Windows only", runtime.GOOS)
}

func daemonStatus(_ int) (statusResult, error) {
	return statusResult{}, fmt.Errorf("serve install: unsupported platform %q; macOS, Linux, and Windows only", runtime.GOOS)
}

// diagnosticsTail is a no-op on non-tier-1 platforms (freebsd/openbsd/etc.).
// On unsupported platforms installDaemon already returns an error before
// runInstall ever reaches the post-install probe, so this function is in
// practice unreachable; the stub exists purely so the cross-platform install
// code (which references diagnosticsTail unconditionally) compiles.
func diagnosticsTail() (string, error) {
	return "", nil
}
