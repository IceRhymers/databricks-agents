package modeldiscovery

import (
	"context"
	"net/http"
	"sort"
)

// Model is one entry for a client-side model picker: the BASE service FQN
// (NO [1m] suffix) plus 1M eligibility as a separate flag.
type Model struct {
	FQN  string
	OneM bool
}

// ListAnthropicModels returns every anthropic-capable service as a picker
// entry, newest-first (by routing-destination version, descending). Services
// that are ambiguous/unparseable (any dest not classifying, or dests spanning
// families) are skipped. Pure function, no I/O.
//
// A service is a candidate iff it advertises the Anthropic Messages API and all
// of its destinations classify to a single family (the same predicate Resolve
// uses). OneM follows the opus/sonnet >= 4.6 rule. The emitted FQN is the
// service's base FQN with no suffix. Ordering is by the newest destination's
// (Major, Minor) descending, with a stable FQN tie-break.
func ListAnthropicModels(services []Service) []Model {
	type entry struct {
		fqn          string
		major, minor int
		oneM         bool
	}
	var entries []entry
	for i := range services {
		svc := services[i]
		if !svc.supportsMessages() {
			continue
		}
		family, major, minor, ok := svc.familyAndNewest()
		if !ok {
			continue
		}
		entries = append(entries, entry{
			fqn:   svc.FQN,
			major: major,
			minor: minor,
			oneM:  isOneM(family, major, minor),
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].major != entries[j].major {
			return entries[i].major > entries[j].major
		}
		if entries[i].minor != entries[j].minor {
			return entries[i].minor > entries[j].minor
		}
		return entries[i].fqn < entries[j].fqn
	})

	models := make([]Model, 0, len(entries))
	for _, e := range entries {
		models = append(models, Model{FQN: e.fqn, OneM: e.oneM})
	}
	return models
}

// DiscoverModels lists model-services, enriches each candidate via an individual
// GetService call, and returns the picker list. Shares listEnriched with
// Discover so both entry points agree on the enrichment and skip semantics.
func DiscoverModels(ctx context.Context, client *http.Client, host, token string) ([]Model, error) {
	enriched, err := listEnriched(ctx, client, host, token)
	if err != nil {
		return nil, err
	}
	return ListAnthropicModels(enriched), nil
}
