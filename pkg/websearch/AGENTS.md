<!-- Parent: ../AGENTS.md -->

# websearch

## Purpose

Local fulfillment of Anthropic's `web_search` and `web_fetch` server-side tools, used by `--with-websearch` mode in databricks-claude as a workaround for the Databricks FMAPI gap. **Zero external dependencies — pure stdlib only.**

This package is the engine; the orchestration (request rewriting, `tool_result` substitution) lives in `pkg/proxy/websearch_handler.go`.

## Key Files

| File | Description |
|------|-------------|
| `types.go` | `Result`, `FetchResult` data types and the `Backend` interface |
| `duckduckgo.go` | DuckDuckGo HTML-scrape backend. POSTs to `https://html.duckduckgo.com/html/`, regex-parses `<a class="result__a">` and `<a class="result__snippet">` markers, decodes the `/l/?uddg=` redirect wrapper to recover real URLs |
| `fetch.go` | `Fetch(ctx, url, byteBudget)` — `http.Get` with timeout, redirect cap (3), 10s hard deadline, byte cap, HTML→text via regex pipeline (kill `<script>`/`<style>`, strip tags, collapse whitespace) |
| `robots.go` | Per-host `robots.txt` cache with 24h TTL. Parses `User-agent: *` blocks; supports `Disallow:` and `Allow:` directives. Fail-open on fetch errors so transient network failures don't deny legitimate fetches |
| `backends.go` | `Get(name string) (Backend, error)` registry. Names: `duckduckgo`, `none` |
| `*_test.go` | Frozen-HTML snapshot test for the DDG parser; `httptest.Server` mocks for fetch/robots |

## For AI Agents

### Working In This Directory

- **Zero deps rule applies.** Do NOT add `golang.org/x/net/html` or any third-party HTML parser. The regex pipeline in `fetch.go` and the markers in `duckduckgo.go` are intentional. If a future content type genuinely cannot be parsed without a real HTML parser, raise it as an issue first — the project's flagship-zero-deps stance trumps fetch-quality improvements.
- The DDG parser is a **canary**. If `TestParseDuckDuckGoHTML_Snapshot` fails, DDG changed their HTML — fix the parser AND update the snapshot in the same commit. Do not silently regenerate the snapshot to make a green test.
- robots.txt parsing is intentionally minimal — `User-agent: *` only, plain prefix `Disallow`/`Allow`. We don't need full RFC 9309 conformance for the workaround use case.
- All HTTP calls send a User-Agent of the form `databricks-claude/<version>`. This is what robots.txt sees.

### Testing Requirements

- Run: `go test ./pkg/websearch/...`
- DDG parser tests use a hand-crafted HTML snapshot, not live calls.
- Fetch tests use `httptest.NewServer` for deterministic redirect/timeout/byte-budget coverage.
- Robots tests use mock servers for the cache-and-evict path; fail-open is asserted explicitly.

### Common Patterns

- All public functions take a `context.Context` for cancellation.
- All HTTP transports use a 10s timeout — never `http.DefaultClient`.
- Backend implementations are stateless apart from any per-host caches; safe to use one instance for the lifetime of the proxy.

### Sunset

This package only exists because Databricks FMAPI doesn't yet implement Anthropic's native server-side `web_search`/`web_fetch` tools. When that lands, the `--with-websearch` flag in the root binary will be deprecated for one release and then removed, and this package goes with it. Build accordingly: don't grow features that wouldn't be replaced by the native FMAPI implementation.

## Dependencies

### Internal
- None

### External
- None (pure Go stdlib: `net/http`, `regexp`, `strings`, `sync`, `time`, `context`)
