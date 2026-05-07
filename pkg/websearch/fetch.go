package websearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Fetch retrieves the URL via HTTP GET, follows up to maxRedirects redirects,
// caps the body to byteBudget bytes, and returns readable text.
//
// robotsAllowed, if non-nil, is consulted before issuing the request and is
// expected to enforce robots.txt. When nil, no robots check is performed
// (callers should normally pass &Robots{}).
func Fetch(ctx context.Context, rawURL string, byteBudget int, robotsAllowed RobotsChecker) (*FetchResult, error) {
	if byteBudget <= 0 {
		byteBudget = 100 * 1024
	}

	if robotsAllowed != nil {
		ok, reason, err := robotsAllowed.Allowed(ctx, rawURL, DefaultUserAgent)
		if err != nil {
			// robots check failures are non-fatal — proceed.
		} else if !ok {
			return nil, fmt.Errorf("robots.txt disallows fetch: %s", reason)
		}
	}

	client := defaultHTTPClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "text/html, text/plain, */*;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, int64(byteBudget)+1)
	raw, err := io.ReadAll(limited)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	truncated := len(raw) > byteBudget
	if truncated {
		raw = raw[:byteBudget]
	}

	ct := resp.Header.Get("Content-Type")
	text := htmlToText(string(raw))

	return &FetchResult{
		URL:         resp.Request.URL.String(),
		ContentType: ct,
		Text:        text,
		Truncated:   truncated,
	}, nil
}

// htmlToText strips scripts, styles, tags, decodes basic entities, and
// collapses whitespace. Cheap-but-effective; not a full readability port.
func htmlToText(html string) string {
	if !strings.Contains(html, "<") {
		return strings.TrimSpace(html)
	}
	html = scriptStyleRe.ReplaceAllString(html, " ")
	html = tagRe.ReplaceAllString(html, " ")
	html = stripHTMLEntities(html)
	html = whitespaceRe.ReplaceAllString(html, " ")
	return strings.TrimSpace(html)
}

// defaultHTTPClient returns an http.Client suited for both search and fetch:
// short timeout, max 3 redirects.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}
