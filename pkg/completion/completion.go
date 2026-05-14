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

// SubcommandDef describes a subcommand for shell completion.
// Subcommand names are offered as completions at position 1 (before any flags).
//
// When Flags is non-empty, the generated script also offers those flags
// (and short aliases) when the cursor is INSIDE that subcommand. This lets
// `databricks-claude serve --<TAB>` complete serve-scoped flags.
//
// When Subcommands is non-empty, the generated script offers nested
// subcommand names at the next position (e.g. `databricks-claude serve <TAB>`
// → install/uninstall/status). Nested subcommands can themselves carry
// Flags + Subcommands recursively.
type SubcommandDef struct {
	Name        string          // subcommand name, e.g. "serve"
	Description string          // human-readable description shown in completions
	Flags       []FlagDef       // flags scoped to this subcommand (optional)
	Subcommands []SubcommandDef // nested subcommands (optional)
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
// completes subcommand names. When a SubcommandDef carries its own Flags or
// Subcommands, the script offers nested completion inside that subcommand
// (e.g. `serve <TAB>` → install/uninstall/status; `serve install --<TAB>` →
// install-scoped flags).
func GenerateBashFull(binaryName string, flags []FlagDef, subcommands []SubcommandDef) string {
	fn := bashFuncName(binaryName)
	var b strings.Builder

	// Named completer function bodies — emitted once at top level. Walk the
	// whole tree so nested subcommand flags' completers are declared too.
	for _, c := range uniqueCompletersTree(flags, subcommands) {
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

	// Walk the subcommand chain in COMP_WORDS to find the deepest match.
	// `chainStart` tracks the index of the current subcommand-name slot;
	// `flags`/`subcmds` track what's in scope for completion at the cursor.
	if len(subcommands) > 0 {
		b.WriteString(emitBashSubcommandDispatch(flags, subcommands))
	} else {
		b.WriteString(emitBashFlagBlock(flags, "    "))
	}

	b.WriteString("}\n\n")
	b.WriteString("complete -F " + fn + " " + binaryName + "\n")
	return b.String()
}

// emitBashSubcommandDispatch renders the depth-aware dispatch block that
// walks COMP_WORDS to find the active subcommand and offers flags +
// nested subcommands scoped to it.
func emitBashSubcommandDispatch(rootFlags []FlagDef, subcommands []SubcommandDef) string {
	var b strings.Builder
	b.WriteString("    # Walk COMP_WORDS to find the active subcommand chain. Each\n")
	b.WriteString("    # `case` block matches one level of the subcommand tree; flags\n")
	b.WriteString("    # and subcommand names are scoped to the deepest match.\n")
	b.WriteString("    local sub1=\"\" sub2=\"\"\n")
	b.WriteString("    for (( i=1; i < COMP_CWORD; i++ )); do\n")
	b.WriteString("        local w=\"${COMP_WORDS[i]}\"\n")
	b.WriteString("        if [[ \"$w\" == -* ]]; then continue; fi\n")
	b.WriteString("        if [[ -z \"$sub1\" ]]; then sub1=\"$w\"\n")
	b.WriteString("        elif [[ -z \"$sub2\" ]]; then sub2=\"$w\"\n")
	b.WriteString("        fi\n")
	b.WriteString("    done\n")
	b.WriteString("\n")
	b.WriteString("    case \"$sub1\" in\n")

	for _, sub := range subcommands {
		b.WriteString("        " + sub.Name + ")\n")
		if len(sub.Subcommands) > 0 {
			b.WriteString("            case \"$sub2\" in\n")
			for _, sub2 := range sub.Subcommands {
				b.WriteString("                " + sub2.Name + ")\n")
				b.WriteString(emitBashFlagBlock(sub2.Flags, "                    "))
				b.WriteString("                    return\n")
				b.WriteString("                    ;;\n")
			}
			b.WriteString("                \"\")\n")
			// Inside `serve` but no nested subcommand chosen yet — offer
			// nested subcommands when the cursor isn't on a flag.
			b.WriteString("                    if [[ \"$cur\" != -* ]]; then\n")
			b.WriteString("                        local subcmds2=\"" + subcommandNames(sub.Subcommands) + "\"\n")
			b.WriteString("                        COMPREPLY=($(compgen -W \"$subcmds2\" -- \"$cur\"))\n")
			b.WriteString("                        return\n")
			b.WriteString("                    fi\n")
			b.WriteString(emitBashFlagBlock(sub.Flags, "                    "))
			b.WriteString("                    return\n")
			b.WriteString("                    ;;\n")
			b.WriteString("            esac\n")
		} else {
			b.WriteString(emitBashFlagBlock(sub.Flags, "            "))
			b.WriteString("            return\n")
		}
		b.WriteString("            ;;\n")
	}

	b.WriteString("        \"\")\n")
	// Position 1: offer top-level subcommands or root flags.
	b.WriteString("            if [[ \"${COMP_CWORD}\" == \"1\" ]] && [[ \"$cur\" != -* ]]; then\n")
	b.WriteString("                local subcmds=\"" + subcommandNames(subcommands) + "\"\n")
	b.WriteString("                COMPREPLY=($(compgen -W \"$subcmds\" -- \"$cur\"))\n")
	b.WriteString("                return\n")
	b.WriteString("            fi\n")
	b.WriteString(emitBashFlagBlock(rootFlags, "            "))
	b.WriteString("            ;;\n")
	b.WriteString("    esac\n")
	return b.String()
}

// emitBashFlagBlock renders the standard "value completion for flags" + "flag
// name completion" block at the given indent. Used for both the root-level
// fallback and inside each subcommand case.
func emitBashFlagBlock(flags []FlagDef, indent string) string {
	var b strings.Builder
	b.WriteString(indent + "case \"$prev\" in\n")
	for _, f := range flags {
		if !f.TakesArg {
			continue
		}
		b.WriteString(indent + "    --" + f.Name + ")\n")
		switch f.Completer {
		case "":
			b.WriteString(indent + "        return\n")
		case "__files":
			b.WriteString(indent + "        COMPREPLY=($(compgen -f -- \"$cur\"))\n")
			b.WriteString(indent + "        return\n")
		default:
			b.WriteString(indent + "        COMPREPLY=($(compgen -W \"$(" + f.Completer + ")\" -- \"$cur\"))\n")
			b.WriteString(indent + "        return\n")
		}
		b.WriteString(indent + "        ;;\n")
	}
	b.WriteString(indent + "esac\n")
	b.WriteString(indent + "if [[ \"$cur\" == -* ]]; then\n")
	b.WriteString(indent + "    local flags_loc=\"" + allFlagNames(flags) + "\"\n")
	b.WriteString(indent + "    COMPREPLY=($(compgen -W \"$flags_loc\" -- \"$cur\"))\n")
	b.WriteString(indent + "fi\n")
	return b.String()
}

// GenerateZsh returns a zsh completion script for the given binary and flags.
// Equivalent to GenerateZshFull with no subcommands.
func GenerateZsh(binaryName string, flags []FlagDef) string {
	return GenerateZshFull(binaryName, flags, nil)
}

// GenerateZshFull returns a zsh completion script with optional subcommand support.
// When subcommands carry their own Flags or Subcommands, the script offers
// nested completion (e.g. `serve install --<TAB>` → install-scoped flags).
func GenerateZshFull(binaryName string, flags []FlagDef, subcommands []SubcommandDef) string {
	fn := "_" + strings.ReplaceAll(binaryName, "-", "_")
	var b strings.Builder

	b.WriteString("#compdef " + binaryName + "\n\n")

	// Named completer function bodies — emitted once at top level. Walk the
	// whole subcommand tree so nested flags' completers are declared too.
	for _, c := range uniqueCompletersTree(flags, subcommands) {
		b.WriteString(zshCompleterBody(binaryName, c))
		b.WriteString("\n")
	}

	b.WriteString(fn + "() {\n")

	// Build profile array if needed (any flag at any depth uses it).
	if hasCompleterTree(flags, subcommands, "__databricks_profiles") {
		b.WriteString("    local -a _profiles\n")
		b.WriteString("    _profiles=(${(f)\"$(__databricks_profiles)\"})\n\n")
	}

	if len(subcommands) > 0 {
		b.WriteString(emitZshSubcommandDispatch(flags, subcommands))
	} else {
		b.WriteString("    _arguments \\\n")
		for _, f := range flags {
			b.WriteString("        " + zshFlagSpec(f) + " \\\n")
		}
		b.WriteString("        '*::: :->passthrough'\n")
	}

	b.WriteString("}\n\n")
	b.WriteString(fn + " \"$@\"\n")
	return b.String()
}

// emitZshSubcommandDispatch emits the depth-aware zsh dispatch block. Walks
// $words to find the active subcommand chain, then offers flags + nested
// subcommand names scoped to that chain.
func emitZshSubcommandDispatch(rootFlags []FlagDef, subcommands []SubcommandDef) string {
	var b strings.Builder
	b.WriteString("    local -a _subcmds\n")
	for _, s := range subcommands {
		desc := strings.ReplaceAll(s.Description, "'", "\\'")
		b.WriteString("    _subcmds+=(" + fmt.Sprintf("'%s:%s'", s.Name, desc) + ")\n")
	}
	b.WriteString("\n")
	b.WriteString("    local sub1=\"\" sub2=\"\"\n")
	b.WriteString("    local i\n")
	b.WriteString("    for (( i=2; i < CURRENT; i++ )); do\n")
	b.WriteString("        local w=\"${words[i]}\"\n")
	b.WriteString("        if [[ \"$w\" == -* ]]; then continue; fi\n")
	b.WriteString("        if [[ -z \"$sub1\" ]]; then sub1=\"$w\"\n")
	b.WriteString("        elif [[ -z \"$sub2\" ]]; then sub2=\"$w\"\n")
	b.WriteString("        fi\n")
	b.WriteString("    done\n\n")

	b.WriteString("    case \"$sub1\" in\n")
	for _, sub := range subcommands {
		b.WriteString("        " + sub.Name + ")\n")
		if len(sub.Subcommands) > 0 {
			b.WriteString("            local -a _subcmds2\n")
			for _, sub2 := range sub.Subcommands {
				desc := strings.ReplaceAll(sub2.Description, "'", "\\'")
				b.WriteString("            _subcmds2+=(" + fmt.Sprintf("'%s:%s'", sub2.Name, desc) + ")\n")
			}
			b.WriteString("            case \"$sub2\" in\n")
			for _, sub2 := range sub.Subcommands {
				b.WriteString("                " + sub2.Name + ")\n")
				b.WriteString("                    _arguments \\\n")
				for _, f := range sub2.Flags {
					b.WriteString("                        " + zshFlagSpec(f) + " \\\n")
				}
				b.WriteString("                        '*::: :->passthrough'\n")
				b.WriteString("                    return\n")
				b.WriteString("                    ;;\n")
			}
			b.WriteString("                \"\")\n")
			b.WriteString("                    if [[ \"${words[CURRENT]}\" != -* ]]; then\n")
			b.WriteString("                        _describe 'subcommand' _subcmds2\n")
			b.WriteString("                        return\n")
			b.WriteString("                    fi\n")
			b.WriteString("                    _arguments \\\n")
			for _, f := range sub.Flags {
				b.WriteString("                        " + zshFlagSpec(f) + " \\\n")
			}
			b.WriteString("                        '*::: :->passthrough'\n")
			b.WriteString("                    return\n")
			b.WriteString("                    ;;\n")
			b.WriteString("            esac\n")
		} else {
			b.WriteString("            _arguments \\\n")
			for _, f := range sub.Flags {
				b.WriteString("                " + zshFlagSpec(f) + " \\\n")
			}
			b.WriteString("                '*::: :->passthrough'\n")
			b.WriteString("            return\n")
		}
		b.WriteString("            ;;\n")
	}
	b.WriteString("        \"\")\n")
	b.WriteString("            if (( CURRENT == 2 )) && [[ \"${words[2]}\" != -* ]]; then\n")
	b.WriteString("                _describe 'subcommand' _subcmds\n")
	b.WriteString("                return\n")
	b.WriteString("            fi\n")
	b.WriteString("            _arguments \\\n")
	for _, f := range rootFlags {
		b.WriteString("                " + zshFlagSpec(f) + " \\\n")
	}
	b.WriteString("                '*::: :->passthrough'\n")
	b.WriteString("            ;;\n")
	b.WriteString("    esac\n")
	return b.String()
}

// GenerateFish returns a fish completion script for the given binary and flags.
// Equivalent to GenerateFishFull with no subcommands.
func GenerateFish(binaryName string, flags []FlagDef) string {
	return GenerateFishFull(binaryName, flags, nil)
}

// GenerateFishFull returns a fish completion script with optional subcommand support.
// When subcommands carry Flags or Subcommands, fish's `__fish_seen_subcommand_from`
// guards scope nested completion so e.g. `serve install --<TAB>` only offers
// install-scoped flags.
func GenerateFishFull(binaryName string, flags []FlagDef, subcommands []SubcommandDef) string {
	var b strings.Builder

	b.WriteString("# Shell completions for " + binaryName + "\n")
	b.WriteString("# Generated by: " + binaryName + " completion fish\n\n")
	b.WriteString("# Disable default file completion.\n")
	b.WriteString("complete -c " + binaryName + " -f\n\n")

	// Named completer function bodies — one declaration even when nested
	// subcommands reuse the same completer.
	for _, c := range uniqueCompletersTree(flags, subcommands) {
		b.WriteString(fishCompleterBody(binaryName, c))
		b.WriteString("\n")
	}

	if len(subcommands) > 0 {
		b.WriteString("# Top-level subcommand completions.\n")
		for _, s := range subcommands {
			desc := strings.ReplaceAll(s.Description, "'", "\\'")
			b.WriteString(fmt.Sprintf("complete -c %s -n '__fish_use_subcommand' -a '%s' -d '%s'\n",
				binaryName, s.Name, desc))
		}
		b.WriteString("\n")

		// For each subcommand, offer its flags (scoped) and any nested
		// subcommands.
		for _, sub := range subcommands {
			b.WriteString(emitFishSubcommandBlock(binaryName, sub, nil))
		}
	}

	// Root flags — apply globally; fish doesn't easily restrict to "no
	// subcommand seen yet", so root flags appear under any subcommand. This
	// matches the historical behaviour of GenerateFishFull which emitted all
	// root flags unconditionally.
	for _, f := range flags {
		b.WriteString(fishFlagLine(binaryName, f) + "\n")
	}
	return b.String()
}

// emitFishSubcommandBlock renders flag completions scoped to one subcommand,
// then recurses into nested subcommands. The `parents` list is the chain of
// already-seen subcommand names, used to build `__fish_seen_subcommand_from`
// guards so e.g. `serve install --` only offers install-scoped flags.
func emitFishSubcommandBlock(binaryName string, sub SubcommandDef, parents []string) string {
	var b strings.Builder
	chain := append([]string{}, parents...)
	chain = append(chain, sub.Name)

	scope := fmt.Sprintf("__fish_seen_subcommand_from %s", strings.Join(chain, " "))
	for _, p := range parents {
		scope += fmt.Sprintf("; and not __fish_seen_subcommand_from %s_skip_marker_%s", binaryName, p)
		_ = p
	}
	// Simplification: fish's __fish_seen_subcommand_from matches if ANY of
	// the listed words appears, which is good enough for our shallow trees.
	scope = fmt.Sprintf("__fish_seen_subcommand_from %s", strings.Join(chain, " "))

	if len(sub.Subcommands) > 0 {
		b.WriteString(fmt.Sprintf("# Nested subcommands under %s.\n", strings.Join(chain, " ")))
		for _, sub2 := range sub.Subcommands {
			desc := strings.ReplaceAll(sub2.Description, "'", "\\'")
			b.WriteString(fmt.Sprintf("complete -c %s -n '%s' -a '%s' -d '%s'\n",
				binaryName, scope, sub2.Name, desc))
		}
		b.WriteString("\n")
	}

	if len(sub.Flags) > 0 {
		b.WriteString(fmt.Sprintf("# Flags for %s.\n", strings.Join(chain, " ")))
		for _, f := range sub.Flags {
			b.WriteString(fishFlagLineScoped(binaryName, f, scope) + "\n")
		}
		b.WriteString("\n")
	}

	for _, sub2 := range sub.Subcommands {
		b.WriteString(emitFishSubcommandBlock(binaryName, sub2, chain))
	}
	return b.String()
}

// fishFlagLineScoped is fishFlagLine with a -n '<condition>' guard so the
// flag only completes when the condition holds (used to scope subcommand-
// specific flags inside fish's completion engine).
func fishFlagLineScoped(binaryName string, f FlagDef, condition string) string {
	base := "complete -c " + binaryName + " -n '" + condition + "'"
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

// uniqueCompletersTree collects unique named completers across the root
// flags AND every (transitive) subcommand's flags. Single declaration of
// each completer body even when multiple subcommands reuse it.
func uniqueCompletersTree(rootFlags []FlagDef, subcommands []SubcommandDef) []string {
	seen := map[string]bool{}
	var out []string
	add := func(flags []FlagDef) {
		for _, f := range flags {
			if f.Completer != "" && f.Completer != "__files" && !seen[f.Completer] {
				seen[f.Completer] = true
				out = append(out, f.Completer)
			}
		}
	}
	add(rootFlags)
	var walk func([]SubcommandDef)
	walk = func(subs []SubcommandDef) {
		for _, s := range subs {
			add(s.Flags)
			walk(s.Subcommands)
		}
	}
	walk(subcommands)
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

// hasCompleterTree reports whether the named completer is referenced anywhere
// in the flag tree (root flags or any nested subcommand's flags). Used to
// decide whether to declare a shell-side helper variable like _profiles once.
func hasCompleterTree(rootFlags []FlagDef, subcommands []SubcommandDef, name string) bool {
	if hasCompleter(rootFlags, name) {
		return true
	}
	for _, s := range subcommands {
		if hasCompleterTree(s.Flags, s.Subcommands, name) {
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
