// Package completion generates shell tab-completion scripts for databricks-*
// proxy binaries. Scripts are produced from a []FlagDef slice so they stay in
// sync with the binary's actual flag set — adding a flag to the slice updates
// completions automatically.
package completion

import (
	"fmt"
	"os"
	"strings"
)

// FlagDef describes one CLI flag for completion generation.
type FlagDef struct {
	Name        string // flag name without "--", e.g. "profile"
	Short       string // single-char alias without "-", e.g. "v" (empty = none)
	Description string // human-readable description shown in completions
	TakesArg    bool   // true if the flag consumes the next token as its value
	Completer   string // named completer function (empty = no value completion)
	// Reserved completer names:
	//   "__databricks_profiles" — reads section headers from ~/.databrickscfg
	//   "__files"              — complete with local file paths
}

// SubcommandDef describes a top-level subcommand for shell completion.
// Subcommand names are offered as completions at position 1 (before any flags).
type SubcommandDef struct {
	Name        string // subcommand name, e.g. "serve"
	Description string // human-readable description shown in completions
}

// Run handles the positional "completion <shell>" subcommand.
// Call this as the very first thing in main(), before any flag parsing.
//
//	if len(os.Args) >= 2 && os.Args[1] == "completion" {
//	    completion.Run(os.Args[2:], flagDefs, "databricks-claude", subcommands...)
//	    os.Exit(0)
//	}
//
// subcommands is optional; pass it to enable subcommand-name completion at
// position 1 (e.g. "serve", "desktop", "setup").
func Run(args []string, flags []FlagDef, binaryName string, subcommands ...SubcommandDef) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s completion <bash|zsh|fish>\n", binaryName)
		os.Exit(1)
	}
	// Homebrew may call `completion --shell=bash`; accept both forms.
	shell := strings.TrimPrefix(args[0], "--shell=")
	switch shell {
	case "bash":
		fmt.Print(GenerateBashFull(binaryName, flags, subcommands))
	case "zsh":
		fmt.Print(GenerateZshFull(binaryName, flags, subcommands))
	case "fish":
		fmt.Print(GenerateFishFull(binaryName, flags, subcommands))
	default:
		fmt.Fprintf(os.Stderr, "%s completion: unknown shell %q (supported: bash zsh fish)\n", binaryName, shell)
		os.Exit(1)
	}
}

// GenerateBash returns a bash completion script for the given binary and flags.
// Equivalent to GenerateBashFull with no subcommands.
func GenerateBash(binaryName string, flags []FlagDef) string {
	return GenerateBashFull(binaryName, flags, nil)
}

// GenerateBashFull returns a bash completion script with optional subcommand support.
// When subcommands is non-empty, typing at position 1 without a "-" prefix
// completes subcommand names.
func GenerateBashFull(binaryName string, flags []FlagDef, subcommands []SubcommandDef) string {
	fn := bashFuncName(binaryName)
	var b strings.Builder

	// Named completer function bodies.
	for _, c := range uniqueCompleters(flags) {
		b.WriteString(bashCompleterBody(binaryName, c))
		b.WriteString("\n")
	}

	b.WriteString(fn + "() {\n")
	b.WriteString("    local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	b.WriteString("    local prev=\"${COMP_WORDS[COMP_CWORD-1]}\"\n")
	b.WriteString("\n")
	b.WriteString("    # After --, pass through to the wrapped tool.\n")
	b.WriteString("    local i\n")
	b.WriteString("    for (( i=1; i < COMP_CWORD; i++ )); do\n")
	b.WriteString("        if [[ \"${COMP_WORDS[i]}\" == \"--\" ]]; then return; fi\n")
	b.WriteString("    done\n")
	b.WriteString("\n")

	if len(subcommands) > 0 {
		b.WriteString("    # Subcommand completion at position 1.\n")
		b.WriteString("    if [[ \"${COMP_CWORD}\" == \"1\" ]] && [[ \"$cur\" != -* ]]; then\n")
		b.WriteString("        local subcmds=\"" + subcommandNames(subcommands) + "\"\n")
		b.WriteString("        COMPREPLY=($(compgen -W \"$subcmds\" -- \"$cur\"))\n")
		b.WriteString("        return\n")
		b.WriteString("    fi\n")
		b.WriteString("\n")
	}

	b.WriteString("    # Value completion for flags that take arguments.\n")
	b.WriteString("    case \"$prev\" in\n")
	for _, f := range flags {
		if !f.TakesArg {
			continue
		}
		b.WriteString("        --" + f.Name + ")\n")
		switch f.Completer {
		case "":
			b.WriteString("            return\n")
		case "__files":
			b.WriteString("            COMPREPLY=($(compgen -f -- \"$cur\"))\n")
			b.WriteString("            return\n")
		default:
			b.WriteString("            COMPREPLY=($(compgen -W \"$(" + f.Completer + ")\" -- \"$cur\"))\n")
			b.WriteString("            return\n")
		}
		b.WriteString("            ;;\n")
	}
	b.WriteString("    esac\n")
	b.WriteString("\n")
	b.WriteString("    # Flag name completion.\n")
	b.WriteString("    if [[ \"$cur\" == -* ]]; then\n")
	b.WriteString("        local flags=\"" + allFlagNames(flags) + "\"\n")
	b.WriteString("        COMPREPLY=($(compgen -W \"$flags\" -- \"$cur\"))\n")
	b.WriteString("    fi\n")
	b.WriteString("}\n\n")
	b.WriteString("complete -F " + fn + " " + binaryName + "\n")
	return b.String()
}

// GenerateZsh returns a zsh completion script for the given binary and flags.
// Equivalent to GenerateZshFull with no subcommands.
func GenerateZsh(binaryName string, flags []FlagDef) string {
	return GenerateZshFull(binaryName, flags, nil)
}

// GenerateZshFull returns a zsh completion script with optional subcommand support.
func GenerateZshFull(binaryName string, flags []FlagDef, subcommands []SubcommandDef) string {
	fn := "_" + strings.ReplaceAll(binaryName, "-", "_")
	var b strings.Builder

	b.WriteString("#compdef " + binaryName + "\n\n")

	// Named completer function bodies.
	for _, c := range uniqueCompleters(flags) {
		b.WriteString(zshCompleterBody(binaryName, c))
		b.WriteString("\n")
	}

	b.WriteString(fn + "() {\n")

	// Build profile array if needed.
	if hasCompleter(flags, "__databricks_profiles") {
		b.WriteString("    local -a _profiles\n")
		b.WriteString("    _profiles=(${(f)\"$(__databricks_profiles)\"})\n\n")
	}

	if len(subcommands) > 0 {
		b.WriteString("    local -a _subcmds\n")
		for _, s := range subcommands {
			desc := strings.ReplaceAll(s.Description, "'", "\\'")
			b.WriteString("    _subcmds+=(" + fmt.Sprintf("'%s:%s'", s.Name, desc) + ")\n")
		}
		b.WriteString("\n")
		b.WriteString("    if (( CURRENT == 2 )) && [[ \"${words[2]}\" != -* ]]; then\n")
		b.WriteString("        _describe 'subcommand' _subcmds\n")
		b.WriteString("        return\n")
		b.WriteString("    fi\n\n")
	}

	b.WriteString("    _arguments \\\n")
	for _, f := range flags {
		b.WriteString("        " + zshFlagSpec(f) + " \\\n")
	}
	b.WriteString("        '*::: :->passthrough'\n")
	b.WriteString("}\n\n")
	b.WriteString(fn + " \"$@\"\n")
	return b.String()
}

// GenerateFish returns a fish completion script for the given binary and flags.
// Equivalent to GenerateFishFull with no subcommands.
func GenerateFish(binaryName string, flags []FlagDef) string {
	return GenerateFishFull(binaryName, flags, nil)
}

// GenerateFishFull returns a fish completion script with optional subcommand support.
func GenerateFishFull(binaryName string, flags []FlagDef, subcommands []SubcommandDef) string {
	var b strings.Builder

	b.WriteString("# Shell completions for " + binaryName + "\n")
	b.WriteString("# Generated by: " + binaryName + " completion fish\n\n")
	b.WriteString("# Disable default file completion.\n")
	b.WriteString("complete -c " + binaryName + " -f\n\n")

	// Named completer function bodies.
	for _, c := range uniqueCompleters(flags) {
		b.WriteString(fishCompleterBody(binaryName, c))
		b.WriteString("\n")
	}

	if len(subcommands) > 0 {
		b.WriteString("# Subcommand completions.\n")
		for _, s := range subcommands {
			desc := strings.ReplaceAll(s.Description, "'", "\\'")
			b.WriteString(fmt.Sprintf("complete -c %s -n '__fish_use_subcommand' -a '%s' -d '%s'\n",
				binaryName, s.Name, desc))
		}
		b.WriteString("\n")
	}

	for _, f := range flags {
		b.WriteString(fishFlagLine(binaryName, f) + "\n")
	}
	return b.String()
}

// --- helpers ----------------------------------------------------------------

func bashFuncName(binaryName string) string {
	return "_" + strings.ReplaceAll(binaryName, "-", "_")
}

func allFlagNames(flags []FlagDef) string {
	var names []string
	for _, f := range flags {
		names = append(names, "--"+f.Name)
		if f.Short != "" {
			names = append(names, "-"+f.Short)
		}
	}
	return strings.Join(names, " ")
}

func uniqueCompleters(flags []FlagDef) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range flags {
		if f.Completer != "" && f.Completer != "__files" && !seen[f.Completer] {
			seen[f.Completer] = true
			out = append(out, f.Completer)
		}
	}
	return out
}

func hasCompleter(flags []FlagDef, name string) bool {
	for _, f := range flags {
		if f.Completer == name {
			return true
		}
	}
	return false
}

// bashCompleterBody returns the shell function definition for a named completer.
func bashCompleterBody(binaryName, completer string) string {
	switch completer {
	case "__databricks_profiles":
		return `__databricks_profiles() {
    [ -f ~/.databrickscfg ] && grep '^\[' ~/.databrickscfg | tr -d '[]'
}`
	default:
		return "# unknown completer: " + completer
	}
}

func zshCompleterBody(binaryName, completer string) string {
	switch completer {
	case "__databricks_profiles":
		return `__databricks_profiles() {
    [ -f ~/.databrickscfg ] && grep '^\[' ~/.databrickscfg | tr -d '[]'
}`
	default:
		return "# unknown completer: " + completer
	}
}

func fishCompleterBody(binaryName, completer string) string {
	switch completer {
	case "__databricks_profiles":
		return `function __databricks_profiles
    test -f ~/.databrickscfg; and grep '^\[' ~/.databrickscfg | tr -d '[]'
end`
	default:
		return "# unknown completer: " + completer
	}
}

// zshFlagSpec returns the _arguments spec string for one flag.
func zshFlagSpec(f FlagDef) string {
	desc := f.Description

	if f.Short != "" {
		// Pair short and long together.
		prefix := fmt.Sprintf("'(-%s --%s)'{-%s,--%s}", f.Short, f.Name, f.Short, f.Name)
		if f.TakesArg {
			return fmt.Sprintf("%s'[%s]:%s:'", prefix, desc, f.Name)
		}
		return fmt.Sprintf("%s'[%s]'", prefix, desc)
	}

	long := fmt.Sprintf("'--%s", f.Name)
	if f.TakesArg {
		switch f.Completer {
		case "__databricks_profiles":
			return fmt.Sprintf("%s[%s]:%s:($_profiles)'", long, desc, f.Name)
		case "__files":
			return fmt.Sprintf("%s[%s]:%s:_files'", long, desc, f.Name)
		default:
			return fmt.Sprintf("%s[%s]:%s:'", long, desc, f.Name)
		}
	}
	return fmt.Sprintf("%s[%s]'", long, desc)
}

// subcommandNames returns a space-separated string of subcommand names.
func subcommandNames(subcommands []SubcommandDef) string {
	var names []string
	for _, s := range subcommands {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}

// fishFlagLine returns the complete line for one flag.
func fishFlagLine(binaryName string, f FlagDef) string {
	base := "complete -c " + binaryName
	if f.Short != "" {
		base += " -s " + f.Short
	}
	base += " -l " + f.Name
	base += " -d '" + strings.ReplaceAll(f.Description, "'", "\\'") + "'"
	if f.TakesArg {
		base += " -r"
		switch f.Completer {
		case "__files":
			base += " -F"
		case "":
			// no value completion
		default:
			base += " -a '(" + f.Completer + ")'"
		}
	}
	return base
}
