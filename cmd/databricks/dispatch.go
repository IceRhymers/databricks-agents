package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// resolveBinary locates the sibling binary for the given name. It prefers a
// copy co-located with the multiplexer (os.Executable's directory) so an
// install that ships all four binaries in one bin dir — homebrew, scoop,
// `make install` — always dispatches to the matching-version sibling. It falls
// through to PATH when os.Executable() errors, returns a non-absolute path, or
// the co-located file is absent; PATH keeps dispatch correct on layouts where
// the binaries aren't co-located (at the cost of possible version skew, which
// #204's lockstep packaging addresses).
func resolveBinary(binary string) (string, error) {
	file := binaryFileName(binary)

	if exe, err := os.Executable(); err == nil && filepath.IsAbs(exe) {
		candidate := filepath.Join(filepath.Dir(exe), file)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}

	if path, err := exec.LookPath(binary); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("cannot find %s (install it, or put it on PATH)", binary)
}

// binaryFileName appends the platform executable suffix to a binary name.
func binaryFileName(binary string) string {
	if runtime.GOOS == "windows" {
		return binary + ".exe"
	}
	return binary
}

// isExecutableFile reports whether path exists and is a regular file. On unix
// it additionally requires an executable bit; on windows executability is not
// mode-encoded, so existence as a regular file is sufficient.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}
