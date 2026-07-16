<!-- Generated: 2026-04-06 | Updated: 2026-07-15 -->

# databricks-agents

## Purpose
Monorepo of transparent proxy wrappers that auto-refresh Databricks OAuth tokens via the Databricks CLI, intercepting each wrapped tool's API calls, injecting fresh workspace tokens per-request, and optionally routing OpenTelemetry metrics/logs through the Databricks OTEL endpoint. Three per-tool launchers today: `databricks-claude` (wraps Claude Code), `databricks-codex` (wraps the OpenAI Codex CLI, folded in via #201), and `databricks-opencode` (wraps the OpenCode CLI, folded in via #202), plus the `databricks-agents` multiplexer (`ucode`-style dispatcher, #203) that runs `databricks-agents <claude|codex|opencode> ...` by exec-ing the matching per-tool binary. Zero external Go dependencies -- pure stdlib only.

## Key Files (`cmd/databricks-claude/`)

> All `*.go` source files listed below now live under `cmd/databricks-claude/` (the `main` package, relocated from the repo root in #197). `cmd/databricks-codex/` (see its own table below) is a sibling launcher. `Makefile`, `go.mod`, `CLAUDE.md`, and `README.md` remain at the repo root. The Go module is `github.com/IceRhymers/databricks-agents`.

| File | Description |
|------|-------------|
| `main.go` | Thin CLI entry point (#200): subcommand dispatch (completion/update/serve/desktop/setup/config/hooks/doctor + credential-helper alias), `parseArgs`, help/version, then `buildClaudeLaunchPlan(a)` → `core.Run(ClaudeProfile(), plan, a.ClaudeArgs)`. Retains shared package-`main` helpers (`envBlock`, `parseArgs`, `handleHelp`, `defaultModelRouting`, `launchModelRouting`, `databricksFullSetupEnv`, `buildUpdaterConfig`, `handlePrintEnv`). No proxy/port/refcount/child logic remains here. |
| `launch_claude.go` | `buildClaudeLaunchPlan(a *Args) (core.LaunchPlan, error)` (#200): all claude-specific wrapper pre-flight — logging setup, settings.json read, profile resolution, auth (browser-login fallback, MUST precede token seed), startup security warnings, upstream/OTEL discovery, token seeding, port resolution, settings→state OTEL-table migration, TLS validation, websearch backend — plus the proxyURL-dependent `BuildEnv` closure (OTEL/`CLAUDE_*` env emission). Returns a neutral `core.LaunchPlan` for `core.Run`. |
| `proxy.go` | Thin facade over `internal/core/proxy` (`ProxyConfig`, `NewProxyServer`, `StartProxy`, `recoveryHandler`). After #200 the launch path calls `proxy.NewServer` directly inside `core.Run`; this facade is retained for `proxy_test.go` cmd-level coverage. |
| `token.go` | Facade over `internal/core/tokencache`: implements `databricksFetcher` (shells out to `databricks auth token`), host discovery via `databricks auth env`, and AI Gateway URL construction (`{host}/ai-gateway/anthropic`). Consumed by `buildClaudeLaunchPlan` (stays claude-side; a future issue may promote it into `internal/core`). |
| `process.go` | Wraps `internal/core/childproc`: `ForwardSignals`. (`RunChild` removed in #200 — the launch path now calls `childproc.Run` directly inside `core.Run` with `BinaryName` from `profile.ChildBinary` and the managed marker from `LaunchPlan.ManagedEnvVar`.) |
| `state.go` | `persistentState` struct for `~/.claude/.databricks-claude.json` (profile, port, CLI path, OTEL table names) plus `loadState`/`saveState`/`resolvePort` — thin facades over `internal/core/state` (#216) |
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
| `main_test.go` | Tests for `parseArgs`, `handleHelp`, `handlePrintEnv`, `deriveLogsTable`, `databricksFullSetupEnv`, `/shutdown` + idle-timeout lifecycle, and command-tree/completion parity |
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

## Key Files (`cmd/databricks-codex/`)

The `databricks-codex` launcher (`main` package, folded in via #201). Same monorepo, same `internal/core` engine, but wraps the OpenAI Codex CLI instead of Claude Code: no settings.json-style env block (patches `~/.codex/config.toml` every session via `internal/codex/tomlconfig`), no daemon, no Desktop/MDM surface.

| File | Description |
|------|--------------|
| `main.go` | Thin CLI entry point: subcommand dispatch (completion/hooks/config/serve/update), `parseArgs`, help/version, then `buildCodexLaunchPlan(a)` → `core.Run(CodexProfile(patcher), plan, a.CodexArgs)`. Hosts shared helpers: `resolveModel`/`defaultModel`, `resolveProfile`, `resolveOtel`, `deriveLogsTable`, `handlePrintEnv`, `buildUpdaterConfig`. |
| `launch_codex.go` | `buildCodexLaunchPlan(a *Args) (core.LaunchPlan, codexSettingsPatcher, error)`: codex pre-flight — logging, profile/model resolution + state saves, auth, port/TLS resolution, token seed, host discovery, gateway URL (`{host}/ai-gateway/openai/v1`), OTEL table resolution (metrics+logs only), `exec.LookPath("codex")` guard. Returns `BuildEnv: nil` (codex writes no env block) plus the field-bearing `codexSettingsPatcher` it constructed. |
| `profile_codex.go` | `CodexProfile(patcher)` factory, `codexSettingsPatcher` (`SettingsPatcher` impl — computes `base_url` + OTEL endpoints from `req.ProxyURL`, delegates to `tomlconfig.Manager.Patch`), `codexDaemon` (inert `DaemonStrategy` — codex has no daemon, `Install`/`Uninstall` return `profile.ErrDaemonUnsupported`), `codexHooks` (`HookInstaller` — delegates to `hooks.go` against `~/.codex/hooks.json`). |
| `serve_codex.go` | `serve` subcommand: session/headless sibling entrypoint (lifecycle wrap + idle timeout, no child process, no daemon sub-subcommands). Calls the same `codexSettingsPatcher.Patch` writer as wrapper mode via `profile2Request`, so both paths emit byte-identical config.toml. |
| `hooks.go` | `installHooks`/`uninstallHooks` manage the SessionStart entry in `~/.codex/hooks.json` and flip `[features] hooks = true` in config.toml (documented TOCTOU vs. `tomlconfig.Manager`, carried forward as `TODO(#72 follow-up)`). `headlessEnsure`/`headlessEnsureConfig` spawn `databricks-codex serve --port=N`. |
| `hooks_cmd.go` | `hooks <install\|uninstall\|session-start>` subcommand dispatcher |
| `cli_config.go` | `config <otel\|show>` subcommand runner. Smaller surface than claude's `config`: no `write` (no settings.json bootstrap), no `websearch` (no local fulfillment). `resolveConfigOTEL` is the pure orchestration resolver. |
| `state.go` | `persistentState` for `~/.codex/.databricks-codex.json` (profile, model, port, TLS paths, OTEL tables, `OtelMetricsDisabled`/`OtelLogsDisabled` sticky bits) |
| `token.go` | `NewTokenProvider`/`DiscoverHost`/`ConstructGatewayURL` (gateway `/ai-gateway/openai/v1`) |
| `proxy.go` | Facade over `internal/core/proxy`; used directly by `serve_codex.go` (which doesn't route through `core.Run`) |
| `commands.go` | Source-of-truth `rootCommand` tree (`config`, `hooks`, `serve` subcommands) |
| `completion_flags.go` | `flagDefs`/`knownFlags`/`knownSubcommands`, derived from `rootCommand` |

## Key Files (`cmd/databricks-opencode/`)

The `databricks-opencode` launcher (`main` package, folded in via #202). Same monorepo, same `internal/core` engine, but wraps the OpenCode CLI: no settings.json-style env block (surgically patches `~/.config/opencode/opencode.json` every session via `internal/opencode/jsonconfig`), no daemon, no Desktop/MDM surface, and no OTEL. Distinctly larger than codex: dual upstreams (Anthropic `/v1` + Gemini Native `/v1beta`) off one proxy port, plus a default-on Responses-API SSE rewriter.

| File | Description |
|------|--------------|
| `main.go` | Thin CLI entry point: subcommand dispatch (completion/config/hooks/serve/update), `parseArgs`, help/version, then `buildOpencodeLaunchPlan(a)` → `core.Run(OpencodeProfile(patcher), plan, a.OpencodeArgs)`. Hosts shared helpers: `defaultModel` (`databricks-claude-opus-4-7`)/`resolveModel`, `resolveProfile`, `handlePrintEnv`, `buildUpdaterConfig`. |
| `launch_opencode.go` | `buildOpencodeLaunchPlan(a *Args) (core.LaunchPlan, opencodeSettingsPatcher, error)`: opencode pre-flight — logging, profile/model resolution + state saves, auth, port/TLS resolution, token seed, host discovery, gateway URLs (Anthropic `{host}/ai-gateway/anthropic` + Gemini `{host}/ai-gateway/gemini/v1beta`), `exec.LookPath("opencode")` guard. Returns `BuildEnv: nil`, a `/v1beta` Gemini route, `ResponsesRewrite: {Enabled: true}`, no OTEL upstream/tables, plus the field-bearing `opencodeSettingsPatcher`. |
| `profile_opencode.go` | `OpencodeProfile(patcher)` factory, `opencodeSettingsPatcher` (`SettingsPatcher` impl — `NeedsConfig`-gated, delegates to `jsonconfig.Config.Patch`; `Restore` is a no-op), `opencodeDaemon` (inert `DaemonStrategy` — opencode has no daemon, `Install`/`Uninstall` return `profile.ErrDaemonUnsupported`), `opencodeHooks` (`HookInstaller` — delegates to `hooks.go`, resolving the config dir itself). |
| `serve_opencode.go` | `serve` subcommand: session/headless sibling entrypoint (lifecycle wrap + idle timeout, no child process, no refcount, no daemon sub-subcommands). Calls the same `opencodeSettingsPatcher.Patch` writer as wrapper mode via `servePatchRequest`, so both paths emit byte-identical opencode.json. `parseServeIdleTimeout` adds the "bare number = minutes" grammar. |
| `hooks.go` | `installHooks`/`uninstallHooks` write/remove the JS plugin at `<config-dir>/plugins/databricks-proxy/index.js` and register/unregister it in opencode.json via `jsonconfig.AddPlugin`/`RemovePlugin`. `headlessEnsure` spawns `databricks-opencode serve --port=N` via `headless.Config.EnsureCommand=["serve"]` (no refcount — OpenCode has no session-end event). |
| `hooks_cmd.go` | `hooks <install\|uninstall\|session-start>` subcommand dispatcher (no `session-end` — OpenCode has no session-end event) |
| `config_cmd.go` | `config <show>` subcommand runner. Smallest surface of the three launchers: only `show` (the `--print-env` diagnostic) — no `write` (no bootstrap), no `otel` (no telemetry). |
| `state.go` | `persistentState` for `~/.config/opencode/.databricks-opencode.json` (profile, model, port, TLS paths). `defaultPort` `49156`. No OTEL fields. |
| `token.go` | `NewTokenProvider`/`DiscoverHost`/`ConstructGatewayURL` (Anthropic `/ai-gateway/anthropic`) + `ConstructGeminiGatewayURL` (Gemini `/ai-gateway/gemini/v1beta`) |
| `configdir.go` | `opencodeConfigDir` — `$XDG_CONFIG_HOME/opencode` or `~/.config/opencode` |
| `proxy.go` | Facade over `internal/core/proxy` (`ProxyConfig` adds `GeminiUpstream` for the `/v1beta` route; `ResponsesRewrite` default-on); used directly by `serve_opencode.go` (which doesn't route through `core.Run`) |
| `commands.go` | Source-of-truth `rootCommand` tree (`config`, `hooks`, `serve` subcommands) |
| `completion_flags.go` | `flagDefs`/`knownFlags`/`knownSubcommands`, derived from `rootCommand` |

## Key Files (`cmd/databricks-agents/`)

The `databricks-agents` multiplexer (`main` package, #203) — the `ucode`-style dispatcher. `databricks-agents <agent> [args]` behaves identically to `databricks-<agent> [args]`; `databricks-agents list` enumerates the agents; `databricks-agents completion <shell>` emits nested completion. (Named with the `-agents` suffix so it never shadows the Databricks CLI on PATH.) It is an exec-delegation dispatcher, NOT the issue's literal `argv[1]→profile.Registry→core.Run`: each launcher's full subcommand surface lives in its own un-importable `package main` and `core.Run` only runs the wrapper-launch path, so the multiplexer locates and exec-s the real sibling binary — making "identical" definitional while leaving the three launchers untouched. The three launcher packages are unchanged by #203.

| File | Description |
|------|--------------|
| `main.go` | Entry point + the local agent manifest (`agents` slice = name→sibling binary→summary, the registration mechanism #203 chose since full `profile.Profile`s aren't constructible here). `run([]string) int` dispatches: `-h`/`--help` (exit 0), bare (usage→stderr, exit 2), `--version`, `list`, `completion`, else `lookup(agent)` → `resolveBinary` → `delegate`; unknown agent → exit 2. Package doc comment is the doc.go-equivalent. |
| `dispatch.go` | `resolveBinary(binary)` — prefers a copy co-located with `os.Executable()`'s dir (so a one-bin-dir install always hits the matching-version sibling), falls through to `exec.LookPath` on error/relative/miss. `binaryFileName` (adds `.exe` on windows), `isExecutableFile`. |
| `dispatch_unix.go` | `//go:build !windows` — `delegate` via `syscall.Exec(path, {path, args...}, os.Environ())`: replaces the process image (same PID → perfect signal/exit fidelity); argv[0] = resolved sibling path (child sees `databricks-<agent>` basename, never misfires the credential-helper alias); env = `os.Environ()`. |
| `dispatch_windows.go` | `//go:build windows` — `delegate` spawns `exec.Command` (Windows has no execve) with inherited stdio + `os.Environ()`, forwards SIGINT/SIGTERM, propagates the child's `ExitCode`. |
| `completion.go` | Nested completion. `bash` (fully functional): composes each `databricks-<agent> completion bash` (registration lines stripped) + a git-style `_databricks_agents` wrapper that rewrites `COMP_WORDS`/`COMP_CWORD` to the sibling frame and calls `_databricks_<agent>`. `zsh` mirrors it (rewrites `words`/`CURRENT`). `fish` is agent-name-only (documented fallback — fish's binary-keyed model resists clean delegation). A sibling whose completion can't be obtained degrades to name-only. `runSiblingCompletion` is a package var for test injection. |
| `main_test.go` / `dispatch_test.go` / `completion_test.go` | Unit + integration coverage: list/usage/version/unknown-agent(exit 2)/reserved-word; `resolveBinary` PATH-fallback/miss; delegation fidelity (built stub sibling — asserts argv[0] basename, verbatim args incl. `--`, env propagation, exit-code 7 passthrough); completion generation + missing-sibling degrade; a **bash-driven functional** test that sources the script and asserts `databricks-agents claude serve ⇥` reaches the sibling subtree. |

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `cmd/databricks-agents/` | The `databricks-agents` multiplexer (`main` package, #203). Exec-delegation dispatcher over the three per-tool binaries. See the Key Files table above. |
| `cmd/databricks-claude/` | CLI entry point (`main` package), relocated from the repo root in #197. Builds the `databricks-claude` binary. |
| `cmd/databricks-codex/` | CLI entry point (`main` package) for the codex launcher, folded in via #201. Builds the `databricks-codex` binary. See the Key Files table above. |
| `cmd/databricks-opencode/` | CLI entry point (`main` package) for the opencode launcher, folded in via #202. Builds the `databricks-opencode` binary. See the Key Files table above. |
| `internal/cmd/` | Pre-existing command-tree parsing/help/completion library (shared by all three launchers' `commands.go`) |
| `internal/core/` | The shared tool-agnostic engine (proxy, tokencache, authcheck, childproc, state, headless, lifecycle, portbind, health, refcount, updater, completion, cli). Promoted from `pkg/` in #198; module-private (see `internal/core/doc.go`). `run.go` (#200) adds `LaunchPlan` + `Run(profile.Profile, LaunchPlan, []string) int` — the wrapper-mode launch engine (bind → serve/watch → BuildEnv → Patch → child → refcount teardown) shared by both launchers. |
| `internal/codex/` | codex-specific libraries -- the `internal/`-tree analog of `pkg/` for codex, since it must be importable from `cmd/databricks-codex` (see `internal/codex/AGENTS.md`) |
| `internal/codex/tomlconfig/` | codex-specific string-based surgical patcher for `~/.codex/config.toml` (see `internal/codex/tomlconfig/AGENTS.md`). Not tool-agnostic, so it lives outside `internal/core`; not promoted to `pkg/` since only `cmd/databricks-codex` imports it. |
| `internal/opencode/` | opencode-specific libraries -- the `internal/`-tree analog of `pkg/` for opencode, since it must be importable from `cmd/databricks-opencode` (see `internal/opencode/AGENTS.md`) |
| `internal/opencode/jsonconfig/` | opencode-specific string/stdlib JSONC surgical patcher for `~/.config/opencode/opencode.json` (see `internal/opencode/jsonconfig/AGENTS.md`). Owns both the `databricks-proxy` (Anthropic `/v1`) and `databricks-gemini-proxy` (Gemini `/v1beta`) providers; a stdlib `stripJSONC` replaces the external `tidwall/jsonc` dep. Not tool-agnostic, so it lives outside `internal/core`; not promoted to `pkg/` since only `cmd/databricks-opencode` imports it. |
| `internal/profile/` | The per-tool `Profile` abstraction (#199): `Profile` struct + `SettingsPatcher`/`DaemonStrategy`/`HookInstaller` interfaces. Interfaces live here; concrete impls live in each launcher's `package main` (`profile_claude.go`, `profile_codex.go`, `profile_opencode.go`). |
| `pkg/` | Only the Claude/Anthropic-coupled libraries remain after #198 (see `pkg/AGENTS.md`): `modeldiscovery`, `mdmprofile`, `websearch`. Neither codex nor opencode has `pkg/*` packages — each keeps its one tool-specific config patcher (`tomlconfig`, `jsonconfig`) under `internal/codex/` / `internal/opencode/` instead. |
| `pkg/mdmprofile/` | Platform-specific readers for MDM-managed preferences (darwin: plist, windows: registry, other: stub). Used by the credential helper to resolve the Databricks profile on endpoint machines. Claude Desktop-only; codex has no Desktop/MDM surface. |
| `.github/` | GitHub Actions CI configuration (see `.github/AGENTS.md`) |
| `.claude/` | Claude Code project configuration (settings only, no AGENTS.md needed) |

## For AI Agents

### Working In This Directory
- **Zero external dependencies** -- do not add any third-party imports. All code must use the Go stdlib only.
- Both launchers' entry-point packages are `main`: `cmd/databricks-claude/` and `cmd/databricks-codex/`. The `.go` files in each are thin facades that delegate to the shared engine in `internal/core/` (plus each launcher's own tool-coupled libraries -- claude: `pkg/*`; codex: `internal/codex/tomlconfig`). Keep them thin.
- `main.go` (either launcher) is a thin dispatcher: it dispatches subcommands, parses flags, then delegates wrapper-mode to a launcher-specific `buildXLaunchPlan` (claude: `launch_claude.go`; codex: `launch_codex.go`) + `core.Run` (`internal/core/run.go`, the generic bind/serve/patch/child lifecycle). Each launcher's `token.go` owns Databricks auth; `proxy.go` is a facade over `internal/core/proxy`. Keep launcher-specific launch assembly in `launch_*.go` and tool-agnostic lifecycle in `internal/core`.
- codex has no daemon, no settings.json-style env block, and no Desktop/MDM surface -- do not add those without an explicit design decision; its `serve` is a session/headless leaf only, and its "settings file" is `~/.codex/config.toml`, patched every session (not a one-shot bootstrap).

### Testing Requirements
- Run `make test` or `go test ./... -v`
- Tests use **helper binaries compiled at test time** to mock the `databricks` CLI (see `buildHelperBinary`, `buildSlowBinary`, `buildAuthEnvBinary` in `cmd/databricks-claude/token_test.go`); codex reuses the same pattern for its own token/auth tests
- `warmToken()` pre-loads the token cache to avoid subprocess calls during proxy tests
- Settings/config isolation: tests create temp directories for `settings.json` (claude) or `config.toml` (codex) so no test touches the real user config

### Common Patterns
- **Atomic file writes**: all JSON/TOML writes use temp-file-then-`os.Rename` in the same directory
- **Settings.json lifecycle** (claude): `SaveAndOverwrite` / `FullSetup` saves originals, patches env block, `Restore` puts them back (smart handoff to surviving sessions)
- **config.toml lifecycle** (codex): `tomlconfig.Manager.Patch` surgically rewrites only the managed keys/sections (`profile`, `profiles.databricks-proxy`, `model_providers.databricks-proxy`, `otel`), preserving all other user content byte-for-byte
- **Token caching**: mutex-guarded, 5-minute refresh buffer, fallback to last good token on error

### Critical Safety Rules
- **Never break settings.json / config.toml restore** -- a botched restore leaves the user's config pointing at a dead proxy
- **OTEL key persistence** -- claude: when `otelKeysPersistent` is true, Restore must skip OTEL keys. codex: `OtelMetricsDisabled`/`OtelLogsDisabled` sticky bits in state let `config otel disable` suppress export while preserving table-name preferences for a future re-enable
- **Session handoff** -- when multiple instances of the same launcher run concurrently, the exiting session hands the base URL to the most recent survivor

## Dependencies

- `internal/core/authcheck` -- pre-flight auth verification
- `internal/core/childproc` -- child process management
- `internal/core/proxy` -- HTTP/WebSocket reverse proxy, API key auth, TLS, security checks, log sanitization
- `internal/core/tokencache` -- generic token caching
- `internal/core/{state,headless,lifecycle,portbind,health,refcount,updater,completion,cli}` -- the rest of the shared engine
- `internal/cmd` -- command-tree parsing/help/completion library
- `internal/profile` -- the per-tool `Profile` abstraction (interfaces only; impls live in each launcher)
- `internal/codex/tomlconfig` -- codex-only: surgical `~/.codex/config.toml` patcher
- `internal/opencode/jsonconfig` -- opencode-only: surgical JSONC `~/.config/opencode/opencode.json` patcher
- `pkg/modeldiscovery`, `pkg/mdmprofile`, `pkg/websearch` -- claude-only libraries (not promoted into core; codex and opencode have no equivalents)

### External
- None (pure Go stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
