//go:build darwin

package mdmprofile

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"os/user"
	"path/filepath"
)

// managedPrefsDir resolves the per-user managed preferences directory.
// Overridable for testing.
var managedPrefsDir = func() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "/Library/Managed Preferences"
	}
	return filepath.Join("/Library/Managed Preferences", u.Username)
}

// ReadKey returns the value of the given key from the MDM-managed plist for
// the given domain (e.g. "com.icerhymers.databricks-claude"). Checks the
// managed preferences directory first (written by MDM on enrolled devices),
// then falls back to ~/Library/Preferences for unmanaged dev/test machines.
// Returns "" on any read or parse error.
func ReadKey(domain, key string) (string, error) {
	// 1. MDM-managed preferences (requires device enrollment).
	managed := filepath.Join(managedPrefsDir(), domain+".plist")
	if v, err := readPlistFile(managed, key); err == nil && v != "" {
		return v, nil
	}

	// 2. User preferences fallback (unmanaged machines / developer testing).
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil
	}
	unmanaged := filepath.Join(home, "Library", "Preferences", domain+".plist")
	if v, err := readPlistFile(unmanaged, key); err == nil && v != "" {
		return v, nil
	}

	return "", nil
}

// Read returns the value of the "databricksProfile" key from the MDM-managed
// plist for the given domain. Shim over ReadKey for backwards compatibility.
func Read(domain string) (string, error) {
	return ReadKey(domain, "databricksProfile")
}

// readPlistFile reads a plist XML file and returns the string value of the
// given key. Returns ("", nil) when the file does not exist or the key is absent.
func readPlistFile(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return parsePlistString(data, key)
}

// parsePlistString walks Apple plist XML tokens seeking the first
// <key>k</key><string>v</string> pair where key text equals wantKey.
// Uses encoding/xml rather than cgo or CFPreferences so no external
// dependency is needed.
func parsePlistString(data []byte, wantKey string) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	nextIsValue := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return "", err
				}
				nextIsValue = (k == wantKey)
			case "string":
				if nextIsValue {
					var v string
					if err := dec.DecodeElement(&v, &t); err != nil {
						return "", err
					}
					return v, nil
				}
				nextIsValue = false
			default:
				nextIsValue = false
			}
		}
	}
	return "", nil
}
