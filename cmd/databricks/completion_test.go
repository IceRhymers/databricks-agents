package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBashBody returns a self-contained sibling bash completion script (with a
// registration line, to exercise stripping) whose `serve` subcommand completes
// install/status — enough to drive the dispatch wrapper functionally.
func fakeBashBody(binary string) string {
	fn := siblingFuncName(binary)
	return fn + `() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    if [[ "${COMP_WORDS[1]}" == "serve" ]]; then
        COMPREPLY=($(compgen -W "install status" -- "$cur"))
    fi
}
complete -F ` + fn + ` ` + binary + `
`
}

func withSibling(t *testing.T, fn func(a agent, shell string) (string, bool)) {
	t.Helper()
	orig := runSiblingCompletion
	runSiblingCompletion = fn
	t.Cleanup(func() { runSiblingCompletion = orig })
}

func TestGenerateBash_Structure(t *testing.T) {
	withSibling(t, func(a agent, shell string) (string, bool) {
		return fakeBashBody(a.Binary), true
	})
	script := generateBash()

	// Sibling functions embedded, registration lines stripped.
	for _, a := range agents {
		fn := siblingFuncName(a.Binary)
		if !strings.Contains(script, fn+"()") {
			t.Errorf("bash script missing sibling function %q", fn)
		}
		if strings.Contains(script, "complete -F "+fn+" "+a.Binary) {
			t.Errorf("bash script leaked sibling registration line for %q", a.Binary)
		}
		// A delegation case arm exists for each available agent.
		if !strings.Contains(script, "\n            "+a.Name+")\n") {
			t.Errorf("bash script missing dispatch arm for %q", a.Name)
		}
	}
	// The multiplexer's own wrapper + registration.
	if !strings.Contains(script, "_databricks() {") {
		t.Error("bash script missing _databricks wrapper")
	}
	if !strings.Contains(script, "complete -F _databricks databricks\n") {
		t.Error("bash script missing multiplexer registration")
	}
	// Position-1 offers agent names + reserved words.
	if !strings.Contains(script, "claude codex opencode list completion") {
		t.Error("bash script missing position-1 candidates")
	}
}

func TestGenerateBash_MissingSiblingDegradesToNameOnly(t *testing.T) {
	withSibling(t, func(a agent, shell string) (string, bool) {
		if a.Name == "codex" {
			return "", false // codex sibling unavailable
		}
		return fakeBashBody(a.Binary), true
	})
	script := generateBash()

	// No delegation arm for the missing agent...
	if strings.Contains(script, "\n            codex)\n") {
		t.Error("expected no codex dispatch arm when its sibling is unavailable")
	}
	if strings.Contains(script, siblingFuncName("databricks-codex")+"()") {
		t.Error("expected no codex sibling function when unavailable")
	}
	// ...but its name is still completable at position 1, and the script is valid.
	if !strings.Contains(script, "claude codex opencode list completion") {
		t.Error("codex should still be name-completable at position 1")
	}
	if !strings.Contains(script, "\n            claude)\n") {
		t.Error("available agent claude should still have a dispatch arm")
	}
}

func TestGenerateZsh_Structure(t *testing.T) {
	withSibling(t, func(a agent, shell string) (string, bool) {
		fn := siblingFuncName(a.Binary)
		return "#compdef " + a.Binary + "\n" + fn + "() {\n    :\n}\n" + fn + " \"$@\"\n", true
	})
	script := generateZsh()

	if !strings.HasPrefix(script, "#compdef databricks\n") {
		t.Error("zsh script missing #compdef databricks header")
	}
	for _, a := range agents {
		fn := siblingFuncName(a.Binary)
		if !strings.Contains(script, fn+"()") {
			t.Errorf("zsh script missing sibling function %q", fn)
		}
		// Sibling's own #compdef header and invocation stripped.
		if strings.Contains(script, "#compdef "+a.Binary) {
			t.Errorf("zsh script leaked sibling #compdef for %q", a.Binary)
		}
		if strings.Contains(script, fn+` "$@"`) {
			t.Errorf("zsh script leaked sibling invocation for %q", a.Binary)
		}
	}
	if !strings.Contains(script, "_databricks \"$@\"\n") {
		t.Error("zsh script missing multiplexer invocation")
	}
}

func TestGenerateFish_AgentNames(t *testing.T) {
	script := generateFish()
	for _, a := range agents {
		if !strings.Contains(script, "-a '"+a.Name+"'") {
			t.Errorf("fish script missing agent %q", a.Name)
		}
	}
}

// TestBashCompletion_Functional sources the generated bash script and drives a
// real completion for `databricks claude serve ⇥`, asserting COMPREPLY carries
// the sibling's subtree candidates. This is the only test that catches an
// off-by-one in the COMP_CWORD decrement / COMP_WORDS rewrite — a string check
// cannot.
func TestBashCompletion_Functional(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	withSibling(t, func(a agent, shell string) (string, bool) {
		if a.Name == "claude" {
			return fakeBashBody(a.Binary), true
		}
		return "", false
	})
	script := generateBash()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "comp.bash")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	driver := `
source "` + scriptPath + `"
COMP_WORDS=(databricks claude serve "")
COMP_CWORD=3
_databricks
echo "REPLY:${COMPREPLY[*]}"
`
	out, err := exec.Command(bashPath, "-c", driver).CombinedOutput()
	if err != nil {
		t.Fatalf("bash driver failed: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "install") || !strings.Contains(got, "status") {
		t.Errorf("nested completion did not reach sibling subtree; COMPREPLY output:\n%s", got)
	}
}
