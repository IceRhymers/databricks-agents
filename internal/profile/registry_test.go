package profile

import (
	"reflect"
	"testing"
)

// TestRegistry_RoundTrip verifies Register followed by Lookup returns the same
// Profile.
func TestRegistry_RoundTrip(t *testing.T) {
	r := NewRegistry()
	p := Profile{Name: "databricks-claude", ChildBinary: "claude"}
	r.Register(p)

	got, ok := r.Lookup("databricks-claude")
	if !ok {
		t.Fatal("Lookup(databricks-claude) = _, false; want found")
	}
	if got.Name != p.Name || got.ChildBinary != p.ChildBinary {
		t.Errorf("Lookup returned %+v, want %+v", got, p)
	}
}

// TestRegistry_UnknownMiss verifies Lookup of an unregistered name misses.
func TestRegistry_UnknownMiss(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup("nope"); ok {
		t.Error("Lookup(nope) = _, true; want not found")
	}
}

// TestRegistry_NamesSorted verifies Names returns registered names in sorted
// order regardless of registration order.
func TestRegistry_NamesSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(Profile{Name: "databricks-opencode"})
	r.Register(Profile{Name: "databricks-claude"})
	r.Register(Profile{Name: "databricks-codex"})

	got := r.Names()
	want := []string{"databricks-claude", "databricks-codex", "databricks-opencode"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

// TestRegistry_NamesEmpty verifies Names on an empty registry returns an empty
// (non-nil-usable) slice.
func TestRegistry_NamesEmpty(t *testing.T) {
	r := NewRegistry()
	if got := r.Names(); len(got) != 0 {
		t.Errorf("Names() on empty registry = %v, want empty", got)
	}
}
