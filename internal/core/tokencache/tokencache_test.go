package tokencache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// mockFetcher implements TokenFetcher for testing.
type mockFetcher struct {
	token  string
	expiry time.Time
	err    error
	calls  int
}

func (m *mockFetcher) FetchToken(_ context.Context) (string, time.Time, error) {
	m.calls++
	return m.token, m.expiry, m.err
}

func TestTokenProvider_FreshToken(t *testing.T) {
	f := &mockFetcher{token: "tok-fresh", expiry: time.Now().Add(60 * time.Minute)}
	tp := NewTokenProvider(f)

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "tok-fresh" {
		t.Errorf("got token %q, want %q", tok, "tok-fresh")
	}
	if tp.CachedToken() != "tok-fresh" {
		t.Error("token not cached after fresh fetch")
	}
}

func TestTokenProvider_CacheHit(t *testing.T) {
	f := &mockFetcher{token: "tok-cached", expiry: time.Now().Add(60 * time.Minute)}
	tp := NewTokenProvider(f)

	// Prime the cache
	if _, err := tp.Token(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Replace fetcher with one that always fails; cache should be returned instead
	tp.mu.Lock()
	tp.fetcher = &mockFetcher{err: fmt.Errorf("should not be called")}
	tp.mu.Unlock()

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if tok != "tok-cached" {
		t.Errorf("got %q, want cached token", tok)
	}
}

func TestTokenProvider_RefreshNearExpiry(t *testing.T) {
	f := &mockFetcher{token: "tok-refreshed", expiry: time.Now().Add(60 * time.Minute)}
	tp := NewTokenProvider(f)
	tp.SetCache("tok-old", time.Now().Add(3*time.Minute)) // within 5-minute buffer

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "tok-refreshed" {
		t.Errorf("expected refresh; got %q", tok)
	}
}

func TestTokenProvider_FallbackOnError(t *testing.T) {
	f := &mockFetcher{err: fmt.Errorf("fetch failed")}
	tp := NewTokenProvider(f)
	// Seed cache (expired so refresh is attempted)
	tp.SetCache("tok-last-good", time.Now().Add(-1*time.Minute))

	tok, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error with cached fallback: %v", err)
	}
	if tok != "tok-last-good" {
		t.Errorf("got %q, want last-good cached token", tok)
	}
}

func TestTokenProvider_NoCachedTokenError(t *testing.T) {
	f := &mockFetcher{err: fmt.Errorf("fetch failed")}
	tp := NewTokenProvider(f)

	_, err := tp.Token(context.Background())
	if err == nil {
		t.Fatal("expected error on first-call failure with no cache, got nil")
	}
}
