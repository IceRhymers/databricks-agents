<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# childproc

## Purpose
Child process lifecycle management. Starts `claude` (or any binary) as a subprocess, forwards SIGINT/SIGTERM from the parent, and propagates the child's exit code.

## Key Files

| File | Description |
|------|-------------|
| `childproc.go` | `Config` struct, `Run` (start + wait + exit code), `ForwardSignals` (SIGINT/SIGTERM relay goroutine with cancel) |
| `childproc_test.go` | Tests for signal forwarding and exit code propagation |

## For AI Agents

### Working In This Directory
- `Run` connects the child's stdin/stdout/stderr directly to the parent's -- the child inherits the terminal.
- `ForwardSignals` returns a cancel function that must be deferred to stop the goroutine and prevent leaks.
- The signal channel buffer size is 4 to handle rapid signal bursts.

### Testing Requirements
- Run: `go test ./pkg/childproc/...`

### Common Patterns
- Exit code extraction: check `*exec.ExitError` to get the child's exit code; default to 1 on other errors.

## Dependencies

### Internal
- None

### External
- `os/exec`, `os/signal`, `syscall` (stdlib)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
