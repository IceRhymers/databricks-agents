package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// advisorLoopContext carries the per-request state the server loop needs:
// the original (pre-rewrite) request, the advisor constraints captured on
// the request side, the proxy's upstream + token source, and per-tool-name
// invocation counters that persist across loop turns.
type advisorLoopContext struct {
	upstream    *url.URL
	tokenSource TokenSource
	toolName    string
	verbose     bool
	settings    AdvisorSettings
	constraints advisorToolSet
	executorReq *anthropic.Request
	usesByTool  map[string]int
}

// runAdvisorServerLoop executes the non-streaming continuation loop. The
// incoming upstreamResponse is the first (already-received) response body
// from the upstream; the loop consumes advisor calls, dispatches sub-
// inferences, and continues the turn until:
//
//   - the response contains a client tool_use (stop; client executes)
//   - the response contains no tool_use at all (natural end)
//   - maxAdvisorTurns is hit (log a warning, return accumulated partial)
//   - a continuation forward fails (return accumulated partial)
//
// Returns the final response body to hand back to the client. The envelope
// (id, model, usage) comes from the LAST upstream response; content is the
// accumulated blocks across all turns; stop_reason comes from the terminal
// turn. On any advisor-side failure the loop still returns a well-formed
// response — the outer request never fails because the advisor failed.
func runAdvisorServerLoop(
	ctx context.Context,
	lc *advisorLoopContext,
	firstResponseBody []byte,
	transport http.RoundTripper,
) ([]byte, error) {
	lg := logger{prefix: lc.toolName, verbose: lc.verbose}

	accumulated := []json.RawMessage{}
	currentBody := firstResponseBody

	for turn := 0; turn < maxAdvisorTurns; turn++ {
		var resp anthropic.Response
		if err := json.Unmarshal(currentBody, &resp); err != nil {
			lg.Logf("advisor: loop turn %d: unparseable response, returning accumulated: %v", turn, err)
			return synthesizeTerminalEnvelope(currentBody, accumulated, "end_turn"), nil
		}

		// Classify content blocks.
		advisorCalls := []json.RawMessage{}
		hasClientToolUse := false
		for _, block := range resp.Content {
			var probe struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(block, &probe); err != nil {
				continue
			}
			if probe.Type != "tool_use" {
				continue
			}
			if lc.constraints.isAdvisorToolUseName(probe.Name) {
				advisorCalls = append(advisorCalls, block)
			} else {
				hasClientToolUse = true
			}
		}

		// Client tool present → stop. Append turn content verbatim, set
		// stop_reason="tool_use", return.
		if hasClientToolUse {
			lg.Vlogf("advisor: loop turn %d: client tool_use present, stopping", turn)
			accumulated = append(accumulated, resp.Content...)
			return synthesizeTerminalEnvelope(currentBody, accumulated, "tool_use"), nil
		}

		// No tool_use at all → natural end. Keep upstream stop_reason.
		if len(advisorCalls) == 0 {
			lg.Vlogf("advisor: loop turn %d: natural end (stop_reason=%s)", turn, resp.StopReason)
			accumulated = append(accumulated, resp.Content...)
			return synthesizeTerminalEnvelope(currentBody, accumulated, resp.StopReason), nil
		}

		// Advisor calls present. Emit the in-progress turn's text blocks into
		// accumulated content (the tool_use blocks themselves get rewritten
		// as server_tool_use below).
		for _, block := range resp.Content {
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(block, &probe); err != nil {
				continue
			}
			if probe.Type == "text" {
				accumulated = append(accumulated, block)
			}
		}

		// Dispatch each advisor call in order. Build the continuation's
		// tool_result blocks as we go.
		toolResults := []json.RawMessage{}
		for _, callBlock := range advisorCalls {
			var probe struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(callBlock, &probe); err != nil {
				continue
			}
			serverID := mintAdvisorServerID(probe.ID)
			constraints := lc.constraints[probe.Name]

			// Enforce max_uses (counter persists across loop turns).
			if constraints.MaxUses > 0 && lc.usesByTool[probe.Name] >= constraints.MaxUses {
				lg.Logf("advisor: max_uses_exceeded tool=%s max=%d", probe.Name, constraints.MaxUses)
				stu, _ := anthropic.BuildAdvisorServerToolUseBlock(serverID)
				errBlock, _ := anthropic.BuildAdvisorErrorBlock(serverID, "max_uses_exceeded")
				accumulated = append(accumulated, stu, errBlock)
				toolResults = append(toolResults, buildToolResultBlock(probe.ID, advisorUnavailableText("max_uses_exceeded")))
				continue
			}
			lc.usesByTool[probe.Name]++

			// Dispatch the sub-inference.
			call := resolveAdvisorCall(constraints, lc.settings, lc.executorReq.Model)
			sub, err := executeAdvisorCall(ctx, lc.upstream, lc.tokenSource, lc.toolName, lc.verbose, lc.executorReq, resp.Content, call)

			stu, _ := anthropic.BuildAdvisorServerToolUseBlock(serverID)
			accumulated = append(accumulated, stu)

			if err != nil {
				code := advisorErrorCode(err)
				lg.Logf("advisor: sub-inference failed tool=%s code=%s err=%v", probe.Name, code, err)
				errBlock, _ := anthropic.BuildAdvisorErrorBlock(serverID, code)
				accumulated = append(accumulated, errBlock)
				toolResults = append(toolResults, buildToolResultBlock(probe.ID, advisorUnavailableText(code)))
				continue
			}

			// Success. stop_reason only surfaced in the content when the
			// client set an explicit max_tokens cap (mirrors Anthropic).
			stopReason := ""
			if call.MaxTokensSet {
				stopReason = sub.StopReason
			}
			successBlock, _ := anthropic.BuildAdvisorSuccessBlock(serverID, sub.Text, stopReason)
			accumulated = append(accumulated, successBlock)
			toolResults = append(toolResults, buildToolResultBlock(probe.ID, sub.Text))
		}

		// Build the continuation request and forward upstream.
		contBody, err := buildAdvisorContinuationRequest(lc.executorReq, resp, toolResults)
		if err != nil {
			lg.Logf("advisor: continuation build failed: %v; returning accumulated", err)
			return synthesizeTerminalEnvelope(currentBody, accumulated, "end_turn"), nil
		}
		nextBody, err := forwardAdvisorContinuation(ctx, lc, contBody, transport)
		if err != nil {
			lg.Logf("advisor: continuation forward failed: %v; returning accumulated", err)
			return synthesizeTerminalEnvelope(currentBody, accumulated, "end_turn"), nil
		}
		currentBody = nextBody
	}

	// Turn cap hit with advisor calls potentially still pending. Return what
	// we have — the agent can re-invoke the advisor on the next turn if the
	// model wants to continue.
	lg.Logf("advisor: maxAdvisorTurns=%d reached; returning accumulated partial", maxAdvisorTurns)
	return synthesizeTerminalEnvelope(currentBody, accumulated, "end_turn"), nil
}

// buildToolResultBlock constructs a user-side tool_result content block for
// the continuation request.
func buildToolResultBlock(toolUseID, text string) json.RawMessage {
	tr := map[string]any{
		"type":        "tool_result",
		"tool_use_id": toolUseID,
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
	}
	b, _ := json.Marshal(tr)
	return b
}

// buildAdvisorContinuationRequest assembles the follow-up upstream request:
// original messages + the assistant turn reconstructed from the response
// (thinking stripped, tool_use inputs normalized to {}, text kept) + a user
// message carrying the tool_result blocks.
func buildAdvisorContinuationRequest(
	executorReq *anthropic.Request,
	resp anthropic.Response,
	toolResults []json.RawMessage,
) ([]byte, error) {
	// Reconstruct the assistant turn. thinking/redacted_thinking stripped —
	// re-sent thinking blocks require valid signatures and would 400.
	assistantContent := []json.RawMessage{}
	for _, block := range resp.Content {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &probe); err != nil {
			continue
		}
		switch probe.Type {
		case "thinking", "redacted_thinking":
			continue
		case "tool_use":
			// Normalize input to {} — advisor takes no parameters and the
			// upstream may have emitted partial/empty input.
			var p struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			_ = json.Unmarshal(block, &p)
			normalized := map[string]any{
				"type":  "tool_use",
				"id":    p.ID,
				"name":  p.Name,
				"input": map[string]any{},
			}
			if b, err := json.Marshal(normalized); err == nil {
				assistantContent = append(assistantContent, b)
			}
		default:
			assistantContent = append(assistantContent, block)
		}
	}

	messages := make([]json.RawMessage, 0, len(executorReq.Messages)+2)
	messages = append(messages, executorReq.Messages...)

	assistantMsg := map[string]any{"role": "assistant", "content": assistantContent}
	ab, err := json.Marshal(assistantMsg)
	if err != nil {
		return nil, err
	}
	messages = append(messages, ab)

	userMsg := map[string]any{"role": "user", "content": toolResults}
	ub, err := json.Marshal(userMsg)
	if err != nil {
		return nil, err
	}
	messages = append(messages, ub)

	// Copy the executor request, swap messages, and re-emit. Tools and
	// system are preserved; thinking config is dropped (thinking blocks
	// would require signature validation on the assistant turn we're
	// replaying, and the continuation carries tool_results which don't
	// belong to a thinking context).
	out := map[string]json.RawMessage{}
	for k, v := range executorReq.Extras {
		if k == "thinking" {
			continue
		}
		out[k] = v
	}
	if executorReq.Model != "" {
		b, _ := json.Marshal(executorReq.Model)
		out["model"] = b
	}
	if len(executorReq.Tools) > 0 {
		b, _ := json.Marshal(executorReq.Tools)
		out["tools"] = b
	}
	if len(executorReq.System) > 0 {
		out["system"] = executorReq.System
	}
	mb, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	out["messages"] = mb
	// Force non-streaming for the continuation — the server loop consumes
	// complete responses.
	out["stream"] = json.RawMessage("false")
	return json.Marshal(out)
}

// forwardAdvisorContinuation POSTs the continuation body to the upstream
// /v1/messages endpoint and returns the response body. Failures map to a
// generic error (the caller degrades gracefully by returning accumulated
// content).
func forwardAdvisorContinuation(
	ctx context.Context,
	lc *advisorLoopContext,
	body []byte,
	transport http.RoundTripper,
) ([]byte, error) {
	if transport == nil {
		transport = http.DefaultTransport
	}
	token, err := lc.tokenSource.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	basePath := strings.TrimRight(lc.upstream.Path, "/")
	outURL := *lc.upstream
	outURL.Path = basePath + "/v1/messages"
	outURL.RawPath = ""
	outURL.RawQuery = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, outURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("x-api-key", token)
	}
	req.Header.Set("x-databricks-use-coding-agent-mode", "true")
	req.Host = lc.upstream.Host

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, sanitizeLogOutput(snippet))
	}
	return respBody, nil
}

// synthesizeTerminalEnvelope produces the final response body handed back
// to the client. Envelope fields (id, model, usage, etc.) come from the
// most recent upstream response; content is replaced with the accumulated
// blocks; stop_reason is forced to the terminal reason supplied by the
// caller.
func synthesizeTerminalEnvelope(lastUpstreamBody []byte, accumulated []json.RawMessage, stopReason string) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(lastUpstreamBody, &raw); err != nil {
		// Worst case: emit a bare-bones envelope.
		minimal := map[string]any{
			"type":        "message",
			"role":        "assistant",
			"content":     accumulated,
			"stop_reason": stopReason,
		}
		b, _ := json.Marshal(minimal)
		return b
	}
	cb, err := json.Marshal(accumulated)
	if err == nil {
		raw["content"] = cb
	}
	if stopReason != "" {
		sr, _ := json.Marshal(stopReason)
		raw["stop_reason"] = sr
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return lastUpstreamBody
	}
	return out
}
