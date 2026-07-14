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
go install github.com/IceRhymers/databricks-agents/cmd/databricks-claude@latest
```

## Pick Your Setup

There are three ways to use `databricks-claude`. Most people want **session hooks** — set it once and `claude` just works everywhere, including IDE extensions. Pick whichever rows match your workflow; you can install more than one.

| Primary client | Recommended setup |
|----------------|-------------------|
| CLI Claude Code, VS Code extension, JetBrains plugin | **Session hooks** — `databricks-claude hooks install --profile <name>`. |
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
databricks-claude config websearch enable --backend duckduckgo
```

This persists `with_websearch=true` to `~/.claude/.databricks-claude.json`; subsequent `databricks-claude` invocations (and the `serve` daemon) pick it up automatically. To turn it off, run `databricks-claude config websearch disable`.

How it works:

- The proxy detects `web_search_*` and `web_fetch_*` entries in outgoing `/v1/messages` requests and rewrites them to standard client-tool definitions named `web_search` and `web_fetch`. This is required because the AI Gateway rejects unknown server-tool types.
- On the response side, the proxy parses the SSE stream from the AI Gateway. When the model emits a `tool_use` block for the rewritten `web_search`/`web_fetch` tool, the proxy: (a) rewrites the on-the-wire block type to `server_tool_use`, (b) accumulates the streaming `input_json_delta` fragments to assemble the tool's input, (c) executes the local backend (`Search` or `Fetch`), and (d) injects a synthetic `web_search_tool_result` content block per Anthropic's documented shape so Claude Code's helper sees results inline.
- For non-streaming (`stream=false`) responses, the proxy applies the same transformation to the JSON body before forwarding.
- A legacy fallback path also handles generic Anthropic API clients that do a client-tool loop: if the client returns an `is_error` `tool_result` for the rewritten tool, the proxy substitutes locally-fulfilled output on the next turn.
- All fulfillment is **headless** — pure stdlib HTTP, no browser process. JavaScript-rendered pages are not supported.
- `robots.txt` is enforced per host with a session cache.

`config websearch enable` flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--backend` | `duckduckgo` | Search backend. Values: `duckduckgo` (zero config, HTML scrape), `none` (disable search but keep fetch). |
| `--fetch-budget` | `102400` (100KB) | Max bytes returned per `web_fetch` call. Larger pages are truncated. |

Limitations:

- No JavaScript rendering — fetched pages are static HTML only.
- `robots.txt` blocks return an error `tool_result`; the model is told why.
- Per-fetch byte cap defaults to 100KB to protect the context window.
- Search backends `brave` and `searxng` are deferred to follow-up work; only `duckduckgo` and `none` are wired today.

When `with_websearch=false` (the default), the proxy forwards request bytes unchanged — there is no behavior change for users who don't opt in.

## Session Hooks (recommended)

Install hooks so every Claude Code session auto-starts the proxy on startup and releases it cleanly on exit — no manual `serve --session-mode` needed. The hooks keep the proxy running for all Claude clients — including ones that don't use the `databricks-claude` wrapper directly, such as the [Claude VS Code extension](https://marketplace.visualstudio.com/items?itemName=Anthropic.claude-code) and JetBrains/IntelliJ plugin.

> **Coexists with Claude Desktop.** If you've also installed the Claude Desktop mobileconfig, the hook's proxy lifecycle is harmless inside Desktop sessions — Desktop's inference does not consult `ANTHROPIC_BASE_URL` (it uses its own MDM-driven `inferenceCredentialHelper`).

### Install

```bash
databricks-claude hooks install --profile <name>
```

This is one-step setup: it persists your profile/port, writes `ANTHROPIC_BASE_URL` to `~/.claude/settings.json`, and registers the SessionStart and SessionEnd hooks. No prior `databricks-claude` invocation needed. Re-running is idempotent.

- **SessionStart** — calls `databricks-claude hooks session-start` on session startup: starts the proxy if it isn't already running. (Hook-invoked internal — not intended to run by hand.)
- **SessionEnd** — calls `databricks-claude hooks session-end` on session end: decrements the refcount; proxy exits when the last session closes. (Hook-invoked internal.)

### Uninstall

```bash
databricks-claude hooks uninstall
```

Removes only the databricks-claude hook entries. Other hooks in your settings are untouched.

### Notes

- Idempotent — safe to re-run after upgrades.
- The proxy starts on the configured port (default `49153`). If you use a custom port via `--port`, the hooks will respect that setting automatically (port is saved to the state file).
- Unclean exits (force-quit, OOM kill) are covered by the idle timeout — the proxy self-exits after 30 minutes with no inference traffic.

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

**Fleet rollout is documented end-to-end in the [Rollout Guide](#rollout-guide)** — helper-mode and daemon-mode walkthroughs, the signing prerequisite, per-endpoint setup, and verification. The path-pinning flags below are the ones you'll most often pass when generating artifacts from a reference workstation:

```bash
databricks-claude desktop generate-config \
  --profile <fleet-profile> \
  --binary-path /usr/local/bin/databricks-claude-credential-helper \
  --databricks-cli-path /usr/local/bin/databricks
```

- `--binary-path` — absolute path of the credential-helper symlink (or hardlink/copy) on every target endpoint.
- `--databricks-cli-path` — pins the `databricks` CLI absolute path, persisted to `~/.claude/.databricks-claude.json`.

The packaging method (`.pkg` installer, custom `brew` formula, etc.) is responsible for ensuring `databricks-claude` and its `databricks-claude-credential-helper` symlink land at the paths you embed in the config. For the signed `.pkg` route, see [MDM deployment with signed `.pkg`](#mdm-deployment-with-signed-pkg-self-signed) below.

### Daemon-mode (advanced)

Pass `--daemon` to emit artifacts pointing Claude Desktop at a local `databricks-claude serve` daemon instead of the Databricks AI Gateway directly. Daemon-mode unlocks OTLP forwarding from Desktop (helper-mode cannot, as Anthropic ships no `otlpCredentialHelper`). Requires `databricks-claude serve install` on each endpoint. Helper-mode (no flag) remains the default and recommended path for most deployments.

```bash
databricks-claude desktop generate-config --daemon \
  --profile <fleet-profile> \
  --port 49153 \
  --daemon-fake-key <fleet-key>
```

Add `--otel` to also emit OTLP keys — the emitted `otlpEndpoint` carries a `/otel` path prefix so Claude Desktop's exporter lands on the daemon's telemetry route rather than its inference catch-all. See the **[Rollout Guide](#rollout-guide)** for the full daemon-mode walkthrough: per-endpoint daemon install, first-launch auth, and verification.

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

## Rollout Guide

End-to-end guide for deploying `databricks-claude` to a Claude Desktop fleet. This is the **single source of truth** for fleet rollout — the [Claude Desktop Integration](#claude-desktop-integration) and [serve Subcommand](#serve-subcommand) sections cross-link here rather than duplicating rollout steps.

### Opinionated design: one workspace per fleet

**`databricks-claude` assumes one Databricks workspace per fleet, dedicated to Claude capacity** — a single profile shared across every end-user in your organization. This is the architectural opinion, not just a docs framing:

- One workspace = one Databricks profile name = one MDM artifact baked with that profile name = one auth command every end-user runs.
- The wrapper's profile-resolution chain (`flag → state → MDM tier → DEFAULT`) exists so end-users never think about profiles; the MDM tier carries the fleet's canonical answer.
- Multi-tenant / per-user workspace selection is **out of scope by design**. If your org has multiple Claude-capacity workspaces (one per business unit, etc.), each needs its own MDM artifact pushed to a distinct subset of endpoints — treat each subset as an independent fleet.

Why this opinion: it simplifies every code path (no per-user profile guessing), every deploy decision (one artifact per fleet), and every doc (no decision tree about profiles). It matches how organizations actually allocate Databricks workspaces.

**This guide is written for one admin deploying to N endpoints, all sharing one Databricks workspace.** Adapt accordingly if your org carves differently.

### Which mode? (read this first)

- **Helper-mode (default)** — pick this unless you specifically need OTLP telemetry from Claude Desktop. Desktop talks to the Databricks AI Gateway directly; the `databricks-claude` credential helper refreshes OAuth tokens on a TTL. Simplest deployment, fewest moving parts on endpoints, no daemon.
- **Daemon-mode (`--daemon`)** — pick this if you need OTLP forwarding from Claude Desktop, or if your fleet's security policy forbids short-lived tokens leaving the endpoint per call. Desktop talks to a localhost `databricks-claude serve` daemon that owns OAuth refresh. Higher deployment complexity — requires `serve install` on every endpoint.
- **Helper-mode + OTLP from Desktop is not currently supported.** Anthropic ships no `otlpCredentialHelper`, and OTLP requires dynamic auth that only the daemon provides. If upstream changes this, a third option will be added here.

### Signing prerequisite

**Upstream `databricks-claude` ships unsigned.** Signing is the deployer's responsibility — see [issue #54](https://github.com/IceRhymers/databricks-claude/issues/54) for the rationale (notarization and EV-cert infrastructure is cost-prohibitive for a no-revenue OSS project; it is an adopter concern, not an upstream deliverable). For fleet rollout you have three options:

1. **Build and sign yourself (recommended for real fleets).** Fork the repo (or clone and tag a version), build with `make`, sign with your org's Developer ID (macOS) and/or EV cert (Windows), and notarize via `make notarize` — a stub target documented for adopters (see below). Push the signed artifacts via your MDM. For the self-signed `.pkg` + trust-profile route, see [MDM deployment with signed `.pkg`](#mdm-deployment-with-signed-pkg-self-signed).
2. **Distribute via package manager.** `brew install IceRhymers/tap/databricks-claude` (macOS) and `scoop install databricks-claude` (Windows) strip the quarantine attribute automatically. Good for self-service installs; does not cover MDM-pushed deployments.
3. **Click-through Gatekeeper / SmartScreen per endpoint.** Only viable at very small N (< 5) or for dev/test installs. For macOS, document `xattr -dr com.apple.quarantine /path/to/databricks-claude` for your users.

**For real fleet rollouts (option 1), this guide assumes you have handled signing before pushing artifacts.** The rollout flow itself is identical regardless of who signed.

The repo ships a `make notarize` stub for adopters who build and sign their own releases. It documents the expected contract (`DEVELOPER_ID_APPLICATION`, an `xcrun notarytool` keychain profile, the codesign → notarize → staple sequence) and exits with guidance when unconfigured. Implementing it end-to-end is an adopter responsibility — upstream ships the surface to fork-and-fill.

### Helper-mode rollout (default)

1. **Build, sign, and install the binary.** See [Signing prerequisite](#signing-prerequisite). The binary and its `databricks-claude-credential-helper` alias must land at a predictable path on every endpoint (e.g. `/usr/local/bin`).
2. **Generate MDM artifacts** on the admin's machine:
   ```bash
   databricks-claude desktop generate-config --profile <fleet-profile>
   ```
   Produces `databricks-claude-desktop.{mobileconfig,reg,json}` in the current directory. The profile name baked in becomes the canonical fleet answer for every endpoint. For fleet path-pinning add `--binary-path` and `--databricks-cli-path` (see [MDM / fleet rollout](#mdm--fleet-rollout)), or `--for-pkg` if you deploy via the signed `.pkg`.
3. **Sign the artifacts if your MDM requires it.** Some MDM systems sign their own payloads — check your vendor's docs.
4. **Push via MDM.** Distribute the `.mobileconfig` to macOS endpoints (Jamf / Kandji / Intune / Workspace ONE) and import the `.reg` on Windows endpoints (Group Policy or your MDM's registry-push mechanism).
5. **First launch on each endpoint.** The end-user runs the one-time auth command:
   ```bash
   databricks auth login --host <workspace-url> --profile <fleet-profile>
   databricks-claude setup --profile <fleet-profile>
   ```
   With one profile per fleet this is a single command everyone runs once — trivially scriptable as an MDM login-triggered init script if you want it automated. `setup` is idempotent (no-op when already authed), so it is safe to run repeatedly.
6. **Verify.** On the endpoint:
   ```bash
   databricks-claude setup --profile <fleet-profile>   # reports "Already authenticated"
   ```
   Then restart Claude Desktop — its third-party-inference path now runs against your Databricks AI Gateway, with tokens refreshed automatically by the credential helper.

### Daemon-mode rollout

Steps 1–3 are the same as helper-mode (build, sign, and install the binary).

4. **Generate artifacts in daemon-mode** on the admin's machine:
   ```bash
   databricks-claude desktop generate-config --daemon \
     --profile <fleet-profile> \
     --port 49153 \
     --daemon-fake-key <fleet-key>
   ```
   Add `--otel` to also emit OTLP keys so Claude Desktop forwards telemetry through the daemon. Daemon-mode artifacts omit the credential helper, set `gatewayBaseUrl` to `http://127.0.0.1:<port>` with a static localhost-gate key, and (with `--otel`) set `otlpEndpoint` to `http://127.0.0.1:<port>/otel` — the `/otel` path prefix is required so Desktop's exporter lands on the daemon's telemetry route, not its inference catch-all.
5. **Install the daemon on each endpoint.** Three paths, in order of preference:
   - **Per-user (self-service):** the end-user runs `databricks-claude serve install` once after the binary is installed. This registers a per-user OS service (LaunchAgent / systemd user unit / Scheduled Task) — see [Installing as a background service](#installing-as-a-background-service).
   - **MDM init script:** push a login-triggered script that runs `databricks-claude serve install --skip-auth-check` from each user's context. `--skip-auth-check` is required because MDM scripts run without a tty and the install-time auth probe cannot prompt. Consult your MDM vendor's docs for per-user login-script delivery.
   - **System-wide (cross-user)** install is explicitly out of scope for `serve install` — deploy a LaunchDaemon (macOS) or systemd system unit (Linux) manually if you need it.
6. **Push MDM artifacts** (same as helper-mode step 4).
7. **First launch on each endpoint.** The end-user runs the one-time auth command:
   ```bash
   databricks auth login --host <workspace-url> --profile <fleet-profile>
   ```
   Auto-bootstrap on first daemon start was considered and rejected — see [issue #166](https://github.com/IceRhymers/databricks-claude/issues/166). With one profile per fleet, a single manual auth command is trivial enough not to warrant the daemon-side complexity.
8. **Verify.** On the endpoint:
   ```bash
   databricks-claude serve status
   ```
   Confirm it reports the daemon as registered, running, and healthy (`Registered=yes`, `Running=yes`, `Healthy=yes`). Then restart Claude Desktop — its inference path now runs through the localhost daemon, which owns OAuth refresh.

### Cross-cutting concerns

#### Profile resolution chain

Every endpoint resolves the Databricks profile via `flag → saved state → MDM tier → DEFAULT`:

- **flag** — an explicit `--profile` on a command invocation.
- **saved state** — `~/.claude/.databricks-claude.json`, written by `setup` / `generate-config` / a wrapper run on that machine.
- **MDM tier** — the `databricksProfile` key in the `com.icerhymers.databricks-claude` domain (macOS managed-preferences plist; Windows registry under `HKCU\SOFTWARE\IceRhymers\databricks-claude`), read by `pkg/mdmprofile`. This is what the generated artifact carries.
- **DEFAULT** — the fallback sentinel.

With one profile per fleet, the MDM tier is always the canonical answer — end-users never specify a profile.

#### Token refresh expectations

Databricks OAuth refresh tokens are short-lived — roughly **24 hours**, not 90 days. Every end-user re-authenticates roughly daily, so token-expiry recovery is the common case, not an edge case:

- **Helper-mode** recovers transparently. When the access token expires, Claude Desktop's next credential-helper poll fails the fast-path, the helper runs `databricks auth login` (its subprocess stdout routed to stderr so it does not corrupt the token stream Desktop reads), a browser SSO window opens, and the helper retries — emitting a fresh token. To avoid a browser-at-first-launch surprise, run `databricks-claude setup --profile <fleet-profile>` before the user opens Desktop (this is what the first-launch step does).
- **Daemon-mode** recovers via the daemon's normal `tp.Token()` refresh path. The daemon owns OAuth refresh and runs it on its standard code path — validated to work headless under LaunchAgent / systemd with no controlling tty.

In both modes the end-user only sees an interruption if the *refresh* token itself expires — then they re-run `databricks auth login --host <workspace-url> --profile <fleet-profile>`.

#### Gatekeeper / Defender SmartScreen

Covered in [Signing prerequisite](#signing-prerequisite). Per-OS specifics: macOS quarantine is cleared with `xattr -dr com.apple.quarantine <path>` or by signing + notarizing the binary; Windows SmartScreen is satisfied by an EV-signed binary or a click-through "Run anyway" at small N. `serve install` prints a one-line warning on an unsigned or quarantined macOS binary but does not block the install.

#### Verification per endpoint

A single command an admin can run to check endpoint health:

```bash
# Helper-mode:
databricks-claude setup --profile <fleet-profile> && databricks-claude config show

# Daemon-mode (additionally):
databricks-claude serve status
```

#### Mode switching

To switch a fleet between modes, the admin generates new MDM artifacts (helper-mode or daemon-mode), pushes them, and end-users pick up the new config on the next Desktop restart:

- **Helper-mode → daemon-mode:** push daemon-mode artifacts and install the daemon on each endpoint. A daemon left installed from a prior rollout is harmless and will pick up requests.
- **Daemon-mode → helper-mode:** push helper-mode artifacts, then run `databricks-claude serve uninstall` on each endpoint (via an MDM script) to remove the now-unused daemon.

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

# With proxy API key authentication:
databricks-claude --proxy-api-key my-secret-key "explain this codebase"

# With TLS:
databricks-claude --tls-cert cert.pem --tls-key key.pem "explain this codebase"
```

OTEL telemetry, websearch, and the persistent settings.json bootstrap moved
behind the `config` subcommand in v0.x — see [`config` Subcommand](#config-subcommand)
below.

### Alias (optional)

```bash
echo 'alias claude="databricks-claude"' >> ~/.zshrc  # or ~/.bashrc
```

Claude Desktop integration lives under the `desktop` subcommand — run `databricks-claude desktop` for its action list and flags.

## `config` Subcommand

Persistent config editor. Mutates `~/.claude/settings.json` (env block) and `~/.claude/.databricks-claude.json` (state file) for *future* invocations — none of these subcommands affect the current invocation, and the storage semantics (two-store model, sentinel guards, OTEL section *removal* on disable, state-file preservation when toggling) match the legacy root flags exactly.

```
config otel enable  [--metrics-table T] [--logs-table T] [--traces] [--traces-table T]
config otel disable [--metrics] [--logs] [--traces]      # no flags = disable everything
config websearch enable  [--backend duckduckgo|none] [--fetch-budget N]
config websearch disable
config write                                             # bootstrap settings.json
config show                                              # diagnostic dump
```

### `config otel enable|disable`

Toggle OpenTelemetry signal export. Tables (metrics/logs/traces) persist to the state file; OTEL env keys are written to settings.json's env block. `disable` clears settings.json keys but **preserves** state-file table preferences so a subsequent `enable` restores them.

```bash
# Enable with explicit metrics + logs tables:
databricks-claude config otel enable \
  --metrics-table main.claude_telemetry.claude_otel_metrics \
  --logs-table   main.claude_telemetry.claude_otel_logs

# Bare enable — applies the legacy default metrics table and derives logs from it:
databricks-claude config otel enable

# Per-signal disable (others stay live):
databricks-claude config otel disable --metrics
databricks-claude config otel disable --logs
databricks-claude config otel disable --traces

# Disable everything (state file table prefs preserved):
databricks-claude config otel disable
```

### `config websearch enable|disable`

Toggle local web_search / web_fetch fulfillment in the proxy (workaround until Databricks FMAPI ships native server-side tool support). State file only — no settings.json key.

```bash
# Default backend (duckduckgo, 100KB fetch budget):
databricks-claude config websearch enable

# Disable scraping but keep web_fetch:
databricks-claude config websearch enable --backend none --fetch-budget 204800

# Turn it off (clears backend + fetch-budget so a re-enable picks up defaults):
databricks-claude config websearch disable
```

See [Web Search & Fetch (Workaround, opt-in)](#web-search--fetch-workaround-opt-in) for backend details and limitations.

### `config write`

Writes the first-run `~/.claude/settings.json` env block (proxy URL, model routing, custom headers, optional OTEL keys) and exits. No proxy startup, no port binding, no child process — purely a settings bootstrap. Idempotent.

Model routing is **auto-discovered from Unity Catalog** on every run: `config write` fetches a token and queries the Unity AI Gateway model-services API for the newest Claude model per family (opus/sonnet/haiku) that supports the Anthropic Messages API, then persists the result to `~/.claude/.databricks-claude.json` so later launches don't need to hit the network. If a family can't be resolved (no `EXECUTE` grant, empty catalog), `config write` prints a copy-pasteable pin hint for that family and fails loudly rather than silently mis-routing; it only aborts entirely when *zero* families resolve. See [`doctor` Subcommand](#doctor-subcommand) to diagnose or fix drift later without re-running the full bootstrap.

```bash
# Bare bootstrap (default profile, default port):
databricks-claude config write

# MDM rollout — bake fleet-wide profile + workspace into settings.json:
databricks-claude config write --profile databricks-ai-inference

# Bootstrap with OTEL routing AND websearch enabled:
databricks-claude config write \
  --metrics-table main.telemetry.claude_otel_metrics \
  --logs-table   main.telemetry.claude_otel_logs \
  --with-websearch
```

### `config show`

Print the resolved configuration with the token redacted. Read-only — zero writes to settings.json or state. Equivalent to the legacy `--print-env`.

```bash
databricks-claude config show
```

```
databricks-claude configuration:
  Profile:              DEFAULT
  DATABRICKS_HOST:      https://adb-1234567890123456.7.azuredatabricks.net
  ANTHROPIC_BASE_URL:   https://adb-.../ai-gateway/anthropic
  ANTHROPIC_AUTH_TOKEN: dapi-***
  Upstream binary:      /usr/local/bin/claude
  OTEL enabled:         false
```

> **Migrating from pre-`config` versions:** the 14 root flags (`--otel*`, `--no-otel*`, `--write-claude-config`, `--print-env`, `--with-websearch`, `--websearch-*`) were **removed, not aliased** — they now pass through to claude as unknown args. Update any scripts to use the new `config` subcommands. Storage stayed identical: settings.json + `.databricks-claude.json` written exactly the same way as before.

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

## hooks Subcommand

Session-hook deployment mode for Claude Code. Installs SessionStart/SessionEnd hook entries into `~/.claude/settings.json` that spin a refcount-managed proxy up on session start and tear it down on session end — so `claude` "just works" against your Databricks workspace without manually launching `databricks-claude` each time. See [Session Hooks (recommended)](#session-hooks-recommended) for the user-facing onboarding.

```bash
# First-time install:
databricks-claude hooks install --profile <name>

# Remove the hook entries (e.g. before switching to the long-lived `serve` daemon):
databricks-claude hooks uninstall
```

`hooks install` is one-step setup: it persists `--profile` / `--port` to the state file, writes a placeholder `ANTHROPIC_BASE_URL` into `~/.claude/settings.json` (the SessionStart hook overwrites it with the discovered AI Gateway URL on first run), and registers the SessionStart and SessionEnd entries. Idempotent — re-running after upgrades is safe and produces no duplicates.

`hooks uninstall` removes only the databricks-claude hook entries; other hooks in your settings are untouched. Tolerates "not installed" (no-op when no databricks-claude hooks are present).

| Subcommand | Purpose |
|------|---------|
| `hooks install` | Install SessionStart/SessionEnd hooks AND perform first-run env bootstrap. Accepts `--profile P` / `--port N`. |
| `hooks uninstall` | Remove databricks-claude hooks from `~/.claude/settings.json`. |
| `hooks session-start` | **Hook-invoked internal.** Refcount-acquire + spawn the proxy if not already healthy. Called by the SessionStart hook JSON; not intended to be invoked directly. |
| `hooks session-end` | **Hook-invoked internal.** POST `/shutdown` to decrement the refcount; proxy exits when the last session ends. Called by the SessionEnd hook JSON; not intended to be invoked directly. |

The hooks deployment mode is unchanged behaviorally from earlier releases — the proxy still spins up on SessionStart and tears down on SessionEnd. Only the surface moved: prior to this release the same actions were `databricks-claude --install-hooks` / `--uninstall-hooks` / `--headless-ensure` / `--headless-release`. The old root flags have been **removed** (not aliased) and the generated hook JSON now invokes the new subcommand names. No back-compat for already-installed hooks; re-run `hooks install` to refresh the entries.

## serve Subcommand

`serve` runs the standalone proxy under one of two lifecycle policies. **One mode flag is REQUIRED** — bare `serve` (no `--session-mode`, no `--daemon`, no sub-subcommand) is a hard error so a typo at the hooks spawn site can't silently degrade to the wrong lifecycle.

| Mode | One-liner |
|------|-----------|
| `serve --session-mode` | Session-scoped proxy. Refcounted, `/shutdown` route, idle-timeout, settings.json restore-on-exit, fallback-port bind. Was the `--headless` root flag prior to #174. Used by IDE extensions and the `hooks session-start` internal. |
| `serve --daemon` | Long-lived daemon. No refcount, no `/shutdown`, exclusive-port bind, `daemon:true` in `/health`, append-only logging, never mutates `settings.json`. |
| `serve install\|uninstall\|status` | Daemon OS-service registration (LaunchAgent / schtasks / systemd --user). No mode flag needed — these are meta-operations on the daemon's service manifest. |

`serve` is the standalone-proxy entrypoint for the **third deployment mode** (long-lived daemon) alongside the per-session CLI wrapper (`databricks-claude [args] -- claude-args`) and the SessionStart hooks (`hooks install`).

### Daemon mode

Owns Databricks OAuth refresh and exposes inference + OTLP on `127.0.0.1`. Designed for LaunchAgent (macOS) or systemd (Linux) deployment, where the daemon is started once at login and kept running. Configure your client to point at the daemon:
- **Claude Desktop:** via MDM, set `gatewayBaseUrl: http://127.0.0.1:<port>` with a static fake API key (no per-user secret distribution).
- **Claude Code:** edit `~/.claude/settings.json` once to set `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>` in the env block. The daemon does NOT mutate `settings.json` itself — it stays outside the per-tool lifecycle by design.

```bash
# Minimal daemon on default port:
databricks-claude serve --daemon

# With explicit profile, port, and persistent log file:
databricks-claude serve --daemon \
  --profile databricks-ai-inference \
  --port 49153 \
  --log-file /var/log/databricks-claude/daemon.log

# With OTEL table routing:
databricks-claude serve --daemon \
  --otel-metrics-table main.claude_telemetry.claude_otel_metrics \
  --otel-logs-table main.claude_telemetry.claude_otel_logs
```

| Flag | Purpose |
|------|---------|
| `--daemon` | **Required** to select the daemon lifecycle (or use a sub-subcommand). |
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

> For a full fleet rollout — generating MDM artifacts, signing, per-endpoint install, and verification — see the **[Rollout Guide](#rollout-guide)**. This section covers the `serve install` command itself.

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

### Using `serve` with Claude Code

Once you've installed the daemon (`serve install`), Claude Code still needs to know to talk to it. Rather than hand-editing `~/.claude/settings.json` and getting model names wrong as Databricks ships new ones, **let the wrapper bootstrap your settings once**:

```bash
# 1. One-time: bootstrap settings.json with the right env block
#    (proxy URL, fake auth token, Databricks model routing, custom headers).
databricks-claude config write

# 2. Start the daemon as a service (or run `serve` directly).
databricks-claude serve install
```

`claude` will now route through whichever instance is listening on `127.0.0.1:49153` — the daemon, in this case.

Optional: pair with `--profile`, `--port`, or `--with-websearch` if you use a non-default workspace or want local web-search/web-fetch fulfillment:

```bash
databricks-claude config write --profile my-workspace --port 49153

# Enable local web_search/web_fetch fulfillment in the daemon:
databricks-claude config write --with-websearch
```

`--with-websearch` (and its sibling `--backend` / `--fetch-budget` knobs) persists to `~/.claude/.databricks-claude.json` so the daemon picks it up on its next start. Without this, the daemon serves stock Anthropic web-tool requests, which Databricks FMAPI does not currently support — `claude` would then fail web searches even though the proxy is otherwise healthy. (You can also turn websearch on independently of the bootstrap with `databricks-claude config websearch enable`.)

**Why bootstrap via `config write` instead of hand-editing `settings.json`?**

The wrapper writes more than `ANTHROPIC_BASE_URL` on first run. It also writes Databricks-specific model routing (`ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`), the `x-databricks-use-coding-agent-mode` custom header, and the experimental-betas flag. The model routing is **auto-discovered live from Unity Catalog** every time `config write` runs — it queries the Unity AI Gateway model-services API for the newest Claude model per family you have `EXECUTE` access to, rather than a name baked into the binary. Letting the wrapper write them keeps you in sync as Databricks ships new models or retires old aliases; run `databricks-claude doctor` any time to check whether your settings.json has drifted from what discovery would resolve today. Hand-edits get stale.

The write is idempotent — `ensureConfig` short-circuits when the env block already matches.

**Fallback (if `config write` is unavailable):** Use `databricks-claude serve --session-mode`, wait for `PROXY_URL=http://127.0.0.1:49153`, then stop it (Ctrl+C). This works but binds the proxy port unnecessarily — use `config write` instead.

**Notes:**

- **The daemon does NOT mutate `~/.claude/settings.json`.** That's the whole point of the daemon vs. the per-session CLI wrapper — it lives outside the per-tool lifecycle. The one-time `config write` bootstrap above is the wrapper doing its first-run setup; subsequent daemon restarts do not touch your settings.
- **Re-bootstrap when model names drift.** If you upgrade `databricks-claude` and the project ships new default model names, re-run `databricks-claude config write` once to refresh them. The bootstrap is idempotent and only writes keys that differ.
- **OTEL tables persist to state.** Run `databricks-claude serve --daemon --otel-metrics-table foo --otel-logs-table bar` once; the daemon (or its installed service) picks them up from `~/.claude/.databricks-claude.json` on every restart thereafter.
- **Don't run the CLI wrapper (`databricks-claude claude …`) at the same time as the daemon for the same workspace.** Pick one deployment mode per workspace; mixing both means two proxies fighting over the same port and settings block.
- **Hooks coexist cleanly.** If you've also installed SessionStart hooks (`databricks-claude hooks install`), they probe the daemon's `/health` and no-op when it's running, falling back to per-session proxy only if the daemon is down.

To stop using the daemon, run `databricks-claude serve uninstall` and remove the `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` lines from `~/.claude/settings.json` (or delete the whole `env` block if you don't use Claude Code anymore).

## Session-Scoped Proxy (`serve --session-mode`)

`serve --session-mode` starts the proxy without launching a `claude` child process, for use by IDE extensions and external tooling. This was the `--headless` root flag prior to #174 — see the breaking-change callout below.

```bash
databricks-claude serve --session-mode
# prints: PROXY_URL=http://127.0.0.1:<port>
```

### Lifecycle Management

- **`GET /health`** — liveness check, returns `{"tool":"databricks-claude","version":"...","pid":...,"daemon":false}`
- **`POST /shutdown`** — decrements the session refcount; when it reaches 0, the proxy exits. Returns `{"remaining": N, "exiting": true/false}`
- **Idle timeout** — after 30 minutes with no proxied requests, the proxy shuts down automatically. Configure with `--idle-timeout <duration>` (e.g. `10m`, `1h`). Use `--idle-timeout 0` to disable. Only meaningful in `--session-mode` — the `--daemon` lifecycle has no idle exit.

### Optional flags (session mode)

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `49153` | Proxy listen port (fallback-aware in session mode). |
| `--idle-timeout` | `30m` | Idle timeout (`0` disables). Only meaningful in `--session-mode`. |
| `--proxy-api-key` | | Require Bearer token auth on all proxy requests. |
| `--tls-cert`, `--tls-key` | | Enable TLS on the proxy listener. |
| `--upstream` | auto-discovered | Override the AI Gateway URL. |
| `--profile` | state → `DEFAULT` | Databricks CLI profile. |
| `--log-file` | | Write debug logs to a file. |
| `--verbose`, `-v` | `false` | Enable debug logging to stderr. |

> **Breaking change (#174):** `databricks-claude --headless` is now `databricks-claude serve --session-mode`. `--idle-timeout` moved from root to a `serve` flag. Bare `serve` (no mode flag, no sub-subcommand) now exits with code 2 — specify `--session-mode`, `--daemon`, or one of `install|uninstall|status`. The required-explicit-mode invariant prevents a typo at the hooks spawn site from silently degrading to the daemon lifecycle (wrong refcount semantics, broken `hooks session-end`).

## `doctor` Subcommand

Non-interactive diagnostic for model routing. `doctor` runs the same Unity AI Gateway model discovery as `config write`, diffs the discovered per-family models (opus/sonnet/haiku) against the pins currently written into `~/.claude/settings.json`, and prints the delta. Read-only by default — without `--fix`, it never touches settings.json.

```bash
# Diagnose model drift (read-only, exits 1 if anything is out of date):
databricks-claude doctor

# Apply the discovered models to settings.json:
databricks-claude doctor --fix
```

Per-family status:

| Status | Meaning |
|--------|---------|
| `ok` | settings.json pin matches discovery |
| `drift` | pin differs from discovery (non-legacy) |
| `stale-legacy` | pin is a legacy `databricks-...` name; migrate to the UC FQN |
| `unresolved` | discovery found no model for the family; the current pin is preserved under `--fix` (a working pin is never blanked) |
| `new` | no pin yet; discovery found one |

| Flag | Default | Description |
|------|---------|-------------|
| `--profile string` | state → `DEFAULT` | Databricks CLI profile |
| `--port int` | state → `49153` | Proxy port baked into `ANTHROPIC_BASE_URL` when `--fix` rewrites settings.json |
| `--fix` | | Rewrite settings.json to the discovered models, through the same atomic writer the launch path uses |
| `--help`, `-h` | | Show this help message |

Exit codes:

| Code | Meaning |
|------|---------|
| 0 | all pins up to date, or `--fix` applied |
| 1 | drift detected without `--fix`, or discovery/write failure |

`doctor` is the sanctioned recovery path for the hook/daemon flow, which can't prompt — run it (with `--fix`) whenever settings.json's model pins look stale, e.g. after Databricks retires a legacy `databricks-claude-*` alias or ships a newer model in a family you have access to.

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
| `--upstream` | auto-discovered | Override the AI Gateway URL |
| `--proxy-api-key` | | Require Bearer token auth on all proxy requests |
| `--port` | `49153` | Proxy listen port (saved for future sessions) |
| `--tls-cert` | | Path to TLS certificate file (requires `--tls-key`) |
| `--tls-key` | | Path to TLS private key file (requires `--tls-cert`) |
| `--version` | | Print version and exit |
| `--help`, `-h` | | Print the wrapper's flags and exit. Use `databricks-claude -- --help` to forward to claude's own `--help`. |

OpenTelemetry, websearch, settings.json bootstrap, and resolved-config diagnostic flags moved behind the [`config` subcommand](#config-subcommand) tree.

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

Run `databricks-claude config show` to see the resolved configuration without starting the proxy. The token is redacted so it's safe to share output for debugging.

```bash
databricks-claude config show
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

### Diagnose model routing drift

If Claude Code is calling a model you didn't expect (e.g. after Databricks ships a new model or retires a legacy alias), run `databricks-claude doctor` to diff your settings.json model pins against what Unity AI Gateway discovery resolves today, and `databricks-claude doctor --fix` to apply the discovered models. See [`doctor` Subcommand](#doctor-subcommand) for the full status matrix and exit codes.

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
  - Flags like `--port` (or `--metrics-table` under `config otel enable`) suppress file completion.
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
import "github.com/IceRhymers/databricks-agents/pkg/completion"

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
