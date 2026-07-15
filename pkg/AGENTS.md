<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# pkg

## Purpose
After #198, `pkg/` holds **only the Claude/Anthropic-coupled libraries** — everything tool-agnostic was promoted into `internal/core/` (the shared engine; see `../internal/core/doc.go`). The three packages here are deliberately kept out of core because they are Claude-only, and move later with the claude Profile in #E. Each is self-contained, has its own tests, and is stdlib-only.

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `modeldiscovery/` | Unity AI Gateway model-services discovery: lists UC model-services, resolves the newest anthropic-capable model per family (opus/sonnet/haiku) via a pure `Resolve()` function, numeric version sort, and the 1M-context predicate. Claude-coupled (Opus/Sonnet/Haiku + `anthropic/v1/messages` predicate). |
| `mdmprofile/` | Platform-specific readers for Desktop MDM-managed preferences (darwin plist / windows registry / other stub). Claude Desktop-only. |
| `websearch/` | Web-search backends (DuckDuckGo etc.) for Claude's server-side web-search tool. Imported by `internal/core/proxy`'s websearch handler — a documented, temporary `internal/core → pkg` back-edge resolved in #E. |

> The tool-agnostic packages (authcheck, childproc, cli, completion, headless, health, lifecycle, portbind, proxy, refcount, state, tokencache, updater) now live under `../internal/core/`; their per-package `AGENTS.md` files moved with them.

## For AI Agents

### Working In This Directory
- Each package must remain **zero external dependencies** -- Go stdlib only.

### Testing Requirements
- Each package has co-located `*_test.go` files. Run `go test ./pkg/...` to test these, or `go test ./...` for the whole module.

### Common Patterns
- Packages that do I/O use **atomic temp-file + rename** for safe writes.
- Test-overridable globals (e.g., `execCommand` in `authcheck`) enable unit testing without real CLI calls.

## Dependencies

### Internal
- No cross-package dependencies within `pkg/`.

### External
- None (pure Go stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
