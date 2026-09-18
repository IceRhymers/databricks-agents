<!-- Parent: ../AGENTS.md -->

# anthropic

## Purpose

Minimal pass-through types for Anthropic's Messages API request body, plus the helpers used by the proxy's server-side tool implementations to detect and rewrite Anthropic's server tools: `web_search`/`web_fetch` (`websearch_handler.go`) and `advisor` (`advisor_*.go`).

This package is intentionally thin: we decode just enough to mutate `tools[]` and `messages[]`, and pass everything else through `json.RawMessage` / `Extras map[string]json.RawMessage` so unknown fields round-trip byte-equivalently. We are NOT building a full Anthropic API client.

## Key Files

| File | Description |
|------|-------------|
| `types.go` | `Request` / `Response` structs with custom `UnmarshalJSON`/`MarshalJSON` for Extras-preserving roundtrip. Websearch helpers: `IsWebSearchTool`, `IsWebFetchTool`, `IsAnnotatedTool`, `RewriteWebSearchTool`, `RewriteWebFetchTool`, `BuildWebSearch*Block`, `ParseWebSearch/FetchInput`. Advisor helpers: `IsAdvisorTool` (type prefix `advisor_`), `ConvertAdvisorTool`, `AdvisorConstraints` + `ParseAdvisorConstraints`, `BuildAdvisorServerToolUseBlock`, `BuildAdvisorSuccessBlock`, `BuildAdvisorErrorBlock`, `ExtractAdvisorResultText`, and the `AdvisorToolVersion` / `AdvisorToolName` / `AdvisorDescription` constants |
| `types_test.go` | Roundtrip-preservation tests, tool-detection tests, rewrite-output snapshot tests |

## For AI Agents

### Working In This Directory

- **Roundtrip preservation is load-bearing.** When `--with-websearch=false`, the proxy must forward a request whose bytes (modulo Authorization header) are identical to what `httputil.ReverseProxy` would have sent. The `Request.MarshalJSON` / `UnmarshalJSON` Extras pattern guarantees this. If you add a typed field, you MUST also remove it from the Extras map on Marshal — failing to do so will double-emit the field and break `httputil.ReverseProxy` parity.
- **Annotation prefix is `[databricks-claude:websearch]`** — included in the `description` field of rewritten client tools. This is how `IsAnnotatedTool` distinguishes our injected tools from user-supplied tools that happen to share the names `web_search` / `web_fetch`. Do NOT match on name alone.
- The detection helpers match on `type` PREFIX (`web_search_`, `web_fetch_`, `advisor_`) so future Anthropic versions like `web_search_20251201` / `advisor_20991231` continue to be detected without code changes.
- **`ConvertAdvisorTool` must emit an EMPTY schema.** The advisor takes no parameters; `input` is always `{}` on the wire. The proxy-enforced constraint fields (`max_uses`, `model`, `max_tokens`) are lifted into `AdvisorConstraints` on the request side and must NEVER appear on the converted tool — `advisor_tools_test.go` asserts this.
- **`AdvisorConstraints.MaxTokensSet` is load-bearing**, not redundant with `MaxTokens != 0`. It records whether the CLIENT explicitly set a cap, which is the sole gate on whether the success block echoes `stop_reason` — mirroring Anthropic's behavior of surfacing truncation only when the caller imposed the limit. An explicit `max_tokens: 0` therefore still sets it.
- **`BuildAdvisorSuccessBlock` omits `stop_reason` entirely when the argument is empty** — absent, not null and not `""`. Emitting the key unconditionally would tell clients the advisor was truncated on every call.
- `ExtractAdvisorResultText` is the replay-side inverse of the block builders and must degrade to `[Advisor unavailable]` / `[Advisor unavailable: <code>]` rather than erroring: replayed history is attacker-adjacent input in the sense that it's whatever the client chose to send back.
- We deliberately do not validate `model`, `max_tokens`, etc. — we only touch what we have to. Anthropic adds fields constantly; the Extras map is our forward-compat shield.

### Testing Requirements

- Run: `go test ./internal/core/proxy/anthropic/...`
- Roundtrip tests use canonicalized JSON comparison (decode → re-encode → semantic compare) to avoid false negatives on key ordering.

### Out of scope

- Full response modelling. `Response` exists for the non-streaming rewrite paths (websearch JSON rewrite, advisor server loop) and stays minimal-plus-Extras for the same forward-compat reason as `Request`. Streaming responses are handled frame-by-frame in the proxy package, not modelled here.
- Validation of message/content-block shapes beyond what's needed to find tool_use / tool_result blocks.
- General-purpose Anthropic SDK functionality. If you find yourself adding a fully-typed `Message` struct, stop and reconsider — `json.RawMessage` is doing the right job here.

## Dependencies

### Internal
- None

### External
- None (pure Go stdlib: `encoding/json`, `bytes`, `strings`)
