package main

import (
	"os"
	"path/filepath"
)

// opencodeConfigDir returns the opencode config directory.
// opencode uses xdg-basedir on all platforms, which resolves to ~/.config/opencode
// (respecting $XDG_CONFIG_HOME if set).
func opencodeConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}
