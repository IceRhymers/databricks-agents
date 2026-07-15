package tokencache

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// TokenFetcher is the interface for fetching fresh tokens from an external
// source (e.g., Databricks CLI, OAuth provider, etc.).
type TokenFetcher interface {
	FetchToken(ctx context.Context) (token string, expiry time.Time, err error)
}

// TokenProvider caches tokens obtained via a TokenFetcher, refreshing them
// when they are within 5 minutes of expiry.
type TokenProvider struct {
	fetcher     TokenFetcher
	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

// NewTokenProvider creates a new TokenProvider backed by the given fetcher.
func NewTokenProvider(fetcher TokenFetcher) *TokenProvider {
	return &TokenProvider{fetcher: fetcher}
}

// Token returns a valid access token, refreshing if necessary.
func (tp *TokenProvider) Token(ctx context.Context) (string, error) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	// Return cached token if still valid (with 5-minute buffer)
	if tp.cachedToken != "" && time.Now().Before(tp.expiresAt.Add(-5*time.Minute)) {
		return tp.cachedToken, nil
	}

	token, expiry, err := tp.fetcher.FetchToken(ctx)
	if err != nil {
		if tp.cachedToken != "" {
			log.Printf("token refresh failed, using cached token: %v", err)
			return tp.cachedToken, nil
		}
		return "", fmt.Errorf("failed to fetch token: %w", err)
	}

	tp.cachedToken = token
	tp.expiresAt = expiry
	return tp.cachedToken, nil
}

// SetCache directly seeds the token cache. Useful for testing or pre-warming.
func (tp *TokenProvider) SetCache(token string, expiry time.Time) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.cachedToken = token
	tp.expiresAt = expiry
}

// CachedToken returns the currently cached token (for testing).
func (tp *TokenProvider) CachedToken() string {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.cachedToken
}

// Expiry returns the cached token's expiry time.
// Returns zero if no token has been fetched yet.
func (tp *TokenProvider) Expiry() time.Time {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.expiresAt
}
