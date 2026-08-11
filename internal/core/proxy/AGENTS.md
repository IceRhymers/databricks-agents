<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# proxy

## Purpose
HTTP/WebSocket reverse proxy with two built-in routes — inference (`/`) and OTEL (`/otel/`) — plus optional `Config.Routes` for path-prefix dispatch to additional AI Gateway upstreams (e.g. Anthropic + Gemini Native on the same port). Injects fresh Databricks OAuth tokens per-request, supports optional API key authentication, TLS listeners, WebSocket upgrades (for databricks-codex), and includes security checks and log sanitization.

## Key Files

| File | Description |
|------|-------------|
| `proxy.go` | `Config` struct, `TokenSource` interface, `NewServer` (mux with inference + OTEL routes), `Start` (bind listener with optional TLS), `RecoveryHandler`, `requireAPIKey` middleware, `handleWebSocket` (bidirectional pipe for Codex), `ValidateTLSConfig` |
| `models_reroute.go` | `modelsRerouteHandler` + `transformModelsToOpenAI` — optional OpenAI-shaped `GET /models` discovery. Registered only when `Config.OpenAIModelsUpstream` is set; fetches `{host}/ai-gateway/v1/models` and rewrites the Databricks body to the OpenAI `{id, object:"model", ...}` shape. Non-GET `/models` and `/models/{id}` fall through to the inference catch-all. |
| `checks.go` | `SecurityChecks` -- startup warnings for running as root or permissive umask |
| `sanitize.go` | `sanitizeLogOutput` -- redacts Bearer tokens, `dapi-*` PATs, and `x-api-key` values from log strings |
| `proxy_test.go` | Comprehensive tests for inference routing, OTEL routing, token injection, WebSocket handling, API key auth, error responses, panic recovery |
| `checks_test.go` | Tests for security check warnings |
| `sanitize_test.go` | Tests for sensitive value redaction patterns |

## For AI Agents

### Working In This Directory
- The `TokenSource` interface has a single method: `Token(ctx) (string, error)`. The root package's `TokenProvider` satisfies it.
- **Built-in routes**: `/` for inference (AI Gateway), `/otel/` for telemetry. Path algebra prepends the upstream's base path.
- **Optional `Config.Routes`** (`UpstreamRoute` slice): registers additional path-prefix routes via `mux.Handle(PathPrefix+"/", ...)`. Each route shares the same `inferenceHandler` factory (token injection + WS detect + path-prepend) — only the upstream URL differs and a `StripPrefix` (defaults to `PathPrefix`) is removed from the incoming path before the existing prepend logic runs. `Routes: nil` is byte-identical to behavior before this field existed (sibling consumers `databricks-codex`, `databricks-cursor`, `databricks-opencode` rely on this).
- **Optional `Config.OpenAIModelsUpstream`** (OpenAI-shaped launchers only): when non-empty, registers `GET /models` at the exact mux path `/models` (see `models_reroute.go`). OpenAI-shaped clients (e.g. JetBrains IntelliJ) enumerate models via `GET /models`, but the `/ai-gateway/openai/v1` upstream has no `/models` endpoint (Databricks 400s on `/ai-gateway/openai/v1/models`). Set the field to the working `{host}/ai-gateway/v1/models` URL; the handler fetches it with a fresh token and transforms the body to the OpenAI shape. Empty (claude, opencode) = route not registered, byte-identical to before.
- **WebSocket support** exists for databricks-codex (Codex CLI), not for Claude Code (which uses HTTP+SSE). The `isWebSocketUpgrade` check is passive -- zero overhead for non-upgrade requests.
- `FlushInterval: -1` on both reverse proxies enables streaming (SSE).
- All routes are wrapped in `RecoveryHandler` for panic safety (returns HTTP 502).
- `requireAPIKey` middleware wraps the entire mux when `APIKey` is non-empty.
- `UCMetricsTable` may be empty (e.g., databricks-codex) -- in that case the `X-Databricks-UC-Table-Name` header is omitted for metrics paths.
- OTEL route distinguishes logs vs metrics by checking if the path contains `/v1/logs`.

### Testing Requirements
- Run: `go test ./pkg/proxy/...`
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
