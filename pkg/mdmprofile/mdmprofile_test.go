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
