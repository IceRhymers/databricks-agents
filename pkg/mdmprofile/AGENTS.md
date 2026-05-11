<!-- Generated: 2026-05-11 | Updated: 2026-05-11 -->

# pkg/mdmprofile

## Purpose
Platform-specific readers for MDM-managed application preferences. Used by the
credential helper to resolve the Databricks profile name on endpoint machines
where no `~/.claude/.databricks-claude.json` state file exists.

## API

```go
// ReadKey returns the value of an arbitrary key from the MDM-managed
// preferences for the given domain. Returns "" (nil error) when no value
// is set or on any read error. This is the primary API — multiple keys
// per domain are now supported (e.g. databricksProfile, databricksCliPath).
func ReadKey(domain, key string) (string, error)

// Read is a thin shim preserved for backward compatibility: it returns
// ReadKey(domain, "databricksProfile"). Prefer ReadKey for new callers.
func Read(domain string) (string, error)
```

Call with `"com.icerhymers.databricks-claude"` as the domain. Today's recognised
keys are `databricksProfile` (#146) and `databricksCliPath` (#150), both
written by the fleet `.mobileconfig` or `.reg` artifact generated via
`databricks-claude desktop generate-config`.

Adding a new MDM key:
1. Generator side: extend the builder (`buildMobileconfig`/`buildRegFile`/`buildDevModeJSON`) in `desktop_config.go` to emit the new key when populated.
2. Reader side: call `mdmprofile.ReadKey(domain, "<your-key>")` from the resolution chain that needs it. No changes required in this package — `ReadKey` is the generic API.

## Key Files

| File | Description |
|------|-------------|
| `darwin.go` | Reads `/Library/Managed Preferences/<user>/<domain>.plist` using `encoding/xml` (pure stdlib, no cgo). Falls back to `~/Library/Preferences/<domain>.plist` for unmanaged dev machines. |
| `windows.go` | Reads `HKCU\SOFTWARE\IceRhymers\databricks-claude\databricksProfile` via `syscall.RegOpenKeyEx` / `RegQueryValueEx`. |
| `other.go` | No-op stub returning `""` for all other platforms. |
| `darwin_test.go` | Tests plist parsing and the `Read` function with temp plist files. |
| `AGENTS.md` | This file. |

## For AI Agents

- **Zero external dependencies** — darwin uses `encoding/xml`, windows uses `syscall`. No cgo bindings to CoreFoundation.
- `managedPrefsDir` in `darwin.go` is a `var func() string` so tests can inject a temp directory.
- The `parsePlistString` function is an unexported XML walker; test it via the exported `Read` function or the package-internal `darwin_test.go`.
- Windows uses HKCU (not HKLM) to match the `.reg` artifact written by `buildRegFile`.
