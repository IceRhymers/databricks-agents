<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# proxy

## Purpose
HTTP/WebSocket reverse proxy with two built-in routes — inference (`/`) and OTEL (`/otel/`) — plus optional `Config.Routes` for path-prefix dispatch to additional AI Gateway upstreams (e.g. Anthropic + Gemini Native on the same port). Injects fresh Databricks OAuth tokens per-request, supports optional API key authentication, TLS listeners, WebSocket upgrades (for databricks-codex), and includes security checks and log sanitization.

Also hosts two **server-side tool** implementations — Anthropic server tools that Databricks FMAPI does not fulfill, so the proxy fulfills them itself while keeping the client-facing wire contract Anthropic-native: `--with-websearch` (`web_search`/`web_fetch`, local HTTP fulfillment) and `--with-advisor` (the `advisor` tool, fulfilled by a sub-inference). Both follow the same shape: rewrite the server tool into a plain function tool for the upstream, intercept the resulting `tool_use`, fulfill proxy-side, and synthesize `server_tool_use` + `*_tool_result` blocks back to the client.

## Key Files

| File | Description |
|------|-------------|
| `proxy.go` | `Config` struct, `TokenSource` interface, `NewServer` (mux with inference + OTEL routes), `Start` (bind listener with optional TLS), `RecoveryHandler`, `requireAPIKey` middleware, `handleWebSocket` (bidirectional pipe for Codex), `ValidateTLSConfig` |
| `checks.go` | `SecurityChecks` -- startup warnings for running as root or permissive umask |
| `sanitize.go` | `sanitizeLogOutput` -- redacts Bearer tokens, `dapi-*` PATs, and `x-api-key` values from log strings |
| `advisor.go` | `AdvisorSettings` (Enabled/DefaultModel/DefaultMaxTokens), the typed error taxonomy (`ErrTooManyRequests`/`ErrOverloaded`/`ErrPromptTooLong`/`ErrExecutionTimeExceeded`/`ErrUnavailable`) + `advisorErrorCode` mapping, `resolveAdvisorCall` (model/max_tokens resolution chain), `mintAdvisorServerID`, and the compile-time constants (`maxAdvisorTurns = 4`, `advisorTimeout = 60s`, `sseAdvisorPingInterval = 15s`, `advisorMinMaxTokens = 1024`, `advisorDefaultMaxTokens = 2048`) |
| `advisor_tools.go` | `advisorToolSet` (name → constraints, the per-request interception key), `injectAdvisorTool` (auto-injection: enabled + ≥1 tool declared + none present; idempotent, non-mutating), `prepareAdvisorToolsForUpstream` (convert to function tool + capture constraints; STRIP when disabled) |
| `advisor_request.go` | `advisorRequestPrep` + `tryRewriteAdvisorRequest` — the request-side transform: replay expansion (unconditional), auto-injection, tool conversion. Plus `requestWantsStream` / `forceNonStreaming` (the advisor+websearch streaming downgrade) |
| `advisor_call.go` | The advisor sub-inference: `executeAdvisorCall` (direct upstream POST with the proxy's own `TokenSource`, 60s ctx, HTTP status → typed error), `synthesizeAdvisorRequest` (system prompt + `<agent_system_context>` + stripped history + in-progress-turn text + elicitation turn + `tool_choice:none`), `stripThinkingFromMessage`, `extractTextBlocksFromAssistantTurn`, `parseAdvisorSubResponse` |
| `advisor_loop.go` | `advisorLoopContext` + `runAdvisorServerLoop` — the non-streaming continuation loop (classify → enforce `max_uses` → dispatch → accumulate blocks → build + forward continuation), `buildAdvisorContinuationRequest`, `forwardAdvisorContinuation`, `synthesizeTerminalEnvelope` |
| `advisor_stream.go` | `handleAdvisorStreamingInterleave` — one SSE connection across continuation segments. `streamExecutorSegment` (suppress advisor `tool_use`, remap downstream indices, classify the segment end), `runAdvisorWithPings` (15s keepalive while blocked), `emitAdvisorBlockPair` (synthesize call + result at fresh indices), `emitAdvisorTerminal` (never leave a dangling stream), `setIndexInData` |
| `advisor_replay.go` | `expandAdvisorExchanges` — rewrites client-replayed `server_tool_use`/`advisor_tool_result` history into function-tool form, SPLITTING the assistant turn at each exchange so every `tool_use` is immediately followed by its `tool_result` |
| `advisor_test.go` | Resolution chain (model + max_tokens floor/default), `error_code` mapping incl. wrapped errors, id minting, block-builder wire shapes, `ExtractAdvisorResultText` |
| `advisor_tools_test.go` | Type-prefix detection, injection scoping/idempotency/non-mutation, function-tool conversion shape (asserts constraint fields never leak upstream), constraint capture |
| `advisor_request_test.go` | Request-prep matrix (disabled no-op, enabled inject+convert, toolless no-op, disabled-strips, unconditional replay expansion) + END-TO-END tests through a real `inferenceHandler` for both streaming and non-streaming |
| `advisor_loop_test.go` | Server loop: natural end, client-tool stop, single/multi-turn continuation, `max_uses` across turns, error-code degradation matrix, continuation failure, unparseable response, turn cap, `stop_reason` echo gating, thinking stripping |
| `advisor_stream_test.go` | Suppression + index remapping (no gap where the advisor block was), synthesized event shapes (call start/delta/stop, result start/stop with no delta), `max_uses`, failure degradation, continuation-failure terminal synthesis, turn cap |
| `proxy_test.go` | Comprehensive tests for inference routing, OTEL routing, token injection, WebSocket handling, API key auth, error responses, panic recovery |
| `checks_test.go` | Tests for security check warnings |
| `sanitize_test.go` | Tests for sensitive value redaction patterns |

## For AI Agents

### Working In This Directory
- The `TokenSource` interface has a single method: `Token(ctx) (string, error)`. The root package's `TokenProvider` satisfies it.
- **Built-in routes**: `/` for inference (AI Gateway), `/otel/` for telemetry. Path algebra prepends the upstream's base path.
- **Optional `Config.Routes`** (`UpstreamRoute` slice): registers additional path-prefix routes via `mux.Handle(PathPrefix+"/", ...)`. Each route shares the same `inferenceHandler` factory (token injection + WS detect + path-prepend) — only the upstream URL differs and a `StripPrefix` (defaults to `PathPrefix`) is removed from the incoming path before the existing prepend logic runs. `Routes: nil` is byte-identical to behavior before this field existed (sibling consumers `databricks-codex`, `databricks-cursor`, `databricks-opencode` rely on this).
- **WebSocket support** exists for databricks-codex (Codex CLI), not for Claude Code (which uses HTTP+SSE). The `isWebSocketUpgrade` check is passive -- zero overhead for non-upgrade requests.
- `FlushInterval: -1` on both reverse proxies enables streaming (SSE).
- All routes are wrapped in `RecoveryHandler` for panic safety (returns HTTP 502).
- `requireAPIKey` middleware wraps the entire mux when `APIKey` is non-empty.
- `UCMetricsTable` may be empty (e.g., databricks-codex) -- in that case the `X-Databricks-UC-Table-Name` header is omitted for metrics paths.
- OTEL route distinguishes logs vs metrics by checking if the path contains `/v1/logs`.

### Working on the advisor server-side tool
- **The wire contract is asymmetric and that is the whole point.** Toward the client it is Anthropic-native (`advisor_*` tool declaration in, `server_tool_use` + `advisor_tool_result` block pair out). Toward the upstream it is a plain function tool with an empty schema. The proxy performs the execution loop in between. Never let `advisor_*` types, `server_tool_use`, or `advisor_tool_result` reach the upstream — it 400s.
- **Never fail the outer request because the advisor failed.** Every failure path (sub-inference error, continuation forward failure, unparseable response, turn cap) returns a well-formed response carrying an error-shaped `advisor_tool_result` plus whatever was accumulated. The continuation also carries `[Advisor unavailable: <code>]` as the `tool_result` text so the executor model can react rather than the turn dying. Agents are long-running; fail-open is correct for an advisory dependency.
- **Strip thinking blocks everywhere in the advisor leg.** `thinking` / `redacted_thinking` signatures are model- and turn-bound; replaying them into a different inference context produces `thinking.signature: Field required` 400s. Strip from the history (`stripThinkingFromMessage`), from the in-progress turn (`extractTextBlocksFromAssistantTurn` keeps text only), and from continuation requests (`buildAdvisorContinuationRequest` also drops the `thinking` config key).
- **The in-progress turn's `tool_use` blocks are DANGLING.** No `tool_result` exists for them yet, and an unpaired `tool_use` 400s. The advisor sub-inference prompt keeps only `text` blocks from that turn.
- **`max_uses` is a proxy-enforced, CROSS-TURN counter** (`advisorLoopContext.usesByTool`). The upstream only sees a function tool, so the gateway owns the cap. Enforcement happens PRE-dispatch — a capped call emits `error_code: max_uses_exceeded` and never reaches the upstream.
- **`tool_use` must be immediately followed by its `tool_result`.** This is the single most common 400 when implementing server tools. `expandAdvisorExchanges` handles it in one well-tested transform by SPLITTING the assistant turn at each advisor exchange; `advisor_replay_test.go`'s `assertToolUsePairing` helper enforces the invariant. Do not hand-roll this elsewhere.
- **Replay expansion is UNCONDITIONAL** — it runs even when the feature is disabled, because a client may still replay advisor blocks from a session recorded while it was on.
- **Streaming: suppress-then-synthesize, never mutate in place.** Drop the upstream's raw advisor `tool_use` frames, remap passthrough indices so client-facing indices stay contiguous (no gap where the suppressed block was), and synthesize clean `server_tool_use` / `advisor_tool_result` events at fresh monotonically increasing indices. Keep `ping`s flowing while the blocking sub-inference runs, and always emit a terminal `message_delta` + `message_stop` so the client never sees a dangling stream.
- **`tool_use` blocks are terminal in current Anthropic streams** — once `content_block_stop` fires for a `tool_use`, no further text deltas arrive for that turn. The interleave relies on this (the segment streams to completion, THEN the advisor runs, THEN the pair is emitted). Same invariant the websearch SSE path documents. If Anthropic starts interleaving text + `tool_use`, revisit the ordering.
- **Advisor + websearch streaming together is a documented limitation** — such requests are DOWNGRADED to non-streaming (`forceNonStreaming`) and go through the server loop.
- **The sub-inference is billed as an independent request.** The parent's `usage` is NOT inflated. `tool_choice:{"type":"none"}` plus advisor-tool stripping is the recursion guard — the advisor sees the executor's tool context but cannot call anything, including itself.
- **Exactly one `message_start` across all segments.** The continuation segments must not emit a second one (`skipMessageStart`).

### Testing Requirements
- Run: `go test ./internal/core/proxy/...`
- Advisor tests use `upstreamScript` / `sseUpstreamScript` (in `advisor_loop_test.go` / `advisor_request_test.go`) to script an ordered sequence of upstream responses and capture the request bodies for assertion. `sseUpstreamScript` dispatches on the request's `stream` flag so one fake upstream serves both the streaming executor segments and the non-streaming advisor sub-inference, the way a real upstream does.
- The end-to-end advisor tests drive a REAL `inferenceHandler` via `NewServer` + `httptest`, not just the helpers — helper-level tests passing while composition is broken is the known failure mode for this kind of interception layer.
- Tests use `httptest.NewServer` and mock `TokenSource` implementations.
- `getuid` in `checks.go` is overridable for testing root-detection logic.

### Common Patterns
- Token injection: both `Authorization: Bearer` and `x-api-key` headers are set on every proxied request.
- Verbose logging: error responses (4xx+) are logged with first 500 chars, sanitized via `sanitizeLogOutput`.

## Dependencies

### Internal
- None

### External
- `net/http/httputil`, `crypto/tls`, `net`, `net/url` (stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
