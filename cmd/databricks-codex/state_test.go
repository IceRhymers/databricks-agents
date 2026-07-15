package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadState(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Save a profile.
	if err := saveState(persistentState{Profile: "aidev"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// Load it back.
	s := loadState()
	if s.Profile != "aidev" {
		t.Errorf("got profile %q, want %q", s.Profile, "aidev")
	}
}

func TestLoadState_Missing(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "nonexistent.json") }
	defer func() { statePath = orig }()

	s := loadState()
	if s.Profile != "" {
		t.Errorf("expected empty profile from missing file, got %q", s.Profile)
	}
}

func TestLoadState_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	os.WriteFile(p, []byte("not json"), 0o644)

	orig := statePath
	statePath = func() string { return p }
	defer func() { statePath = orig }()

	s := loadState()
	if s.Profile != "" {
		t.Errorf("expected empty profile from invalid JSON, got %q", s.Profile)
	}
}

func TestSaveState_OverwritesPrevious(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	saveState(persistentState{Profile: "first"})
	saveState(persistentState{Profile: "second"})

	s := loadState()
	if s.Profile != "second" {
		t.Errorf("got profile %q, want %q", s.Profile, "second")
	}
}

func TestSaveAndLoadState_OtelLogsTable(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	if err := saveState(persistentState{OtelLogsTable: "custom.db.logs"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	s := loadState()
	if s.OtelLogsTable != "custom.db.logs" {
		t.Errorf("got OtelLogsTable %q, want %q", s.OtelLogsTable, "custom.db.logs")
	}
}

func TestSaveAndLoadState_OtelMetricsTable(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	if err := saveState(persistentState{OtelMetricsTable: "custom.db.metrics"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	s := loadState()
	if s.OtelMetricsTable != "custom.db.metrics" {
		t.Errorf("got OtelMetricsTable %q, want %q", s.OtelMetricsTable, "custom.db.metrics")
	}
}

func TestSaveState_PreservesExistingFields(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Save with profile only.
	saveState(persistentState{Profile: "aidev"})

	// Read-modify-write: add OtelLogsTable while preserving Profile.
	s := loadState()
	s.OtelLogsTable = "custom.db.logs"
	saveState(s)

	// Verify both fields survive.
	s = loadState()
	if s.Profile != "aidev" {
		t.Errorf("got Profile %q, want %q", s.Profile, "aidev")
	}
	if s.OtelLogsTable != "custom.db.logs" {
		t.Errorf("got OtelLogsTable %q, want %q", s.OtelLogsTable, "custom.db.logs")
	}
}

func TestSaveState_OverwritesOtelLogsTable(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	saveState(persistentState{OtelLogsTable: "first.table"})
	saveState(persistentState{OtelLogsTable: "second.table"})

	s := loadState()
	if s.OtelLogsTable != "second.table" {
		t.Errorf("got OtelLogsTable %q, want %q", s.OtelLogsTable, "second.table")
	}
}

func TestSaveAndLoadState_Model(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	if err := saveState(persistentState{Model: "databricks-gpt-5-4-mini"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	s := loadState()
	if s.Model != "databricks-gpt-5-4-mini" {
		t.Errorf("got Model %q, want %q", s.Model, "databricks-gpt-5-4-mini")
	}
}

func TestSaveState_ModelPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	saveState(persistentState{Profile: "aidev", OtelLogsTable: "my.table"})

	s := loadState()
	s.Model = "custom-model"
	saveState(s)

	s = loadState()
	if s.Profile != "aidev" {
		t.Errorf("got Profile %q, want %q", s.Profile, "aidev")
	}
	if s.OtelLogsTable != "my.table" {
		t.Errorf("got OtelLogsTable %q, want %q", s.OtelLogsTable, "my.table")
	}
	if s.Model != "custom-model" {
		t.Errorf("got Model %q, want %q", s.Model, "custom-model")
	}
}

func TestResolvePort(t *testing.T) {
	tests := []struct {
		name     string
		portFlag int
		state    persistentState
		want     int
	}{
		{"flag wins", 9999, persistentState{Port: 8080}, 9999},
		{"state wins over default", 0, persistentState{Port: 8080}, 8080},
		{"default when no flag and no state", 0, persistentState{}, defaultPort},
		{"flag wins over default", 5555, persistentState{}, 5555},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePort(tc.portFlag, tc.state)
			if got != tc.want {
				t.Errorf("resolvePort(%d, %+v) = %d, want %d", tc.portFlag, tc.state, got, tc.want)
			}
		})
	}
}

func TestSaveAndLoadState_Port(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	if err := saveState(persistentState{Port: 49154}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	s := loadState()
	if s.Port != 49154 {
		t.Errorf("got Port %d, want %d", s.Port, 49154)
	}
}
