package mdmprofile

import "testing"

// TestRead_UnknownDomainReturnsEmpty verifies the contract that Read never
// errors and returns "" when no preference is set for the given domain.
// This runs on every platform (darwin, windows, other).
func TestRead_UnknownDomainReturnsEmpty(t *testing.T) {
	// Use a domain name that definitely has no plist/registry entry.
	v, err := Read("com.icerhymers.databricks-claude.test.definitely-does-not-exist")
	if err != nil {
		t.Errorf("Read returned unexpected error: %v", err)
	}
	if v != "" {
		t.Errorf("Read = %q, want empty string for unknown domain", v)
	}
}

// TestReadKey_UnknownDomainReturnsEmpty verifies ReadKey never errors and
// returns "" when the domain/key pair does not exist.
// Runs on every platform (darwin, windows, other).
func TestReadKey_UnknownDomainReturnsEmpty(t *testing.T) {
	v, err := ReadKey("com.icerhymers.databricks-claude.test.definitely-does-not-exist", "databricksCliPath")
	if err != nil {
		t.Errorf("ReadKey returned unexpected error: %v", err)
	}
	if v != "" {
		t.Errorf("ReadKey = %q, want empty string for unknown domain/key", v)
	}
}
