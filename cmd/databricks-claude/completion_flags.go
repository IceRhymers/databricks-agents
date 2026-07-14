package main

import (
	"github.com/IceRhymers/databricks-claude/pkg/completion"
)

// flagDefs is the legacy flat-slice view of the root command's flags. It is
// now derived from rootCommand (in commands.go) so the tree is the only
// source of truth — adding a flag means editing rootCommand, never this
// file. Kept as a package-level variable because main.go feeds it directly
// to pkg/completion and the parity tests in main_test.go assert against it.
//
// Order: Persistent flags first (--profile, --port), then Flags. Matches
// rootCommand.AllFlags() so the bash/zsh/fish completion scripts stay
// stable across the migration.
//
// Removed in #170: --daemon, --daemon-fake-key (desktop-scoped — they are
// parsed inside runDesktopCommand by extractDaemonFlag /
// extractDaemonFakeKeyFlag and never reach the root parser; listing them
// here only added noise to root completions). They will return as
// desktop-scoped completions when `desktop` migrates onto the tree (#171).
var flagDefs = rootCommand.CompletionFlags()

// knownFlags is the set of "--flag" names that databricks-claude owns at
// the root. Anything not in this set is forwarded transparently to the
// wrapped claude binary by parseArgs. Derived from rootCommand so it can
// never drift from completion or help text.
var knownFlags = rootCommand.KnownFlags()

// knownSubcommands is the set of top-level subcommands surfaced as
// position-1 completions. Derived from rootCommand.Subcommands.
//
// serve sub-subcommands (install/uninstall/status) are not listed here —
// the current pkg/completion only completes at position 1; deeper
// completion lives in `databricks-claude serve --help`.
var knownSubcommands []completion.SubcommandDef = rootCommand.CompletionSubcommands()
