<!-- Parent: ../../AGENTS.md -->
<!-- Generated: 2026-07-15 | Updated: 2026-07-15 -->

# internal/opencode

## Purpose
Holds opencode-specific libraries that are neither tool-agnostic (so they don't belong in `internal/core/`) nor imported by more than one launcher (so they don't belong in `pkg/`). Folded in via #202 alongside `cmd/databricks-opencode`. Mirrors the role `internal/codex/` plays for codex (and `pkg/` plays for claude's Claude-coupled libraries), just under `internal/` so it must be importable from `cmd/databricks-opencode` (`package main`) while staying module-private.

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `jsonconfig/` | String/stdlib JSONC surgical patcher for `~/.config/opencode/opencode.json` — `Config.Patch`/`NeedsConfig`/`UpdateProxyURL`/`AddPlugin`/`RemovePlugin`. Owns the `databricks-proxy` (Anthropic `/v1`) and `databricks-gemini-proxy` (Gemini `/v1beta`) providers. See `jsonconfig/AGENTS.md`. |

## For AI Agents

### Working In This Directory
- Each package here must remain **zero external dependencies** -- Go stdlib only. The in-package `stripJSONC` deliberately replaces the external `tidwall/jsonc` dependency the standalone repo used.
- Only `cmd/databricks-opencode` should import from here. If a future launcher needs the same functionality, promote the shared parts into `internal/core/` rather than importing across launcher-specific trees.

### Testing Requirements
- Each package has co-located `*_test.go` files. Run `go test ./internal/opencode/...` to test these, or `go test ./...` for the whole module.

## Dependencies

### Internal
- No cross-package dependencies within `internal/opencode/`.

### External
- None (pure Go stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
