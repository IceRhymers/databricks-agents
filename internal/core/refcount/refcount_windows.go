//go:build windows

package refcount

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type counter struct {
	Count int `json:"count"`
}

// TODO: replace with a named Windows mutex (LockFileEx) for cross-process coordination.
// The current sync.Mutex only synchronizes within a single process.
var mu sync.Mutex

func withLock(path string, fn func(*counter)) error {
	mu.Lock()
	defer mu.Unlock()

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("refcount: open %s: %w", path, err)
	}
	defer f.Close()

	var c counter
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		c = counter{}
	}
	fn(&c)

	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
