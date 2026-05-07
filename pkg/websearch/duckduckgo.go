package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// DuckDuckGoBackend scrapes the DuckDuckGo HTML endpoint. Zero config — no
// API key. The HTML is server-rendered with stable class markers; we parse
// with stdlib regexp to avoid pulling in golang.org/x/net/html.
type DuckDuckGoBackend struct {
	UserAgent string // override; default set in Search
	Endpoint  string // override; default https://html.duckduckgo.com/html/
	Client    *http.Client
}

// Name returns the canonical backend name.
func (d *DuckDuckGoBackend) Name() string { return "duckduckgo" }

// DefaultUserAgent is the User-Agent we present to DuckDuckGo. Real-looking
// UA — DDG returns no results to obvious bots.
const DefaultUserAgent = "Mozilla/5.0 (compatible; databricks-claude/websearch)"

// Search posts the query to DuckDuckGo HTML and parses the result list.
func (d *DuckDuckGoBackend) Search(ctx context.Context, query string, max int) ([]Result, error) {
	if max <= 0 {
		max = 5
	}
	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = "https://html.duckduckgo.com/html/"
	}
	ua := d.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	client := d.Client
	if client == nil {
		client = defaultHTTPClient()
	}

	form := url.Values{}
	form.Set("q", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ua)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	return parseDuckDuckGoHTML(string(body), max), nil
}

// regexes for parsing DDG HTML. The markup uses the stable
// `result__a` (title link) and `result__snippet` (description) class names.
var (
	ddgResultBlockRe = regexp.MustCompile(`(?s)<div\s+class="(?:[^"]*\s)?result\s[^"]*"[^>]*>(.*?)</div>\s*</div>`)
	ddgTitleRe       = regexp.MustCompile(`(?s)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe     = regexp.MustCompile(`(?s)<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	ddgSnippetDivRe  = regexp.MustCompile(`(?s)<div\s+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</div>`)
)

// parseDuckDuckGoHTML extracts up to max results from a DDG HTML response.
// Exposed (lowercase) for test access via package-internal snapshot test.
func parseDuckDuckGoHTML(html string, max int) []Result {
	out := make([]Result, 0, max)
	blocks := ddgResultBlockRe.FindAllStringSubmatch(html, -1)
	for _, b := range blocks {
		if len(out) >= max {
			break
		}
		body := b[1]
		titleM := ddgTitleRe.FindStringSubmatch(body)
		if titleM == nil {
			continue
		}
		rawURL := stripHTMLEntities(titleM[1])
		title := stripTags(titleM[2])
		// Decode DDG redirect wrapper: //duckduckgo.com/l/?uddg=<encoded>
		if u := decodeDDGRedirect(rawURL); u != "" {
			rawURL = u
		}
		var snippet string
		if m := ddgSnippetRe.FindStringSubmatch(body); m != nil {
			snippet = stripTags(m[1])
		} else if m := ddgSnippetDivRe.FindStringSubmatch(body); m != nil {
			snippet = stripTags(m[1])
		}
		if rawURL == "" || title == "" {
			continue
		}
		out = append(out, Result{
			Title:   strings.TrimSpace(title),
			URL:     strings.TrimSpace(rawURL),
			Snippet: strings.TrimSpace(snippet),
		})
	}
	return out
}

func decodeDDGRedirect(u string) string {
	// Forms: "//duckduckgo.com/l/?uddg=..." or "https://duckduckgo.com/l/?uddg=..."
	if !strings.Contains(u, "duckduckgo.com/l/") {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	if v := parsed.Query().Get("uddg"); v != "" {
		if dec, err := url.QueryUnescape(v); err == nil {
			return dec
		}
	}
	return ""
}

var (
	tagRe          = regexp.MustCompile(`<[^>]+>`)
	whitespaceRe   = regexp.MustCompile(`\s+`)
	scriptStyleRe  = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</\s*(script|style)\s*>`)
	htmlEntities   = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&apos;", "'", "&nbsp;", " ")
)

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = stripHTMLEntities(s)
	s = whitespaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func stripHTMLEntities(s string) string { return htmlEntities.Replace(s) }
