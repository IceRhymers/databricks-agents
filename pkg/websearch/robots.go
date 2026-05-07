package websearch

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// RobotsChecker is the surface used by Fetch to delegate robots.txt checks.
type RobotsChecker interface {
	Allowed(ctx context.Context, rawURL string, userAgent string) (allowed bool, reason string, err error)
}

// Robots provides a per-host session cache of robots.txt rules. Only the
// minimum needed for the workaround is supported: User-agent: * with
// Disallow lines. Other directives are silently ignored.
//
// Zero value is ready to use.
type Robots struct {
	mu    sync.Mutex
	cache map[string]robotsEntry
}

type robotsEntry struct {
	rules     []string // Disallow paths for *
	fetchedAt time.Time
}

const robotsTTL = 24 * time.Hour

// Allowed reports whether userAgent may fetch rawURL according to the host's
// robots.txt. A network error fetching robots.txt is treated as "allowed"
// (fail-open) but returned via err for callers who want to surface it.
func (r *Robots) Allowed(ctx context.Context, rawURL string, userAgent string) (bool, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true, "", err
	}
	if u.Host == "" {
		return true, "", nil
	}

	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]robotsEntry)
	}
	entry, ok := r.cache[u.Host]
	r.mu.Unlock()

	if !ok || time.Since(entry.fetchedAt) > robotsTTL {
		entry = r.fetch(ctx, u.Scheme, u.Host)
		r.mu.Lock()
		r.cache[u.Host] = entry
		r.mu.Unlock()
	}

	path := u.Path
	if path == "" {
		path = "/"
	}
	for _, dis := range entry.rules {
		if dis == "" {
			continue
		}
		if strings.HasPrefix(path, dis) {
			return false, "Disallow: " + dis, nil
		}
	}
	return true, "", nil
}

func (r *Robots) fetch(ctx context.Context, scheme, host string) robotsEntry {
	if scheme == "" {
		scheme = "https"
	}
	robotsURL := scheme + "://" + host + "/robots.txt"
	client := defaultHTTPClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return robotsEntry{fetchedAt: time.Now()}
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return robotsEntry{fetchedAt: time.Now()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return robotsEntry{fetchedAt: time.Now()}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return robotsEntry{rules: parseRobots(string(body)), fetchedAt: time.Now()}
}

// parseRobots returns the Disallow rules that apply to User-agent: *.
// Multiple `User-agent: *` blocks are merged. Other agent blocks are skipped.
func parseRobots(body string) []string {
	var rules []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	inStar := false
	currentAgents := map[string]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			// Group separator.
			currentAgents = map[string]bool{}
			inStar = false
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		val := strings.TrimSpace(line[colon+1:])
		switch key {
		case "user-agent":
			currentAgents[strings.ToLower(val)] = true
			if currentAgents["*"] {
				inStar = true
			}
		case "disallow":
			if inStar && val != "" {
				rules = append(rules, val)
			}
		}
	}
	return rules
}
