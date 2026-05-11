//go:build !darwin && !windows

package mdmprofile

// ReadKey is a no-op stub on platforms other than darwin and windows.
// MDM managed preferences are not supported on these platforms.
func ReadKey(_, _ string) (string, error) {
	return "", nil
}

// Read is a no-op stub on platforms other than darwin and windows.
func Read(_ string) (string, error) {
	return ReadKey("", "")
}
