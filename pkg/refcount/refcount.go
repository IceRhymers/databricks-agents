// Package refcount provides cross-process session reference counting backed by a JSON file.
package refcount

import (
	"fmt"
	"os"
	"path/filepath"
)

// PathForPort returns the file path used for cross-process session counting.
// prefix is the tool-specific prefix (e.g. ".databricks-claude-sessions").
func PathForPort(prefix string, port int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", prefix, port))
}

// Acquire atomically increments the session counter at path.
func Acquire(path string) error {
	return withLock(path, func(c *counter) { c.Count++ })
}

// Release atomically decrements the session counter and returns the remaining count.
func Release(path string) (int, error) {
	var remaining int
	err := withLock(path, func(c *counter) {
		if c.Count > 0 {
			c.Count--
		}
		remaining = c.Count
	})
	return remaining, err
}
