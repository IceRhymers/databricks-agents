<!-- Parent: ../../AGENTS.md -->
<!-- Generated: 2026-07-15 | Updated: 2026-07-15 -->

# internal/codex

## Purpose
Holds codex-specific libraries that are neither tool-agnostic (so they don't belong in `internal/core/`) nor imported by more than one launcher (so they don't belong in `pkg/`). Folded in via #201 alongside `cmd/databricks-codex`. Mirrors the role `pkg/` plays for claude's Claude-coupled libraries (`modeldiscovery`, `mdmprofile`, `websearch`), just under `internal/` instead, since it must be importable from `cmd/databricks-codex` (`package main`) while staying module-private.

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `tomlconfig/` | String-based surgical patcher for `~/.codex/config.toml` — `Manager.Patch`/`Backup`/`Restore`/`RestoreFromBackup`/`UpdateProxyURL`. See `tomlconfig/AGENTS.md`. |

## For AI Agents

### Working In This Directory
- Each package here must remain **zero external dependencies** -- Go stdlib only.
- Only `cmd/databricks-codex` should import from here. If a future launcher needs the same functionality, promote the shared parts into `internal/core/` rather than importing across launcher-specific trees.

### Testing Requirements
- Each package has co-located `*_test.go` files. Run `go test ./internal/codex/...` to test these, or `go test ./...` for the whole module.

## Dependencies

### Internal
- No cross-package dependencies within `internal/codex/`.

### External
- None (pure Go stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
