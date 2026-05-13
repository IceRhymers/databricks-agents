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
// Returning ("", nil) is required so the install path on those platforms
// doesn't break compilation — the per-OS dispatch already errors out at
// installDaemon before we'd ever call this, but the cross-OS install code
// references diagnosticsTail unconditionally.
func diagnosticsTail() (string, error) {
	return "", nil
}
