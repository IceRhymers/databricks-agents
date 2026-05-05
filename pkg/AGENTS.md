<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# pkg

## Purpose
Reusable library packages extracted from the root `main` package. Each sub-package is self-contained, has its own tests, and exposes clean interfaces. The root `.go` files in the project are thin facades that wire these packages together.

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `authcheck/` | Pre-flight Databricks authentication verification and interactive login (see `authcheck/AGENTS.md`) |
| `childproc/` | Child process lifecycle: start, signal forwarding, exit code propagation (see `childproc/AGENTS.md`) |
| `completion/` | Shell completion script generation (bash, zsh, fish) from `FlagDef` slices |
| `headless/` | Headless mode helpers: `Ensure` (start-if-absent) and `Release` (refcount decrement) |
| `health/` | `/health` endpoint handler returning JSON liveness data |
| `lifecycle/` | HTTP handler wrapper adding `/shutdown` and idle timeout |
| `portbind/` | Port binding helpers with configured-port support |
| `proxy/` | HTTP/WebSocket reverse proxy, security checks, log sanitization (see `proxy/AGENTS.md`) |
| `refcount/` | Atomic session reference counter |
| `state/` | JSON-file state persistence helpers (atomic writes) |
| `tokencache/` | Generic mutex-guarded token cache with configurable fetcher (see `tokencache/AGENTS.md`) |
| `updater/` | GitHub release checker with 24-hour cache and numeric semver comparison |

## For AI Agents

### Working In This Directory
- Each package must remain **zero external dependencies** -- Go stdlib only.
- Packages expose interfaces (`TokenFetcher`, `TokenSource`, `Locker`, `SessionTracker`) so the root `main` package can plug in concrete implementations.
- Do not add cross-dependencies between sibling packages. Each package should be independently importable.

### Testing Requirements
- Each package has co-located `*_test.go` files. Run `go test ./pkg/...` to test all packages.
- `proxy/` has the most extensive tests.

### Common Patterns
- Packages that do I/O use **atomic temp-file + rename** for safe writes.
- Test-overridable globals (e.g., `execCommand` in `authcheck`) enable unit testing without real CLI calls.

## Dependencies

### Internal
- No cross-package dependencies within `pkg/`.

### External
- None (pure Go stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
