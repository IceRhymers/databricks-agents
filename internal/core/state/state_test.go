package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type testState struct {
	Profile string `json:"profile,omitempty"`
	Port    int    `json:"port,omitempty"`
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := Save(path, testState{Profile: "aidev", Port: 49154}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load[testState](path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Profile != "aidev" {
		t.Errorf("Profile = %q, want %q", got.Profile, "aidev")
	}
	if got.Port != 49154 {
		t.Errorf("Port = %d, want %d", got.Port, 49154)
	}
}

func TestLoad_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	got, err := Load[testState](path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Profile != "" || got.Port != 0 {
		t.Errorf("expected zero value from missing file, got %+v", got)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte("not json"), 0o644)

	got, err := Load[testState](path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Profile != "" {
		t.Errorf("expected zero value from invalid JSON, got %+v", got)
	}
}

func TestSave_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	Save(path, testState{Profile: "first"})
	Save(path, testState{Profile: "second"})

	got, _ := Load[testState](path)
	if got.Profile != "second" {
		t.Errorf("Profile = %q, want %q", got.Profile, "second")
	}
}

func TestResolvePort(t *testing.T) {
	tests := []struct {
		name        string
		flagPort    int
		savedPort   int
		defaultPort int
		want        int
	}{
		{"flag wins", 9999, 8080, 49153, 9999},
		{"saved wins over default", 0, 8080, 49153, 8080},
		{"default when no flag and no saved", 0, 0, 49153, 49153},
		{"flag wins over default", 5555, 0, 49153, 5555},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolvePort(tc.flagPort, tc.savedPort, tc.defaultPort)
			if got != tc.want {
				t.Errorf("ResolvePort(%d, %d, %d) = %d, want %d",
					tc.flagPort, tc.savedPort, tc.defaultPort, got, tc.want)
			}
		})
	}
}

// TestSave_ConcurrentWrites verifies that two goroutines saving to the same
// state file simultaneously produce a valid, uncorrupted file that matches
// exactly one of the two payloads — never a torn mix of both.
//
// Assumes POSIX-atomic rename: on Windows, MoveFileEx(MOVEFILE_REPLACE_EXISTING)
// can transiently return ERROR_ACCESS_DENIED on concurrent same-path renames.
func TestSave_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	want1 := testState{Profile: "workspace-a", Port: 49153}
	want2 := testState{Profile: "workspace-b", Port: 49154}

	var wg sync.WaitGroup
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = Save(path, want1)
	}()
	go func() {
		defer wg.Done()
		err2 = Save(path, want2)
	}()
	wg.Wait()

	if err1 != nil {
		t.Errorf("goroutine 1 Save error: %v", err1)
	}
	if err2 != nil {
		t.Errorf("goroutine 2 Save error: %v", err2)
	}

	// The file must exist and parse cleanly. Read it directly rather than via
	// Load, which swallows parse errors by design and would mask corruption.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var got testState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("state is not valid JSON after concurrent writes: %v\ncontent: %s", err, data)
	}

	// Whole-struct equality: a field-by-field check would let a torn write
	// interleaving Profile from one writer with Port from the other slip past.
	// Which writer wins is deliberately unasserted.
	if got != want1 && got != want2 {
		t.Errorf("state is %+v — matches neither %+v nor %+v; file is corrupt", got, want1, want2)
	}

	// No leftover temp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("unexpected leftover file in dir: %s", e.Name())
		}
	}
}
