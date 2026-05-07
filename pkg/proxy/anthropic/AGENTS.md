<!-- Parent: ../AGENTS.md -->

# anthropic

## Purpose

Minimal pass-through types for Anthropic's Messages API request body, plus the helpers used by `pkg/proxy/websearch_handler.go` to detect and rewrite Anthropic's server-side `web_search` and `web_fetch` tool entries.

This package is intentionally thin: we decode just enough to mutate `tools[]` and `messages[]`, and pass everything else through `json.RawMessage` / `Extras map[string]json.RawMessage` so unknown fields round-trip byte-equivalently. We are NOT building a full Anthropic API client.

## Key Files

| File | Description |
|------|-------------|
| `types.go` | `Request` struct with custom `UnmarshalJSON`/`MarshalJSON` for Extras-preserving roundtrip; helpers `IsWebSearchTool`, `IsWebFetchTool`, `IsAnnotatedTool`, `RewriteWebSearchTool`, `RewriteWebFetchTool` |
| `types_test.go` | Roundtrip-preservation tests, tool-detection tests, rewrite-output snapshot tests |

## For AI Agents

### Working In This Directory

- **Roundtrip preservation is load-bearing.** When `--with-websearch=false`, the proxy must forward a request whose bytes (modulo Authorization header) are identical to what `httputil.ReverseProxy` would have sent. The `Request.MarshalJSON` / `UnmarshalJSON` Extras pattern guarantees this. If you add a typed field, you MUST also remove it from the Extras map on Marshal — failing to do so will double-emit the field and break `httputil.ReverseProxy` parity.
- **Annotation prefix is `[databricks-claude:websearch]`** — included in the `description` field of rewritten client tools. This is how `IsAnnotatedTool` distinguishes our injected tools from user-supplied tools that happen to share the names `web_search` / `web_fetch`. Do NOT match on name alone.
- The detection helpers match on `type` PREFIX (`web_search_` and `web_fetch_`) so future Anthropic versions like `web_search_20251201` continue to be detected without code changes.
- We deliberately do not validate `model`, `max_tokens`, etc. — we only touch what we have to. Anthropic adds fields constantly; the Extras map is our forward-compat shield.

### Testing Requirements

- Run: `go test ./pkg/proxy/anthropic/...`
- Roundtrip tests use canonicalized JSON comparison (decode → re-encode → semantic compare) to avoid false negatives on key ordering.

### Out of scope

- Response-body parsing. The proxy streams responses verbatim; only requests are mutated.
- Validation of message/content-block shapes beyond what's needed to find tool_use / tool_result blocks.
- General-purpose Anthropic SDK functionality. If you find yourself adding a fully-typed `Message` struct, stop and reconsider — `json.RawMessage` is doing the right job here.

## Dependencies

### Internal
- None

### External
- None (pure Go stdlib: `encoding/json`, `bytes`, `strings`)
