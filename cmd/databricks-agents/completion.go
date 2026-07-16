package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runCompletion handles `databricks-agents completion <shell>`.
func runCompletion(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: databricks-agents completion <bash|zsh|fish>")
		return 1
	}
	shell := strings.TrimPrefix(args[0], "--shell=")
	switch shell {
	case "bash":
		fmt.Print(generateBash())
	case "zsh":
		fmt.Print(generateZsh())
	case "fish":
		fmt.Print(generateFish())
	default:
		fmt.Fprintf(os.Stderr, "databricks-agents completion: unknown shell %q (supported: bash zsh fish)\n", shell)
		return 1
	}
	return 0
}

// runSiblingCompletion returns the sibling binary's completion script for the
// given shell, and whether it was obtained. Declared as a package var so tests
// can inject deterministic sibling output without real binaries on disk.
var runSiblingCompletion = func(a agent, shell string) (string, bool) {
	path, err := resolveBinary(a.Binary)
	if err != nil {
		return "", false
	}
	out, err := exec.Command(path, "completion", shell).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// siblingFuncName is the completion function name a sibling defines for both
// bash and zsh (e.g. databricks-claude → _databricks_claude), deterministically
// derived by internal/core/completion's bashFuncName/zsh naming.
func siblingFuncName(binary string) string {
	return "_" + strings.ReplaceAll(binary, "-", "_")
}

// reservedWords are top-level multiplexer subcommands that must never be
// treated as agent names in completion.
const reservedWords = "list completion"

// generateBash composes the three sibling bash completion scripts (with their
// `complete -F` registration lines stripped) and a git-style `_databricks_agents`
// dispatch wrapper. When the cursor is past an agent word, the wrapper rewrites
// COMP_WORDS/COMP_CWORD to the sibling's frame and calls the sibling's
// `_databricks_<agent>` function, so the agent's own subtree drives completion.
// An agent whose sibling completion can't be obtained degrades to name-only.
func generateBash() string {
	var b strings.Builder
	var available []agent

	for _, a := range agents {
		body, ok := runSiblingCompletion(a, "bash")
		if !ok {
			continue
		}
		b.WriteString(stripBashRegistration(body))
		b.WriteString("\n")
		available = append(available, a)
	}

	b.WriteString("_databricks_agents() {\n")
	b.WriteString("    local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	b.WriteString("    if (( COMP_CWORD >= 2 )); then\n")
	b.WriteString("        case \"${COMP_WORDS[1]}\" in\n")
	for _, a := range available {
		fn := siblingFuncName(a.Binary)
		b.WriteString("            " + a.Name + ")\n")
		b.WriteString("                COMP_WORDS=(" + a.Binary + " \"${COMP_WORDS[@]:2}\")\n")
		b.WriteString("                (( COMP_CWORD-- ))\n")
		b.WriteString("                " + fn + "\n")
		b.WriteString("                return\n")
		b.WriteString("                ;;\n")
	}
	b.WriteString("            completion)\n")
	b.WriteString("                if (( COMP_CWORD == 2 )); then\n")
	b.WriteString("                    COMPREPLY=($(compgen -W \"bash zsh fish\" -- \"$cur\"))\n")
	b.WriteString("                fi\n")
	b.WriteString("                return\n")
	b.WriteString("                ;;\n")
	b.WriteString("        esac\n")
	b.WriteString("        return\n")
	b.WriteString("    fi\n")
	b.WriteString("    COMPREPLY=($(compgen -W \"" + agentNames() + " " + reservedWords + "\" -- \"$cur\"))\n")
	b.WriteString("}\n")
	b.WriteString("complete -F _databricks_agents databricks-agents\n")
	return b.String()
}

// stripBashRegistration removes the trailing `complete -F <fn> <binary>` line(s)
// so the sibling's function is defined but not registered against its own
// binary (we call it directly from the dispatch wrapper).
func stripBashRegistration(script string) string {
	lines := strings.Split(script, "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "complete -F ") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// generateZsh composes the sibling zsh completion functions (with `#compdef`
// headers and trailing invocation lines stripped) and a `_databricks_agents` dispatch
// wrapper that rewrites zsh's `words`/`CURRENT` to the sibling frame. Best
// effort — bash is the functionally-tested path; a degraded agent is name-only.
func generateZsh() string {
	var b strings.Builder
	b.WriteString("#compdef databricks-agents\n\n")
	var available []agent

	for _, a := range agents {
		body, ok := runSiblingCompletion(a, "zsh")
		if !ok {
			continue
		}
		b.WriteString(stripZshHeaderAndInvocation(body, siblingFuncName(a.Binary)))
		b.WriteString("\n")
		available = append(available, a)
	}

	b.WriteString("_databricks_agents() {\n")
	b.WriteString("    if (( CURRENT >= 3 )); then\n")
	b.WriteString("        case \"${words[2]}\" in\n")
	for _, a := range available {
		fn := siblingFuncName(a.Binary)
		b.WriteString("            " + a.Name + ")\n")
		b.WriteString("                words=(" + a.Binary + " \"${(@)words[3,-1]}\")\n")
		b.WriteString("                (( CURRENT-- ))\n")
		b.WriteString("                " + fn + "\n")
		b.WriteString("                return\n")
		b.WriteString("                ;;\n")
	}
	b.WriteString("            completion)\n")
	b.WriteString("                _values 'shell' bash zsh fish\n")
	b.WriteString("                return\n")
	b.WriteString("                ;;\n")
	b.WriteString("        esac\n")
	b.WriteString("        return\n")
	b.WriteString("    fi\n")
	b.WriteString("    local -a _top\n")
	b.WriteString("    _top=(" + agentNames() + " " + reservedWords + ")\n")
	b.WriteString("    _describe 'command' _top\n")
	b.WriteString("}\n\n")
	b.WriteString("_databricks_agents \"$@\"\n")
	return b.String()
}

// stripZshHeaderAndInvocation removes the leading `#compdef` directive and the
// trailing `<fn> "$@"` invocation from a sibling zsh script, leaving just the
// helper + completion function definitions for embedding.
func stripZshHeaderAndInvocation(script, fn string) string {
	lines := strings.Split(script, "\n")
	invocation := fn + ` "$@"`
	kept := lines[:0]
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "#compdef ") {
			continue
		}
		if trimmed == invocation {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// generateFish emits agent-name completion for the multiplexer. Fish's
// declarative, binary-keyed completion model makes clean subtree delegation to
// the siblings impractical without re-deriving each agent's flag tree, so
// subtree completion is a documented fallback here (bash/zsh carry it).
func generateFish() string {
	var b strings.Builder
	b.WriteString("# Shell completions for databricks-agents (multiplexer)\n")
	b.WriteString("# Generated by: databricks-agents completion fish\n\n")
	b.WriteString("complete -c databricks-agents -f\n\n")
	for _, a := range agents {
		desc := strings.ReplaceAll(a.Summary, "'", "\\'")
		b.WriteString(fmt.Sprintf("complete -c databricks-agents -n '__fish_use_subcommand' -a '%s' -d '%s'\n",
			a.Name, desc))
	}
	b.WriteString("complete -c databricks-agents -n '__fish_use_subcommand' -a 'list' -d 'List registered agents'\n")
	b.WriteString("complete -c databricks-agents -n '__fish_use_subcommand' -a 'completion' -d 'Emit shell completion'\n")
	return b.String()
}
