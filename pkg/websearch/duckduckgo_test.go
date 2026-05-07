package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ddgSampleHTML = `
<html><body>
<div class="result results_links results_links_deep web-result">
  <h2 class="result__title">
    <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1&amp;rut=abc">Example Page <b>One</b></a>
  </h2>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1">A snippet describing the <b>first</b> example page.</a>
</div>
</div>
<div class="result results_links results_links_deep web-result">
  <h2 class="result__title">
    <a class="result__a" href="https://example.org/two">Second &amp; result</a>
  </h2>
  <div class="result__snippet">Snippet for the second result with &lt;tags&gt; stripped.</div>
</div>
</div>
</body></html>
`

func TestParseDuckDuckGoHTML_Snapshot(t *testing.T) {
	results := parseDuckDuckGoHTML(ddgSampleHTML, 5)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %#v", len(results), results)
	}
	r0 := results[0]
	if !strings.Contains(r0.Title, "Example Page") {
		t.Errorf("result[0].Title=%q, expected to contain 'Example Page'", r0.Title)
	}
	if r0.URL != "https://example.com/page1" {
		t.Errorf("result[0].URL=%q, want decoded https://example.com/page1", r0.URL)
	}
	if !strings.Contains(r0.Snippet, "first") {
		t.Errorf("result[0].Snippet=%q missing 'first'", r0.Snippet)
	}
	r1 := results[1]
	if r1.URL != "https://example.org/two" {
		t.Errorf("result[1].URL=%q, want https://example.org/two", r1.URL)
	}
	if !strings.Contains(r1.Title, "Second & result") {
		t.Errorf("result[1].Title=%q, expected entity decode", r1.Title)
	}
}

func TestParseDuckDuckGoHTML_Empty(t *testing.T) {
	results := parseDuckDuckGoHTML("<html><body>no results here</body></html>", 5)
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestDuckDuckGoBackend_Search_Mock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("q"); got != "golang stdlib" {
			t.Errorf("got q=%q, want %q", got, "golang stdlib")
		}
		w.Write([]byte(ddgSampleHTML))
	}))
	defer srv.Close()

	b := &DuckDuckGoBackend{Endpoint: srv.URL}
	results, err := b.Search(context.Background(), "golang stdlib", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len=%d, want 2", len(results))
	}
}
