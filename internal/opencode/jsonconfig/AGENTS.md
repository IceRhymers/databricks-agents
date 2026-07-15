<!-- Parent: ../AGENTS.md -->

# internal/opencode/jsonconfig

## Purpose

JSONC-aware OpenCode config manager. Reads and patches `~/.config/opencode/opencode.json`, supporting JSON with comments and trailing commas. Implements surgical patching only — opencode is a patch-and-leave-it persistent config: we own BOTH `provider.databricks-proxy` (Anthropic via `@ai-sdk/anthropic` on `/v1`) AND `provider.databricks-gemini-proxy` (Gemini Native via `@ai-sdk/google` on `/v1beta`) and rewrite them idempotently on every run that `NeedsConfig` reports stale. No backup, no restore.

## Key Files

| File | Purpose |
|------|---------|
| `jsonconfig.go` | Config type and methods: readConfig (JSONC stripping), Patch (inject proxy settings), NeedsConfig (idempotency check), UpdateProxyURL, AddPlugin/RemovePlugin |
| `stripjsonc.go` | `stripJSONC` — stdlib JSONC stripper (comments + trailing commas), string/escape aware; zero external deps |
| `jsonconfig_test.go` | Tests: JSONC parsing, patch behaviour, NeedsConfig states, plugin add/remove, `stripJSONC` edge cases |

## Core Concepts

### Managed Keys

The config manager owns and patches:
- `model` — active model identifier (set only if --model explicit or absent)
- `provider.databricks-proxy.npm` — `@ai-sdk/anthropic`
- `provider.databricks-proxy.options.baseURL` — local proxy address + `/v1`
- `provider.databricks-proxy.options.apiKey` — placeholder key (real auth is injected by the proxy)
- `provider.databricks-proxy.models[…]` — registered Databricks Claude model entries
- `provider.databricks-gemini-proxy.npm` — `@ai-sdk/google`
- `provider.databricks-gemini-proxy.options.baseURL` — local proxy address + `/v1beta`
- `provider.databricks-gemini-proxy.options.apiKey` — placeholder key (SDK requires non-empty)
- `provider.databricks-gemini-proxy.options.headers.Authorization` — `Bearer <placeholder>` to override the SDK's default `x-goog-api-key` auth scheme; the proxy rewrites with the live token
- `provider.databricks-gemini-proxy.models[…]` — registered Databricks Gemini model entries

### Surgical Patching

`Patch` only touches the keys listed above. User keys at any other path
(e.g. `provider.openai`, `commands`, `agents`, `theme`, `mcpServers`) are
preserved verbatim. The provider map is read, mutated, and written back
in one atomic write — never wholesale replaced.

### JSONC Support

```go
// Strips comments and trailing commas before JSON parsing
clean := stripJSONC(data)
```

`stripJSONC` is a pure-stdlib, string/escape-aware stripper (in
`stripjsonc.go`) — no external dependency. It runs two byte-scans:

1. `stripComments` removes `//` line comments and `/* … */` block
   comments, tracking in-string state so comment markers inside string
   literals (e.g. `"https://x.com"`, `"a /* b */ c"`) are preserved and
   backslash escapes (`\"`, `\\`) are respected. Newlines terminating
   line comments are kept.
2. `removeTrailingCommas` deletes any comma followed — skipping only
   whitespace — by `}` or `]`, again respecting string literals. Running
   after comment stripping guarantees the `,/*c*/]` and `, // note\n}`
   cases collapse to a legal trailing comma removal.

Allows users to write:
```jsonc
{
  // OpenCode configuration
  "model": "my-model",  // with trailing comma
  "provider": { ... }
}
```

Note: only comments and trailing commas are stripped. JSON5 features
(unquoted keys, single quotes) are not supported.

### Idempotency via NeedsConfig

`NeedsConfig(proxyURL)` returns true when (any of):
- the config file is missing
- the `provider` section is absent
- the `provider.databricks-proxy` block is absent OR baseURL ≠ `proxyURL + "/v1"` OR `options.apiKey` is missing OR `npm` ≠ `@ai-sdk/anthropic`
- the `provider.databricks-gemini-proxy` block is absent OR baseURL ≠ `proxyURL + "/v1beta"` OR `options.apiKey` is missing OR `npm` ≠ `@ai-sdk/google`

Callers (the root `EnsureConfig` in `config.go`) skip `Patch` when
`NeedsConfig` is false, making startup a no-op for already-configured
sessions.

### Atomic Writes

```go
// 1. Create temp file in same directory
tmp, _ := os.CreateTemp(dir, ".config-*.tmp")
// 2. Set restrictive permissions (0o600)
os.Chmod(tmpPath, 0o600)
// 3. Write data
tmp.Write(data)
tmp.Close()
// 4. Atomic rename
os.Rename(tmpPath, path)
```

Ensures config is never observed in a half-written state.

## Methods

### Config Lifecycle

| Method | Purpose |
|--------|---------|
| `New(dir)` | Create Config for `<dir>/opencode.json` |
| `NewWithPath(configPath)` | Create with explicit path (testing) |
| `Path()` | Return the config file path |

### Patching

| Method | Purpose |
|--------|---------|
| `Patch(proxyURL, modelName, apiKey, forceModel)` | Inject BOTH databricks-proxy and databricks-gemini-proxy providers; set model only if forceModel or absent |
| `NeedsConfig(proxyURL)` | Report whether a Patch is required (drift detection on either provider) |
| `UpdateProxyURL(proxyURL)` | Change baseURL on BOTH providers (anthropic to `proxyURL+"/v1"`, gemini to `proxyURL+"/v1beta"`); no provider re-injection |

### Plugin Management

| Method | Purpose |
|--------|---------|
| `AddPlugin(pluginPath)` | Append to top-level `plugin` array (idempotent) |
| `RemovePlugin(pluginPath)` | Remove from `plugin` array; drop the key if empty |

### Internal Helpers

| Method | Purpose |
|--------|---------|
| `readConfig()` | Read, JSONC-strip, parse JSON; return empty map if missing |
| `writeConfig(config)` | Marshal and atomically write config |
| `atomicWrite(path, data)` | Temp file + rename |
| `stripJSONC(data)` | Strip comments + trailing commas (stdlib, string/escape aware) |

## For AI Agents

### Testing the Config Manager

```bash
go test ./internal/opencode/jsonconfig/... -v
```

Key test scenarios in `jsonconfig_test.go`:
- JSONC parsing (comments, trailing commas)
- `stripJSONC` edge cases (line/block comments, multi-line blocks, trailing commas before `}`/`]`, comment-like text and `//` URLs inside strings, escaped quotes, comma-then-comment-then-bracket, empty/whitespace input)
- Patch with `forceModel` true/false
- Patch preserves unrelated user keys (commands, agents, other providers)
- `NeedsConfig` for missing file, missing provider, mismatched URL
- `UpdateProxyURL` changes baseURL without altering model
- Plugin add/remove idempotency

### Common Patterns

1. **Configuring a session** (the only live caller, `EnsureConfig`):
   ```go
   cfg := jsonconfig.New(cfgDir)
   if cfg.NeedsConfig(proxyURL) {
       cfg.Patch(proxyURL, modelName, apiKey, forceModel)
   }
   ```

2. **Hooks** (registering a plugin):
   ```go
   cfg.AddPlugin(pluginPath)
   ```

### Important Notes

- **No backup, no restore**: opencode runs the proxy on a stable port and the config persists pointing at it across runs. Crash recovery is not needed and not implemented; previous sidecar/sentinel machinery has been removed (see issue #74).
- **forceModel parameter**: if true, `model` is always written; if false, only set when absent (preserves user choice).
- **Permissions**: temp files written with `0o600` (user read/write only).
- **JSONC limitations**: only strips comments and trailing commas.

## Dependencies

| Dependency | Purpose |
|------------|---------|
| Go stdlib | `encoding/json`, `os`, `path/filepath`, `fmt` — plus the in-package `stripJSONC` stripper (no external deps) |
