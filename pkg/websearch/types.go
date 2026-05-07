// Package websearch provides local fulfillment of Anthropic's web_search
// and web_fetch server-side tools. It exists as a workaround until
// Databricks FMAPI ships native server-side tool support.
//
// Zero external dependencies — pure Go stdlib.
package websearch

import "context"

// Result is one entry returned by a search backend.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// FetchResult is the readable text extracted from a single URL fetch.
type FetchResult struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type,omitempty"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// Backend is implemented by anything that can answer web searches.
type Backend interface {
	// Name returns the canonical short name of the backend ("duckduckgo", "none").
	Name() string
	// Search runs a search and returns up to max results.
	Search(ctx context.Context, query string, max int) ([]Result, error)
}
