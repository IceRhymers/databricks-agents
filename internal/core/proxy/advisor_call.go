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

// advisorSystemPrompt is the literal system prompt the proxy authors for the
// advisor sub-inference. If the executor request had a text system prompt,
// it's appended verbatim wrapped in <agent_system_context>.
const advisorSystemPrompt = `You are a senior strategic advisor reviewing an AI agent's conversation transcript. Your role is to provide brief, plain-text guidance — a concrete plan or course-correction — to help the agent proceed effectively. Be concise and direct. Do not call any tools; respond with guidance text only.`

// advisorElicitationTurn is the synthetic user turn that closes the advisor
// prompt, asking for guidance.
const advisorElicitationTurn = `(Advisor) Provide concise strategic guidance for how to proceed based on the conversation above. Do not call tools; respond with guidance text only.`

// advisorSubResponse is the parsed bits of the sub-inference's response we
// need: the guidance text and the stop_reason.
type advisorSubResponse struct {
	Text       string
	StopReason string
}

// executeAdvisorCall performs one advisor sub-inference:
//  1. Synthesize the prompt (system + stripped history + in-progress-turn
//     text + elicitation turn).
//  2. POST it to the upstream /v1/messages with tool_choice:none and
//     thinking stripped, using the proxy's TokenSource for auth.
//  3. Parse and return the guidance text + stop_reason.
//
// Failures map to typed errors (ErrTooManyRequests/ErrOverloaded/etc.) which
// callers convert into error-shaped advisor_tool_result blocks. The outer
// request never fails because the advisor failed.
func executeAdvisorCall(
	ctx context.Context,
	upstream *url.URL,
	tokenSource TokenSource,
	toolName string,
	verbose bool,
	executorReq *anthropic.Request,
	inProgressTurn []json.RawMessage,
	call resolvedAdvisorCall,
) (*advisorSubResponse, error) {
	lg := logger{prefix: toolName, verbose: verbose}

	subCtx, cancel := context.WithTimeout(ctx, advisorTimeout)
	defer cancel()

	body, err := synthesizeAdvisorRequest(executorReq, inProgressTurn, call)
	if err != nil {
		return nil, fmt.Errorf("%w: synthesize: %v", ErrUnavailable, err)
	}

	// Build the sub-request directly against the upstream's /v1/messages
	// endpoint. Reuses the proxy's token injection so the sub-call lands
	// under the same identity/routing as the parent.
	token, terr := tokenSource.Token(subCtx)
	if terr != nil {
		lg.Logf("advisor: token fetch error: %v", terr)
	}
	basePath := strings.TrimRight(upstream.Path, "/")
	outURL := *upstream
	outURL.Path = basePath + "/v1/messages"
	outURL.RawPath = ""
	outURL.RawQuery = ""

	req, err := http.NewRequestWithContext(subCtx, http.MethodPost, outURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("x-api-key", token)
	}
	req.Header.Set("x-databricks-use-coding-agent-mode", "true")
	req.Host = upstream.Host

	lg.Vlogf("advisor: dispatch sub-inference model=%s max_tokens=%d", call.Model, call.MaxTokens)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		if subCtx.Err() == context.DeadlineExceeded {
			return nil, ErrExecutionTimeExceeded
		}
		return nil, fmt.Errorf("%w: round trip: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	respBody, rerr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if rerr != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrUnavailable, rerr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		lg.Logf("advisor: sub-inference HTTP %d: %s", resp.StatusCode, sanitizeLogOutput(snippet))
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return nil, ErrTooManyRequests
		case 529:
			return nil, ErrOverloaded
		case http.StatusRequestEntityTooLarge:
			return nil, ErrPromptTooLong
		default:
			return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
		}
	}

	parsed, err := parseAdvisorSubResponse(respBody)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrUnavailable, err)
	}
	lg.Vlogf("advisor: sub-inference ok stop_reason=%s text_len=%d", parsed.StopReason, len(parsed.Text))
	return parsed, nil
}

// synthesizeAdvisorRequest assembles the sub-inference's request body:
// system prompt (advisor boilerplate + optional <agent_system_context>),
// messages (stripped history + in-progress turn's text blocks + elicitation
// user turn), tools (executor's original tools, advisor stripped), and
// tool_choice:{"type":"none"} so the advisor cannot recurse or call tools.
func synthesizeAdvisorRequest(
	executorReq *anthropic.Request,
	inProgressTurn []json.RawMessage,
	call resolvedAdvisorCall,
) ([]byte, error) {
	// --- System prompt ---
	sysPrompt := advisorSystemPrompt
	if execSys := extractTextSystemPrompt(executorReq.System); execSys != "" {
		sysPrompt = sysPrompt + "\n\n<agent_system_context>\n" + execSys + "\n</agent_system_context>"
	}

	// --- Messages ---
	messages := make([]json.RawMessage, 0, len(executorReq.Messages)+2)
	for _, m := range executorReq.Messages {
		stripped, keep := stripThinkingFromMessage(m)
		if !keep {
			continue
		}
		messages = append(messages, stripped)
	}
	// In-progress assistant turn: keep only text blocks. tool_use /
	// server_tool_use are dangling (no tool_result yet) and would 400.
	if textOnly := extractTextBlocksFromAssistantTurn(inProgressTurn); len(textOnly) > 0 {
		contentJSON, err := json.Marshal(textOnly)
		if err == nil {
			turn := map[string]any{"role": "assistant", "content": json.RawMessage(contentJSON)}
			if b, err := json.Marshal(turn); err == nil {
				messages = append(messages, b)
			}
		}
	}
	// Elicitation turn.
	elicitation := map[string]any{
		"role": "user",
		"content": []map[string]string{
			{"type": "text", "text": advisorElicitationTurn},
		},
	}
	elicJSON, err := json.Marshal(elicitation)
	if err != nil {
		return nil, err
	}
	messages = append(messages, elicJSON)

	// --- Tools: executor's originals, with advisor tools stripped (recursion
	// guard). We include them so the advisor sees the tool context, but
	// tool_choice:none makes calling them impossible. ---
	sanitizedTools := make([]json.RawMessage, 0, len(executorReq.Tools))
	for _, t := range executorReq.Tools {
		if anthropic.IsAdvisorTool(t) {
			continue
		}
		sanitizedTools = append(sanitizedTools, t)
	}

	// --- Top-level request ---
	out := map[string]any{
		"model":       call.Model,
		"max_tokens":  call.MaxTokens,
		"system":      sysPrompt,
		"messages":    messages,
		"tool_choice": map[string]string{"type": "none"},
	}
	if len(sanitizedTools) > 0 {
		out["tools"] = sanitizedTools
	}
	return json.Marshal(out)
}

// extractTextSystemPrompt returns the executor's system prompt when it is a
// plain string or an array of text blocks. Non-text shapes yield "".
func extractTextSystemPrompt(system json.RawMessage) string {
	if len(system) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(system, &s); err == nil {
		return s
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(system, &arr); err != nil {
		return ""
	}
	var parts []string
	for _, block := range arr {
		var probe struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &probe); err != nil {
			continue
		}
		if probe.Type == "text" && probe.Text != "" {
			parts = append(parts, probe.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// stripThinkingFromMessage removes thinking and redacted_thinking blocks
// from a single message. Returns (mutated, keep); keep=false when the
// message had only thinking content (dropping it entirely is correct — an
// assistant message with no visible content is invalid upstream).
func stripThinkingFromMessage(msg json.RawMessage) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(msg, &m); err != nil {
		return msg, true
	}
	contentRaw, ok := m["content"]
	if !ok {
		return msg, true
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		// String content or other shape — keep verbatim.
		return msg, true
	}
	out := make([]json.RawMessage, 0, len(blocks))
	for _, b := range blocks {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(b, &probe); err != nil {
			out = append(out, b)
			continue
		}
		if probe.Type == "thinking" || probe.Type == "redacted_thinking" {
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, false
	}
	newContent, err := json.Marshal(out)
	if err != nil {
		return msg, true
	}
	m["content"] = newContent
	newMsg, err := json.Marshal(m)
	if err != nil {
		return msg, true
	}
	return newMsg, true
}

// extractTextBlocksFromAssistantTurn pulls text blocks out of the in-progress
// assistant turn's content. tool_use / server_tool_use / advisor_tool_result
// are stripped — they are dangling in the sub-inference context. thinking is
// stripped by the same reasoning as the history strip.
func extractTextBlocksFromAssistantTurn(content []json.RawMessage) []map[string]string {
	out := []map[string]string{}
	for _, b := range content {
		var probe struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &probe); err != nil {
			continue
		}
		if probe.Type == "text" && probe.Text != "" {
			out = append(out, map[string]string{"type": "text", "text": probe.Text})
		}
	}
	return out
}

// parseAdvisorSubResponse extracts the guidance text and stop_reason from
// the upstream's response. Concatenates all text blocks; ignores tool_use
// (tool_choice:none means there shouldn't be any).
func parseAdvisorSubResponse(body []byte) (*advisorSubResponse, error) {
	var resp anthropic.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, block := range resp.Content {
		var probe struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &probe); err != nil {
			continue
		}
		if probe.Type == "text" {
			text.WriteString(probe.Text)
		}
	}
	return &advisorSubResponse{Text: text.String(), StopReason: resp.StopReason}, nil
}
