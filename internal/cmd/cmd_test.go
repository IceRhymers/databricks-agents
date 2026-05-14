package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestAllFlagsOrdersPersistentBeforeFlags(t *testing.T) {
	c := Command{
		Persistent: []FlagDef{{Name: "profile"}, {Name: "port"}},
		Flags:      []FlagDef{{Name: "verbose"}},
	}
	got := c.AllFlags()
	want := []string{"profile", "port", "verbose"}
	if len(got) != len(want) {
		t.Fatalf("AllFlags len=%d, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("AllFlags[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
}

func TestKnownFlagsCoversPersistentAndLocal(t *testing.T) {
	c := Command{
		Persistent: []FlagDef{{Name: "profile"}},
		Flags:      []FlagDef{{Name: "verbose"}, {Name: "port", TakesArg: true}},
	}
	known := c.KnownFlags()
	for _, want := range []string{"--profile", "--verbose", "--port"} {
		if !known[want] {
			t.Errorf("KnownFlags missing %q", want)
		}
	}
	if known["--unknown"] {
		t.Errorf("KnownFlags should not contain --unknown")
	}
}

func TestToCompletionDropsResolutionMetadata(t *testing.T) {
	f := FlagDef{
		Name:        "profile",
		Short:       "p",
		Description: "Databricks profile",
		TakesArg:    true,
		Completer:   "__databricks_profiles",
		StateKey:    "profile",
		EnvVar:      "DATABRICKS_CONFIG_PROFILE",
		MDMKey:      "databricksProfile",
		Default:     "DEFAULT",
	}
	got := f.ToCompletion()
	if got.Name != "profile" || got.Short != "p" || got.Description != "Databricks profile" ||
		!got.TakesArg || got.Completer != "__databricks_profiles" {
		t.Errorf("ToCompletion lost completion-relevant fields: %+v", got)
	}
}

func TestCompletionSubcommandsMapsShortToDescription(t *testing.T) {
	c := Command{Subcommands: []Command{
		{Name: "serve", Short: "Long-lived daemon"},
		{Name: "setup", Short: "Idempotent auth bootstrap"},
	}}
	got := c.CompletionSubcommands()
	if len(got) != 2 || got[0].Name != "serve" || got[0].Description != "Long-lived daemon" ||
		got[1].Name != "setup" || got[1].Description != "Idempotent auth bootstrap" {
		t.Errorf("CompletionSubcommands = %+v", got)
	}
}

// TestCompletionSubcommandsRecursesIntoChildren verifies that
// CompletionSubcommands carries each child's Flags + Subcommands, so the
// shell-completion generator can offer nested completion.
func TestCompletionSubcommandsRecursesIntoChildren(t *testing.T) {
	c := Command{
		Subcommands: []Command{
			{
				Name:  "serve",
				Short: "daemon",
				Flags: []FlagDef{{Name: "log-file", TakesArg: true}},
				Subcommands: []Command{
					{Name: "install", Short: "register"},
				},
			},
		},
	}
	got := c.CompletionSubcommands()
	if len(got) != 1 {
		t.Fatalf("got %d subcommands, want 1", len(got))
	}
	if len(got[0].Flags) != 1 || got[0].Flags[0].Name != "log-file" {
		t.Errorf("serve flags: %+v", got[0].Flags)
	}
	if len(got[0].Subcommands) != 1 || got[0].Subcommands[0].Name != "install" {
		t.Errorf("serve subcommands: %+v", got[0].Subcommands)
	}
}

func TestRenderUsesLongVerbatimWithVarSubstitution(t *testing.T) {
	c := Command{Long: "version={{Version}}\n"}
	var buf bytes.Buffer
	if err := Render(&buf, c, map[string]string{"Version": "1.2.3"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if buf.String() != "version=1.2.3\n" {
		t.Errorf("Render output = %q, want %q", buf.String(), "version=1.2.3\n")
	}
}

// --- Parse tests ---

func TestParse_LongFlagSpaceForm(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "profile", TakesArg: true}}}
	r, err := c.Parse([]string{"--profile", "prod"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Strings["profile"] != "prod" || !r.Set["profile"] {
		t.Errorf("expected profile=prod set=true, got %+v", r)
	}
}

func TestParse_LongFlagEqualForm(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "profile", TakesArg: true}}}
	r, _ := c.Parse([]string{"--profile=prod"})
	if r.Strings["profile"] != "prod" || !r.Set["profile"] {
		t.Errorf("expected profile=prod set=true, got %+v", r)
	}
}

func TestParse_BoolFlag(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "verbose"}}}
	r, _ := c.Parse([]string{"--verbose"})
	if !r.Bools["verbose"] || !r.Set["verbose"] {
		t.Errorf("expected verbose=true set=true, got %+v", r)
	}
}

func TestParse_BoolFlagExplicitFalse(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "daemon"}}}
	for _, v := range []string{"--daemon=false", "--daemon=0", "--daemon=no", "--daemon=NO"} {
		r, _ := c.Parse([]string{v})
		if r.Bools["daemon"] {
			t.Errorf("%q: expected daemon=false, got true", v)
		}
		if !r.Set["daemon"] {
			t.Errorf("%q: expected set=true even on falsy explicit", v)
		}
	}
}

func TestParse_ShortAlias(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "verbose", Short: "v"}, {Name: "help", Short: "h"}}}
	r, _ := c.Parse([]string{"-v", "-h"})
	if !r.Bools["verbose"] || !r.Bools["help"] {
		t.Errorf("expected -v→verbose, -h→help, got %+v", r)
	}
}

func TestParse_PersistentInherited(t *testing.T) {
	c := Command{
		Persistent: []FlagDef{{Name: "profile", TakesArg: true}},
		Flags:      []FlagDef{{Name: "log-file", TakesArg: true}},
	}
	r, _ := c.Parse([]string{"--profile", "p", "--log-file", "/tmp/x"})
	if r.Strings["profile"] != "p" || r.Strings["log-file"] != "/tmp/x" {
		t.Errorf("persistent + local flag: got %+v", r)
	}
}

func TestParse_UnknownLongFlagToPositional(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "profile", TakesArg: true}}}
	r, _ := c.Parse([]string{"--unknown", "--profile", "x", "--also-unknown=val"})
	if r.Strings["profile"] != "x" {
		t.Errorf("known flag still consumed: got %+v", r)
	}
	want := []string{"--unknown", "--also-unknown=val"}
	if len(r.Positional) != len(want) || r.Positional[0] != want[0] || r.Positional[1] != want[1] {
		t.Errorf("Positional = %v, want %v", r.Positional, want)
	}
}

func TestParse_DoubleDashTerminator(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "profile", TakesArg: true}}}
	r, _ := c.Parse([]string{"--profile", "p", "--", "--help", "rest"})
	if r.Strings["profile"] != "p" {
		t.Errorf("--profile should be consumed before --, got %+v", r)
	}
	want := []string{"--help", "rest"}
	if len(r.Positional) != len(want) || r.Positional[0] != want[0] || r.Positional[1] != want[1] {
		t.Errorf("Positional after --: %v, want %v", r.Positional, want)
	}
}

func TestParse_PositionalAction(t *testing.T) {
	c := Command{Flags: []FlagDef{{Name: "profile", TakesArg: true}}}
	r, _ := c.Parse([]string{"generate-config", "--profile", "p"})
	if len(r.Positional) != 1 || r.Positional[0] != "generate-config" {
		t.Errorf("Positional: got %v, want [generate-config]", r.Positional)
	}
	if r.Strings["profile"] != "p" {
		t.Errorf("flag after positional should still parse, got %+v", r)
	}
}

func TestParse_BareTakesArgAtEnd(t *testing.T) {
	// Mirrors the hand-rolled scanner tolerance: bare `--profile` with no
	// following token leaves the value empty rather than erroring.
	c := Command{Flags: []FlagDef{{Name: "profile", TakesArg: true}}}
	r, _ := c.Parse([]string{"--profile"})
	if r.Strings["profile"] != "" || !r.Set["profile"] {
		t.Errorf("bare --profile at end: got %+v, want empty string + Set=true", r)
	}
}

func TestParse_SetTrueGuardsStatePersistence(t *testing.T) {
	// The sentinel-guard contract from shouldPersistOTELTable: Set must be
	// true ONLY when the user explicitly provided the flag, never when the
	// flag was absent. Drives the parity between the historical *Set bools
	// and the new ParseResult.Set map.
	c := Command{Flags: []FlagDef{{Name: "otel-metrics-table", TakesArg: true}}}
	r, _ := c.Parse([]string{})
	if r.Set["otel-metrics-table"] {
		t.Errorf("Set should be false when flag absent, got %+v", r)
	}
	r2, _ := c.Parse([]string{"--otel-metrics-table=cat.s.t"})
	if !r2.Set["otel-metrics-table"] || r2.Strings["otel-metrics-table"] != "cat.s.t" {
		t.Errorf("Set should be true with value, got %+v", r2)
	}
}

func TestParse_NestedSubcommandFlagsOnDeepest(t *testing.T) {
	// install command inherits --profile from parent serve via Persistent.
	// (The runner is responsible for walking the chain; Parse itself sees a
	// flat AllFlags.) This test pins that AllFlags on a child with declared
	// Persistent ++ Flags accepts both.
	install := Command{
		Persistent: []FlagDef{{Name: "profile", TakesArg: true}},
		Flags:      []FlagDef{{Name: "skip-auth-check"}},
	}
	r, _ := install.Parse([]string{"--profile", "prod", "--skip-auth-check"})
	if r.Strings["profile"] != "prod" || !r.Bools["skip-auth-check"] {
		t.Errorf("nested-flag parse: got %+v", r)
	}
}

func TestRenderFallsBackToProgrammaticWhenLongIsEmpty(t *testing.T) {
	c := Command{
		Name:  "demo",
		Short: "Example",
		Flags: []FlagDef{{Name: "verbose", Description: "Enable debug"}},
		Subcommands: []Command{
			{Name: "child", Short: "A child"},
		},
	}
	var buf bytes.Buffer
	if err := Render(&buf, c, nil); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"demo — Example", "--verbose", "Enable debug", "child", "A child"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render fallback missing %q in:\n%s", want, out)
		}
	}
}
