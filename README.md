# databricks-claude

> **Disclaimer:** This is an unofficial, community-built workaround to enable Databricks OAuth SSO authentication with this AI coding tool. It is not supported, endorsed, or recognized by Databricks. Use at your own risk.

Transparent proxy wrapper for Claude Code that auto-refreshes Databricks OAuth tokens — so you never manually paste a token again.

## The Problem

Databricks AI Gateway supports short-lived OAuth tokens. Claude Code only supports a static `ANTHROPIC_AUTH_TOKEN` in `~/.claude/settings.json`. Without this tool, you'd need to configure long-living credentials with PAT tokens.

## Prerequisites

- [Databricks CLI](https://docs.databricks.com/dev-tools/cli/databricks-cli.html) installed and authenticated (`databricks auth login`)
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) installed
- A Databricks Model Serving endpoint with [AI Gateway](https://docs.databricks.com/aws/en/ai-gateway/) enabled (currently in public Beta)
- Go 1.22+ (only required if building from source)

## Install

Via Homebrew (recommended):

```
brew tap IceRhymers/tap
brew install databricks-claude
```

### Via Scoop (Windows)

```powershell
scoop bucket add icerhymers https://github.com/IceRhymers/scoop-bucket
scoop install databricks-claude
```

### Direct binary (Windows)

Download the latest release from the [releases page](https://github.com/IceRhymers/databricks-claude/releases), pick `databricks-claude-windows-amd64.exe` (or `arm64`), rename it to `databricks-claude.exe`, and place it somewhere on your `PATH`.

### From source

```bash
go install github.com/IceRhymers/databricks-claude@latest
```

## Pick Your Setup

There are three ways to use `databricks-claude`. Most people want **session hooks** — set it once and `claude` just works everywhere, including IDE extensions. Pick whichever rows match your workflow; you can install more than one.

| Primary client | Recommended setup |
|----------------|-------------------|
| CLI Claude Code, VS Code extension, JetBrains plugin | **Session hooks** — `databricks-claude --install-hooks --profile <name>`. |
| Claude Desktop (chat UI and/or embedded Claude Code) | **Mobileconfig** — `databricks-claude desktop generate-config` + install in System Settings. |
| Both | Install both. They coexist without conflict. |
| One-off / scripted invocations | Use the [raw wrapper](#cli-usage) directly. |

The two automated modes are independent — neither requires the other — and the binary supports either or both.

## Web Search & Fetch (Workaround, opt-in)

> ⚠️ **This is a workaround, not a permanent feature.** Databricks AI Gateway's Anthropic compatibility layer doesn't yet support Anthropic's native `web_search` and `web_fetch` server-side tools. Until that ships, this proxy can locally fulfill those tool calls so Claude Code's research workflows work against Databricks-served models.
>
> When Databricks ships native server-side tool support, this flag will print a deprecation warning for one minor release before being removed.

Enable with:

```bash
databricks-claude --with-websearch --profile <name>
```

How it works:

- The proxy detects `web_search_*` and `web_fetch_*` entries in outgoing `/v1/messages` requests and rewrites them to standard client-tool definitions named `web_search` and `web_fetch`. This is required because the AI Gateway rejects unknown server-tool types.
- On the response side, the proxy parses the SSE stream from the AI Gateway. When the model emits a `tool_use` block for the rewritten `web_search`/`web_fetch` tool, the proxy: (a) rewrites the on-the-wire block type to `server_tool_use`, (b) accumulates the streaming `input_json_delta` fragments to assemble the tool's input, (c) executes the local backend (`Search` or `Fetch`), and (d) injects a synthetic `web_search_tool_result` content block per Anthropic's documented shape so Claude Code's helper sees results inline.
- For non-streaming (`stream=false`) responses, the proxy applies the same transformation to the JSON body before forwarding.
- A legacy fallback path also handles generic Anthropic API clients that do a client-tool loop: if the client returns an `is_error` `tool_result` for the rewritten tool, the proxy substitutes locally-fulfilled output on the next turn.
- All fulfillment is **headless** — pure stdlib HTTP, no browser process. JavaScript-rendered pages are not supported.
- `robots.txt` is enforced per host with a session cache.

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--with-websearch` | `false` | Enable the workaround. Persists to `~/.claude/.databricks-claude.json`. |
| `--websearch-backend` | `duckduckgo` | Search backend. Values: `duckduckgo` (zero config, HTML scrape), `none` (disable search but keep fetch). |
| `--websearch-fetch-budget` | `102400` (100KB) | Max bytes returned per `web_fetch` call. Larger pages are truncated. |

Limitations:

- No JavaScript rendering — fetched pages are static HTML only.
- `robots.txt` blocks return an error `tool_result`; the model is told why.
- Per-fetch byte cap defaults to 100KB to protect the context window.
- Search backends `brave` and `searxng` are deferred to follow-up work; only `duckduckgo` and `none` are wired today.

When `--with-websearch=false` (the default), the proxy forwards request bytes unchanged — there is no behavior change for users who don't opt in.

## Session Hooks (recommended)

Install hooks so every Claude Code session auto-starts the proxy on startup and releases it cleanly on exit — no manual `--headless` needed. The hooks keep the proxy running for all Claude clients — including ones that don't use the `databricks-claude` wrapper directly, such as the [Claude VS Code extension](https://marketplace.visualstudio.com/items?itemName=Anthropic.claude-code) and JetBrains/IntelliJ plugin.

> **Coexists with Claude Desktop.** If you've also installed the Claude Desktop mobileconfig, the hook's proxy lifecycle is harmless inside Desktop sessions — Desktop's inference does not consult `ANTHROPIC_BASE_URL` (it uses its own MDM-driven `inferenceCredentialHelper`).

### Install

```bash
databricks-claude --install-hooks --profile <name>
```

This is one-step setup: it persists your profile/port, writes `ANTHROPIC_BASE_URL` to `~/.claude/settings.json`, and registers the SessionStart and SessionEnd hooks. No prior `databricks-claude` invocation needed. Re-running is idempotent.

- **SessionStart** — calls `databricks-claude --headless-ensure` on session startup: starts the proxy if it isn't already running.
- **SessionEnd** — calls `databricks-claude --headless-release` on session end: decrements the refcount; proxy exits when the last session closes.

### Uninstall

```bash
databricks-claude --uninstall-hooks
```

Removes only the databricks-claude hook entries. Other hooks in your settings are untouched.

### Notes

- Idempotent — safe to re-run after upgrades.
- The proxy starts on the configured port (default `49153`). If you use a custom port via `--port`, the hooks will respect that setting automatically (port is saved to the state file).
- Unclean exits (force-quit, OOM kill) are covered by the idle timeout — the proxy self-exits after 30 minutes with no inference traffic.

### Claude Code Plugin (marketplace install)

Hooks are also distributed as a Claude Code plugin. Add this repo as a marketplace, then install the plugin:

```
/plugin marketplace add IceRhymers/databricks-claude
/plugin install databricks-claude@IceRhymers-databricks-claude
```

The `.claude-plugin/` directory and `hooks/hooks.json` at the repo root define the plugin.

## Claude Desktop Integration

`databricks-claude` can act as the credential helper for the Claude Desktop app's third-party-inference mode. Desktop calls a single executable (no args allowed) once per token TTL and uses whatever it prints to stdout as the bearer token for AI Gateway requests.

### One-time setup

1. **Install** `databricks-claude` (Homebrew, `make install`, or `go install`). All install methods drop a `databricks-claude-credential-helper` symlink next to the main binary; that symlink is the path Claude Desktop will invoke.
2. **Authenticate** with the workspace you want Desktop to talk to: `databricks auth login --profile <name>`.
3. **Generate the desktop config:**
   ```bash
   databricks-claude desktop generate-config --profile <name>
   ```
   This writes three artifacts into the current directory, all encoding the same Databricks gateway / credential-helper defaults:
   - `databricks-claude-desktop.mobileconfig` — ready-to-install macOS configuration profile.
   - `databricks-claude-desktop.reg` — ready-to-merge Windows registry script.
   - `databricks-claude-desktop.json` — editable source. Import into Claude Desktop's developer mode if you need to customize allow-lists, tools, branding, etc. — Desktop can then export your edits back to `.mobileconfig` / `.reg` for MDM rollout.

   Pass `--output <path>` for a single file (extension `.mobileconfig`, `.reg`, or `.json` selects the format).
4. **Install the config:**
   - **macOS**: `open databricks-claude-desktop.mobileconfig`, then approve in System Settings → Privacy & Security → Profiles.
   - **Windows**: double-click the `.reg` file, or `reg import databricks-claude-desktop.reg`.

   For fleet rollout via Jamf / Kandji / Intune / Group Policy, ship the same `.mobileconfig` or `.reg` to your endpoints. See [MDM / fleet rollout](#mdm--fleet-rollout) for path-pinning flags, or [MDM internal deployment (self-signed)](#mdm-internal-deployment-self-signed) if you are rolling out via a signed `.pkg` installer.
5. **Restart Claude Desktop.**

After this, Desktop's third-party-inference path runs against your Databricks AI Gateway, with tokens refreshed automatically by the credential helper.

### Customizing the configuration

The defaults baked into the generated artifacts (model list, gateway URL, credential-helper path, telemetry/extension toggles) are all you need to get Claude Desktop talking to Databricks. If you want to tweak Claude Desktop's full set of policy keys — allow-lists, available tools, branding, telemetry policy, extension behavior, etc. — load `databricks-claude-desktop.json` into Claude Desktop's developer mode and edit from there:

1. **Enable developer mode** — in the menu bar:
   **Help → Troubleshooting → Enable Developer mode**.
2. **Open the third-party inference UI**:
   **Developer → Configure third-party inference**.
3. **Create a new configuration**. Click the configuration name in the top-right of the UI to open the **CONFIGURATIONS** menu, then choose **New configuration**. Give it a name (e.g. `Databricks`).
4. **Reveal the configuration on disk**. Open the same **CONFIGURATIONS** menu and choose **Reveal in Finder** (macOS) / **Reveal in Explorer** (Windows). This opens the configuration library directory:
   - **macOS**: `~/Library/Application Support/Claude-3p/configLibrary/`
   - **Windows**: `%APPDATA%\Claude-3p\configLibrary\` (use *Reveal in Explorer* to confirm the exact path on your install)

   Inside that directory you'll find:
   - One JSON file per configuration, named `<uuid>.json` — the same schema as `databricks-claude-desktop.json`.
   - An index file (`{ "appliedId": "<uuid>", "entries": [ { "id": "<uuid>", "name": "<config name>" } ] }`) that tracks which configuration is currently applied.
5. **Replace the new configuration's JSON file** with the contents of `databricks-claude-desktop.json`. Keep the original filename (the `<uuid>.json` Claude Desktop generated) — only the contents change. Do not edit the index file.
6. **Apply and edit** in Claude Desktop. Switch back to the app, select your new configuration in the dropdown, then edit any of the [Claude Desktop configuration keys](https://support.claude.com/en/articles/14680741-install-and-configure-claude-cowork-with-third-party-platforms) (allow-lists, tools, branding, etc.) directly in the UI.
7. **Export** for fleet rollout. Claude Desktop's UI has an Export action that writes the configuration out as `.mobileconfig` (macOS) or `.reg` (Windows), ready to ship to MDM (Jamf, Kandji, Intune, Group Policy).
8. **Restart Claude Desktop**, or distribute the exported file to your fleet.

> Claude Desktop does not have a "Import JSON" UI today — file replacement under `configLibrary/` is the supported import path.

Reference: [Install and configure Claude with third-party platforms](https://support.claude.com/en/articles/14680741-install-and-configure-claude-cowork-with-third-party-platforms) — full list of Claude Desktop configuration keys and the developer-mode workflow.

### How dispatch works

The `inferenceCredentialHelper` MDM key in the generated config points at `…/databricks-claude-credential-helper` (the symlink). When invoked under that name, the binary checks `argv[0]` and routes directly to the credential-helper code path — no flags required. The same binary still runs as a Claude Code wrapper when invoked under its primary name.

### MDM / fleet rollout

For rolling this out to a fleet via Jamf, Kandji, Intune, etc., generate the config from a reference workstation with paths that match your endpoint layout:

```bash
# Bake fleet-wide paths into the generated config
databricks-claude desktop generate-config \
  --profile <name> \
  --binary-path /usr/local/bin/databricks-claude-credential-helper \
  --databricks-cli-path /usr/local/bin/databricks
```

- `--binary-path` is the absolute path of the credential-helper symlink (or hardlink/copy) on every target endpoint.
- `--databricks-cli-path` pins the `databricks` CLI absolute path. It's persisted to `~/.claude/.databricks-claude.json` on the generating machine; admins should arrange for the same field to be set on every endpoint (either by running this command per-user, or by dropping the state file via the same MDM tooling).

The packaging method (`.pkg` installer, custom `brew` formula, etc.) is responsible for ensuring `databricks-claude` and its `databricks-claude-credential-helper` symlink land at the paths you embed in the config.

For fleet rollout via MDM using the signed `.pkg` installer, see [MDM deployment with signed `.pkg`](#mdm-deployment-with-signed-pkg-self-signed) below.

#### Fleet rollout — per-user init script

After your MDM policy deploys `databricks-claude.pkg` and the workspace `.mobileconfig`, run this user-scope init script (e.g. as a Jamf policy or Intune Win32 app triggered at login):

```bash
#!/bin/bash
PROFILE="databricks-ai-inference"
HOST="https://my-ai-workspace.cloud.databricks.com"
databricks auth login --host "$HOST" --profile "$PROFILE"
/usr/local/bin/databricks-claude setup --profile "$PROFILE"
```

`generate-config --for-pkg --profile databricks-ai-inference` bakes the same profile into the `.mobileconfig`'s `com.icerhymers.databricks-claude` MDM payload. The credential helper reads this payload on first launch — so if the user opens Claude Desktop before the init script runs, the helper can still resolve the correct workspace via MDM without a local state file.

The `setup` subcommand is idempotent: re-running it when the user is already authenticated prints "Already authenticated" and exits 0. Use `--force` to re-run the browser login regardless.

#### How recovery works

Databricks OAuth refresh tokens live roughly 24 hours. When a token expires, Claude Desktop's next 55-second re-poll fires the credential helper. The helper recovers automatically:

1. Tries `databricks auth token --profile <resolved>` — fails (token expired).
2. Calls `databricks auth login --profile <resolved>` with the subprocess's stdout routed to stderr (Desktop's stdout watch is preserved). A browser window opens.
3. The user completes the SSO flow (~30–60 seconds on a fast IdP).
4. The helper retries `databricks auth token` — succeeds, emits the fresh token to stdout.
5. Claude Desktop resumes with a fresh token.

If the user takes more than 55 seconds to complete SSO, Desktop's TTL fires again. On the retry, the token cache is freshly warm and the second invocation is instant.

To warm the cache proactively and avoid the browser-at-first-launch surprise:

```bash
/usr/local/bin/databricks-claude setup --profile databricks-ai-inference
```

This is what the fleet init script above does — running it before the user opens Desktop ensures the first credential-helper invocation hits the fast-path and returns a token without a browser prompt.

### MDM deployment with signed `.pkg` (self-signed)

> **Audience**: any admin rolling out Claude Desktop + Databricks AI Gateway to their workforce via MDM (Jamf, Kandji, Intune, etc.). This repo is a template — fork it, set your org's signing identity, and ship signed `.pkg`s to your managed Macs. The `.pkg` is signed with a self-signed certificate that is only trusted on endpoints where the matching trust profile has been deployed via MDM. For unmanaged Macs, use the Homebrew tap.

#### The three artifacts

Each release publishes three artifacts for managed fleet deployment:

- `databricks-claude.pkg` — the installer. Deploys the binary and creates a `databricks-claude-credential-helper` symlink at `/usr/local/bin`.
- `databricks-claude-trust.mobileconfig` — Configuration Profile that establishes the signing certificate as a trusted root for code-signing. Deploy this once per fleet before deploying the `.pkg`.
- A workspace-specific `.mobileconfig` — generated per Databricks workspace by an MDM admin:
  ```bash
  databricks-claude desktop generate-config --for-pkg --profile <workspace-profile>
  ```
  The `--for-pkg` flag bakes the canonical `/usr/local/bin/databricks-claude-credential-helper` path so the credential-helper path matches what the `.pkg` installer places on disk.

#### Deployment order

1. Deploy `databricks-claude-trust.mobileconfig` (once per fleet — establishes cert trust before the signed binary arrives).
2. Deploy `databricks-claude.pkg` (installs the binary and credential-helper symlink).
3. Deploy the workspace-specific `.mobileconfig` (points Claude Desktop at the correct AI Gateway and credential helper).

#### Cert rotation runbook

**Cadence**: Rotate at least 60 days before the certificate expires. The cert is 5-year self-signed; track expiry via the `notBefore`/`notAfter` fields in `dist/signing-cert.pem`. Future work: add automated cert-expiry alerting.

**Sequence**:

1. Run `make generate-signing-cert` with `P12_PASSWORD` set to a strong random value, plus `CERT_CN` / `CERT_ORG` / `CERT_COUNTRY` set to your org's identity (see [Maintainer cert bootstrap](#maintainer-cert-bootstrap-one-time)). Keep the CN identical to the previous rotation unless you specifically intend to change the displayed signing identity.
2. Update GitHub repo secrets: `APPLE_INTERNAL_SIGNING_P12_BASE64`, `APPLE_INTERNAL_SIGNING_P12_PASSWORD`, `APPLE_INTERNAL_SIGNING_CERT_PEM` (and `APPLE_INTERNAL_SIGNING_IDENTITY` only if the certificate CN changed).
3. Cut a new release — release-please will dispatch the package-macos job, producing a new `.pkg` and a new `databricks-claude-trust.mobileconfig`.
4. MDM admins deploy the new trust profile **alongside** the old one, before the old certificate expires. This overlap window prevents a gap where no trusted certificate covers endpoints in the middle of the rollout.
5. Once the new release is broadly deployed, remove the old trust profile.

**Rollback**: Keep the prior `.p12` and identity in a separate secret-vault entry. If the new certificate fails MDM acceptance: restore the old GitHub secrets, redeploy the old trust profile, and cut a hotfix release using the prior identity. Do not overwrite the prior P12 vault entry until the new certificate has been broadly accepted.

#### Maintainer cert bootstrap (one-time)

Generate the initial signing certificate with **your org's identity** baked into the cert subject, then load it into GitHub secrets:

```bash
P12_PASSWORD=<strong-random-value> \
CERT_CN="<Your Org> Claude Desktop Code Signing" \
CERT_ORG="<Your Org>" \
CERT_COUNTRY=US \
  make generate-signing-cert
```

`CERT_CN`, `CERT_ORG`, and `CERT_COUNTRY` set the cert subject. They default to deliberately template-y placeholders (`...REPLACE FOR PROD`) so an unconfigured run is obviously not production-ready; override them before rolling out to your fleet. The CN is what your endpoints will see in `pkgutil --check-signature` and Gatekeeper dialogs once the trust profile is deployed, so pick something your IT/security org will recognize as authoritative.

The command prints the base64-encoded `.p12`, the PEM certificate, and the signing identity string. Paste each into the corresponding GitHub repo secret (`APPLE_INTERNAL_SIGNING_P12_BASE64`, `APPLE_INTERNAL_SIGNING_P12_PASSWORD`, `APPLE_INTERNAL_SIGNING_CERT_PEM`, `APPLE_INTERNAL_SIGNING_IDENTITY`). The `.p12` file itself must not be committed — it is covered by `.gitignore`.

### Troubleshooting

The helper logs every invocation (best-effort, silent on failure) to:
- macOS: `~/Library/Logs/databricks-claude/credential-helper.log`
- Linux: `~/.cache/databricks-claude/credential-helper.log`

Each entry records the resolved profile, CLI path, and either the token length on success or the underlying error. If Desktop reports `invalid_config` or 401, check this log first.

## CLI Usage

If you'd rather invoke the wrapper directly (no hooks installed), use it exactly like `claude`. Every flag and argument is forwarded.

```bash
# Use exactly like claude:
databricks-claude "explain this codebase"

# With a specific Databricks CLI profile:
databricks-claude --profile my-workspace "write tests for auth.py"

# Verbose logging (debug output to stderr):
databricks-claude --verbose "fix the bug in main.go"

# Log to file:
databricks-claude --log-file /tmp/dc.log "fix the bug in main.go"

# Both stderr and file:
databricks-claude -v --log-file /tmp/dc.log "fix the bug in main.go"

# With OTEL telemetry (metrics + logs by default):
databricks-claude --otel "summarize this PR"

# With custom OTEL tables (each signal is independent — only emit what you point at a table):
databricks-claude --otel --otel-metrics-table main.catalog.metrics --otel-logs-table main.catalog.logs "summarize this PR"

# Enable Claude Code traces beta (CLAUDE_CODE_ENHANCED_TELEMETRY_BETA):
databricks-claude --otel-traces --otel-traces-table main.catalog.traces "summarize this PR"

# Per-signal disable — clears just that signal's keys, leaves others intact:
databricks-claude --no-otel-metrics
databricks-claude --no-otel-logs
databricks-claude --no-otel-traces

# Disable all OTEL (clears every persisted signal key):
databricks-claude --no-otel

# With proxy API key authentication:
databricks-claude --proxy-api-key my-secret-key "explain this codebase"

# With TLS:
databricks-claude --tls-cert cert.pem --tls-key key.pem "explain this codebase"
```

### Alias (optional)

```bash
echo 'alias claude="databricks-claude"' >> ~/.zshrc  # or ~/.bashrc
```

Claude Desktop integration lives under the `desktop` subcommand — run `databricks-claude desktop` for its action list and flags.

## `setup` Subcommand

Idempotent auth bootstrap. Persists the active profile to `~/.claude/.databricks-claude.json` and runs `databricks auth login` only when the profile isn't already authenticated. Safe to re-run on every login — designed for fleet init scripts and per-user LaunchAgents / login-trigger scripts.

```bash
# First-time bootstrap on a new endpoint:
databricks-claude setup \
  --profile databricks-ai-inference \
  --host https://my-ai-workspace.cloud.databricks.com

# Idempotent re-run (no-op when authed) — safe in a LaunchAgent:
databricks-claude setup --profile databricks-ai-inference

# Force a re-login (switched workspaces, or revoked the old token):
databricks-claude setup --profile databricks-ai-inference --force
```

| Flag | Purpose |
|------|---------|
| `--profile NAME` | Databricks CLI profile to bootstrap (default: saved state > `"DEFAULT"`) |
| `--host URL` | Workspace URL, forwarded verbatim to `databricks auth login --host` on first login |
| `--force` | Always re-run `databricks auth login` even when already authenticated |
| `--help`, `-h` | Show subcommand help |

**Behaviour:**

1. Resolve profile (flag → saved state → `"DEFAULT"`) and persist it to the state file so subsequent `databricks-claude` invocations (including the Claude Desktop credential helper) pick it up.
2. If already authenticated for that profile and `--force` was not passed: print a success line and exit 0 without spawning a browser.
3. Otherwise exec `databricks auth login --profile X [--host Y]` with attached stdin/stdout/stderr (interactive browser OAuth flow).
4. Re-check authentication. Exit 0 on success, non-zero on failure.

**Exit codes:**

| Code | Meaning |
|------|---------|
| 0 | Already authenticated, or login succeeded |
| 1 | State write failed, auth login failed, or still unauthenticated after login |

`setup` is the same auth flow the credential helper uses for daily token recovery — running it proactively in a fleet init script keeps users from seeing the recovery browser tab on their first Claude Desktop launch.

## serve Subcommand

Long-lived daemon that serves **both Claude Code and Claude Desktop** with persistent Databricks OAuth. A third deployment mode alongside the per-session CLI wrapper (`databricks-claude claude …`) and SessionStart hooks — useful when you want a single OAuth-refreshing proxy that survives across sessions.

Owns Databricks OAuth refresh and exposes inference + OTLP on `127.0.0.1`. Distinguished from `--headless` mode by: no session refcount, no `/shutdown` route, append-only logging, and `daemon:true` in `/health` so hooks can detect and no-op.

Designed for LaunchAgent (macOS) or systemd (Linux) deployment, where the daemon is started once at login and kept running. Configure your client to point at the daemon:
- **Claude Desktop:** via MDM, set `gatewayBaseUrl: http://127.0.0.1:<port>` with a static fake API key (no per-user secret distribution).
- **Claude Code:** edit `~/.claude/settings.json` once to set `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>` in the env block. The daemon does NOT mutate `settings.json` itself — it stays outside the per-tool lifecycle by design.

```bash
# Minimal daemon on default port:
databricks-claude serve

# With explicit profile, port, and persistent log file:
databricks-claude serve \
  --profile databricks-ai-inference \
  --port 49153 \
  --log-file /var/log/databricks-claude/daemon.log

# With OTEL table routing:
databricks-claude serve \
  --otel-metrics-table main.claude_telemetry.claude_otel_metrics \
  --otel-logs-table main.claude_telemetry.claude_otel_logs
```

| Flag | Purpose |
|------|---------|
| `--port int` | Proxy listen port (default: `49153`). Bound exclusively — MDM-baked `gatewayBaseUrl` is a fixed URL and cannot follow a fallback port. |
| `--profile string` | Databricks config profile (default: saved state → MDM `databricksProfile` key → `"DEFAULT"`) |
| `--log-file string` | Append-only log file (`O_APPEND`, not `O_TRUNC`). Safe for log rotation. Restarts preserve prior content. |
| `--verbose`, `-v` | Also write debug logs to stderr (combinable with `--log-file`) |
| `--otel-metrics-table string` | Unity Catalog table for OTEL metrics. Resolution: flag → saved state → MDM `otelMetricsTable` key → empty. |
| `--otel-logs-table string` | Unity Catalog table for OTEL logs (same resolution chain) |
| `--otel-traces-table string` | Unity Catalog table for OTEL traces (same resolution chain) |
| `--help`, `-h` | Show subcommand help |

**OTEL table behavior when empty:** The daemon does **not** fail startup when a table is unset. It forwards OTLP for that signal without the `X-Databricks-UC-Table-Name` header; Databricks ingest rejects those requests with a visible 4xx — an actionable failure, not a silent one.

**MDM keys** (domain `com.icerhymers.databricks-claude`):

| Key | Purpose |
|-----|---------|
| `databricksProfile` | Databricks CLI profile name |
| `otelMetricsTable` | UC table for OTEL metrics |
| `otelLogsTable` | UC table for OTEL logs |
| `otelTracesTable` | UC table for OTEL traces |

**Endpoints:**

| Endpoint | Description |
|----------|-------------|
| `GET /health` | Returns `{"tool":"databricks-claude","daemon":true,"version":"...","profile":"...","token_valid_until":"..."}` |
| `POST /shutdown` | **Not registered** — returns 404. Stop the daemon via SIGTERM (e.g. `launchctl stop` or `systemctl stop`). |

**Note:** `--otel` / `--no-otel*` flags are **not** supported for `serve`. Those flags mutate `~/.claude/settings.json` to configure Claude Code's OTLP emission. In daemon mode, Claude Desktop reads OTLP config from MDM, not from any wrapper-mutated file. Omit `otlpEndpoint` from the MDM profile to disable OTLP fleet-wide.

**Port collision:** If port `49153` is unavailable at startup, `serve` prints the error and exits (unlike the CLI wrapper, which falls back to `:0`). The MDM-baked `gatewayBaseUrl` is a fixed URL that cannot follow a dynamic fallback. Stop the existing instance before restarting.

### Installing as a background service

Register the daemon as a per-user OS service so it starts automatically at login:

| OS | One-liner |
|----|-----------|
| macOS | `databricks-claude serve install` |
| Linux | `databricks-claude serve install` |
| Windows | `databricks-claude serve install` |

The install command writes a native service manifest and starts the daemon immediately:
- **macOS:** LaunchAgent plist at `~/Library/LaunchAgents/databricks-claude-daemon.plist`
- **Linux:** systemd user unit at `~/.config/systemd/user/databricks-claude-daemon.service`
- **Windows:** Scheduled Task (logon trigger) via `schtasks.exe`

Service name across all platforms: **`databricks-claude-daemon`**.

Optional flags for `serve install`:
- `--profile <name>` — Databricks profile to bake into the manifest
- `--port <int>` — proxy port (default: 49153)
- `--log-file <path>` — log file path (default: per-OS, e.g. `~/Library/Logs/databricks-claude-daemon/serve.log`)
- `--otel-metrics-table`, `--otel-logs-table`, `--otel-traces-table` — OTEL UC tables
- `--skip-auth-check` — skip the install-time auth probe (required for CI / MDM init scripts where stdin is not a tty and `databricks auth login` cannot prompt)

#### Install-time authentication

By default, `serve install` verifies that the resolved profile has a valid Databricks token **before** writing any service-manager manifest. Behaviour:

- **Interactive tty + authed**: install proceeds silently.
- **Interactive tty + not authed**: runs `databricks auth login --profile <name>` to prompt the browser flow, then proceeds.
- **Non-tty + not authed**: aborts with an actionable error before writing any unit file. The daemon path is non-interactive (it cannot pop a browser under systemd/launchd/schtasks), so writing a unit that's guaranteed to crash-loop would be worse than failing fast.
- **Non-tty + authed**: install proceeds silently.
- **`--skip-auth-check`**: bypass the probe entirely. The unit is written immediately; the daemon will refuse to start until `databricks auth login --profile <name>` has been run separately. Use this in MDM fleet init scripts where auth is seeded out-of-band.

After install, a `/health` probe runs against `127.0.0.1:<port>` with a 10-second deadline to verify the daemon actually came up healthy. On timeout, the install command surfaces a diagnostics tail (`journalctl --user` on Linux, `launchctl print` plus the daemon stderr log on macOS) to stderr — but **does not auto-uninstall**. The unit file stays put so you can debug it. Re-running `serve install` is idempotent.

**Limitation**: install must be run as the user the daemon will run as. Running `sudo databricks-claude serve install` writes a systemd unit owned by root or a LaunchAgent under `/Library/LaunchAgents`, neither of which is what the per-user `serve` design intends. If you need to install for a different user from a privileged shell, use `sudo -u <user> -- databricks-claude serve install` so the unit/plist lands in that user's `$HOME`. MDM fleet rollouts that need cross-user install at scale are out of scope for this command; deploy a system-wide LaunchDaemon or systemd system unit manually if you need that.

#### Status / removal

```bash
# Check if the daemon is registered, running, and healthy:
databricks-claude serve status

# Remove the OS service registration (stops the daemon too):
databricks-claude serve uninstall
```

**After a binary upgrade:** The manifest bakes in the binary path at install time. Re-run `serve install` after upgrading to refresh the path. `serve status` will warn if the manifest path doesn't match the current binary.

> **macOS Gatekeeper note:** If your binary is unsigned or quarantined, `serve install` prints a one-line warning but the install still proceeds. To suppress the warning, run `xattr -dr com.apple.quarantine /path/to/databricks-claude` or sign the binary. The install is not blocked.

> **Linux user-session note:** `systemd --user` services run inside your login session. If you log out, the daemon stops — it restarts automatically on your next login. This is correct behavior for interactive per-user deployments. If you want the daemon to survive all logouts, run `loginctl enable-linger` first (requires your admin's approval on managed devices). This is not done automatically.

## Headless Mode

`--headless` starts the proxy without launching a `claude` child process, for use by IDE extensions and external tooling.

```bash
databricks-claude --headless
# prints: PROXY_URL=http://127.0.0.1:<port>
```

### Lifecycle Management

- **`GET /health`** — liveness check, returns `{"tool":"databricks-claude","version":"...","pid":...}`
- **`POST /shutdown`** — decrements the session refcount; when it reaches 0, the proxy exits. Returns `{"remaining": N, "exiting": true/false}`
- **Idle timeout** — after 30 minutes with no proxied requests, the proxy shuts down automatically. Configure with `--idle-timeout <duration>` (e.g. `10m`, `1h`). Use `--idle-timeout 0` to disable.

## How It Works

`databricks-claude` wraps the `claude` binary. It:

1. Binds a local HTTP proxy on a configured port (default `49153`, stored in `~/.claude/.databricks-claude.json`)
2. Writes `~/.claude/settings.json` once to point `ANTHROPIC_BASE_URL` at the proxy (idempotent — no restore on exit)
3. Launches `claude` with your args — fully transparent
4. Injects fresh Databricks OAuth tokens on every request (auto-refreshed from `databricks auth token`)
5. Tracks concurrent sessions with a ref-count; the last session out closes the listener

## Reference

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--profile` | `DEFAULT` | Databricks CLI profile |
| `--verbose`, `-v` | `false` | Enable debug logging to stderr |
| `--log-file` | | Write debug logs to a file (combinable with `--verbose`) |
| `--otel` | `false` | Enable OTEL telemetry proxying (metrics + logs). A signal is emitted only when its table is set. |
| `--otel-metrics-table` | `main.claude_telemetry.claude_otel_metrics` (only when `--otel` is set) | Unity Catalog table for OTEL metrics |
| `--otel-logs-table` | derived from metrics table when `--otel` is set | Unity Catalog table for OTEL logs |
| `--otel-traces` | `false` | Enable Claude Code's `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA` traces export. Standalone — does not require `--otel`. |
| `--otel-traces-table` | (none) | Unity Catalog table for OTEL traces — required for traces to actually be emitted |
| `--no-otel` | | Clear every persisted OTEL key (metrics + logs + traces + telemetry toggle) |
| `--no-otel-metrics` | | Clear only the metrics keys from `~/.claude/settings.json` |
| `--no-otel-logs` | | Clear only the logs keys from `~/.claude/settings.json` |
| `--no-otel-traces` | | Clear only the traces keys (incl. `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA`) from `~/.claude/settings.json` |
| `--upstream` | auto-discovered | Override the AI Gateway URL |
| `--proxy-api-key` | | Require Bearer token auth on all proxy requests |
| `--port` | `49153` | Proxy listen port (saved for future sessions) |
| `--tls-cert` | | Path to TLS certificate file (requires `--tls-key`) |
| `--tls-key` | | Path to TLS private key file (requires `--tls-cert`) |
| `--headless` | `false` | Start proxy without launching claude (for IDE extensions) |
| `--idle-timeout` | `30m` | Idle timeout in headless mode (`0` disables) |
| `--version` | | Print version and exit |
| `--print-env` | | Print resolved configuration (token redacted) and exit |
| `--help`, `-h` | | Print the wrapper's flags and exit. Use `databricks-claude -- --help` to forward to claude's own `--help`. |

All other flags and args are forwarded to `claude`.

Unity Catalog table schemas (Delta Lake DDL) for all three OTel signals are in [`docs/otel-uc-schemas.sql`](docs/otel-uc-schemas.sql).

### Auto-Discovery

On first run (when `ANTHROPIC_BASE_URL` is not set), `databricks-claude` auto-discovers:

- Your workspace host from `databricks auth env`
- Constructs the AI Gateway URL: `<host>/ai-gateway/anthropic`

### Profile Resolution Order

1. `--profile` CLI flag (writes to state file for future runs)
2. `profile` from `~/.claude/.databricks-claude.json` (state file)
3. `DEFAULT`

> **Note:** `DATABRICKS_CONFIG_PROFILE` is intentionally *not* consulted during
> resolution. Claude's `settings.json` injects env vars into child processes,
> which would override the user's explicit `--profile` choice persisted in the
> state file.

### Persistent Config (`~/.claude/.databricks-claude.json`)

On first setup (when `ANTHROPIC_BASE_URL` is not yet configured), `databricks-claude` saves your resolved profile to `~/.claude/.databricks-claude.json`. This file persists independently of `settings.json` — your profile is never lost when config is rewritten.

```json
{
  "profile": "my-workspace"
}
```

This means you only need to pass `--profile` once — subsequent runs will automatically use the saved profile. To switch profiles, pass `--profile <new-profile>` and the persistent config is updated.

The file is only written when the profile is not `DEFAULT` (the implicit default doesn't need saving).

## Debugging

### Verify your auth setup

Run `--print-env` to see the resolved configuration without starting the proxy. The token is redacted so it's safe to share output for debugging.

```bash
databricks-claude --print-env
```

Example output:

```
databricks-claude configuration:
  Profile:              DEFAULT
  DATABRICKS_HOST:      https://adb-1234567890123456.7.azuredatabricks.net
  ANTHROPIC_BASE_URL:   https://adb-1234567890123456.7.azuredatabricks.net/ai-gateway/anthropic
  ANTHROPIC_AUTH_TOKEN: dapi-***
  Upstream binary:      /usr/local/bin/claude
  OTEL enabled:         false
```

If the token shows as empty or the base URL looks wrong, check your Databricks CLI profile with `databricks auth env`.

### View full usage

`databricks-claude --help` (or `-h`) prints only the wrapper's own flags and subcommands. To reach claude's own `--help` — or pass any flag through to the wrapped `claude` CLI — use the `--` separator: anything after `--` is forwarded verbatim. For example, `databricks-claude -- --help` shows claude's help, and `databricks-claude -- --model opus -p "hi"` runs claude with the given flags.

## Shell Tab Completions

`databricks-claude` includes a completion engine (`pkg/completion`) that generates shell scripts from the binary's own flag definitions, so they stay in sync automatically. If you installed via Homebrew, completions are registered automatically — no manual setup required.

### Manual installation

If you installed from source or want to set completions up yourself, source the output of the `completion` subcommand in your shell rc file:

```bash
# Bash (~/.bashrc)
eval "$(databricks-claude completion bash)"

# Zsh (~/.zshrc)
eval "$(databricks-claude completion zsh)"

# Fish (~/.config/fish/config.fish)
databricks-claude completion fish | source
```

### What gets completed

- **Flag names** — `--<Tab>` lists all flags (long and short forms).
- **Flag values** — context-aware completions for flags that accept a value:
  - `--profile` completes from `~/.databrickscfg` section headers (updated live, no rehash needed).
  - `--upstream`, `--log-file`, `--tls-cert`, `--tls-key` complete with local file paths.
  - Flags like `--port` or `--otel-metrics-table` suppress file completion.
- **Passthrough boundary** — after a bare `--`, completions stop. Everything beyond that is forwarded to the wrapped `claude` binary.

### How the engine works

This section documents the `pkg/completion` package for other projects that import it.

The `completion` subcommand is the very first check in `main()`, before any config loading, auth, or state. This makes it safe to call in restricted environments like the Homebrew install sandbox.

```
main.go
  └─ if os.Args[1] == "completion"
       └─ completion.Run(args, flagDefs, binaryName)
            ├─ "bash"  → GenerateBash()
            ├─ "zsh"   → GenerateZsh()
            └─ "fish"  → GenerateFish()
```

**`FlagDef` struct** — each flag is described by a single struct in `completion_flags.go`:

| Field | Type | Purpose |
|-------|------|---------|
| `Name` | `string` | Flag name without `--` (e.g. `"profile"`) |
| `Short` | `string` | Single-char alias without `-` (e.g. `"v"`), or empty |
| `Description` | `string` | Human-readable description shown in completions |
| `TakesArg` | `bool` | `true` if the flag consumes the next token as its value |
| `Completer` | `string` | Named completer function, or empty for no value completion |

**Named completers** — two built-in completer names are supported:

- `"__databricks_profiles"` — reads `[section]` headers from `~/.databrickscfg`.
- `"__files"` — completes with local file paths (uses each shell's native mechanism).

Completers are emitted as shell functions embedded in the generated script — no external dependencies at completion time.

**Adding a new flag** — add an entry to the `flagDefs` slice. The completion script, `knownFlags` map, and flag parsing all derive from this single slice. Consistency tests enforce that every `FlagDef` appears in `knownFlags` and vice-versa.

**Integrating in another binary** — import `pkg/completion`, define your own `[]FlagDef`, and add the early-exit check to `main()`:

```go
import "github.com/IceRhymers/databricks-claude/pkg/completion"

var flagDefs = []completion.FlagDef{ /* ... */ }

func main() {
    if len(os.Args) >= 2 && os.Args[1] == "completion" {
        completion.Run(os.Args[2:], flagDefs, "my-binary")
        os.Exit(0)
    }
    // ... rest of main
}
```

## Automatic Update Check

`databricks-claude` checks for newer releases on startup (once every 24 hours) and prints a one-line notice to stderr when an update is available. The check is synchronous with a 2-second timeout — if GitHub is unreachable it silently skips.

### Update notification

When a newer version exists you'll see:

```
# Direct install
databricks-claude: update available (v0.11.0). Run: databricks-claude update

# Homebrew install
databricks-claude: update available (v0.11.0). Run: brew upgrade databricks-claude
```

### `update` subcommand

```bash
databricks-claude update
```

Force-checks GitHub for the latest release (bypasses the 24-hour cache) and prints upgrade instructions:

| Install method | Output |
|---|---|
| Already latest | `databricks-claude v0.10.1 is already the latest version` |
| Direct install | `Update available: v0.11.0. Download from: https://github.com/...` |
| Homebrew | `Update available: v0.11.0. Run: brew upgrade databricks-claude` |

No binary is replaced — the command prints instructions only. In-place self-update is planned for a future release.

### Opt out

```bash
# Per-invocation flag
databricks-claude --no-update-check

# Per-session or permanent (add to shell profile)
export DATABRICKS_NO_UPDATE_CHECK=1
```

Both suppress the startup check and disable the `update` subcommand.

## Development

```bash
git clone https://github.com/IceRhymers/databricks-claude
cd databricks-claude
make test
make build
```

## License

MIT
