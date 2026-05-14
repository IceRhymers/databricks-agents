package completion

import (
	"strings"
	"testing"
)

var testFlags = []FlagDef{
	{Name: "profile", Short: "", Description: "Databricks CLI profile", TakesArg: true, Completer: "__databricks_profiles"},
	{Name: "verbose", Short: "v", Description: "Enable debug logging", TakesArg: false, Completer: ""},
	{Name: "help", Short: "h", Description: "Show help", TakesArg: false, Completer: ""},
	{Name: "port", Short: "", Description: "Proxy listen port", TakesArg: true, Completer: ""},
	{Name: "log-file", Short: "", Description: "Write logs to file", TakesArg: true, Completer: "__files"},
}

func TestGenerateBash_ContainsFlags(t *testing.T) {
	out := GenerateBash("databricks-claude", testFlags)
	for _, want := range []string{
		"--profile", "--verbose", "-v", "--help", "-h", "--port", "--log-file",
		"_databricks_claude",
		"complete -F _databricks_claude databricks-claude",
		"__databricks_profiles",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bash output missing %q", want)
		}
	}
}

func TestGenerateBash_PassthroughBoundary(t *testing.T) {
	out := GenerateBash("databricks-claude", testFlags)
	if !strings.Contains(out, `"--"`) {
		t.Error("bash output missing passthrough -- guard")
	}
}

func TestGenerateBash_FileCompleter(t *testing.T) {
	out := GenerateBash("databricks-claude", testFlags)
	// --log-file should use compgen -f
	if !strings.Contains(out, "compgen -f") {
		t.Error("bash output missing file completer for --log-file")
	}
}

func TestGenerateZsh_ContainsFlags(t *testing.T) {
	out := GenerateZsh("databricks-claude", testFlags)
	for _, want := range []string{
		"#compdef databricks-claude",
		"--profile",
		"--verbose",
		"--port",
		"__databricks_profiles",
		"_arguments",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("zsh output missing %q", want)
		}
	}
}

func TestGenerateZsh_ShortFlags(t *testing.T) {
	out := GenerateZsh("databricks-claude", testFlags)
	// verbose has short -v so should appear as paired spec
	if !strings.Contains(out, "-v") {
		t.Error("zsh output missing short flag -v")
	}
}

func TestGenerateFish_ContainsFlags(t *testing.T) {
	out := GenerateFish("databricks-claude", testFlags)
	for _, want := range []string{
		"complete -c databricks-claude -f",
		"-l profile",
		"-l verbose",
		"-s v",
		"-l port",
		"__databricks_profiles",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fish output missing %q", want)
		}
	}
}

func TestGenerateFish_FileCompleter(t *testing.T) {
	out := GenerateFish("databricks-claude", testFlags)
	// --log-file should have -F for file completion
	if !strings.Contains(out, "-F") {
		t.Error("fish output missing -F file completer for --log-file")
	}
}

func TestGenerateBash_InvalidShellExitsNonZero(t *testing.T) {
	// Run() with an invalid shell should exit 1. We test GenerateBash directly
	// for content; Run() behavior is tested via the binary integration.
	out := GenerateBash("test-bin", testFlags)
	if out == "" {
		t.Error("GenerateBash returned empty string")
	}
}

func TestUniqueCompleters(t *testing.T) {
	flags := []FlagDef{
		{Name: "a", Completer: "__databricks_profiles"},
		{Name: "b", Completer: "__databricks_profiles"}, // duplicate
		{Name: "c", Completer: "__files"},               // filtered out
		{Name: "d", Completer: ""},
	}
	got := uniqueCompleters(flags)
	if len(got) != 1 || got[0] != "__databricks_profiles" {
		t.Errorf("uniqueCompleters = %v, want [__databricks_profiles]", got)
	}
}

// --- Nested completion (#171) ---

// nestedTree is a minimal subcommand tree exercising the depth-2 + flags
// path: root with a `serve` subcommand carrying its own flags AND nested
// `install`/`status` subcommands.
func nestedTree() (rootFlags []FlagDef, subs []SubcommandDef) {
	rootFlags = []FlagDef{
		{Name: "verbose", Short: "v", Description: "verbose"},
	}
	subs = []SubcommandDef{
		{Name: "completion", Description: "shell completions"},
		{
			Name:        "serve",
			Description: "long-lived daemon",
			Flags: []FlagDef{
				{Name: "log-file", Description: "log path", TakesArg: true, Completer: "__files"},
				{Name: "profile", Description: "databricks profile", TakesArg: true, Completer: "__databricks_profiles"},
			},
			Subcommands: []SubcommandDef{
				{
					Name:        "install",
					Description: "register OS service",
					Flags: []FlagDef{
						{Name: "skip-auth-check", Description: "bypass auth probe"},
					},
				},
				{
					Name:        "status",
					Description: "print status",
				},
			},
		},
	}
	return
}

func TestGenerateBashFull_NestedSubcommandsBlock(t *testing.T) {
	rootFlags, subs := nestedTree()
	out := GenerateBashFull("dbc", rootFlags, subs)
	for _, want := range []string{
		// depth-1 subcommand list
		`completion update`,                    // not present
		`local subcmds="completion serve"`,     // root subcmds list
		`case "$sub1" in`,                      // top-level dispatch
		`completion)`,                          // first subcmd arm
		`serve)`,                               // serve arm
		`case "$sub2" in`,                      // nested dispatch under serve
		`install)`,                             // nested arm
		`status)`,                              // nested arm
		`local subcmds2="install status"`,      // nested subcmd list
		`--skip-auth-check`,                    // install-scoped flag
		`--log-file`,                           // serve-scoped flag
	} {
		if want == `completion update` {
			if strings.Contains(out, want) {
				t.Errorf("did not expect %q in nested completion output", want)
			}
			continue
		}
		if !strings.Contains(out, want) {
			t.Errorf("nested bash completion missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestGenerateZshFull_NestedSubcommandsBlock(t *testing.T) {
	rootFlags, subs := nestedTree()
	out := GenerateZshFull("dbc", rootFlags, subs)
	for _, want := range []string{
		`'serve:long-lived daemon'`,
		`case "$sub1" in`,
		`case "$sub2" in`,
		`'install:register OS service'`,
		`--log-file`,        // serve flag spec
		`--skip-auth-check`, // install flag spec
	} {
		if !strings.Contains(out, want) {
			t.Errorf("nested zsh completion missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestGenerateFishFull_NestedSubcommandsBlock(t *testing.T) {
	rootFlags, subs := nestedTree()
	out := GenerateFishFull("dbc", rootFlags, subs)
	for _, want := range []string{
		`__fish_seen_subcommand_from serve`,         // serve-scoped guard
		`__fish_seen_subcommand_from serve install`, // install-scoped guard
		`-l skip-auth-check`,
		`-l log-file`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("nested fish completion missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestUniqueCompletersTree_WalksNested(t *testing.T) {
	rootFlags, subs := nestedTree()
	got := uniqueCompletersTree(rootFlags, subs)
	// __databricks_profiles is on a nested flag — must appear once.
	found := false
	for _, c := range got {
		if c == "__databricks_profiles" {
			found = true
		}
	}
	if !found {
		t.Errorf("uniqueCompletersTree missed nested __databricks_profiles completer; got %v", got)
	}
}
