<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# tokencache

## Purpose
Generic mutex-guarded token cache. Accepts any `TokenFetcher` implementation and caches tokens with automatic refresh 5 minutes before expiry. On refresh failure, returns the last known good token rather than failing.

## Key Files

| File | Description |
|------|-------------|
| `tokencache.go` | `TokenFetcher` interface, `TokenProvider` struct with `Token` (cache-aware fetch), `SetCache` (pre-warm/test), `CachedToken` (test accessor) |
| `tokencache_test.go` | Tests for cache hit/miss, refresh timing, fallback-on-error behavior |

## For AI Agents

### Working In This Directory
- The `TokenFetcher` interface has a single method: `FetchToken(ctx) (token, expiry, err)`.
- The root package implements `databricksFetcher` (shells out to `databricks auth token`) and passes it to `NewTokenProvider`.
- **5-minute refresh buffer**: tokens are refreshed when `time.Now()` is within 5 minutes of `expiresAt`. This is critical for avoiding auth failures during long-running sessions.
- **Graceful degradation**: if refresh fails but a cached token exists, the stale token is returned with a log warning. Only the first fetch can fail hard.
- `SetCache` is used by `proxy_test.go`'s `warmToken()` helper to pre-load the cache in tests.
- The `TokenProvider` also implements `proxy.TokenSource` (same `Token(ctx) (string, error)` signature).

### Testing Requirements
- Run: `go test ./pkg/tokencache/...`
- Tests use mock `TokenFetcher` implementations with controllable expiry times and error returns.

### Common Patterns
- Mutex-guarded read-through cache with graceful fallback.

## Dependencies

### Internal
- None

### External
- `sync`, `time` (stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
