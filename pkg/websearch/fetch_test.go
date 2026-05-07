package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetch_Small(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><h1>Hi</h1><p>world</p></body></html>"))
	}))
	defer srv.Close()

	fr, err := Fetch(context.Background(), srv.URL+"/p", 1024, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(fr.Text, "Hi") || !strings.Contains(fr.Text, "world") {
		t.Errorf("text missing content: %q", fr.Text)
	}
	if fr.Truncated {
		t.Errorf("unexpected truncation")
	}
}

func TestFetch_Truncates(t *testing.T) {
	big := "<html><body>" + strings.Repeat("A", 2000) + "</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(big))
	}))
	defer srv.Close()

	fr, err := Fetch(context.Background(), srv.URL, 100, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !fr.Truncated {
		t.Errorf("expected truncated=true; got false; text len=%d", len(fr.Text))
	}
}

func TestFetch_StripsScriptStyle(t *testing.T) {
	body := `<html><head><style>body{color:red}</style><script>alert(1)</script></head><body>Hello world</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	fr, err := Fetch(context.Background(), srv.URL, 1024, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if strings.Contains(fr.Text, "alert") || strings.Contains(fr.Text, "color:red") {
		t.Errorf("script/style not stripped: %q", fr.Text)
	}
	if !strings.Contains(fr.Text, "Hello world") {
		t.Errorf("body missing: %q", fr.Text)
	}
}

type denyAll struct{}

func (denyAll) Allowed(_ context.Context, rawURL string, _ string) (bool, string, error) {
	return false, "Disallow: /", nil
}

func TestFetch_RobotsBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called when robots denies")
	}))
	defer srv.Close()
	_, err := Fetch(context.Background(), srv.URL+"/p", 1024, denyAll{})
	if err == nil {
		t.Fatal("expected error when robots denies")
	}
	if !strings.Contains(err.Error(), "robots.txt") {
		t.Errorf("expected robots.txt in error, got %v", err)
	}
}
