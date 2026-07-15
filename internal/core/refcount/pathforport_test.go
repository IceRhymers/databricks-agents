package refcount

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathForPort(t *testing.T) {
	want := filepath.Join(os.TempDir(), ".databricks-claude-sessions-12345")
	got := PathForPort(".databricks-claude-sessions", 12345)
	if got != want {
		t.Errorf("PathForPort(..., 12345) = %q, want %q", got, want)
	}
}
