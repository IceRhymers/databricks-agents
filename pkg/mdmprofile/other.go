//go:build !darwin && !windows

package mdmprofile

// Read is a no-op stub on platforms other than darwin and windows.
// MDM managed preferences are not supported on these platforms.
func Read(_ string) (string, error) {
	return "", nil
}
