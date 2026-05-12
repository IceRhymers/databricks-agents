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
