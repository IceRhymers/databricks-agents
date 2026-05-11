<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# databricks-claude

## Purpose
Transparent proxy wrapper for Claude Code that auto-refreshes Databricks OAuth tokens via the Databricks CLI. Intercepts Claude Code's API calls, injects fresh workspace tokens per-request, and optionally routes OpenTelemetry metrics/logs through the Databricks OTEL endpoint. Zero external Go dependencies -- pure stdlib only.

## Key Files

| File | Description |
|------|-------------|
| `main.go` | CLI entry point: flag parsing, config resolution from `~/.claude/settings.json` and persistent config, token seeding, AI Gateway discovery, proxy startup, settings patching, child launch, and settings restore on exit |
| `proxy.go` | Thin facade over `pkg/proxy`: defines `ProxyConfig`, wires up `NewProxyServer` and `StartProxy` |
| `token.go` | Facade over `pkg/tokencache`: implements `databricksFetcher` (shells out to `databricks auth token`), host discovery via `databricks auth env`, and AI Gateway URL construction (`{host}/ai-gateway/anthropic`) |
| `process.go` | Wraps `pkg/childproc`: `RunChild`, `ForwardSignals` |
| `state.go` | `persistentState` struct and helpers for `~/.claude/.databricks-claude.json` (profile, port, CLI path, OTEL table names) |
| `hooks.go` | Session hook install/uninstall: `installHooks`, `uninstallHooks` |
| `ensureconfig.go` | Bootstrap helpers for first-run settings patching |
| `completion_flags.go` | `flagDefs` slice shared by shell completion and flag parsing |
| `databrickscfg.go` | Reads `~/.databrickscfg` section headers for profile completion |
| `desktop_config.go` | `desktop` subcommand: `generate-config` writing `.mobileconfig`, `.reg`, and `.json` artifacts for Claude Desktop; credential-helper alias dispatch |
| `desktop_trust.go` | `generate-trust-profile` subcommand for MDM trust profile generation |
| `setup.go` | `setup` subcommand: idempotent auth bootstrap for fleet init scripts — resolves and persists the profile, then runs `databricks auth login` if not already authenticated (or always with `--force`) |
| `desktop_config_test.go` | Tests for `buildMobileconfig`, `buildRegFile`, `buildDevModeJSON`, `writeDesktopConfigByPath`, `guardDevJSONOutputPath`, `writeFileAtomic`, install-instruction routing, and model-list consistency across all three artifacts |
| `main_test.go` | Tests for `parseArgs`, `handlePrintEnv`, persistent config, `deriveLogsTable`, full integration scenarios |
| `process_test.go` | Tests for `RunChild`, signal forwarding, exit code propagation |
| `proxy_test.go` | Tests for inference and OTEL proxy routing, token injection, panic recovery |
| `token_test.go` | Tests using helper binaries compiled at test time to mock the `databricks` CLI |
| `state_test.go` | Tests for persistent state load/save |
| `hooks_test.go` | Tests for hook install/uninstall |
| `Makefile` | Build, install, test, cross-compile, lint targets |
| `go.mod` | Module declaration (`github.com/IceRhymers/databricks-claude`, Go 1.22, zero deps) |
| `CLAUDE.md` | Project-level AI agent instructions |
| `README.md` | User-facing documentation |

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `pkg/` | Reusable library packages extracted from the monolithic main (see `pkg/AGENTS.md`) |
| `pkg/mdmprofile/` | Platform-specific readers for MDM-managed preferences (darwin: plist, windows: registry, other: stub). Used by the credential helper to resolve the Databricks profile on endpoint machines. |
| `.github/` | GitHub Actions CI configuration (see `.github/AGENTS.md`) |
| `.claude/` | Claude Code project configuration (settings only, no AGENTS.md needed) |

## For AI Agents

### Working In This Directory
- **Zero external dependencies** -- do not add any third-party imports. All code must use the Go stdlib only.
- The root package is `main`. The six `.go` files here are thin facades that delegate to `pkg/` sub-packages. Keep them thin.
- `main.go` owns flag parsing and orchestration flow. `process.go` owns settings.json lifecycle. `token.go` owns Databricks auth. `proxy.go` owns HTTP proxy wiring.
- `lock.go` and `registry.go` are pure type-alias forwarding files -- they exist only for backward compatibility with root-level tests.

### Testing Requirements
- Run `make test` or `go test ./... -v`
- Tests use **helper binaries compiled at test time** to mock the `databricks` CLI (see `buildHelperBinary`, `buildSlowBinary`, `buildAuthEnvBinary` in `token_test.go`)
- `warmToken()` in `proxy_test.go` pre-loads the token cache to avoid subprocess calls during proxy tests
- `process_test.go` creates temp directories for settings.json isolation

### Common Patterns
- **Atomic file writes**: all JSON writes use temp-file-then-`os.Rename` in the same directory
- **Settings.json lifecycle**: `SaveAndOverwrite` / `FullSetup` saves originals, patches env block, `Restore` puts them back (smart handoff to surviving sessions)
- **Token caching**: mutex-guarded, 5-minute refresh buffer, fallback to last good token on error

### Critical Safety Rules
- **Never break settings.json restore** -- a botched restore leaves the user's Claude config pointing at a dead proxy
- **OTEL key persistence** -- when `otelKeysPersistent` is true, Restore must skip OTEL keys
- **Session handoff** -- when multiple instances run concurrently, the exiting session hands `ANTHROPIC_BASE_URL` to the most recent survivor

## Dependencies

### Internal
- `pkg/authcheck` -- pre-flight auth verification
- `pkg/childproc` -- child process management
- `pkg/filelock` -- file-based locking
- `pkg/proxy` -- HTTP/WebSocket reverse proxy, API key auth, TLS, security checks, log sanitization
- `pkg/registry` -- session tracking
- `pkg/settings` -- settings.json read/write/restore engine
- `pkg/tokencache` -- generic token caching

### External
- None (pure Go stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
