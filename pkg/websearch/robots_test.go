package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRobots_StarBlock(t *testing.T) {
	body := `
# comment
User-agent: *
Disallow: /private
Disallow: /secret

User-agent: BadBot
Disallow: /
`
	rules := parseRobots(body)
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2: %v", len(rules), rules)
	}
	want := map[string]bool{"/private": true, "/secret": true}
	for _, r := range rules {
		if !want[r] {
			t.Errorf("unexpected rule %q", r)
		}
	}
}

func TestParseRobots_MultipleStarBlocks(t *testing.T) {
	body := "User-agent: *\nDisallow: /a\n\nUser-agent: *\nDisallow: /b\n"
	rules := parseRobots(body)
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2: %v", len(rules), rules)
	}
}

func TestRobots_Allowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/robots.txt") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	}))
	defer srv.Close()

	r := &Robots{}
	ok, _, err := r.Allowed(context.Background(), srv.URL+"/public/page", "ua")
	if err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if !ok {
		t.Error("/public should be allowed")
	}
	ok2, reason, err := r.Allowed(context.Background(), srv.URL+"/private/secret", "ua")
	if err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if ok2 {
		t.Errorf("/private should be disallowed (reason=%q)", reason)
	}
}

func TestRobots_FetchFailureFailsOpen(t *testing.T) {
	r := &Robots{}
	ok, _, _ := r.Allowed(context.Background(), "http://127.0.0.1:1/anything", "ua")
	if !ok {
		t.Error("expected fail-open (allowed=true) when robots.txt unreachable")
	}
}
