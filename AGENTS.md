<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# databricks-claude

## Purpose
Transparent proxy wrapper for Claude Code that auto-refreshes Databricks OAuth tokens via the Databricks CLI. Intercepts Claude Code's API calls, injects fresh workspace tokens per-request, and optionally routes OpenTelemetry metrics/logs through the Databricks OTEL endpoint. Zero external Go dependencies -- pure stdlib only.

## Key Files

> All `*.go` source files listed below now live under `cmd/databricks-claude/` (the `main` package, relocated from the repo root in #197). `Makefile`, `go.mod`, `CLAUDE.md`, and `README.md` remain at the repo root. The Go module is `github.com/IceRhymers/databricks-agents`.

| File | Description |
|------|-------------|
| `main.go` | Thin CLI entry point (#200): subcommand dispatch (completion/update/serve/desktop/setup/config/hooks/doctor + credential-helper alias), `parseArgs`, help/version, then `buildClaudeLaunchPlan(a)` → `core.Run(ClaudeProfile(), plan, a.ClaudeArgs)`. Retains shared package-`main` helpers (`envBlock`, `parseArgs`, `handleHelp`, `defaultModelRouting`, `launchModelRouting`, `databricksFullSetupEnv`, `buildUpdaterConfig`, `handlePrintEnv`, persistent-config helpers). No proxy/port/refcount/child logic remains here. |
| `launch_claude.go` | `buildClaudeLaunchPlan(a *Args) (core.LaunchPlan, error)` (#200): all claude-specific wrapper pre-flight — logging setup, settings.json read, profile resolution, auth (browser-login fallback, MUST precede token seed), startup security warnings, upstream/OTEL discovery, token seeding, port resolution, settings→state OTEL-table migration, TLS validation, websearch backend — plus the proxyURL-dependent `BuildEnv` closure (OTEL/`CLAUDE_*` env emission). Returns a neutral `core.LaunchPlan` for `core.Run`. |
| `proxy.go` | Thin facade over `internal/core/proxy` (`ProxyConfig`, `NewProxyServer`, `StartProxy`, `recoveryHandler`). After #200 the launch path calls `proxy.NewServer` directly inside `core.Run`; this facade is retained for `proxy_test.go` cmd-level coverage. |
| `token.go` | Facade over `internal/core/tokencache`: implements `databricksFetcher` (shells out to `databricks auth token`), host discovery via `databricks auth env`, and AI Gateway URL construction (`{host}/ai-gateway/anthropic`). Consumed by `buildClaudeLaunchPlan` (stays claude-side; a future issue may promote it into `internal/core`). |
| `process.go` | Wraps `internal/core/childproc`: `ForwardSignals`. (`RunChild` removed in #200 — the launch path now calls `childproc.Run` directly inside `core.Run` with `BinaryName` from `profile.ChildBinary` and the managed marker from `LaunchPlan.ManagedEnvVar`.) |
| `state.go` | `persistentState` struct and helpers for `~/.claude/.databricks-claude.json` (profile, port, CLI path, OTEL table names) |
| `hooks.go` | Session hook install/uninstall: `installHooks`, `uninstallHooks` |
| `ensureconfig.go` | Bootstrap helpers for first-run settings patching |
| `commands.go` | Source-of-truth `rootCommand` declaration plus `desktopCommand` / `setupCommand` / `configCommand` / `serveCommand` tree nodes (drives parsing, help, completion) |
| `config.go` | `config` subcommand runner: dispatches `config otel enable\|disable`, `config websearch enable\|disable`, `config write`, `config show`. Pure resolvers `resolveConfigOTEL` / `resolveConfigWebSearch` for testability. `config write` is a discovery-time writer: runs `pkg/modeldiscovery.Discover` and persists the resolved `ModelSet` into state. |
| `doctor.go` | `doctor` subcommand: non-interactive diff of settings.json model pins vs. `pkg/modeldiscovery.Discover` output (`diffModelRouting`, pure/testable). Read-only by default; `--fix` persists the discovered models to state and rewrites settings.json through `bootstrapSettings`. |
| `completion_flags.go` | `flagDefs` slice shared by shell completion and flag parsing |
| `databrickscfg.go` | Reads `~/.databrickscfg` section headers for profile completion |
| `desktop_config.go` | `desktop` subcommand: `generate-config` writing `.mobileconfig`, `.reg`, and `.json` artifacts for Claude Desktop; credential-helper alias dispatch |
| `desktop_trust.go` | `generate-trust-profile` subcommand for MDM trust profile generation |
| `setup.go` | `setup` subcommand: idempotent auth bootstrap for fleet init scripts — resolves and persists the profile, then runs `databricks auth login` if not already authenticated (or always with `--force`) |
| `serve.go` | `serve` subcommand dispatcher: `runServe(args)` routes to `runServeInstall` (sub-subcommands), `runServeSession` (--session-mode), or the inline daemon body (--daemon). `determineServeMode` enforces the required-explicit-mode invariant; `buildServeProxyConfig(state, resolved, mode)` is the shared `proxy.Config` factory across both lifecycle policies. |
| `serve_session.go` | `runServeSession(args)` body for `serve --session-mode`. Refcounted, `/shutdown` route, idle-timeout, settings.json restore. Was the `--headless` root flag prior to #174. |
| `serve_install.go` | Cross-platform dispatcher for `serve install`/`uninstall`/`status` sub-subcommands; flag parsing, binary resolution, status pretty-print |
| `serve_install_darwin.go` | macOS LaunchAgent plist rendering and `launchctl` orchestration (build tag: `darwin`) |
| `serve_install_linux.go` | Linux systemd user unit rendering and `systemctl --user` orchestration (build tag: `linux`) |
| `serve_install_windows.go` | Windows Scheduled Task creation via `schtasks.exe` (build tag: `windows`) |
| `serve_install_other.go` | Stub returning "unsupported platform" for non-darwin/linux/windows (build tag: `!darwin && !windows && !linux`) |
| `desktop_config_test.go` | Tests for `buildMobileconfig`, `buildRegFile`, `buildDevModeJSON`, `writeDesktopConfigByPath`, `guardDevJSONOutputPath`, `writeFileAtomic`, install-instruction routing, and model-list consistency across all three artifacts |
| `main_test.go` | Tests for `parseArgs`, `handlePrintEnv`, persistent config, `deriveLogsTable`, full integration scenarios |
| `config_test.go` | Tests for `config` subcommand parity, OTEL orchestration matrix, websearch resolver, state-preservation invariant on `config otel disable` |
| `doctor_test.go` | Tests for `diffModelRouting`'s status matrix (ok/drift/stale-legacy/unresolved/new) |
| `process_test.go` | Tests for `ForwardSignals` signal forwarding and child exit-code propagation |
| `proxy_test.go` | Tests for inference and OTEL proxy routing, token injection, panic recovery |
| `token_test.go` | Tests using helper binaries compiled at test time to mock the `databricks` CLI |
| `state_test.go` | Tests for persistent state load/save |
| `hooks_test.go` | Tests for hook install/uninstall |
| `serve_install_test.go` | Cross-platform tests for dispatcher, flag parsing, status pretty-print (no build tag) |
| `serve_install_darwin_test.go` | macOS plist template rendering and `plutil -lint` validation (build tag: `darwin`) |
| `serve_install_linux_test.go` | Linux systemd unit template rendering tests (build tag: `linux`) |
| `serve_install_windows_test.go` | Windows schtasks `/TR` argument building tests (build tag: `windows`) |
| `Makefile` | Build, install, test, cross-compile, lint targets |
| `go.mod` | Module declaration (`github.com/IceRhymers/databricks-agents`, Go 1.22, zero deps) |
| `CLAUDE.md` | Project-level AI agent instructions |
| `README.md` | User-facing documentation |

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `cmd/databricks-claude/` | CLI entry point (`main` package), relocated from the repo root in #197. Builds the `databricks-claude` binary. |
| `internal/cmd/` | Pre-existing command-tree parsing/help/completion library (distinct from `cmd/databricks-claude/`) |
| `internal/core/` | The shared tool-agnostic engine (proxy, tokencache, authcheck, childproc, state, headless, lifecycle, portbind, health, refcount, updater, completion, cli). Promoted from `pkg/` in #198; module-private (see `internal/core/doc.go`). `run.go` (#200) adds `LaunchPlan` + `Run(profile.Profile, LaunchPlan, []string) int` — the wrapper-mode launch engine (bind → serve/watch → BuildEnv → Patch → child → refcount teardown) shared by all launchers. |
| `internal/profile/` | Placeholder for profile resolution (epic #196); empty `doc.go` skeleton added in #197, filled by #D/#E |
| `pkg/` | Only the Claude/Anthropic-coupled libraries remain after #198 (see `pkg/AGENTS.md`): `modeldiscovery`, `mdmprofile`, `websearch` |
| `pkg/mdmprofile/` | Platform-specific readers for MDM-managed preferences (darwin: plist, windows: registry, other: stub). Used by the credential helper to resolve the Databricks profile on endpoint machines. |
| `.github/` | GitHub Actions CI configuration (see `.github/AGENTS.md`) |
| `.claude/` | Claude Code project configuration (settings only, no AGENTS.md needed) |

## For AI Agents

### Working In This Directory
- **Zero external dependencies** -- do not add any third-party imports. All code must use the Go stdlib only.
- The CLI entry-point package is `main`, located at `cmd/databricks-claude/`. The `.go` files there are thin facades that delegate to the shared engine in `internal/core/` (plus the remaining Claude-coupled `pkg/*` packages). Keep them thin.
- `main.go` is a thin launcher: it dispatches subcommands, parses flags, then delegates wrapper-mode to `buildClaudeLaunchPlan` (`launch_claude.go`, claude pre-flight) + `core.Run` (`internal/core/run.go`, the generic bind/serve/patch/child lifecycle). `token.go` owns Databricks auth; `proxy.go` is a test-facing facade over `internal/core/proxy`. Keep claude-specific launch assembly in `launch_claude.go` and tool-agnostic lifecycle in `internal/core`.
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

- `internal/core/authcheck` -- pre-flight auth verification
- `internal/core/childproc` -- child process management
- `internal/core/proxy` -- HTTP/WebSocket reverse proxy, API key auth, TLS, security checks, log sanitization
- `internal/core/tokencache` -- generic token caching
- `internal/core/{state,headless,lifecycle,portbind,health,refcount,updater,completion,cli}` -- the rest of the shared engine
- `internal/cmd` -- command-tree parsing/help/completion library
- `pkg/modeldiscovery`, `pkg/mdmprofile`, `pkg/websearch` -- the remaining Claude-coupled libraries (not promoted into core)

### External
- None (pure Go stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
