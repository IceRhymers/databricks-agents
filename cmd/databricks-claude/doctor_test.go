package main

import (
	"testing"

	"github.com/IceRhymers/databricks-claude/pkg/modeldiscovery"
)

// TestDiffModelRouting drives the pure diff surface of the doctor command
// through the full status matrix (mirrors config_test.go's resolver tests —
// the pure function is the tested surface; runDoctor needs a live databricks
// CLI and is not integration-tested).
func TestDiffModelRouting(t *testing.T) {
	tests := []struct {
		name       string
		current    ModelRouting
		discovered ModelRouting
		unresolved []modeldiscovery.Unresolved
		want       map[string]string // family -> expected Status
	}{
		{
			name:       "all-ok",
			current:    ModelRouting{Opus: "ws.default.opus", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			discovered: ModelRouting{Opus: "ws.default.opus", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			want:       map[string]string{"opus": "ok", "sonnet": "ok", "haiku": "ok"},
		},
		{
			name:       "drift-opus-changed-non-legacy",
			current:    ModelRouting{Opus: "ws.default.opus", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			discovered: ModelRouting{Opus: "ws.default.opus-new", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			want:       map[string]string{"opus": "drift", "sonnet": "ok", "haiku": "ok"},
		},
		{
			name:       "stale-legacy-opus",
			current:    ModelRouting{Opus: "databricks-claude-opus-4-7", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			discovered: ModelRouting{Opus: "workspace.default.x", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			want:       map[string]string{"opus": "stale-legacy", "sonnet": "ok", "haiku": "ok"},
		},
		{
			name:       "unresolved-opus-preserves-current",
			current:    ModelRouting{Opus: "ws.default.opus", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			discovered: ModelRouting{Opus: "", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			unresolved: []modeldiscovery.Unresolved{{Family: "opus", PinCommand: "pin opus"}},
			want:       map[string]string{"opus": "unresolved", "sonnet": "ok", "haiku": "ok"},
		},
		{
			name:       "new-opus-no-current-pin",
			current:    ModelRouting{Opus: "", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			discovered: ModelRouting{Opus: "ws.default.opus", Sonnet: "ws.default.sonnet", Haiku: "ws.default.haiku"},
			want:       map[string]string{"opus": "new", "sonnet": "ok", "haiku": "ok"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deltas := diffModelRouting(tt.current, tt.discovered, tt.unresolved)
			got := map[string]string{}
			for _, d := range deltas {
				got[d.Family] = d.Status
			}
			for fam, wantStatus := range tt.want {
				if got[fam] != wantStatus {
					t.Errorf("family %q: got status %q, want %q", fam, got[fam], wantStatus)
				}
			}

			// For the unresolved case, verify the current pin is surfaced so
			// the --fix path can preserve it (never blank a working pin).
			if tt.name == "unresolved-opus-preserves-current" {
				for _, d := range deltas {
					if d.Family == "opus" && d.Current != "ws.default.opus" {
						t.Errorf("unresolved opus: Current=%q, want the preserved current pin", d.Current)
					}
				}
			}
		})
	}
}
