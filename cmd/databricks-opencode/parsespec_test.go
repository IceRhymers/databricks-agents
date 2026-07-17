package main

import "testing"

// TestBindingsCoverKnownFlags asserts bidirectional set equality between the
// binding table in newParseSpec and the authoritative knownFlags gate. This is
// the structural inverse-drift guarantee that supersedes the now-dormant
// grep-based parity check: a Known flag with no
// binding (or a binding for a non-Known flag) fails here.
func TestBindingsCoverKnownFlags(t *testing.T) {
	spec := newParseSpec(&Args{})

	for name := range knownFlags {
		if _, ok := spec.Bindings[name]; !ok {
			t.Errorf("knownFlags has %q but newParseSpec has no binding for it", name)
		}
	}
	for name := range spec.Bindings {
		if !knownFlags[name] {
			t.Errorf("newParseSpec binds %q but it is not in knownFlags", name)
		}
	}
}
