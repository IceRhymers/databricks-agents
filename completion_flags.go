package main

import (
	"github.com/IceRhymers/databricks-claude/pkg/completion"
)

// flagDefs is the authoritative list of flags owned by databricks-claude.
// Everything not listed here is forwarded transparently to the claude binary.
//
// Rules:
//   - TakesArg: true  → the next token is consumed as the flag's value
//   - TakesArg: false → the flag is a boolean toggle
//   - Completer: "__databricks_profiles" → completes from ~/.databrickscfg sections
//   - Completer: "__files"              → completes with local file paths
//   - Short: "x"                        → also accepts -x as a short alias
var flagDefs = []completion.FlagDef{
	{Name: "profile", Description: "Databricks CLI profile (default: DEFAULT)", TakesArg: true, Completer: "__databricks_profiles"},
	{Name: "verbose", Short: "v", Description: "Enable debug logging to stderr"},
	{Name: "version", Description: "Print version and exit"},
	{Name: "help", Short: "h", Description: "Show help message"},
	{Name: "print-env", Description: "Print resolved configuration (token redacted) and exit"},
	{Name: "otel", Description: "Enable OpenTelemetry logs/metrics export"},
	{Name: "no-otel", Description: "Disable OpenTelemetry (clears all signal keys + telemetry toggle)"},
	{Name: "no-otel-metrics", Description: "Clear persisted OTel metrics keys from settings.json"},
	{Name: "no-otel-logs", Description: "Clear persisted OTel logs keys from settings.json"},
	{Name: "no-otel-traces", Description: "Clear persisted OTel traces keys from settings.json"},
	{Name: "otel-metrics-table", Description: "Unity Catalog table for OTel metrics (cat.schema.table)", TakesArg: true},
	{Name: "otel-logs-table", Description: "Unity Catalog table for OTel logs (cat.schema.table)", TakesArg: true},
	{Name: "otel-traces", Description: "Enable OpenTelemetry traces export (Claude Code beta)"},
	{Name: "otel-traces-table", Description: "Unity Catalog table for OTel traces (cat.schema.table)", TakesArg: true},
	{Name: "upstream", Description: "Override upstream claude binary path", TakesArg: true, Completer: "__files"},
	{Name: "log-file", Description: "Write debug logs to file (combinable with --verbose)", TakesArg: true, Completer: "__files"},
	{Name: "proxy-api-key", Description: "Require this API key on all proxy requests", TakesArg: true},
	{Name: "tls-cert", Description: "TLS certificate file for the local proxy (requires --tls-key)", TakesArg: true, Completer: "__files"},
	{Name: "tls-key", Description: "TLS private key file for the local proxy (requires --tls-cert)", TakesArg: true, Completer: "__files"},
	{Name: "port", Description: "Proxy listen port (default: 49153)", TakesArg: true},
	{Name: "headless", Description: "Start proxy without launching claude (for IDE extensions or hooks)"},
	{Name: "idle-timeout", Description: "Idle timeout for headless mode (default: 30m; 0 disables; use e.g. 30s, 5m, 1h)", TakesArg: true},
	{Name: "install-hooks", Description: "Install SessionStart/Stop hooks into ~/.claude/settings.json"},
	{Name: "uninstall-hooks", Description: "Remove databricks-claude hooks from ~/.claude/settings.json"},
	{Name: "headless-ensure", Description: "Start proxy if not running — called by the SessionStart hook"},
	{Name: "headless-release", Description: "Decrement proxy refcount — called by the Stop hook"},
	{Name: "no-update-check", Description: "Skip the automatic update check on startup"},
	{Name: "with-websearch", Description: "Locally fulfill Anthropic web_search/web_fetch tools (workaround for FMAPI gap)"},
	{Name: "websearch-backend", Description: "Web search backend (duckduckgo|none)", TakesArg: true},
	{Name: "websearch-fetch-budget", Description: "Per-fetch byte budget for --with-websearch (default 102400)", TakesArg: true},
}

// knownFlags is the set of flag names (with "--" prefix) that databricks-claude
// owns. Anything not in this set is forwarded to the claude binary.
// Derived from flagDefs so it can never drift from the completion script.
var knownFlags = func() map[string]bool {
	m := make(map[string]bool, len(flagDefs))
	for _, f := range flagDefs {
		m["--"+f.Name] = true
	}
	return m
}()

// knownSubcommands is the authoritative list of top-level subcommands for shell
// completion. They are offered as completions at position 1 (before any flags).
var knownSubcommands = []completion.SubcommandDef{
	{Name: "completion", Description: "Generate shell completion scripts (bash, zsh, fish)"},
	{Name: "update", Description: "Check for a newer release and print upgrade instructions"},
	{Name: "desktop", Description: "Claude Desktop integration (generate-config, credential-helper)"},
	{Name: "setup", Description: "Idempotent auth bootstrap for fleet init scripts"},
	{Name: "serve", Description: "Long-lived daemon for MDM-deployed Claude Desktop deployments"},
}
