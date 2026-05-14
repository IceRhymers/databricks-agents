// Package cmd is the in-repo command-tree registry: a single source of truth
// for the databricks-claude CLI surface (flag set, help text, shell
// completion). The tree is consumed by:
//
//   - parseArgs (main package) — derives the set of "--flag" names the
//     binary recognises; anything not in the tree is forwarded transparently
//     to the wrapped claude binary.
//   - handleHelp (main package) — renders help text from the tree, replacing
//     the giant hand-maintained printf that previously lived in main.go.
//   - pkg/completion — receives the tree's CompletionFlags / CompletionSubcommands
//     to emit bash/zsh/fish completion scripts.
//
// Boundary: this package MUST NOT import the root main package; the
// dependency arrow points one way (main → internal/cmd), so the tree can
// later be lifted to pkg/ without disentangling cycles.
//
// Status: #170 migrates the *root* command onto the tree. serve / setup /
// desktop continue to use hand-rolled flag.FlagSet parsers in their own
// files; their migration lives in a follow-up issue. The Persistent slice
// already holds --profile / --port so subcommand inheritance can be wired
// up when those migrations land.
package cmd

import (
	"io"
	"strings"

	"github.com/IceRhymers/databricks-claude/pkg/completion"
)

// FlagDef describes one CLI flag. Superset of pkg/completion.FlagDef:
// carries everything the shell-completion generator needs (Name, Short,
// Description, TakesArg, Completer) plus resolution-chain metadata
// (StateKey, EnvVar, MDMKey, Default) so a future tree-driven resolver
// can derive the flag → env → state → MDM → default lookup order from the
// same declaration. The resolution-chain fields are CARRIED in #170 but
// not yet consumed; #171/#172 wire them into the actual lookup code.
type FlagDef struct {
	// Name is the flag spelled without the "--" prefix, e.g. "profile".
	Name string
	// Short is the optional single-character alias spelled without the "-"
	// prefix, e.g. "v" for --verbose. Empty means no short alias.
	Short string
	// Description is the one-line description shown in help text and
	// completion scripts. Keep it tight — the help renderer wraps long
	// descriptions but the completion emitters do not.
	Description string
	// TakesArg is true if the flag consumes the next token as its value.
	TakesArg bool
	// Completer is the named completer function fed to pkg/completion.
	// Reserved values: "__databricks_profiles", "__files". Empty means no
	// value completion (the flag's value is opaque to the shell).
	Completer string

	// --- Resolution-chain metadata (carried; not yet consumed) ---

	// StateKey is the JSON key under ~/.claude/.databricks-claude.json that
	// persists this flag's value across sessions. Empty if the flag is
	// not persistable.
	StateKey string
	// EnvVar is the environment variable that may seed this flag's value
	// when the flag itself is absent. Empty if the flag has no env tier.
	EnvVar string
	// MDMKey is the key under the com.icerhymers.databricks-claude MDM
	// domain that admins can pin. Empty if the flag has no MDM tier.
	MDMKey string
	// Default is the literal default string when no other tier supplies a
	// value. Stored as string so the tree stays uniform across types;
	// callers parse it with strconv when needed.
	Default string
}

// ToCompletion narrows a FlagDef to the pkg/completion.FlagDef the shell
// emitters consume. The resolution-chain metadata is dropped here — it has
// no completion-script meaning.
func (f FlagDef) ToCompletion() completion.FlagDef {
	return completion.FlagDef{
		Name:        f.Name,
		Short:       f.Short,
		Description: f.Description,
		TakesArg:    f.TakesArg,
		Completer:   f.Completer,
	}
}

// Command is one node in the CLI tree.
//
// A Command can be:
//   - A leaf with Run set (e.g. "completion", "update").
//   - A branch with Subcommands (e.g. the root, or a future "serve" with
//     install/uninstall/status children).
//   - A main-driven node with Run nil (the root, today): main() walks the
//     tree to derive parsable flags + help text but keeps its own dispatch
//     loop. Run will be populated when subcommands migrate onto the tree.
//
// Persistent flags are conceptually inherited by every subcommand (a child
// command sees its own Flags ++ every ancestor's Persistent). Inheritance
// is declared here but not yet enforced at parse time; subcommand parsing
// still lives in hand-rolled FlagSets pending #171.
type Command struct {
	// Name is the command's word as typed on the CLI (e.g. "serve").
	// For the root command, Name is the binary name ("databricks-claude").
	Name string
	// Short is the one-line description shown when this command appears
	// as a child in a parent's "Subcommands:" listing.
	Short string
	// Long is the full help body. When non-empty, Render writes it
	// verbatim (after substituting registered template variables — see
	// Render). When empty, Render falls back to a programmatic table
	// derived from Flags / Persistent / Subcommands. Today the root
	// carries a hand-formatted Long for byte-for-byte help equivalence
	// with the legacy printf; future subcommands can opt into the
	// programmatic renderer by leaving Long empty.
	Long string
	// Flags are the flags local to this command.
	Flags []FlagDef
	// Persistent flags are inherited by every descendant subcommand.
	// On the root: --profile and --port live here so they remain
	// available everywhere once child commands migrate onto the tree.
	Persistent []FlagDef
	// Subcommands are the immediate children.
	Subcommands []Command
	// Run is the leaf executor. Nil for nodes whose dispatch lives in
	// main() (the root in #170) or for non-leaf branches whose children
	// own execution.
	Run func(args []string) error
}

// Subcommand returns a pointer to the immediate child Command with the given
// name, or nil when no such child exists. Helper for runners that dispatch
// nested subcommands (e.g. `serve install` finding the install node under
// the serve node) without re-walking the slice each time.
func (c Command) Subcommand(name string) *Command {
	for i := range c.Subcommands {
		if c.Subcommands[i].Name == name {
			return &c.Subcommands[i]
		}
	}
	return nil
}

// AllFlags returns Persistent followed by Flags as a single ordered slice.
// Persistent comes first so subcommand parsing (when it migrates) sees
// inherited flags before its own; the help renderer follows the same
// order so persistent flags surface near the top of the flag table.
func (c Command) AllFlags() []FlagDef {
	out := make([]FlagDef, 0, len(c.Persistent)+len(c.Flags))
	out = append(out, c.Persistent...)
	out = append(out, c.Flags...)
	return out
}

// CompletionFlags returns AllFlags converted to pkg/completion.FlagDef so
// it can be passed straight into completion.Run / GenerateBash / etc.
func (c Command) CompletionFlags() []completion.FlagDef {
	flags := c.AllFlags()
	out := make([]completion.FlagDef, len(flags))
	for i, f := range flags {
		out[i] = f.ToCompletion()
	}
	return out
}

// CompletionSubcommands returns the immediate children as
// pkg/completion.SubcommandDef. The conversion is RECURSIVE: each child
// carries its own Flags and Subcommands so the shell-completion generator
// can offer nested completion (e.g. `serve install --<TAB>` →
// install-scoped flags). Flags include each child's Persistent ++ Flags so
// inherited flags surface alongside the child's own.
func (c Command) CompletionSubcommands() []completion.SubcommandDef {
	out := make([]completion.SubcommandDef, len(c.Subcommands))
	for i, s := range c.Subcommands {
		out[i] = completion.SubcommandDef{
			Name:        s.Name,
			Description: s.Short,
			Flags:       s.CompletionFlags(),
			Subcommands: s.CompletionSubcommands(),
		}
	}
	return out
}

// KnownFlags returns the set of "--flag" names this command's parser
// recognises. Includes Persistent ++ Flags. Does NOT walk into
// Subcommands — each child command is parsed by its own dispatcher and
// owns its own KnownFlags.
func (c Command) KnownFlags() map[string]bool {
	flags := c.AllFlags()
	m := make(map[string]bool, len(flags))
	for _, f := range flags {
		m["--"+f.Name] = true
	}
	return m
}

// Render writes the command's help body to w. If c.Long is non-empty it
// is used verbatim, with each "{{key}}" placeholder replaced by vars[key].
// If c.Long is empty, Render falls back to a programmatic table built
// from c.Flags / c.Persistent / c.Subcommands (suitable for child
// commands that migrate onto the tree without curating a Long blob).
//
// Today only the root supplies a Long; the programmatic fallback exists
// so #171 migrations can drop Long with minimal diff.
func Render(w io.Writer, c Command, vars map[string]string) error {
	if c.Long != "" {
		body := c.Long
		for k, v := range vars {
			body = strings.ReplaceAll(body, "{{"+k+"}}", v)
		}
		_, err := io.WriteString(w, body)
		return err
	}
	return renderProgrammatic(w, c)
}

// ParseResult is the generic output of Command.Parse. Runners map this into
// their own typed flag struct (e.g. serveFlags, installFlags) so downstream
// resolution code is untouched. Three maps so callers can answer the three
// questions runners actually ask:
//
//   - "what string did the user provide for --flag?"     → Strings
//   - "did the user pass --flag (boolean toggle)?"        → Bools
//   - "did the user provide --flag at all (used to gate
//     state-persistence writes via the sentinel guard)?"  → Set
//
// Strings and Bools are populated only for known flags (declared on the
// Command's Persistent + Flags). Unknown flags AND any leftover positional
// tokens are appended to Positional in input order so runners can still
// detect e.g. an action keyword like `desktop generate-config` or, for the
// root, forward unknowns to the wrapped claude binary.
type ParseResult struct {
	Strings    map[string]string
	Bools      map[string]bool
	Set        map[string]bool
	Positional []string
}

// Parse evaluates args against c's known flags (Persistent ++ Flags) and
// returns a ParseResult. Behavior mirrors the hand-rolled scanners that
// previously lived in serve.go / serve_install.go / setup.go /
// desktop_config.go:
//
//   - Supports --flag value AND --flag=value forms.
//   - Boolean flags (TakesArg=false) consume no value. An explicit
//     --flag=false / --flag=0 / --flag=no sets the bool to false; anything
//     else (including bare --flag) sets it to true.
//   - Bare "--" terminates flag parsing; everything after is appended to
//     Positional verbatim.
//   - Short aliases declared via FlagDef.Short are honoured (-v → --verbose,
//     -h → --help when those flags are declared on c).
//   - Unknown long flags and unknown short flags are appended to Positional
//     unchanged. This preserves the historical tolerance of the hand-rolled
//     scanners (which silently ignored unknown flags) and lets root callers
//     forward unknowns to claude.
//   - A TakesArg flag that appears without a value (last token, no "=") gets
//     an empty string in Strings — matches parseServeFlags / parseInstallFlags
//     behaviour where bare `--port` left port=0 rather than erroring.
func (c Command) Parse(args []string) (*ParseResult, error) {
	flagsByName := make(map[string]FlagDef)
	flagsByShort := make(map[string]FlagDef)
	for _, f := range c.AllFlags() {
		flagsByName[f.Name] = f
		if f.Short != "" {
			flagsByShort[f.Short] = f
		}
	}

	res := &ParseResult{
		Strings: map[string]string{},
		Bools:   map[string]bool{},
		Set:     map[string]bool{},
	}

	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" {
			res.Positional = append(res.Positional, args[i+1:]...)
			break
		}

		if strings.HasPrefix(a, "--") {
			name := a[2:]
			value := ""
			hasValue := false
			if eq := strings.Index(name, "="); eq >= 0 {
				value = name[eq+1:]
				name = name[:eq]
				hasValue = true
			}
			f, known := flagsByName[name]
			if !known {
				res.Positional = append(res.Positional, a)
				continue
			}
			if f.TakesArg {
				if !hasValue && i+1 < len(args) {
					i++
					value = args[i]
				}
				res.Strings[name] = value
				res.Set[name] = true
			} else {
				res.Bools[name] = !isFalsy(hasValue, value)
				res.Set[name] = true
			}
			continue
		}

		if len(a) > 1 && a[0] == '-' {
			short := a[1:]
			value := ""
			hasValue := false
			if eq := strings.Index(short, "="); eq >= 0 {
				value = short[eq+1:]
				short = short[:eq]
				hasValue = true
			}
			f, known := flagsByShort[short]
			if !known {
				res.Positional = append(res.Positional, a)
				continue
			}
			if f.TakesArg {
				if !hasValue && i+1 < len(args) {
					i++
					value = args[i]
				}
				res.Strings[f.Name] = value
				res.Set[f.Name] = true
			} else {
				res.Bools[f.Name] = !isFalsy(hasValue, value)
				res.Set[f.Name] = true
			}
			continue
		}

		res.Positional = append(res.Positional, a)
	}

	return res, nil
}

// isFalsy reports whether an explicit boolean-flag value should set the bool
// to false. Bare flag (hasValue=false) is always truthy. Explicit values
// "0", "false", "no" (case-insensitive) are falsy; everything else is truthy.
func isFalsy(hasValue bool, value string) bool {
	if !hasValue {
		return false
	}
	return value == "0" || strings.EqualFold(value, "false") || strings.EqualFold(value, "no")
}

// renderProgrammatic emits a default help layout for commands that don't
// curate a Long blob. Used by the programmatic fallback in Render.
func renderProgrammatic(w io.Writer, c Command) error {
	var b strings.Builder
	if c.Short != "" {
		b.WriteString(c.Name)
		b.WriteString(" — ")
		b.WriteString(c.Short)
		b.WriteString("\n\n")
	}
	if flags := c.AllFlags(); len(flags) > 0 {
		b.WriteString("Flags:\n")
		for _, f := range flags {
			b.WriteString("  --")
			b.WriteString(f.Name)
			if f.Short != "" {
				b.WriteString(", -")
				b.WriteString(f.Short)
			}
			if f.TakesArg {
				b.WriteString(" <value>")
			}
			if f.Description != "" {
				b.WriteString("    ")
				b.WriteString(f.Description)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(c.Subcommands) > 0 {
		b.WriteString("Subcommands:\n")
		for _, s := range c.Subcommands {
			b.WriteString("  ")
			b.WriteString(s.Name)
			if s.Short != "" {
				b.WriteString("    ")
				b.WriteString(s.Short)
			}
			b.WriteString("\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
