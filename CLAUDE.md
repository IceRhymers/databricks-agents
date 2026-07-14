# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Transparent proxy wrapper for Claude Code that auto-refreshes Databricks OAuth tokens via the Databricks CLI. Zero external Go dependencies — pure stdlib only.

## Build & Test

```bash
make build                       # produces ./databricks-claude (builds ./cmd/databricks-claude)
make test                        # go test ./... -v
make lint                        # go vet ./...
make install                     # installs to $GOPATH/bin
make dist                        # cross-compile darwin/linux/windows amd64+arm64
go build ./cmd/databricks-claude # build the CLI binary directly (module: github.com/IceRhymers/databricks-agents)
go test -run TestParseArgs -v    # run a single test
go test ./pkg/proxy/... -v       # test a single package
```

The Go module is `github.com/IceRhymers/databricks-agents`. The CLI entry point (`main` package) lives at `cmd/databricks-claude/`; the built binary is still named `databricks-claude`.

## Architecture

### CLI entry-point package (`cmd/databricks-claude/`, package `main`)

The `main` package lives at `cmd/databricks-claude/` — a set of `.go` files that act as thin facades wiring together `pkg/` sub-packages (relocated from the repo root in #197):

- **main.go** — CLI entry point: flag parsing, config resolution (`~/.claude/settings.json` + `~/.claude/.databricks-claude.json`), token seeding, AI Gateway auto-discovery, proxy startup, settings patching, child launch, and explicit settings restore before `os.Exit`. After #174 the session-scoped lifecycle (refcount, /shutdown, idle-timeout) lives in `serve_session.go`; main.go owns wrapper-mode (proxy + claude child) only. `databricksFullSetupEnv(m ModelRouting)` now takes a `ModelRouting` and omits a family's `ANTHROPIC_*_MODEL` key when its FQN is empty (no silent mis-route for an unresolved family). `defaultModelRouting()` is the demoted offline fallback (the former hardcoded model map); `launchModelRouting(s persistentState)` returns the persisted `ModelRouting` from state, filling any blank family from the fallback — used only by launch-path callers, which never call `pkg/modeldiscovery` themselves.
- **proxy.go** — Facade over `pkg/proxy`: defines `ProxyConfig`, wires `NewProxyServer` and `StartProxy`.
- **token.go** — Facade over `pkg/tokencache`: implements `databricksFetcher` (shells out to `databricks auth token`), host discovery via `databricks auth env`, AI Gateway URL construction (`{host}/ai-gateway/anthropic`).
- **process.go** — Wraps `pkg/childproc`: `RunChild`, `ForwardSignals`.
- **state.go** — `persistentState` struct: JSON schema for `~/.claude/.databricks-claude.json` (profile, port, CLI path, OTEL table names, and `Models *ModelRouting` — the discovered per-family model FQNs, nil until a discovery-time writer runs). `ModelRouting{Opus,Sonnet,Haiku string}` mirrors `pkg/modeldiscovery.ModelSet` but is the on-disk/launch-path shape; the `[1m]` suffix, when applicable, is already baked into the FQN string.
- **hooks.go** — Session hook install/uninstall (`installHooks`, `uninstallHooks`).
- **ensureconfig.go** — Bootstrap helpers for first-run settings setup.
- **commands.go** — Source-of-truth `rootCommand` declaration plus `desktopCommand` / `setupCommand` / `configCommand` / `serveCommand` tree nodes. Drives parsing (via `internal/cmd.Command.Parse`), help (via `cmd.Render` against the `Long` template fields), and shell completion (via `CompletionFlags()` / `CompletionSubcommands()` exposed through `completion_flags.go`).
- **config.go** — `config` subcommand runner introduced in #172. Dispatches `config otel enable|disable`, `config websearch enable|disable`, `config write`, `config show`. Pure-function resolvers `resolveConfigOTEL` and `resolveConfigWebSearch` make the orchestration matrix testable in isolation. Storage semantics (two-store model, sentinel-guarded writers, OTEL section *removal* on disable, state-file preservation) are byte-identical with the legacy root flags this subcommand replaced. `runConfigWrite` is a discovery-time writer: it fetches a token, runs `modeldiscovery.Discover` fresh (network allowed here, unlike the launch hot path) to resolve the newest anthropic-capable model per family, persists the resulting `ModelSet` into `persistentState.Models` (load-then-mutate), and fails loud (non-zero exit) when zero families resolve.
- **doctor.go** — `runDoctor` implements the `doctor` subcommand: a non-interactive diagnostic that runs `modeldiscovery.Discover`, diffs the discovered per-family models against the pins currently in `~/.claude/settings.json` (`diffModelRouting`, a pure function mirroring `resolveConfigOTEL`'s testable-resolver pattern), and prints the delta. Read-only by default (exits 1 on any drift so hooks/scripts can detect it); `--fix` persists the discovered `ModelSet` into state and rewrites settings.json through the existing `bootstrapSettings` atomic writer. An unresolved family never blanks the user's current pin under `--fix`. Sanctioned recovery path for the hook/daemon flow, which cannot prompt.
- **completion_flags.go** — `flagDefs` slice driving shell completion and flag parsing; `knownSubcommands` slice driving subcommand-name completion.
- **databrickscfg.go** — Reads `~/.databrickscfg` section headers for profile name resolution.
- **desktop_config.go** — `desktop` subcommand: `generate-config` writing `.mobileconfig`, `.reg`, and `.json` artifacts for Claude Desktop.
- **desktop_trust.go** — `generate-trust-profile` subcommand for MDM trust profile generation.
- **serve.go** — `serve` subcommand: dispatches between the two lifecycle policies after #174. `runServe(args)` first checks for `install`/`uninstall`/`status` sub-subcommands (delegated to `runServeInstall`), then peeks at the args via `determineServeMode` to pick `--session-mode` (delegated to `runServeSession` in serve_session.go) vs `--daemon`. Bare `serve` (no mode flag, no sub-subcommand) is exit-2 — the required-explicit-mode invariant that mitigates the silent-degradation hazard at the hooks spawn site. The daemon body remains in serve.go: parses flags, resolves profile/port/OTEL tables, calls `authcheck.IsAuthenticated` only (never `EnsureAuthenticated` — the daemon path is non-interactive and the install-time pre-auth in `serve install` is the only sanctioned way to seed a token), binds port exclusively, and runs the proxy with `Daemon=true`. On unauthenticated startup, `runServe` calls `log.Fatalf` with an actionable error pointing at `serve install` or `databricks auth login`. The shared `buildServeProxyConfig(state, resolved, mode)` factory wires the per-mode `proxy.Config` differences (Daemon flag, APIKey/TLS gating).
- **serve_session.go** — `runServeSession(args)` implements `serve --session-mode`. Refcounted, `/shutdown` route, idle-timeout-driven exit, settings.json restore, fallback-port bind. Was the `--headless` root flag prior to #174. `runServeSessionLoop` blocks on SIGINT/SIGTERM or `doneCh`. `buildSessionOTELEnv` mirrors main.go's per-table emission semantics so settings.json reflects the running session-scoped proxy.
- **serve_install.go** — Cross-platform dispatcher for `serve install`/`uninstall`/`status` sub-subcommands. Parses install flags (including `--skip-auth-check` and the `cliPath` resolution that bakes `$DATABRICKS_CLI` into the manifest), resolves binary path via `os.Executable()`, gates non-tty installs through `authcheck.EnsureOrCheck` so daemon paths can't be left half-configured, runs a `/health` post-install probe with `diagnosticsTail` fallback for failure visibility, and dispatches to OS-specific `installDaemon`/`uninstallDaemon`/`daemonStatus` functions. `printStatusResult` pretty-prints the `statusResult` struct, surfacing `Running: no (failed, ...)` when the service-manager reports a crash-loop.
- **serve_install_darwin.go** — `//go:build darwin` — LaunchAgent plist rendering (`renderPlist`, including the `<key>EnvironmentVariables</key>` dict that pins `DATABRICKS_CLI`), `launchctl bootstrap`/`kickstart`/`bootout`, `spctl --assess` Gatekeeper warning, `launchctl print` parsing for `state = running` + `last exit code` (non-zero triggers `Failed=true` so a crash-loop is visible), and `diagnosticsTail()` returning `launchctl print` output plus the daemon stderr log tail.
- **serve_install_linux.go** — `//go:build linux` — systemd user unit rendering (`renderUnit`, with `Environment=DATABRICKS_CLI=...` baked into `[Service]` so the daemon finds the CLI under systemd's minimal PATH), `systemctl --user daemon-reload`/`enable --now`/`disable --now`/`is-enabled`/`is-active`/`is-failed`, `systemctl show --property=Result,ExecMainStatus` for structured failure detail, and `diagnosticsTail()` returning `journalctl --user -u <unit> -n 50 --no-pager`.
- **serve_install_windows.go** — `//go:build windows` — `schtasks /create /SC ONLOGON /RL LIMITED` with `/TR` argument escaping (`buildSchtasksCmd`), `schtasks /run`/`/delete /F`/`/query /V /FO CSV` parsing, `diagnosticsTail()` returns a "not implemented" hint pointing at the daemon stderr log (schtasks has no built-in journal). `$DATABRICKS_CLI` env baking is documented as a known gap because `setx` scoping inside `/TR` fights cmd.exe escaping; users with brew-on-Windows need to set `DATABRICKS_CLI` system-wide before install.
- **serve_install_other.go** — `//go:build !darwin && !windows && !linux` — stubs returning "unsupported platform" error for `installDaemon`/`uninstallDaemon`/`daemonStatus`, plus a `("", nil)` `diagnosticsTail` so cross-platform CI builds (freebsd/openbsd) compile.

### Library packages (`pkg/`)

Each package is independently importable with no cross-dependencies:

| Package | Purpose |
|---------|---------|
| `pkg/authcheck` | Pre-flight Databricks auth check + interactive browser login fallback |
| `pkg/childproc` | Child process start, SIGINT/SIGTERM forwarding, exit code propagation |
| `pkg/completion` | Shell completion script generation (bash, zsh, fish) from `FlagDef` slices |
| `pkg/headless` | Detached-proxy spawn helpers: `Ensure` (start-if-absent) and `Release` (refcount decrement). `Config.EnsureCommand` (added in #174) lets databricks-claude target `serve --session-mode` instead of the deleted `--headless` root flag; siblings (databricks-codex, databricks-opencode) leave it empty for the legacy `--headless` shape. |
| `pkg/health` | `/health` endpoint handler returning JSON with tool name, version, and PID |
| `pkg/lifecycle` | HTTP handler wrapper adding `/shutdown` refcount endpoint and idle timeout |
| `pkg/modeldiscovery` | Unity AI Gateway model-services discovery: lists UC model-services, resolves newest anthropic-capable model per family (opus/sonnet/haiku), pure `Resolve()` + numeric version sort + 1M predicate. Stdlib only. |
| `pkg/portbind` | Port binding helpers: bind to a configured port with fallback |
| `pkg/proxy` | HTTP/WebSocket reverse proxy, OTEL routing, API key auth, TLS, panic recovery, log sanitization |
| `pkg/refcount` | Atomic session reference counter with conditional-exit support |
| `pkg/state` | JSON-file state persistence helpers (atomic temp-file + rename) |
| `pkg/tokencache` | Mutex-guarded token cache with 5-min refresh buffer and fallback-on-error |
| `pkg/updater` | GitHub release checker with 24-hour cache and numeric semver comparison |

### Internal packages (`internal/`)

| Package | Purpose |
|---------|---------|
| `internal/cmd` | Pre-existing command-tree parsing/help/completion library (distinct from `cmd/databricks-claude/`; drives `rootCommand` in `commands.go`). |
| `internal/core` | Placeholder for the unified core module surface (epic #196). Empty `doc.go` skeleton added in #197. |
| `internal/profile` | Placeholder for profile resolution (epic #196). Empty `doc.go` skeleton added in #197. |

### Key data flow

1. Wrapper mode: `main.go` seeds token cache → binds proxy on the configured port (default `49153`) → patches `settings.json` to point `ANTHROPIC_BASE_URL` at proxy → launches `claude` as child.
2. Session-scoped standalone proxy: `serve --session-mode` (in `serve_session.go`) does the same setup but blocks on SIGINT/SIGTERM or `/shutdown` instead of launching claude. Handler is wrapped with `pkg/lifecycle` (`POST /shutdown` refcount decrement + conditional exit; idle timeout defaults to 30m, resets on each proxied request).
3. Long-lived daemon: `serve --daemon` (in `serve.go`) binds the port exclusively, never wraps with lifecycle (so `/shutdown` returns 404), and reports `daemon:true` in `/health`.
4. Proxy intercepts every request, injects fresh OAuth token via `pkg/tokencache` → forwards to AI Gateway (`{host}/ai-gateway/anthropic`) or Databricks OTEL endpoint.
5. On exit, settings are restored **explicitly before `os.Exit`** (not via defer — `os.Exit` skips defers). `ANTHROPIC_BASE_URL` is handed off to a surviving session if one exists.

## Key Design Constraints

- **Zero external dependencies** — do not add third-party imports. Pure Go stdlib only.
- **No breaking changes to settings.json** — a botched restore leaves the user's Claude config pointing at a dead proxy. Any change to key names, restore logic, or the save/restore lifecycle requires extreme care.
- **Atomic file writes everywhere** — all JSON writes (settings, registry, persistent config) use temp-file + `os.Rename` in the same directory.
- **Session handoff** — when multiple `databricks-claude` instances run concurrently, the exiting one hands `ANTHROPIC_BASE_URL` to the most recent survivor.
- **OTEL key persistence** — when OTEL is active (state file has any `otel_*_table` set, or settings.json carries OTEL keys), OTEL keys survive settings restore (controlled by `otelKeysPersistent` flag).
- **No model discovery on the launch hot path** — `pkg/modeldiscovery` network calls run only at `config write` / `desktop generate-config` / `doctor` time. The wrapper first-run bootstrap (`main.go`) and the unconditional `serve --session-mode` startup read the persisted `ModelRouting` from state (or fall back to `defaultModelRouting()`); they never call the gateway.

## Testing Patterns

- **Helper binaries**: `token_test.go` compiles small Go programs at test time (`buildHelperBinary`, `buildSlowBinary`, `buildAuthEnvBinary`) to mock the `databricks` CLI. Don't try to mock `exec.Command` directly.
- **Token pre-warming**: `warmToken()` in `proxy_test.go` seeds the cache to avoid subprocess calls during proxy tests.
- **Test-overridable globals**: `authcheck` uses `var execCommand = exec.Command` pattern; `checks.go` uses `var getuid = os.Getuid`. Override in tests, not via interfaces.
- **Settings isolation**: `process_test.go` creates temp directories with fresh `settings.json` files. `pkg/settings` has no dedicated test file — it's tested through root-level integration tests.

## Pre-Commit Consistency Checks

Before generating any commit, verify that documentation files are consistent with the code:

1. **README.md** — flags table, usage examples, and `--print-env` output must match `parseArgs` in `main.go` and `handlePrintEnv` output format.
2. **CLAUDE.md** — architecture description must reflect the current file and package structure.
3. **AGENTS.md files** — key file tables must list files that actually exist; parent references must resolve.

If any documentation is stale, fix it in the same commit or flag it.
