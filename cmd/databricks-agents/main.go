// Command databricks-agents is the ucode-style multiplexer over the per-tool
// wrapper launchers. `databricks-agents claude|codex|opencode [args]` dispatches
// to the matching databricks-<agent> binary and behaves identically to invoking
// that binary directly; `databricks-agents list` enumerates the registered
// agents and `databricks-agents completion <shell>` emits nested shell
// completion. The binary is deliberately NOT named `databricks` so it never
// shadows the Databricks CLI (which the wrappers shell out to) on PATH.
//
// Why exec-delegation rather than the issue's literal "argv[1] → registry →
// core.Run": each launcher's full subcommand surface (config/serve/hooks/
// completion/desktop/setup + the credential-helper argv[0] alias) lives in its
// own package main, which Go cannot import, and core.Run only executes the
// default wrapper-launch path. Locating and exec-ing the real sibling binary
// makes "behaves identically" definitional across the whole surface, while
// leaving the three launchers untouched. See the "Key Files (cmd/databricks-agents/)"
// section of the repo-root AGENTS.md.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// Version is overwritten at build time via -ldflags "-X main.Version=...",
// matching the three sibling launchers.
var Version = "dev"

// agent is one registered wrapper the multiplexer can dispatch to. It is the
// lightweight registration mechanism #203 chose: full profile.Profile values
// aren't constructible here (their SettingsPatcher/DaemonStrategy/HookInstaller
// impls live in each launcher's package main), so the multiplexer registers a
// name → sibling binary → summary triple and delegates by exec-ing the binary.
type agent struct {
	Name    string // subcommand as typed: "claude"
	Binary  string // sibling binary to exec: "databricks-claude"
	Summary string // one-line description for `list` / usage
}

// agents is the source of truth for dispatch, `list`, and completion. Kept
// local to cmd/databricks-agents (zero coupling); a future issue may promote it to an
// internal/agents package if the release matrix ever needs the same list.
var agents = []agent{
	{"claude", "databricks-claude", "Claude Code via Databricks OAuth"},
	{"codex", "databricks-codex", "OpenAI Codex CLI via Databricks OAuth"},
	{"opencode", "databricks-opencode", "OpenCode CLI via Databricks OAuth"},
}

// lookup returns the agent registered under name, or false.
func lookup(name string) (agent, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return agent{}, false
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the testable entry point: it returns the process exit code rather than
// calling os.Exit, except on the delegation path where (on unix) syscall.Exec
// replaces the process image and never returns.
func run(args []string) int {
	if len(args) == 0 {
		// A bare multiplexer has no launch analog (unlike the launchers, which
		// launch their child on bare invocation), so require a subcommand.
		fmt.Fprint(os.Stderr, usage())
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(usage())
		return 0
	case "--version", "version":
		fmt.Printf("databricks-agents %s\n", Version)
		return 0
	case "list":
		printList(os.Stdout)
		return 0
	case "completion":
		return runCompletion(args[1:])
	}

	// Otherwise argv[0] is an agent name → delegate to its sibling binary.
	a, ok := lookup(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "databricks-agents: unknown agent %q\n\nValid agents: %s\n",
			args[0], agentNames())
		return 2
	}

	path, err := resolveBinary(a.Binary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "databricks-agents: %v\n", err)
		return 1
	}
	// delegate replaces the process (unix) or spawns + forwards (windows).
	return delegate(path, args[1:])
}

// printList writes each agent's name and summary, tab-aligned.
func printList(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, a := range agents {
		fmt.Fprintf(tw, "%s\t%s\n", a.Name, a.Summary)
	}
	tw.Flush()
}

// agentNames returns the registered agent names, space-separated.
func agentNames() string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return strings.Join(names, " ")
}

// usage returns the top-level help text.
func usage() string {
	var b strings.Builder
	b.WriteString("databricks-agents — multiplexer for Databricks-OAuth AI coding CLIs\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  databricks-agents <agent> [args...]   run a wrapped agent (identical to databricks-<agent> ...)\n")
	b.WriteString("  databricks-agents list                list registered agents\n")
	b.WriteString("  databricks-agents completion <shell>  emit shell completion (bash|zsh|fish)\n")
	b.WriteString("  databricks-agents --version           print version\n\n")
	b.WriteString("Agents:\n")
	for _, a := range agents {
		fmt.Fprintf(&b, "  %-10s %s\n", a.Name, a.Summary)
	}
	return b.String()
}
