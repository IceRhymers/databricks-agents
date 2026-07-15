package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
)

// runHooksCommand implements `databricks-opencode hooks ...`. args is
// everything after the literal "hooks" token. Dispatches install / uninstall
// / session-start. Bare `hooks` (no args) prints help and exits 2 — same
// convention as `config` with no action.
//
// Consolidates the 3 hooks-lifecycle root flags (--install-hooks,
// --uninstall-hooks, --headless-ensure) off the root command. The hook-install
// logic and refcount-free proxy lifecycle in hooks.go are unchanged
// behaviorally; this dispatcher is purely a surface reshape.
func runHooksCommand(args []string) {
	if len(args) == 0 {
		_ = cmd.Render(os.Stderr, hooksCommand, nil)
		os.Exit(2)
	}
	switch args[0] {
	case "install":
		runHooksInstall(args[1:])
	case "uninstall":
		runHooksUninstall(args[1:])
	case "session-start":
		runHooksSessionStart(args[1:])
	case "--help", "-h", "help":
		_ = cmd.Render(os.Stdout, hooksCommand, nil)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "databricks-opencode: unknown hooks subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, hooksCommand, nil)
		os.Exit(1)
	}
}

// runHooksInstall implements `databricks-opencode hooks install`. Persists
// profile/port to state when explicitly provided and writes the JS plugin into
// the opencode plugins directory. Idempotent — re-running overwrites the plugin
// file rather than duplicating it.
func runHooksInstall(args []string) {
	node := hooksCommand.Subcommand("install")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	// Persist explicit profile/port overrides so the plugin's session-start
	// invocation picks them up via the saved state file.
	if profile := r.Strings["profile"]; profile != "" {
		s := loadState()
		if s.Profile != profile {
			s.Profile = profile
			if err := saveState(s); err != nil {
				log.Printf("databricks-opencode: failed to save profile: %v", err)
			}
		}
	}
	if portStr := r.Strings["port"]; portStr != "" {
		if port := atoiOrZero(portStr); port > 0 {
			s := loadState()
			if s.Port != port {
				s.Port = port
				if err := saveState(s); err != nil {
					log.Printf("databricks-opencode: failed to save port: %v", err)
				}
			}
		}
	}

	if err := installHooks(); err != nil {
		log.Fatalf("databricks-opencode: hooks install: %v", err)
	}
	hookDir, _ := opencodeConfigDir()
	fmt.Fprintf(os.Stderr, "databricks-opencode: hooks installed — opencode plugin written to %s\n", filepath.Join(hookDir, "plugins", "databricks-proxy", "index.js"))
}

// runHooksUninstall implements `databricks-opencode hooks uninstall`. Tolerates
// "not installed" — uninstallHooks leaves opencode.json alone when the plugin
// entry is absent and no-ops on the missing plugin file.
func runHooksUninstall(args []string) {
	node := hooksCommand.Subcommand("uninstall")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	if err := uninstallHooks(); err != nil {
		log.Fatalf("databricks-opencode: hooks uninstall: %v", err)
	}
	fmt.Fprintln(os.Stderr, "databricks-opencode: hooks removed from opencode config")
}

// runHooksSessionStart implements `databricks-opencode hooks session-start`.
// Plugin-invoked internal — replaces the legacy --headless-ensure flag.
// Spawns a detached headless proxy if not already healthy on the resolved
// port. No refcount: OpenCode has no SessionEnd hook to release one, so the
// proxy relies on its idle timeout for shutdown.
func runHooksSessionStart(args []string) {
	node := hooksCommand.Subcommand("session-start")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	state := loadState()
	port := resolvePort(atoiOrZero(r.Strings["port"]), state)
	if err := headlessEnsure(port); err != nil {
		log.Fatalf("databricks-opencode: headless ensure failed: %v", err)
	}
}
