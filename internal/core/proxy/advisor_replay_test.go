package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodedMsg is a decoded message used for assertions about the expanded
// message sequence.
type decodedMsg struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

func decodeMessages(t *testing.T, msgs []json.RawMessage) []decodedMsg {
	t.Helper()
	out := make([]decodedMsg, 0, len(msgs))
	for i, m := range msgs {
		var d decodedMsg
		if err := json.Unmarshal(m, &d); err != nil {
			t.Fatalf("message %d not decodable: %v (%s)", i, err, m)
		}
		out = append(out, d)
	}
	return out
}

// blockTypes returns the "type" of each content block, for shape assertions.
func blockTypes(t *testing.T, blocks []json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		var p struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			out = append(out, "?")
			continue
		}
		out = append(out, p.Type)
	}
	return out
}

// assertNoServerToolBlocks is the load-bearing invariant: the upstream must
// NEVER see server_tool_use or advisor_tool_result block types.
func assertNoServerToolBlocks(t *testing.T, msgs []json.RawMessage) {
	t.Helper()
	for i, m := range msgs {
		s := string(m)
		if strings.Contains(s, `"server_tool_use"`) {
			t.Errorf("message %d still contains server_tool_use: %s", i, s)
		}
		if strings.Contains(s, `"advisor_tool_result"`) {
			t.Errorf("message %d still contains advisor_tool_result: %s", i, s)
		}
	}
}

// assertToolUsePairing enforces the single most common 400 when implementing
// server tools: a tool_use must END an assistant message and the very next
// message must be a user message whose FIRST block is the matching
// tool_result.
func assertToolUsePairing(t *testing.T, msgs []json.RawMessage) {
	t.Helper()
	decoded := decodeMessages(t, msgs)
	for i, m := range decoded {
		if m.Role != "assistant" || len(m.Content) == 0 {
			continue
		}
		types := blockTypes(t, m.Content)
		// Find any tool_use that is NOT the last block.
		for j, ty := range types {
			if ty != "tool_use" {
				continue
			}
			if j != len(types)-1 {
				t.Errorf("message %d: tool_use at block %d is not the last block (types=%v)", i, j, types)
			}
		}
		if types[len(types)-1] != "tool_use" {
			continue
		}
		// A trailing tool_use requires an immediately-following user
		// message carrying the tool_result.
		if i+1 >= len(decoded) {
			t.Errorf("message %d ends in tool_use but is the last message (unpaired)", i)
			continue
		}
		next := decoded[i+1]
		if next.Role != "user" {
			t.Errorf("message %d ends in tool_use but message %d is role=%q, want user", i, i+1, next.Role)
			continue
		}
		nextTypes := blockTypes(t, next.Content)
		if len(nextTypes) == 0 || nextTypes[0] != "tool_result" {
			t.Errorf("message %d ends in tool_use but message %d does not start with tool_result (types=%v)", i, i+1, nextTypes)
		}
		// IDs must match.
		var use struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(m.Content[len(m.Content)-1], &use)
		var res struct {
			ToolUseID string `json:"tool_use_id"`
		}
		_ = json.Unmarshal(next.Content[0], &res)
		if use.ID != res.ToolUseID {
			t.Errorf("message %d tool_use id %q does not match message %d tool_result tool_use_id %q", i, use.ID, i+1, res.ToolUseID)
		}
	}
}

func toolResultText(t *testing.T, block json.RawMessage) string {
	t.Helper()
	var tr struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(block, &tr); err != nil {
		t.Fatalf("tool_result not decodable: %v (%s)", err, block)
	}
	var sb strings.Builder
	for _, c := range tr.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// --- no-op cases ---

func TestExpandAdvisorExchanges_NoAdvisorBlocksIsNoOp(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"hello"}]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	if len(got) != len(in) {
		t.Fatalf("expected no-op, got %d messages (want %d)", len(got), len(in))
	}
	for i := range got {
		if string(got[i]) != string(in[i]) {
			t.Errorf("message %d was rewritten: %s", i, got[i])
		}
	}
}

func TestExpandAdvisorExchanges_EmptyInput(t *testing.T) {
	if got, changed := expandAdvisorExchanges(nil); got != nil || changed {
		t.Errorf("nil input should return (nil, false), got (%v, %v)", got, changed)
	}
	got, changed := expandAdvisorExchanges([]json.RawMessage{})
	if len(got) != 0 || changed {
		t.Errorf("empty input should stay empty and unchanged, got (%v, %v)", got, changed)
	}
}

func TestExpandAdvisorExchanges_UserMessagesUntouched(t *testing.T) {
	// A user message can't hold a server_tool_use, so it must pass through
	// even if it somehow mentions the block type in text.
	in := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"tell me about server_tool_use"}]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	if len(got) != 1 || string(got[0]) != string(in[0]) {
		t.Errorf("user message was rewritten: %v", got)
	}
}

// --- single exchange ---

func TestExpandAdvisorExchanges_SingleExchange(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"do work"}]}`),
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"text","text":"Let me consult the advisor."},
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_result","text":"Split the task in two."}},
			{"type":"text","text":"Okay, splitting it."}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	assertNoServerToolBlocks(t, got)
	assertToolUsePairing(t, got)

	decoded := decodeMessages(t, got)
	// Expect: user, assistant(text+tool_use), user(tool_result), assistant(text)
	if len(decoded) != 4 {
		t.Fatalf("expected 4 messages, got %d: %s", len(decoded), got)
	}
	if decoded[0].Role != "user" {
		t.Errorf("msg0 role: got %q", decoded[0].Role)
	}
	if decoded[1].Role != "assistant" {
		t.Errorf("msg1 role: got %q", decoded[1].Role)
	}
	if ty := blockTypes(t, decoded[1].Content); len(ty) != 2 || ty[0] != "text" || ty[1] != "tool_use" {
		t.Errorf("msg1 block types: got %v, want [text tool_use]", ty)
	}
	if decoded[2].Role != "user" {
		t.Errorf("msg2 role: got %q, want user", decoded[2].Role)
	}
	if text := toolResultText(t, decoded[2].Content[0]); text != "Split the task in two." {
		t.Errorf("tool_result text: got %q", text)
	}
	// Post-result content starts a FRESH assistant message.
	if decoded[3].Role != "assistant" {
		t.Errorf("msg3 role: got %q, want assistant", decoded[3].Role)
	}
	if ty := blockTypes(t, decoded[3].Content); len(ty) != 1 || ty[0] != "text" {
		t.Errorf("msg3 block types: got %v, want [text]", ty)
	}
}

func TestExpandAdvisorExchanges_ConvertedToolUseHasEmptyInput(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{"leftover":"junk"}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_result","text":"go"}}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	decoded := decodeMessages(t, got)
	var use struct {
		Type  string                     `json:"type"`
		Name  string                     `json:"name"`
		Input map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(decoded[0].Content[0], &use); err != nil {
		t.Fatalf("converted block not decodable: %v", err)
	}
	if use.Type != "tool_use" {
		t.Errorf("type: got %q, want tool_use", use.Type)
	}
	if use.Name != "advisor" {
		t.Errorf("name: got %q, want advisor", use.Name)
	}
	if len(use.Input) != 0 {
		t.Errorf("input must be normalized to {}, got %v", use.Input)
	}
}

// --- multiple exchanges in one message ---

func TestExpandAdvisorExchanges_MultipleExchangesInOneMessage(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"text","text":"first"},
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_result","text":"advice one"}},
			{"type":"text","text":"second"},
			{"type":"server_tool_use","id":"srvtoolu_2","name":"advisor","input":{}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_2","content":{"type":"advisor_result","text":"advice two"}},
			{"type":"text","text":"third"}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	assertNoServerToolBlocks(t, got)
	assertToolUsePairing(t, got)

	decoded := decodeMessages(t, got)
	// assistant(text,tool_use) user(tool_result) assistant(text,tool_use) user(tool_result) assistant(text)
	wantRoles := []string{"assistant", "user", "assistant", "user", "assistant"}
	if len(decoded) != len(wantRoles) {
		t.Fatalf("expected %d messages, got %d: %s", len(wantRoles), len(decoded), got)
	}
	for i, want := range wantRoles {
		if decoded[i].Role != want {
			t.Errorf("msg%d role: got %q, want %q", i, decoded[i].Role, want)
		}
	}
	if text := toolResultText(t, decoded[1].Content[0]); text != "advice one" {
		t.Errorf("first tool_result text: got %q", text)
	}
	if text := toolResultText(t, decoded[3].Content[0]); text != "advice two" {
		t.Errorf("second tool_result text: got %q", text)
	}
}

// --- error-shaped results ---

func TestExpandAdvisorExchanges_ErrorResultBecomesUnavailableText(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_tool_result_error","error_code":"overloaded"}}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	assertNoServerToolBlocks(t, got)
	assertToolUsePairing(t, got)
	decoded := decodeMessages(t, got)
	if text := toolResultText(t, decoded[1].Content[0]); text != "[Advisor unavailable: overloaded]" {
		t.Errorf("error tool_result text: got %q", text)
	}
}

// --- orphans / malformed history ---

func TestExpandAdvisorExchanges_OrphanResultStillYieldsToolResult(t *testing.T) {
	// An advisor_tool_result with no preceding server_tool_use (history
	// trimmed by the client). It must still be converted out of the
	// assistant message — the upstream can't see the block type.
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"text","text":"context"},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_orphan","content":{"type":"advisor_result","text":"orphan advice"}}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	assertNoServerToolBlocks(t, got)

	decoded := decodeMessages(t, got)
	if len(decoded) != 2 {
		t.Fatalf("expected assistant + user, got %d: %s", len(decoded), got)
	}
	if decoded[1].Role != "user" {
		t.Errorf("msg1 role: got %q, want user", decoded[1].Role)
	}
	if text := toolResultText(t, decoded[1].Content[0]); text != "orphan advice" {
		t.Errorf("orphan tool_result text: got %q", text)
	}
	var res struct {
		ToolUseID string `json:"tool_use_id"`
	}
	_ = json.Unmarshal(decoded[1].Content[0], &res)
	if res.ToolUseID != "srvtoolu_orphan" {
		t.Errorf("orphan tool_use_id: got %q, want srvtoolu_orphan", res.ToolUseID)
	}
}

func TestExpandAdvisorExchanges_DanglingCallGetsUnavailablePlaceholder(t *testing.T) {
	// A server_tool_use whose result never arrived (stream cut short). The
	// expansion must close the exchange so the upstream never sees an
	// unpaired tool_use.
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"text","text":"consulting"},
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{}}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	assertNoServerToolBlocks(t, got)
	assertToolUsePairing(t, got)
	decoded := decodeMessages(t, got)
	if len(decoded) != 2 {
		t.Fatalf("expected assistant + user placeholder, got %d: %s", len(decoded), got)
	}
	if text := toolResultText(t, decoded[1].Content[0]); text != "[Advisor unavailable]" {
		t.Errorf("placeholder text: got %q", text)
	}
}

func TestExpandAdvisorExchanges_BackToBackCallsEachGetResults(t *testing.T) {
	// Two server_tool_use blocks with no intervening result. Each must end
	// up paired.
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{}},
			{"type":"server_tool_use","id":"srvtoolu_2","name":"advisor","input":{}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_2","content":{"type":"advisor_result","text":"second advice"}}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	assertNoServerToolBlocks(t, got)
	assertToolUsePairing(t, got)
}

// --- non-advisor server tools are left alone ---

func TestExpandAdvisorExchanges_NonAdvisorServerToolUseUntouched(t *testing.T) {
	// A web_search server_tool_use must NOT be rewritten by the advisor
	// expansion — that's the websearch path's business.
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"server_tool_use","id":"srvtoolu_ws","name":"web_search","input":{"query":"x"}},
			{"type":"web_search_tool_result","tool_use_id":"srvtoolu_ws","content":[]}
		]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	if len(got) != 1 || string(got[0]) != string(in[0]) {
		t.Errorf("web_search exchange was rewritten: %v", got)
	}
}

// --- mixed: advisor exchange alongside a client tool_use ---

func TestExpandAdvisorExchanges_PreservesClientToolUse(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"server_tool_use","id":"srvtoolu_1","name":"advisor","input":{}},
			{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_result","text":"run the build"}},
			{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"make"}}
		]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_bash","content":[{"type":"text","text":"ok"}]}]}`),
	}
	got, _ := expandAdvisorExchanges(in)
	assertNoServerToolBlocks(t, got)
	assertToolUsePairing(t, got)

	// The client's Bash tool_use must survive with its input intact.
	if !strings.Contains(string(got[len(got)-2]), `"Bash"`) &&
		!strings.Contains(string(got[len(got)-1]), `"Bash"`) {
		t.Errorf("client Bash tool_use did not survive expansion: %s", got)
	}
	found := false
	for _, m := range got {
		if strings.Contains(string(m), `"command":"make"`) {
			found = true
		}
	}
	if !found {
		t.Error("client tool_use input was lost")
	}
}

// TestExpandAdvisorExchanges_ReportsChangedExplicitly guards the change-flag
// contract. Expansion can rewrite a message WITHOUT changing the message
// count, so callers must not infer "unchanged" from a length comparison.
func TestExpandAdvisorExchanges_ReportsChangedExplicitly(t *testing.T) {
	t.Run("no advisor blocks reports unchanged", func(t *testing.T) {
		in := []json.RawMessage{
			json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"hi"}]}`),
		}
		_, changed := expandAdvisorExchanges(in)
		if changed {
			t.Error("expected changed=false")
		}
	})

	// The regression case: an assistant message whose SOLE content block is
	// an orphan advisor_tool_result expands to exactly one user message —
	// same count, different content. A length-based check would call this
	// "unchanged" and forward advisor_tool_result upstream (guaranteed 400).
	t.Run("sole orphan result reports changed despite equal message count", func(t *testing.T) {
		in := []json.RawMessage{
			json.RawMessage(`{"role":"assistant","content":[{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_result","text":"prior"}}]}`),
		}
		got, changed := expandAdvisorExchanges(in)
		if len(got) != len(in) {
			t.Fatalf("this test is only meaningful when the count is unchanged; got %d want %d", len(got), len(in))
		}
		if !changed {
			t.Error("expected changed=true — the block type was rewritten even though the count held")
		}
		assertNoServerToolBlocks(t, got)
	})
}

// TestTryRewriteAdvisorRequest_SoleOrphanResultNeverReachesUpstream is the
// end-to-end guard for the same bug at the request-prep layer: the caller
// must forward the REWRITTEN body, not the original.
func TestTryRewriteAdvisorRequest_SoleOrphanResultNeverReachesUpstream(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			body := `{"model":"m","tools":[{"name":"Bash","input_schema":{}}],"messages":[` +
				`{"role":"assistant","content":[{"type":"advisor_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"advisor_result","text":"prior advice"}}]}` +
				`]}`
			out, _, ok := tryRewriteAdvisorRequest([]byte(body), AdvisorSettings{Enabled: enabled}, logger{})
			if !ok {
				t.Fatal("expected ok=true so the caller forwards the rewritten body")
			}
			if strings.Contains(string(out), "advisor_tool_result") {
				t.Errorf("advisor_tool_result must never reach the upstream:\n%s", out)
			}
			if !strings.Contains(string(out), "prior advice") {
				t.Error("the advice text must survive as a tool_result")
			}
		})
	}
}
