package main

import (
	"fmt"
	"log"
	"os"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
)

// runHooksCommand implements `databricks-claude hooks ...`. args is everything
// after the literal "hooks" token. Dispatches install / uninstall /
// session-start / session-end. Bare `hooks` (no args) prints help and exits 2
// — same convention as `desktop` / `config` with no action.
//
// Introduced in #173 to consolidate the 4 hooks-lifecycle root flags
// (--install-hooks, --uninstall-hooks, --headless-ensure, --headless-release)
// off the root command. The hook-install logic, first-run bootstrap, and
// refcount-managed proxy lifecycle in hooks.go are unchanged; this dispatcher
// is purely a surface reshape so the deployment mode is discoverable and the
// internal entrypoints stop polluting the user-facing root flag namespace.
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
	case "session-end":
		runHooksSessionEnd(args[1:])
	case "--help", "-h", "help":
		_ = cmd.Render(os.Stdout, hooksCommand, nil)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "databricks-claude: unknown hooks subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, hooksCommand, nil)
		os.Exit(1)
	}
}

// runHooksInstall implements `databricks-claude hooks install`. Lifts the
// pre-#173 --install-hooks block from main.go. Persists profile/port to
// state and writes the SessionStart/SessionEnd hook entries into
// ~/.claude/settings.json. Idempotent.
//
// The placeholder ANTHROPIC_BASE_URL=http://127.0.0.1:<port> is overwritten
// at session start by 'hooks session-start' with the discovered gateway URL.
func runHooksInstall(args []string) {
	node := hooksCommand.Subcommand("install")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	resolvedProfile := r.Strings["profile"]
	if resolvedProfile == "" {
		resolvedProfile = "DEFAULT"
	}
	portFlag := parseIntOrZero(r.Strings["port"])
	port := resolvePort(portFlag, loadState())
	placeholder := fmt.Sprintf("http://127.0.0.1:%d", port)

	if err := bootstrapSettings(portFlag, resolvedProfile, placeholder, nil); err != nil {
		log.Fatalf("databricks-claude: hooks install bootstrap: %v", err)
	}
	if err := ClaudeProfile().HookInstaller.Install(); err != nil {
		log.Fatalf("databricks-claude: hooks install: %v", err)
	}
	fmt.Fprintln(os.Stderr, "databricks-claude: hooks installed — SessionStart and SessionEnd hooks added to ~/.claude/settings.json")
}

// runHooksUninstall implements `databricks-claude hooks uninstall`. Lifts
// the pre-#173 --uninstall-hooks block from main.go. Tolerates "not
// installed" — uninstallHooks returns nil when settings.json does not exist
// or contains no hooks.
func runHooksUninstall(args []string) {
	node := hooksCommand.Subcommand("uninstall")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	if err := ClaudeProfile().HookInstaller.Uninstall(); err != nil {
		log.Fatalf("databricks-claude: hooks uninstall: %v", err)
	}
	fmt.Fprintln(os.Stderr, "databricks-claude: hooks removed from ~/.claude/settings.json")
}

// runHooksSessionStart implements `databricks-claude hooks session-start`.
// Hook-invoked internal — replaces the legacy --headless-ensure flag.
// Refcount-acquire + spawn detached proxy if not healthy.
func runHooksSessionStart(args []string) {
	node := hooksCommand.Subcommand("session-start")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	state := loadState()
	port := resolvePort(parseIntOrZero(r.Strings["port"]), state)
	headlessEnsure(port)
}

// runHooksSessionEnd implements `databricks-claude hooks session-end`.
// Hook-invoked internal — replaces the legacy --headless-release flag.
// POST /shutdown (refcount decrement); proxy exits when last session ends.
func runHooksSessionEnd(args []string) {
	node := hooksCommand.Subcommand("session-end")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	state := loadState()
	port := resolvePort(parseIntOrZero(r.Strings["port"]), state)
	headlessRelease(port)
}
