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
- Managed root keys: `model`, `model_provider`. Managed sections: `model_providers.databricks-proxy`, `otel`
- **Top-level default provider (not a named profile)** — `Patch` registers the proxy as the root default: `model_provider = "databricks-proxy"` + `[model_providers.databricks-proxy]`, with a root `model`. It does NOT write a root `profile = "databricks-proxy"` selector or a `[profiles.databricks-proxy]` section. The hooks path runs bare `codex` (no `--profile` injection point), and Codex ≥0.134 makes a root `profile` selector a hard startup error (#230).
- **One-directional legacy migration** — `Patch` calls `removeSection(content, "profiles.databricks-proxy")` and `removeLegacyRootProfile(content)` to strip the old profile-selector shape. Because databricks-codex is **patch-and-leave** (`Restore` is a runtime no-op — `Manager.Backup()` is never called on the launch path, so `m.original` is nil), the fatal shape would otherwise stay persisted in a returning user's config; the removal is therefore **permanent** in production. `removeLegacyRootProfile` deliberately does not record into `origRootKeys`, so a `Restore` could never resurrect the fatal root key. `removeSection` *does* record the stripped `[profiles.databricks-proxy]` block into `origSections`, but that record is inert: `Restore` is never called on the runtime path, and `restoreSection` is header-anchored (no-ops once the header is gone). A user's own non-proxy root `profile = "<other>"` is left untouched, with a non-fatal `log.Printf` warning.
- **Model resolution reads on-disk `content`, not `m.original`** — since `m.original` is nil at runtime, the preserve-if-present logic reads the root model via `findRootModel(content)`. Precedence: explicit `--model` (`cfg.ModelExplicit`) wins → else existing root `model` preserved → else the resolved default (`cfg.Model`). Emit order is `model` then `model_provider`.
- The `sentinel` constant (`\x00nil`) marks keys/sections that were absent before patching so they can be fully removed on restore
- `origRootKeys` and `origSections` maps track what was changed; `Restore` only undoes those changes
- `inAnySection` scans backward for a `[section]` header to avoid mistaking section-level keys for root-level keys
- **Golden byte-parity** — the `cmd/databricks-codex` config.toml goldens pin the exact patch output (leading `\n`, single inter-section blank line, key order). When intentionally changing the wire shape, re-pin the goldens by running the test and copying the actual bytes; never hand-guess them.

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
