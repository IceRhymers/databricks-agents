<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-07-15 -->

# tomlconfig

## Purpose
String-based surgical TOML manipulation for `~/.codex/config.toml`. Avoids any TOML parser dependency by operating on raw lines. Handles backup/restore, atomic writes, crash recovery, and multi-session proxy URL handoff. The "surgical" approach means only managed keys and sections are modified — all other user content is preserved byte-for-byte.

Import path: `github.com/IceRhymers/databricks-agents/internal/codex/tomlconfig`. Lives under the module-root `internal/codex/` tree so it is importable from `cmd/databricks-codex` (`package main`) while staying codex-specific (not tool-agnostic like `internal/core`).

## Key Files

| File | Description |
|------|-------------|
| `tomlconfig.go` | `Manager` struct with `Backup`, `Patch`, `Restore`, `RestoreFromBackup`, `UpdateProxyURL`; all patching helpers |
| `tomlconfig_test.go` | Tests for patch/restore round-trips, surgical preservation, model resolution, crash recovery |

## For AI Agents

### Working In This Directory
- **No external dependencies** — this package must stay zero-dependency; use only stdlib
- Managed root keys: `profile`. Managed sections: `profiles.databricks-proxy`, `model_providers.databricks-proxy`, `otel`
- The `sentinel` constant (`\x00nil`) marks keys/sections that were absent before patching so they can be fully removed on restore
- `origRootKeys` and `origSections` maps track what was changed; `Restore` only undoes those changes
- Model line handling is special: `origModelLine` / `patchedModelVal` track the preserve-if-present logic for `model =` inside `[profiles.databricks-proxy]`
- `inAnySection` scans backward for a `[section]` header to avoid mistaking section-level keys for root-level keys
- **Byte-parity required** — patch/restore output must stay byte-identical with the standalone `databricks-codex` repo; the `cmd/databricks-codex` config.toml golden depends on it. Do not "improve" the patch/restore logic.

### Testing Requirements
- Run with `go test ./internal/codex/tomlconfig/... -v`
- Tests use in-memory TOML strings and temp files — no mocking needed
- Cover: round-trip (patch then restore produces identical original), surgical preservation (non-managed keys untouched), crash recovery (`RestoreFromBackup`), multi-session handoff (`UpdateProxyURL`)

### Common Patterns
- `atomicWrite`: write to `.config-*.tmp`, chmod 0600, then `os.Rename` into place
- Section boundary detection: next line starting with `[` (but not `[[`) ends the current section
- Backup file: `config.toml.databricks-codex-backup` — written on `Backup()`, removed on successful `Restore()`

## Dependencies

### Internal
- None (standalone package)

### External
- None (stdlib only: `fmt`, `log`, `os`, `path/filepath`, `strings`)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
