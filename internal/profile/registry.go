package profile

import "sort"

// Registry is a name→Profile lookup table. There is deliberately NO
// package-level `var Default` and NO init()-time registration, because each
// launcher is a separate `package main` that cannot import another's Profile —
// init-registration into a shared Default cannot compose the multiplexer.
//
// The multiplexer (#203, cmd/databricks) resolved that constraint by NOT using
// this type for dispatch: full Profile values aren't constructible in
// cmd/databricks (their SettingsPatcher/DaemonStrategy/HookInstaller impls live
// in each launcher's package main), so it registers a lightweight local
// name→binary→summary manifest and dispatches by exec-ing the sibling binary.
// Registry therefore remains an API-shape-only deliverable for full Profiles —
// exercised by tests and available to any future in-process consumer — not a
// live dispatch vehicle.
type Registry struct {
	m map[string]Profile
}

// NewRegistry returns an empty Registry ready for Register.
func NewRegistry() *Registry {
	return &Registry{m: map[string]Profile{}}
}

// Register adds p under p.Name, overwriting any prior entry with the same name.
func (r *Registry) Register(p Profile) {
	r.m[p.Name] = p
}

// Lookup returns the Profile registered under name and whether it was found.
func (r *Registry) Lookup(name string) (Profile, bool) {
	p, ok := r.m[name]
	return p, ok
}

// Names returns the registered profile names in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.m))
	for name := range r.m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
