package proxy

import (
	"encoding/json"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// expandAdvisorExchanges rewrites client-replayed message history so the
// upstream never sees server_tool_use / advisor_tool_result blocks. Per
// assistant message:
//
//   - server_tool_use("advisor") becomes tool_use("advisor", input: {})
//   - the matching advisor_tool_result is removed from the assistant
//     message and re-emitted as a tool_result in a NEW user message
//     inserted immediately after
//
// The assistant turn is SPLIT at each advisor exchange: the converted
// tool_use must END an assistant message, the tool_result user message must
// follow immediately, and any post-result content starts a fresh assistant
// message. Multiple exchanges per message and orphan advisor_tool_result
// blocks are handled.
//
// Returns the (possibly new) messages slice and whether anything changed.
// If no advisor blocks are present the input is returned unmodified
// (aliasing OK — caller treats messages as immutable) with changed=false.
//
// The changed flag is returned EXPLICITLY rather than inferred by the caller
// from a length comparison: expansion can rewrite a message in place without
// changing the message count. The motivating case is an assistant message
// whose sole content block is an orphan advisor_tool_result — it becomes a
// single user tool_result message, 1-in/1-out, but the block type changed.
// Inferring "unchanged" there would forward advisor_tool_result upstream and
// earn a guaranteed 400.
func expandAdvisorExchanges(messages []json.RawMessage) ([]json.RawMessage, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	out := make([]json.RawMessage, 0, len(messages)+2)
	changed := false
	for _, m := range messages {
		expanded, didExpand := expandOneAssistantMessage(m)
		if didExpand {
			changed = true
			out = append(out, expanded...)
		} else {
			out = append(out, m)
		}
	}
	if !changed {
		return messages, false
	}
	return out, true
}

// expandOneAssistantMessage rewrites a single message. Returns
// (replacement messages, true) when the message is an assistant turn
// containing advisor server-tool blocks; (nil, false) otherwise.
func expandOneAssistantMessage(msg json.RawMessage) ([]json.RawMessage, bool) {
	var probe struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(msg, &probe); err != nil {
		return nil, false
	}
	if probe.Role != "assistant" || len(probe.Content) == 0 {
		return nil, false
	}

	// Quick scan: any server_tool_use("advisor") or advisor_tool_result?
	hasAdvisor := false
	for _, b := range probe.Content {
		var p struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			continue
		}
		if p.Type == "server_tool_use" && p.Name == anthropic.AdvisorToolName {
			hasAdvisor = true
			break
		}
		if p.Type == "advisor_tool_result" {
			hasAdvisor = true
			break
		}
	}
	if !hasAdvisor {
		return nil, false
	}

	// Walk blocks, building a sequence of output messages. currentAssistant
	// accumulates blocks for the assistant message currently under
	// construction; on each advisor exchange we flush it, emit the tool_use
	// + a new user tool_result message, then start a fresh assistant.
	out := []json.RawMessage{}
	currentAssistant := []json.RawMessage{}

	// pendingToolResult holds the tool_use_id of the most recent converted
	// server_tool_use whose advisor_tool_result has not yet been consumed.
	// Anthropic's shape guarantees the result block immediately follows the
	// call in well-formed history, but we tolerate orphans by emitting an
	// "[Advisor unavailable]" tool_result.
	pendingToolResultID := ""

	flushAssistant := func() {
		if len(currentAssistant) == 0 {
			return
		}
		content, err := json.Marshal(currentAssistant)
		if err != nil {
			return
		}
		m := map[string]any{"role": "assistant", "content": json.RawMessage(content)}
		if b, err := json.Marshal(m); err == nil {
			out = append(out, b)
		}
		currentAssistant = nil
	}
	emitUserToolResult := func(toolUseID, text string) {
		tr := map[string]any{
			"type":        "tool_result",
			"tool_use_id": toolUseID,
			"content": []map[string]string{
				{"type": "text", "text": text},
			},
		}
		trJSON, err := json.Marshal(tr)
		if err != nil {
			return
		}
		u := map[string]any{"role": "user", "content": []json.RawMessage{trJSON}}
		if b, err := json.Marshal(u); err == nil {
			out = append(out, b)
		}
	}

	for _, b := range probe.Content {
		var p struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			ToolUseID string `json:"tool_use_id"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			currentAssistant = append(currentAssistant, b)
			continue
		}

		switch {
		case p.Type == "server_tool_use" && p.Name == anthropic.AdvisorToolName:
			// If a previous converted tool_use is still awaiting its result,
			// close the exchange with an "unavailable" placeholder so the
			// upstream sees a paired tool_use/tool_result.
			if pendingToolResultID != "" {
				flushAssistant()
				emitUserToolResult(pendingToolResultID, advisorUnavailableText(""))
				pendingToolResultID = ""
			}
			// Convert server_tool_use → tool_use(input:{}). The tool_use
			// must END the assistant message — flush after appending.
			converted := map[string]any{
				"type":  "tool_use",
				"id":    p.ID,
				"name":  anthropic.AdvisorToolName,
				"input": map[string]any{},
			}
			if cb, err := json.Marshal(converted); err == nil {
				currentAssistant = append(currentAssistant, cb)
			}
			pendingToolResultID = p.ID

		case p.Type == "advisor_tool_result":
			// Pair the result with the pending converted tool_use (or, if
			// none is pending — orphan — emit it anyway under its declared
			// tool_use_id so the upstream sees a balanced exchange).
			text := anthropic.ExtractAdvisorResultText(b)
			id := p.ToolUseID
			if pendingToolResultID != "" {
				id = pendingToolResultID
				pendingToolResultID = ""
			}
			flushAssistant()
			emitUserToolResult(id, text)

		default:
			currentAssistant = append(currentAssistant, b)
		}
	}

	// Close out any dangling converted tool_use with an unavailable
	// placeholder so the upstream never sees an unpaired tool_use.
	if pendingToolResultID != "" {
		flushAssistant()
		emitUserToolResult(pendingToolResultID, advisorUnavailableText(""))
		pendingToolResultID = ""
	}
	flushAssistant()

	// Edge case: the assistant message contained ONLY advisor server blocks
	// and they all got converted + split — out may now be a sequence of
	// (assistant tool_use, user tool_result) pairs with no residual
	// assistant text. That's fine; upstream accepts it.
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
