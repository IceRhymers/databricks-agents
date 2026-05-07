package websearch

import (
	"context"
	"fmt"
	"strings"
)

// noneBackend is a sentinel that disables search but leaves fetch enabled.
type noneBackend struct{}

func (noneBackend) Name() string { return "none" }
func (noneBackend) Search(ctx context.Context, query string, max int) ([]Result, error) {
	return nil, fmt.Errorf("web_search backend disabled (--websearch-backend=none)")
}

// Get returns the named backend. Supported names: "duckduckgo" (default,
// zero config), "none" (disabled). brave/searxng are deferred per the issue
// out-of-scope list — extending the registry is the obvious follow-up seam.
func Get(name string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "duckduckgo", "ddg":
		return &DuckDuckGoBackend{}, nil
	case "none", "off", "disabled":
		return noneBackend{}, nil
	default:
		return nil, fmt.Errorf("unknown websearch backend %q (supported: duckduckgo, none)", name)
	}
}
