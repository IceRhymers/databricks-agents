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

func TestSaveAndLoadState_Model(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Save model.
	if err := saveState(persistentState{Profile: "dev", Model: "my-model"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	s := loadState()
	if s.Model != "my-model" {
		t.Errorf("got model %q, want %q", s.Model, "my-model")
	}
	if s.Profile != "dev" {
		t.Errorf("got profile %q, want %q", s.Profile, "dev")
	}
}

func TestModelResolution_SavedState(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Save a model to state.
	saveState(persistentState{Model: "saved-model"})

	// Simulate resolution: no --model flag (empty string).
	model := ""
	if model == "" {
		if saved := loadState(); saved.Model != "" {
			model = saved.Model
		}
	}
	if model == "" {
		model = "databricks-claude-sonnet-4-6"
	}

	if model != "saved-model" {
		t.Errorf("model = %q, want %q (should use saved state)", model, "saved-model")
	}
}

func TestModelResolution_DefaultWhenNoState(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// No saved state — should fall through to default.
	model := ""
	if model == "" {
		if saved := loadState(); saved.Model != "" {
			model = saved.Model
		}
	}
	if model == "" {
		model = "databricks-claude-sonnet-4-6"
	}

	if model != "databricks-claude-sonnet-4-6" {
		t.Errorf("model = %q, want %q (should use default)", model, "databricks-claude-sonnet-4-6")
	}
}

func TestResolvePort_FlagWins(t *testing.T) {
	port := resolvePort(8080, persistentState{Port: 9000})
	if port != 8080 {
		t.Errorf("resolvePort = %d, want 8080 (flag should win)", port)
	}
}

func TestResolvePort_StateWins(t *testing.T) {
	port := resolvePort(0, persistentState{Port: 9000})
	if port != 9000 {
		t.Errorf("resolvePort = %d, want 9000 (state should win)", port)
	}
}

func TestResolvePort_Default(t *testing.T) {
	port := resolvePort(0, persistentState{})
	if port != defaultPort {
		t.Errorf("resolvePort = %d, want %d (should use default)", port, defaultPort)
	}
}

func TestModelExplicit_OverwritesSavedState(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Save initial model.
	saveState(persistentState{Profile: "dev", Model: "old-model"})

	// Simulate explicit --model flag.
	model := "new-model"
	modelExplicit := model != ""

	if modelExplicit {
		saved := loadState()
		saved.Model = model
		if err := saveState(saved); err != nil {
			t.Fatalf("saveState: %v", err)
		}
	}

	// Verify it was saved.
	s := loadState()
	if s.Model != "new-model" {
		t.Errorf("got model %q, want %q", s.Model, "new-model")
	}
	// Profile should be preserved.
	if s.Profile != "dev" {
		t.Errorf("got profile %q, want %q (should preserve)", s.Profile, "dev")
	}
}
