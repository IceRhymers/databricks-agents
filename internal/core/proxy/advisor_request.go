package proxy

import (
	"encoding/json"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// advisorRequestPrep is the per-request advisor state produced by the
// request-side rewrite and consumed by the response-side dispatch.
type advisorRequestPrep struct {
	// constraints maps advisor tool name → proxy-enforced constraints.
	// Empty when the feature is off or no advisor tool was declared.
	constraints advisorToolSet
	// request is the parsed executor request AFTER replay expansion but
	// with the ORIGINAL (pre-conversion) tools — the server loop and
	// streaming interleave use it to build continuations and to synthesize
	// the advisor sub-inference prompt.
	request *anthropic.Request
	// clientWantsStream records the incoming request's stream flag.
	clientWantsStream bool
}

// tryRewriteAdvisorRequest performs the advisor request-side transform on a
// /v1/messages body:
//
//  1. expandAdvisorExchanges — rewrite replayed server_tool_use /
//     advisor_tool_result history into function-tool form (ALWAYS, even when
//     the feature is disabled: the upstream must never see those block types).
//  2. injectAdvisorTool — auto-append the advisor declaration when enabled
//     and the request already declares at least one tool.
//  3. prepareAdvisorToolsForUpstream — convert advisor_* declarations to
//     plain function tools and capture their constraints.
//
// Returns the mutated body, the prep state, and ok=true when anything
// changed. On ok=false the body is unchanged and prep is zero — the caller
// forwards bytes verbatim.
func tryRewriteAdvisorRequest(body []byte, settings AdvisorSettings, lg logger) ([]byte, advisorRequestPrep, bool) {
	var prep advisorRequestPrep

	var req anthropic.Request
	if err := json.Unmarshal(body, &req); err != nil {
		lg.Vlogf("advisor: body not JSON-decodable as Anthropic Request: %v", err)
		return body, prep, false
	}

	prep.clientWantsStream = requestWantsStream(&req)

	// Step 1: replay expansion. Runs unconditionally — clients that followed
	// server-tool semantics replay advisor blocks, and the upstream 400s on
	// them regardless of whether the feature is currently enabled.
	expandedMessages, replayExpanded := expandAdvisorExchanges(req.Messages)
	req.Messages = expandedMessages

	// Step 2: auto-injection (enabled only).
	toolsBefore := len(req.Tools)
	req.Tools = injectAdvisorTool(req.Tools, settings.Enabled)
	injected := len(req.Tools) != toolsBefore
	if injected {
		lg.Vlogf("advisor: auto-injected advisor tool declaration")
	}

	// Snapshot the ORIGINAL tools (advisor declarations intact) for the
	// continuation / sub-inference prompt before conversion rewrites them.
	originalTools := append([]json.RawMessage(nil), req.Tools...)

	// Step 3: conversion + constraint capture.
	converted, constraints := prepareAdvisorToolsForUpstream(req.Tools, settings.Enabled, lg)
	toolsRewritten := constraints.any() || (len(converted) != len(req.Tools))
	req.Tools = converted
	prep.constraints = constraints

	if !replayExpanded && !injected && !toolsRewritten {
		return body, prep, false
	}

	// The executorReq handed to the loop carries the expanded messages and
	// the ORIGINAL tool declarations (so the advisor sub-inference can strip
	// advisor tools itself and the continuation re-sends what the client
	// declared).
	executorReq := req
	executorReq.Tools = originalTools
	prep.request = &executorReq

	out, err := json.Marshal(&req)
	if err != nil {
		return body, advisorRequestPrep{}, false
	}
	if constraints.any() {
		lg.Vlogf("advisor: prepared request (tools=%d advisor_constraints=%d stream=%v)",
			len(req.Tools), len(constraints), prep.clientWantsStream)
	}
	return out, prep, true
}

// requestWantsStream reports whether the incoming request set stream:true.
// The flag lives in Extras (anthropic.Request only types model/messages/
// tools/system).
func requestWantsStream(req *anthropic.Request) bool {
	raw, ok := req.Extras["stream"]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false
	}
	return b
}

// forceNonStreaming rewrites a request body's stream flag to false. Used to
// downgrade advisor+websearch streaming requests to the non-streaming server
// loop (documented limitation, spec §5/§8).
func forceNonStreaming(body []byte) ([]byte, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, false
	}
	raw["stream"] = json.RawMessage("false")
	out, err := json.Marshal(raw)
	if err != nil {
		return body, false
	}
	return out, true
}
