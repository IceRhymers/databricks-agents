//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// delegate replaces the current process image with the sibling binary via
// execve. This gives perfect fidelity: same PID, so signal delivery, exit
// codes, and terminal control are byte-identical to invoking the sibling
// directly. argv[0] is the RESOLVED sibling path (not "claude") so the child
// sees filepath.Base(os.Args[0]) == "databricks-<agent>" — identical to a
// direct launch, and never misfires the credential-helper argv[0] alias. The
// environment is os.Environ() so every env-driven path (DATABRICKS_NO_UPDATE_
// CHECK, DATABRICKS_CLI, profile resolution) is preserved. On success this
// never returns; it only returns an exit code if execve itself fails.
func delegate(path string, args []string) int {
	argv := append([]string{path}, args...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "databricks-agents: exec %s: %v\n", path, err)
		return 1
	}
	return 0 // unreachable on success
}
