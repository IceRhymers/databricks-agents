package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/IceRhymers/databricks-agents/internal/cmd"
	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
)

// runConfigCommand implements the `databricks-opencode config ...` dispatcher.
// args is everything after the literal "config" token. Routes to the show
// runner today; future sub-issues may grow this tree. Bare `config` (no args)
// prints help and exits 2 — same convention used by the sibling launchers.
func runConfigCommand(args []string) {
	if len(args) == 0 {
		_ = cmd.Render(os.Stderr, configCommand, nil)
		os.Exit(2)
	}
	switch args[0] {
	case "show":
		runConfigShow(args[1:])
	case "--help", "-h", "help":
		_ = cmd.Render(os.Stdout, configCommand, nil)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "databricks-opencode: unknown config subcommand %q\n\n", args[0])
		_ = cmd.Render(os.Stderr, configCommand, nil)
		os.Exit(1)
	}
}

// runConfigShow implements `databricks-opencode config show` — the legacy
// --print-env flow lifted into a subcommand. Read-only diagnostic. Matches
// the legacy resolution chain (--profile flag > saved state > DEFAULT) and
// emits byte-identical output to the removed --print-env root flag.
func runConfigShow(args []string) {
	node := configCommand.Subcommand("show")
	r, _ := node.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, *node, nil)
		os.Exit(0)
	}

	profile := r.Strings["profile"]
	saved := loadState()
	if profile == "" {
		profile = saved.Profile
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	// Model resolution mirrors the launch path: saved state > default constant.
	// No flag override — `config show` is read-only and a one-shot diagnostic.
	model := saved.Model
	if model == "" {
		model = defaultModel()
	}

	// Auth check — same gate as the legacy --print-env path.
	if err := authcheck.EnsureAuthenticated(profile, ""); err != nil {
		log.Fatalf("databricks-opencode: auth failed: %v", err)
	}

	// Resolve port (no save). port is not consumed by handlePrintEnv (which
	// reads ANTHROPIC_BASE_URL from the constructed gatewayURL, not the proxy
	// URL) but the resolution mirrors --print-env's input set.
	portFlag := atoiOrZero(r.Strings["port"])
	if portFlag != 0 {
		_ = resolvePort(portFlag, saved)
	}

	host, err := DiscoverHost("", profile)
	if err != nil {
		log.Fatalf("databricks-opencode: failed to discover host: %v\nRun 'databricks auth login' first", err)
	}
	gatewayURL := ConstructGatewayURL(host)

	tp := NewTokenProvider("", profile)
	initialToken, err := tp.Token(context.Background())
	if err != nil {
		log.Fatalf("databricks-opencode: failed to fetch initial token: %v", err)
	}

	handlePrintEnv(host, gatewayURL, initialToken, profile, model)
}

// atoiOrZero parses s as a base-10 int. Returns 0 on parse failure so
// resolvePort can fall back to state/default — the same tolerance the
// hand-rolled --port parsing in parseArgs has.
func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
