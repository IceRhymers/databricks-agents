package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// --- resolveAdvisorCall: model + max_tokens resolution chain ---

func TestResolveAdvisorCall_ModelChain(t *testing.T) {
	tests := []struct {
		name          string
		toolModel     string
		defaultModel  string
		executorModel string
		want          string
	}{
		{"tool declaration wins", "tool-m", "cfg-m", "exec-m", "tool-m"},
		{"config default when tool silent", "", "cfg-m", "exec-m", "cfg-m"},
		{"executor model when both silent", "", "", "exec-m", "exec-m"},
		{"empty when nothing set", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAdvisorCall(
				anthropic.AdvisorConstraints{Model: tc.toolModel},
				AdvisorSettings{DefaultModel: tc.defaultModel},
				tc.executorModel,
			)
			if got.Model != tc.want {
				t.Errorf("Model: got %q, want %q", got.Model, tc.want)
			}
		})
	}
}

func TestResolveAdvisorCall_MaxTokensFloorAndDefault(t *testing.T) {
	tests := []struct {
		name          string
		toolMaxTokens int
		toolSet       bool
		defaultTokens int
		wantMaxTokens int
		wantMaxTokSet bool
	}{
		{"default 2048 when nothing set", 0, false, 0, advisorDefaultMaxTokens, false},
		{"config default applies", 0, false, 3000, 3000, false},
		{"config default floored", 0, false, 100, advisorMinMaxTokens, false},
		{"tool value wins over config", 4096, true, 3000, 4096, true},
		{"tool value floored at 1024", 10, true, 3000, advisorMinMaxTokens, true},
		{"tool value exactly at floor", advisorMinMaxTokens, true, 0, advisorMinMaxTokens, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAdvisorCall(
				anthropic.AdvisorConstraints{MaxTokens: tc.toolMaxTokens, MaxTokensSet: tc.toolSet},
				AdvisorSettings{DefaultMaxTokens: tc.defaultTokens},
				"exec",
			)
			if got.MaxTokens != tc.wantMaxTokens {
				t.Errorf("MaxTokens: got %d, want %d", got.MaxTokens, tc.wantMaxTokens)
			}
			if got.MaxTokensSet != tc.wantMaxTokSet {
				t.Errorf("MaxTokensSet: got %v, want %v", got.MaxTokensSet, tc.wantMaxTokSet)
			}
		})
	}
}

// --- error_code mapping (spec §7.3) ---

func TestAdvisorErrorCode_Mapping(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrTooManyRequests, "too_many_requests"},
		{ErrOverloaded, "overloaded"},
		{ErrPromptTooLong, "prompt_too_long"},
		{ErrExecutionTimeExceeded, "execution_time_exceeded"},
		{ErrUnavailable, "unavailable"},
		{errors.New("some random failure"), "unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := advisorErrorCode(tc.err); got != tc.want {
				t.Errorf("advisorErrorCode(%v): got %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestAdvisorErrorCode_WrappedErrors verifies errors.Is traversal — the call
// path wraps sentinels with %w plus context, and the mapping must still land.
func TestAdvisorErrorCode_WrappedErrors(t *testing.T) {
	wrapped := fmt.Errorf("%w: HTTP 503", ErrUnavailable)
	if got := advisorErrorCode(wrapped); got != "unavailable" {
		t.Errorf("wrapped ErrUnavailable: got %q, want %q", got, "unavailable")
	}
	wrapped2 := fmt.Errorf("outer: %w", ErrPromptTooLong)
	if got := advisorErrorCode(wrapped2); got != "prompt_too_long" {
		t.Errorf("wrapped ErrPromptTooLong: got %q, want %q", got, "prompt_too_long")
	}
}

// --- mintAdvisorServerID ---

func TestMintAdvisorServerID(t *testing.T) {
	if got := mintAdvisorServerID("toolu_abc"); got != "srvtoolu_toolu_abc" {
		t.Errorf("got %q, want srvtoolu_toolu_abc", got)
	}
	// Idempotent: already-prefixed ids pass through unchanged.
	if got := mintAdvisorServerID("srvtoolu_xyz"); got != "srvtoolu_xyz" {
		t.Errorf("got %q, want srvtoolu_xyz (idempotent)", got)
	}
	if got := mintAdvisorServerID(""); got != "srvtoolu_" {
		t.Errorf("got %q, want srvtoolu_", got)
	}
}

// --- advisorUnavailableText ---

func TestAdvisorUnavailableText(t *testing.T) {
	if got := advisorUnavailableText(""); got != "[Advisor unavailable]" {
		t.Errorf("empty code: got %q", got)
	}
	if got := advisorUnavailableText("overloaded"); got != "[Advisor unavailable: overloaded]" {
		t.Errorf("with code: got %q", got)
	}
}

// --- block builders (client-facing wire shapes, spec §3.2) ---

func TestBuildAdvisorServerToolUseBlock_InputAlwaysEmpty(t *testing.T) {
	b, err := anthropic.BuildAdvisorServerToolUseBlock("srvtoolu_1")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["type"] != "server_tool_use" {
		t.Errorf("type: got %v, want server_tool_use", got["type"])
	}
	if got["name"] != "advisor" {
		t.Errorf("name: got %v, want advisor", got["name"])
	}
	input, ok := got["input"].(map[string]any)
	if !ok {
		t.Fatalf("input is not an object: %T", got["input"])
	}
	if len(input) != 0 {
		t.Errorf("input must always be {}, got %v", input)
	}
}

func TestBuildAdvisorSuccessBlock_StopReasonOnlyWhenSupplied(t *testing.T) {
	// No stop_reason: the field must be ABSENT, not null/empty. Anthropic
	// surfaces truncation only when the caller imposed an explicit cap.
	b, err := anthropic.BuildAdvisorSuccessBlock("srvtoolu_1", "guidance", "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		Content   map[string]json.RawMessage
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "advisor_tool_result" {
		t.Errorf("type: got %q", got.Type)
	}
	if got.ToolUseID != "srvtoolu_1" {
		t.Errorf("tool_use_id: got %q", got.ToolUseID)
	}
	if _, present := got.Content["stop_reason"]; present {
		t.Error("stop_reason must be absent when not supplied")
	}

	// With stop_reason: present.
	b2, _ := anthropic.BuildAdvisorSuccessBlock("srvtoolu_1", "guidance", "max_tokens")
	var got2 struct {
		Content map[string]json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(b2, &got2)
	sr, present := got2.Content["stop_reason"]
	if !present {
		t.Fatal("stop_reason must be present when supplied")
	}
	var srStr string
	_ = json.Unmarshal(sr, &srStr)
	if srStr != "max_tokens" {
		t.Errorf("stop_reason: got %q, want max_tokens", srStr)
	}
}

func TestBuildAdvisorErrorBlock_Shape(t *testing.T) {
	b, err := anthropic.BuildAdvisorErrorBlock("srvtoolu_9", "max_uses_exceeded")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		Content   struct {
			Type      string `json:"type"`
			ErrorCode string `json:"error_code"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "advisor_tool_result" {
		t.Errorf("outer type: got %q", got.Type)
	}
	if got.Content.Type != "advisor_tool_result_error" {
		t.Errorf("content type: got %q", got.Content.Type)
	}
	if got.Content.ErrorCode != "max_uses_exceeded" {
		t.Errorf("error_code: got %q", got.Content.ErrorCode)
	}
}

// --- ExtractAdvisorResultText (replay-side parsing, spec §9) ---

func TestExtractAdvisorResultText(t *testing.T) {
	success, _ := anthropic.BuildAdvisorSuccessBlock("s1", "do the thing", "")
	if got := anthropic.ExtractAdvisorResultText(success); got != "do the thing" {
		t.Errorf("success: got %q", got)
	}

	errBlock, _ := anthropic.BuildAdvisorErrorBlock("s1", "overloaded")
	if got := anthropic.ExtractAdvisorResultText(errBlock); got != "[Advisor unavailable: overloaded]" {
		t.Errorf("error: got %q", got)
	}

	// Error shape with no error_code.
	noCode := []byte(`{"type":"advisor_tool_result","tool_use_id":"s1","content":{"type":"advisor_tool_result_error"}}`)
	if got := anthropic.ExtractAdvisorResultText(noCode); got != "[Advisor unavailable]" {
		t.Errorf("error no code: got %q", got)
	}

	// Wrong block type.
	wrong := []byte(`{"type":"text","text":"hi"}`)
	if got := anthropic.ExtractAdvisorResultText(wrong); got != "[Advisor unavailable]" {
		t.Errorf("wrong type: got %q", got)
	}

	// Malformed JSON.
	if got := anthropic.ExtractAdvisorResultText([]byte(`{nope`)); got != "[Advisor unavailable]" {
		t.Errorf("malformed: got %q", got)
	}

	// Success shape with empty text falls back.
	emptyText := []byte(`{"type":"advisor_tool_result","tool_use_id":"s1","content":{"type":"advisor_result","text":""}}`)
	if got := anthropic.ExtractAdvisorResultText(emptyText); got != "[Advisor unavailable]" {
		t.Errorf("empty text: got %q", got)
	}
}
