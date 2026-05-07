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

	if err := saveState(persistentState{Profile: "aidev"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

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

func TestSaveAndLoadState_OtelTables(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	want := persistentState{
		OtelMetricsTable: "cat.schema.claude_otel_metrics",
		OtelLogsTable:    "cat.schema.claude_otel_logs",
		OtelTracesTable:  "cat.schema.claude_otel_traces",
	}
	if err := saveState(want); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got := loadState()
	if got.OtelMetricsTable != want.OtelMetricsTable {
		t.Errorf("OtelMetricsTable: got %q, want %q", got.OtelMetricsTable, want.OtelMetricsTable)
	}
	if got.OtelLogsTable != want.OtelLogsTable {
		t.Errorf("OtelLogsTable: got %q, want %q", got.OtelLogsTable, want.OtelLogsTable)
	}
	if got.OtelTracesTable != want.OtelTracesTable {
		t.Errorf("OtelTracesTable: got %q, want %q", got.OtelTracesTable, want.OtelTracesTable)
	}
}

func TestSaveAndLoadState_OtelTablesRoundtrip(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	// Other fields coexist with OTel table fields.
	if err := saveState(persistentState{
		Profile:          "aidev",
		Port:             49154,
		OtelMetricsTable: "a.b.metrics",
	}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	s := loadState()
	if s.Profile != "aidev" {
		t.Errorf("Profile: got %q, want %q", s.Profile, "aidev")
	}
	if s.OtelMetricsTable != "a.b.metrics" {
		t.Errorf("OtelMetricsTable: got %q, want %q", s.OtelMetricsTable, "a.b.metrics")
	}
	if s.OtelLogsTable != "" {
		t.Errorf("OtelLogsTable: got %q, want empty", s.OtelLogsTable)
	}
}

func TestStatePath_Override(t *testing.T) {
	orig := statePath
	defer func() { statePath = orig }()

	custom := "/tmp/test-state-dir/custom-state.json"
	statePath = func() string { return custom }

	if got := statePath(); got != custom {
		t.Errorf("statePath() = %q, want %q", got, custom)
	}
}

func TestSaveAndLoadState_WebSearch(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	defer func() { statePath = orig }()

	want := persistentState{
		WithWebSearch:        true,
		WebSearchBackend:     "duckduckgo",
		WebSearchFetchBudget: 65536,
	}
	if err := saveState(want); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got := loadState()
	if got.WithWebSearch != want.WithWebSearch {
		t.Errorf("WithWebSearch: got %v, want %v", got.WithWebSearch, want.WithWebSearch)
	}
	if got.WebSearchBackend != want.WebSearchBackend {
		t.Errorf("WebSearchBackend: got %q, want %q", got.WebSearchBackend, want.WebSearchBackend)
	}
	if got.WebSearchFetchBudget != want.WebSearchFetchBudget {
		t.Errorf("WebSearchFetchBudget: got %d, want %d", got.WebSearchFetchBudget, want.WebSearchFetchBudget)
	}
}
