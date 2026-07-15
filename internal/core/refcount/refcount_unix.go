//go:build !windows

package refcount

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
)

type counter struct {
	Count int `json:"count"`
}

func withLock(path string, fn func(*counter)) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("refcount: open %s: %w", path, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("refcount: lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

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
