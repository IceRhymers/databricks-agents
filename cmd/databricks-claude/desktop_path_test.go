package main

import "testing"

// TestMacOSCanonicalHelperPathLiteral locks the canonical install paths to the
// exact string literals baked into .github/workflows/release.yml (postinstall
// scriptlet) and the .pkg layout. Drift between the Go const and the YAML
// would silently break the .mobileconfig generated with --for-pkg, so this
// test exists to fail loudly the moment either side changes.
func TestMacOSCanonicalHelperPathLiteral(t *testing.T) {
	if got, want := MacOSCanonicalBinaryDir, "/usr/local/bin"; got != want {
		t.Errorf("MacOSCanonicalBinaryDir = %q, want %q", got, want)
	}
	if got, want := MacOSCanonicalHelperPath, "/usr/local/bin/databricks-claude-credential-helper"; got != want {
		t.Errorf("MacOSCanonicalHelperPath = %q, want %q", got, want)
	}
}
