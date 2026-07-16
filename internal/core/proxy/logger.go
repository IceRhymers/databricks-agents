package proxy

import "log"

// logger attributes an engine log line to the launcher that owns the proxy.
//
// internal/core/proxy is shared by databricks-claude, databricks-codex and
// databricks-opencode, so a hardcoded tool name here is a lie for two of the
// three. The correct name already travels in Config.ToolName; logger carries it
// to the free functions that do the logging, occupying the parameter slot that
// previously held a bare `verbose bool`.
//
// internal/core/run.go:169 is the sibling precedent: it already sources its log
// prefix from LaunchPlan.ToolName rather than a literal.
//
// The zero value is usable: it logs with an empty prefix rather than panicking.
type logger struct {
	prefix  string
	verbose bool
}

// Logf logs unconditionally. Use for lines that must survive a non-verbose run,
// such as upstream errors.
func (l logger) Logf(format string, args ...any) {
	// prefix travels as a %s argument rather than being concatenated into the
	// format string, so a '%' in a tool name can never become a format verb.
	log.Printf("%s: "+format, append([]any{l.prefix}, args...)...)
}

// Vlogf logs only when verbose is set. Callers must not wrap it in their own
// `if verbose` check — the guard lives here.
func (l logger) Vlogf(format string, args ...any) {
	if !l.verbose {
		return
	}
	l.Logf(format, args...)
}
