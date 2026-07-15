package refcount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func readCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read counter file: %v", err)
	}
	var c counter
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("failed to parse counter file: %v", err)
	}
	return c.Count
}

// TestAcquire_Single verifies a single Acquire sets count to 1.
func TestAcquire_Single(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refcount.json")

	if err := Acquire(path); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	got := readCount(t, path)
	if got != 1 {
		t.Errorf("got count %d, want 1", got)
	}
}

// TestAcquireRelease verifies Acquire followed by Release returns to 0.
func TestAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refcount.json")

	if err := Acquire(path); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	remaining, err := Release(path)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if remaining != 0 {
		t.Errorf("got remaining %d, want 0", remaining)
	}

	got := readCount(t, path)
	if got != 0 {
		t.Errorf("got count %d, want 0", got)
	}
}

// TestConcurrentAcquires verifies that concurrent Acquires produce the correct
// total count thanks to file locking.
func TestConcurrentAcquires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refcount.json")
	n := 50

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := Acquire(path); err != nil {
				t.Errorf("Acquire: %v", err)
			}
		}()
	}
	wg.Wait()

	got := readCount(t, path)
	if got != n {
		t.Errorf("got count %d, want %d", got, n)
	}
}

// TestRelease_NeverNegative verifies that releasing below zero doesn't go negative.
func TestRelease_NeverNegative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refcount.json")

	// Release on empty/non-existent file — should stay at 0.
	remaining, err := Release(path)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if remaining != 0 {
		t.Errorf("got remaining %d, want 0", remaining)
	}

	// Release again — still 0.
	remaining, err = Release(path)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if remaining != 0 {
		t.Errorf("got remaining %d, want 0", remaining)
	}

	got := readCount(t, path)
	if got != 0 {
		t.Errorf("got count %d, want 0", got)
	}
}

// TestMultipleAcquiresAndReleases verifies a sequence of acquires and releases.
func TestMultipleAcquiresAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refcount.json")

	for i := 0; i < 5; i++ {
		if err := Acquire(path); err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
	}

	got := readCount(t, path)
	if got != 5 {
		t.Errorf("after 5 acquires: got count %d, want 5", got)
	}

	for i := 0; i < 3; i++ {
		if _, err := Release(path); err != nil {
			t.Fatalf("Release %d: %v", i, err)
		}
	}

	got = readCount(t, path)
	if got != 2 {
		t.Errorf("after 3 releases: got count %d, want 2", got)
	}
}
