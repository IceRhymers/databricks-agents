//go:build darwin

package mdmprofile

import (
	"os"
	"path/filepath"
	"testing"
)

const testDomain = "com.icerhymers.databricks-claude"

// writePlist writes a minimal Apple plist containing one key=value pair.
func writePlist(t *testing.T, dir, domain, key, value string) string {
	t.Helper()
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>` + key + `</key>
	<string>` + value + `</string>
</dict>
</plist>
`
	path := filepath.Join(dir, domain+".plist")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writePlist: %v", err)
	}
	return path
}

func TestParsePlistString_Found(t *testing.T) {
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>databricksProfile</key>
	<string>my-workspace</string>
</dict>
</plist>`)

	got, err := parsePlistString(plist, "databricksProfile")
	if err != nil {
		t.Fatalf("parsePlistString: %v", err)
	}
	if got != "my-workspace" {
		t.Errorf("got %q, want my-workspace", got)
	}
}

func TestParsePlistString_KeyAbsent(t *testing.T) {
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>otherKey</key>
	<string>other-value</string>
</dict>
</plist>`)

	got, err := parsePlistString(plist, "databricksProfile")
	if err != nil {
		t.Fatalf("parsePlistString: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string when key absent", got)
	}
}

func TestParsePlistString_MultipleKeys(t *testing.T) {
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>firstKey</key>
	<string>first-value</string>
	<key>databricksProfile</key>
	<string>correct-profile</string>
	<key>lastKey</key>
	<string>last-value</string>
</dict>
</plist>`)

	got, err := parsePlistString(plist, "databricksProfile")
	if err != nil {
		t.Fatalf("parsePlistString: %v", err)
	}
	if got != "correct-profile" {
		t.Errorf("got %q, want correct-profile", got)
	}
}

func TestReadPlistFile_NotExist(t *testing.T) {
	v, err := readPlistFile("/nonexistent/path/to/file.plist")
	if err != nil {
		t.Fatalf("expected nil error for non-existent file, got %v", err)
	}
	if v != "" {
		t.Errorf("expected empty string for non-existent file, got %q", v)
	}
}

func TestRead_ManagedPrefs(t *testing.T) {
	dir := t.TempDir()
	writePlist(t, dir, testDomain, "databricksProfile", "mdm-profile")

	origManagedPrefsDir := managedPrefsDir
	managedPrefsDir = func() string { return dir }
	defer func() { managedPrefsDir = origManagedPrefsDir }()

	got, err := Read(testDomain)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "mdm-profile" {
		t.Errorf("Read = %q, want mdm-profile", got)
	}
}

func TestRead_FallbackToUserPrefs(t *testing.T) {
	// Managed prefs dir exists but doesn't have the domain plist.
	managedDir := t.TempDir()
	origManagedPrefsDir := managedPrefsDir
	managedPrefsDir = func() string { return managedDir }
	defer func() { managedPrefsDir = origManagedPrefsDir }()

	// Write the plist in the user prefs location.
	userPrefsDir := t.TempDir()
	writePlist(t, userPrefsDir, testDomain, "databricksProfile", "user-profile")

	// Patch the Read function's home dir resolution: we can't easily override
	// os.UserHomeDir, so instead write the plist at the absolute path Read
	// would look for using the actual home dir. Skip if home dir is not
	// writable in the test environment.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir; skip")
	}
	userPrefsPath := filepath.Join(home, "Library", "Preferences", testDomain+".plist")
	// Remove any pre-existing file and restore after the test.
	existing, _ := os.ReadFile(userPrefsPath)
	defer func() {
		if existing != nil {
			_ = os.WriteFile(userPrefsPath, existing, 0o600)
		} else {
			_ = os.Remove(userPrefsPath)
		}
	}()

	content := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>databricksProfile</key><string>user-pref-profile</string>
</dict></plist>`
	_ = os.MkdirAll(filepath.Dir(userPrefsPath), 0o755)
	if err := os.WriteFile(userPrefsPath, []byte(content), 0o600); err != nil {
		t.Skipf("cannot write user prefs plist: %v", err)
	}

	got, err := Read(testDomain)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "user-pref-profile" {
		t.Errorf("Read = %q, want user-pref-profile", got)
	}
}

func TestRead_NoPlistReturnsEmpty(t *testing.T) {
	// Neither managed nor user prefs file exists.
	managedDir := t.TempDir()
	origManagedPrefsDir := managedPrefsDir
	managedPrefsDir = func() string { return managedDir }
	defer func() { managedPrefsDir = origManagedPrefsDir }()

	// Use a domain that definitely has no plist file.
	got, err := Read("com.icerhymers.databricks-claude.definitely-does-not-exist-test")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "" {
		t.Errorf("Read = %q, want empty string when no plist found", got)
	}
}
