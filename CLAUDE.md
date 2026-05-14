# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Transparent proxy wrapper for Claude Code that auto-refreshes Databricks OAuth tokens via the Databricks CLI. Zero external Go dependencies — pure stdlib only.

## Build & Test

```bash
make build                       # produces ./databricks-claude
make test                        # go test ./... -v
make lint                        # go vet ./...
make install                     # installs to $GOPATH/bin
make dist                        # cross-compile darwin/linux/windows amd64+arm64
go test -run TestParseArgs -v    # run a single test
go test ./pkg/proxy/... -v       # test a single package
```

## Architecture

### Root package (`main`)

The root package is a set of `.go` files that act as thin facades wiring together `pkg/` sub-packages:

- **main.go** — CLI entry point: flag parsing, config resolution (`~/.claude/settings.json` + `~/.claude/.databricks-claude.json`), token seeding, AI Gateway auto-discovery, proxy startup, settings patching, child launch, headless lifecycle management, and explicit settings restore before `os.Exit`.
- **proxy.go** — Facade over `pkg/proxy`: defines `ProxyConfig`, wires `NewProxyServer` and `StartProxy`.
- **token.go** — Facade over `pkg/tokencache`: implements `databricksFetcher` (shells out to `databricks auth token`), host discovery via `databricks auth env`, AI Gateway URL construction (`{host}/ai-gateway/anthropic`).
- **process.go** — Wraps `pkg/childproc`: `RunChild`, `ForwardSignals`.
- **state.go** — `persistentState` struct: JSON schema for `~/.claude/.databricks-claude.json` (profile, port, CLI path, OTEL table names).
- **hooks.go** — Session hook install/uninstall (`installHooks`, `uninstallHooks`).
- **ensureconfig.go** — Bootstrap helpers for first-run settings setup.
- **commands.go** — Source-of-truth `rootCommand` declaration plus `desktopCommand` / `setupCommand` / `configCommand` / `serveCommand` tree nodes. Drives parsing (via `internal/cmd.Command.Parse`), help (via `cmd.Render` against the `Long` template fields), and shell completion (via `CompletionFlags()` / `CompletionSubcommands()` exposed through `completion_flags.go`).
- **config.go** — `config` subcommand runner introduced in #172. Dispatches `config otel enable|disable`, `config websearch enable|disable`, `config write`, `config show`. Pure-function resolvers `resolveConfigOTEL` and `resolveConfigWebSearch` make the orchestration matrix testable in isolation. Storage semantics (two-store model, sentinel-guarded writers, OTEL section *removal* on disable, state-file preservation) are byte-identical with the legacy root flags this subcommand replaced.
- **completion_flags.go** — `flagDefs` slice driving shell completion and flag parsing; `knownSubcommands` slice driving subcommand-name completion.
- **databrickscfg.go** — Reads `~/.databrickscfg` section headers for profile name resolution.
- **desktop_config.go** — `desktop` subcommand: `generate-config` writing `.mobileconfig`, `.reg`, and `.json` artifacts for Claude Desktop.
- **desktop_trust.go** — `generate-trust-profile` subcommand for MDM trust profile generation.
- **serve.go** — `serve` subcommand: long-lived daemon that serves Claude Code and Claude Desktop with persistent Databricks OAuth. A third deployment mode alongside the per-session CLI wrapper (`databricks-claude claude …`) and SessionStart hooks — useful when you want a single OAuth-refreshing proxy that survives across sessions. `runServe(args)` parses flags, resolves profile/port/OTEL tables, calls `authcheck.IsAuthenticated` only (never `EnsureAuthenticated` — the daemon path is non-interactive and the install-time pre-auth in `serve install` is the only sanctioned way to seed a token), binds port exclusively, and runs the proxy with `Daemon=true`. On unauthenticated startup, `runServe` calls `log.Fatalf` with an actionable error pointing at `serve install` or `databricks auth login` — rather than spawning a browser prompt under a service manager with no tty. `printServeHelp()` mirrors `printDesktopHelp`/`printSetupHelp` in shape. When `args[0]` is `install`/`uninstall`/`status`, dispatches to `runServeInstall`.
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
| `pkg/headless` | Headless mode helpers: `Ensure` (start-if-absent) and `Release` (refcount decrement) |
| `pkg/health` | `/health` endpoint handler returning JSON with tool name, version, and PID |
| `pkg/lifecycle` | HTTP handler wrapper adding `/shutdown` refcount endpoint and idle timeout |
| `pkg/portbind` | Port binding helpers: bind to a configured port with fallback |
| `pkg/proxy` | HTTP/WebSocket reverse proxy, OTEL routing, API key auth, TLS, panic recovery, log sanitization |
| `pkg/refcount` | Atomic session reference counter with conditional-exit support |
| `pkg/state` | JSON-file state persistence helpers (atomic temp-file + rename) |
| `pkg/tokencache` | Mutex-guarded token cache with 5-min refresh buffer and fallback-on-error |
| `pkg/updater` | GitHub release checker with 24-hour cache and numeric semver comparison |

### Key data flow

1. `main.go` seeds token cache → binds proxy on the configured port (default `49153`) → patches `settings.json` to point `ANTHROPIC_BASE_URL` at proxy → launches `claude` as child (or enters headless mode)
2. In headless mode, the handler is wrapped with `pkg/lifecycle` which adds `POST /shutdown` (refcount decrement + conditional exit) and idle timeout (default 30m, resets on each proxied request)
3. Proxy intercepts every request, injects fresh OAuth token via `pkg/tokencache` → forwards to AI Gateway (`{host}/ai-gateway/anthropic`) or Databricks OTEL endpoint
4. On exit, settings are restored **explicitly before `os.Exit`** (not via defer — `os.Exit` skips defers). `ANTHROPIC_BASE_URL` is handed off to a surviving session if one exists.

## Key Design Constraints

- **Zero external dependencies** — do not add third-party imports. Pure Go stdlib only.
- **No breaking changes to settings.json** — a botched restore leaves the user's Claude config pointing at a dead proxy. Any change to key names, restore logic, or the save/restore lifecycle requires extreme care.
- **Atomic file writes everywhere** — all JSON writes (settings, registry, persistent config) use temp-file + `os.Rename` in the same directory.
- **Session handoff** — when multiple `databricks-claude` instances run concurrently, the exiting one hands `ANTHROPIC_BASE_URL` to the most recent survivor.
- **OTEL key persistence** — when OTEL is active (state file has any `otel_*_table` set, or settings.json carries OTEL keys), OTEL keys survive settings restore (controlled by `otelKeysPersistent` flag).

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
