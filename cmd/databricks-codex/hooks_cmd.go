package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
)

// runHooksCommand dispatches `databricks-codex hooks <subcommand> [flags]`.
// args is everything after the literal "hooks" token (e.g. ["install"] or
// ["session-start", "--port", "49154"]).
//
// Each subcommand is a thin wrapper around the existing hooks.go logic:
//   - install       → installHooks(~/.codex/hooks.json)
//   - uninstall     → uninstallHooks(~/.codex/hooks.json)
//   - session-start → headlessEnsure(resolved port)
//
// The dispatcher uses cmd.Command.Parse on the matching subcommand node so
// flag parsing stays consistent with the tree's declared semantics
// (TakesArg, Short aliases, --flag=value forms). The parity test in
// main_test.go walks rootCommand.Subcommand("hooks").Subcommands and
// confirms every child name has a case here — drift fails loudly.
func runHooksCommand(args []string) error {
	if len(args) == 0 {
		printHooksHelp()
		return fmt.Errorf("hooks: missing subcommand (expected: install, uninstall, session-start)")
	}

	sub := args[0]
	rest := args[1:]

	node := hooksCommand.Subcommand(sub)
	if node == nil {
		printHooksHelp()
		return fmt.Errorf("hooks: unknown subcommand %q (expected: install, uninstall, session-start)", sub)
	}

	parsed, err := node.Parse(rest)
	if err != nil {
		return fmt.Errorf("hooks %s: %w", sub, err)
	}
	if parsed.Bools["help"] {
		printSubHelp(*node)
		return nil
	}

	switch sub {
	case "install":
		return runHooksInstall()
	case "uninstall":
		return runHooksUninstall()
	case "session-start":
		return runHooksSessionStart(parsed.Strings["port"], parsed.Set["port"])
	default:
		// Unreachable — Subcommand() lookup above already guards this, but
		// the parity test still walks here so a forgotten case fails the
		// build rather than silently no-opping.
		return fmt.Errorf("hooks: unknown subcommand %q", sub)
	}
}

// runHooksInstall is a thin wrapper around installHooks that resolves
// ~/.codex/hooks.json from $HOME and prints the success message
// previously emitted by main.go's --install-hooks branch.
func runHooksInstall() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home dir: %w", err)
	}
	hp := filepath.Join(homeDir, ".codex", "hooks.json")
	if err := installHooks(hp); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	fmt.Fprintln(os.Stderr, "databricks-codex: hooks installed — SessionStart hook added to ~/.codex/hooks.json")
	return nil
}

// runHooksUninstall is a thin wrapper around uninstallHooks.
func runHooksUninstall() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home dir: %w", err)
	}
	hp := filepath.Join(homeDir, ".codex", "hooks.json")
	if err := uninstallHooks(hp); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	fmt.Fprintln(os.Stderr, "databricks-codex: hooks removed from ~/.codex/hooks.json")
	return nil
}

// runHooksSessionStart resolves the port and calls headlessEnsure.
// MUST remain fast/fail-fast — no interactive auth flow.
func runHooksSessionStart(portValue string, portSet bool) error {
	state := loadState()
	portFlag := 0
	if portSet && portValue != "" {
		n, err := strconv.Atoi(portValue)
		if err != nil {
			return fmt.Errorf("session-start: --port: %q is not an integer", portValue)
		}
		portFlag = n
	}
	port := resolvePort(portFlag, state)
	return headlessEnsure(port)
}

// printHooksHelp prints the top-level `hooks` help body.
func printHooksHelp() {
	fmt.Fprint(os.Stderr, hooksHelpTemplate)
}

// printSubHelp prints a subcommand's Long body when --help is passed.
// Falls back to programmatic rendering when Long is empty.
func printSubHelp(c cmd.Command) {
	if c.Long != "" {
		fmt.Fprint(os.Stderr, c.Long)
		return
	}
	_ = cmd.Render(os.Stderr, c, nil)
}
