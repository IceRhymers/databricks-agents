package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestWiring_CallerCounts is the AST wiring gate. It walks every non-_test.go
// .go file in this package, counts CallExpr whose callee is a bare identifier
// (delegation counted uniformly with any other direct call), and asserts the
// seam free functions have exactly the expected number of live callers.
//
// After the #199 conversion:
//   - bootstrapSettings has 6 callers: the 5 UNCONVERTED direct callers
//     (config.go x2, doctor.go, hooks_cmd.go, serve_session.go) plus the 1
//     delegation in profile_claude.go's claudeSettingsPatcher.Patch.
//   - installHooks / uninstallHooks / installDaemon / uninstallDaemon /
//     daemonStatus / diagnosticsTail each have exactly 1 caller: the delegation
//     in profile_claude.go. Any additional caller means a live path bypassed
//     the profile seam; zero means the seam is unwired.
//
// On mismatch the failure names each callee with its actual count and the
// file:line of every call site so drift is diagnosable.
func TestWiring_CallerCounts(t *testing.T) {
	want := map[string]int{
		"bootstrapSettings": 6,
		"installHooks":      1,
		"uninstallHooks":    1,
		"installDaemon":     1,
		"uninstallDaemon":   1,
		"daemonStatus":      1,
		"diagnosticsTail":   1,
	}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	type site struct{ pos string }
	counts := map[string]int{}
	sites := map[string][]site{}

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		astFile, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(astFile, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if _, tracked := want[id.Name]; !tracked {
				return true
			}
			counts[id.Name]++
			sites[id.Name] = append(sites[id.Name], site{pos: fset.Position(call.Pos()).String()})
			return true
		})
	}

	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if counts[name] != want[name] {
			var locs []string
			for _, s := range sites[name] {
				locs = append(locs, s.pos)
			}
			t.Errorf("%s: got %d callers, want %d\n  call sites:\n    %s",
				name, counts[name], want[name], strings.Join(locs, "\n    "))
		}
	}
}
