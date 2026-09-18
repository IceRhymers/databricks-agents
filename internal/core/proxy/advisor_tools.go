package proxy

import (
	"encoding/json"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// advisorToolSet is the per-request view of the advisor tools declared (or
// auto-injected) on the incoming request. Keyed by tool name (always
// anthropic.AdvisorToolName today, but keyed defensively in case a future
// version introduces per-name advisors).
type advisorToolSet map[string]anthropic.AdvisorConstraints

// any reports whether any advisor tool was declared (or injected) and the
// feature is enabled. Hot-path gate for the response-side dispatch.
func (s advisorToolSet) any() bool { return len(s) > 0 }

// isAdvisorToolUseName reports whether a tool_use block's name is one of the
// declared advisor tool names for this request. The upstream emits function-
// tool invocations under the plain name; the proxy recognizes them by name
// against the constraints map captured on the request side.
func (s advisorToolSet) isAdvisorToolUseName(name string) bool {
	if !s.any() {
		return false
	}
	_, ok := s[name]
	return ok
}

// injectAdvisorTool appends the canonical advisor tool declaration to tools
// when (a) the feature is enabled, (b) the request already declares at least
// one tool, and (c) no advisor_* tool is already present. Requests with zero
// tools are untouched — the advisor is scoped to agentic workloads.
// Idempotent; never mutates the caller's slice (returns a fresh slice on
// inject, the original slice otherwise).
func injectAdvisorTool(tools []json.RawMessage, enabled bool) []json.RawMessage {
	if !enabled || len(tools) == 0 {
		return tools
	}
	for _, t := range tools {
		if anthropic.IsAdvisorTool(t) {
			return tools
		}
	}
	injected := append([]json.RawMessage(nil), tools...)
	decl := map[string]any{
		"type": anthropic.AdvisorToolVersion,
		"name": anthropic.AdvisorToolName,
	}
	b, err := json.Marshal(decl)
	if err != nil {
		return tools
	}
	return append(injected, b)
}

// prepareAdvisorToolsForUpstream walks tools, rewrites each advisor_* server
// tool to its plain function-tool form, and captures its constraints into a
// proxy-side map keyed by tool name. Returns the (possibly new) tools slice
// and the captured constraints.
//
// When enabled is false, advisor tools are stripped from the forwarded
// request (and logged) — the upstream must never see a server tool we don't
// intend to fulfill.
func prepareAdvisorToolsForUpstream(tools []json.RawMessage, enabled bool, lg logger) ([]json.RawMessage, advisorToolSet) {
	if len(tools) == 0 {
		return tools, nil
	}
	constraints := advisorToolSet{}
	out := make([]json.RawMessage, 0, len(tools))
	changed := false
	for _, t := range tools {
		if !anthropic.IsAdvisorTool(t) {
			out = append(out, t)
			continue
		}
		changed = true
		if !enabled {
			lg.Vlogf("advisor: feature disabled; stripping advisor tool from forwarded request")
			continue
		}
		c := anthropic.ParseAdvisorConstraints(t)
		name := anthropic.ToolName(t)
		if name == "" {
			name = anthropic.AdvisorToolName
		}
		constraints[name] = c
		out = append(out, anthropic.ConvertAdvisorTool())
	}
	if !changed {
		return tools, nil
	}
	return out, constraints
}
