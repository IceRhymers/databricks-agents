//go:build windows

package mdmprofile

import (
	"syscall"
	"unsafe"
)

// Well-known Windows registry constants not exported by the syscall package.
const (
	hkeyCurrentUser = syscall.Handle(0x80000001)
	keyQueryValue   = uint32(0x0001)
	regSZ           = uint32(1)
)

var (
	modAdvapi32          = syscall.NewLazyDLL("advapi32.dll")
	procRegQueryValueExW = modAdvapi32.NewProc("RegQueryValueExW")
)

// Read returns the value of the "databricksProfile" registry value under
// HKCU\SOFTWARE\IceRhymers\databricks-claude.
// The domain argument is accepted for API symmetry with other platforms but
// is not used — all endpoint configuration is stored under the single key
// above, which matches the HKCU path written by the .reg artifact.
// Returns "" on any error (key absent, value missing, etc.).
func Read(_ string) (string, error) {
	const subKey = `SOFTWARE\IceRhymers\databricks-claude`
	const valueName = "databricksProfile"

	subKeyPtr, err := syscall.UTF16PtrFromString(subKey)
	if err != nil {
		return "", nil
	}
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(hkeyCurrentUser, subKeyPtr, 0, keyQueryValue, &key); err != nil {
		return "", nil // key absent
	}
	defer syscall.RegCloseKey(key)

	namePtr, err := syscall.UTF16PtrFromString(valueName)
	if err != nil {
		return "", nil
	}

	// First call: discover the buffer size.
	var typ, n uint32
	if err := regQueryValueExW(key, namePtr, nil, &typ, nil, &n); err != nil {
		return "", nil
	}
	if typ != regSZ || n == 0 {
		return "", nil
	}

	// Second call: read the value.
	buf := make([]uint16, (n+1)/2)
	if err := regQueryValueExW(key, namePtr, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n); err != nil {
		return "", nil
	}
	return syscall.UTF16ToString(buf), nil
}

// regQueryValueExW is a thin wrapper around RegQueryValueExW so that the
// Syscall6 invocation is isolated and mockable in tests.
func regQueryValueExW(key syscall.Handle, name *uint16, reserved *uint32, typ *uint32, data *byte, n *uint32) error {
	r, _, _ := procRegQueryValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(reserved)),
		uintptr(unsafe.Pointer(typ)),
		uintptr(unsafe.Pointer(data)),
		uintptr(unsafe.Pointer(n)),
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}
