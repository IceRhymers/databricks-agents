package main

import (
	"github.com/IceRhymers/databricks-claude/pkg/websearch"
)

// buildWebSearchBackend resolves the named --websearch-backend to an
// implementation. Pure stdlib, zero external deps.
func buildWebSearchBackend(name string) (websearch.Backend, error) {
	return websearch.Get(name)
}
