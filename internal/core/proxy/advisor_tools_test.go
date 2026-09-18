package proxy

import (
	"encoding/json"
	"testing"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// --- type-prefix detection (spec §3.1: any dated version matches) ---

func TestIsAdvisorTool_TypePrefixDetection(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want bool
	}{
		{"canonical dated version", `{"type":"advisor_20260301","name":"advisor"}`, true},
		{"future dated version", `{"type":"advisor_20991231","name":"advisor"}`, true},
		{"bare prefix", `{"type":"advisor_","name":"advisor"}`, true},
		{"no trailing underscore is NOT a match", `{"type":"advisor","name":"advisor"}`, false},
		{"web_search is not advisor", `{"type":"web_search_20250305","name":"web_search"}`, false},
		{"plain function tool", `{"name":"advisor","input_schema":{}}`, false},
		{"name advisor but no type", `{"name":"advisor"}`, false},
		{"malformed json", `{nope`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := anthropic.IsAdvisorTool([]byte(tc.tool)); got != tc.want {
				t.Errorf("IsAdvisorTool(%s): got %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

// --- auto-injection scoping + idempotency (spec §3.1) ---

func TestInjectAdvisorTool_ScopingAndIdempotency(t *testing.T) {
	clientTool := json.RawMessage(`{"name":"Bash","input_schema":{"type":"object"}}`)
	advisorDecl := json.RawMessage(`{"type":"advisor_20260301","name":"advisor"}`)

	t.Run("no tools means no injection (advisor is scoped to agentic workloads)", func(t *testing.T) {
		got := injectAdvisorTool(nil, true)
		if len(got) != 0 {
			t.Errorf("expected no injection into an empty tools slice, got %d tools", len(got))
		}
		got = injectAdvisorTool([]json.RawMessage{}, true)
		if len(got) != 0 {
			t.Errorf("expected no injection into an empty tools slice, got %d tools", len(got))
		}
	})

	t.Run("disabled means no injection", func(t *testing.T) {
		got := injectAdvisorTool([]json.RawMessage{clientTool}, false)
		if len(got) != 1 {
			t.Errorf("expected no injection when disabled, got %d tools", len(got))
		}
	})

	t.Run("injects when at least one tool is present", func(t *testing.T) {
		got := injectAdvisorTool([]json.RawMessage{clientTool}, true)
		if len(got) != 2 {
			t.Fatalf("expected 2 tools after injection, got %d", len(got))
		}
		if !anthropic.IsAdvisorTool(got[1]) {
			t.Errorf("injected tool is not an advisor tool: %s", got[1])
		}
		var decl struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(got[1], &decl); err != nil {
			t.Fatalf("unmarshal injected: %v", err)
		}
		if decl.Type != anthropic.AdvisorToolVersion {
			t.Errorf("injected type: got %q, want %q", decl.Type, anthropic.AdvisorToolVersion)
		}
		if decl.Name != anthropic.AdvisorToolName {
			t.Errorf("injected name: got %q, want %q", decl.Name, anthropic.AdvisorToolName)
		}
	})

	t.Run("idempotent when an advisor tool already exists", func(t *testing.T) {
		in := []json.RawMessage{clientTool, advisorDecl}
		got := injectAdvisorTool(in, true)
		if len(got) != 2 {
			t.Errorf("expected idempotent no-op, got %d tools", len(got))
		}
		// Double application must also be a no-op.
		got2 := injectAdvisorTool(got, true)
		if len(got2) != 2 {
			t.Errorf("expected second application to be a no-op, got %d tools", len(got2))
		}
	})

	t.Run("never mutates the caller's slice", func(t *testing.T) {
		// Allocate with spare capacity so a naive append would clobber the
		// caller's backing array rather than copying.
		in := make([]json.RawMessage, 1, 4)
		in[0] = clientTool
		got := injectAdvisorTool(in, true)
		if len(in) != 1 {
			t.Errorf("caller's slice length changed: %d", len(in))
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 tools in result, got %d", len(got))
		}
		// Mutating the result must not be observable through the input's
		// backing array.
		got[1] = json.RawMessage(`{"sentinel":true}`)
		if len(in) > 1 {
			t.Error("caller's slice observed the mutation")
		}
	})
}

// --- function-tool conversion (spec §4) ---

func TestConvertAdvisorTool_UpstreamShape(t *testing.T) {
	b := anthropic.ConvertAdvisorTool()
	var got struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			Type                 string                     `json:"type"`
			Properties           map[string]json.RawMessage `json:"properties"`
			AdditionalProperties *bool                      `json:"additionalProperties"`
		} `json:"input_schema"`
		// Constraint fields MUST NOT survive into the upstream tool.
		MaxUses   *int    `json:"max_uses"`
		Model     *string `json:"model"`
		MaxTokens *int    `json:"max_tokens"`
		Type      *string `json:"type"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != "advisor" {
		t.Errorf("name: got %q, want advisor", got.Name)
	}
	if got.Description == "" {
		t.Error("description must be non-empty so the executor model knows what the tool does")
	}
	if got.InputSchema.Type != "object" {
		t.Errorf("input_schema.type: got %q, want object", got.InputSchema.Type)
	}
	if len(got.InputSchema.Properties) != 0 {
		t.Errorf("input_schema.properties must be empty (advisor takes no parameters), got %v", got.InputSchema.Properties)
	}
	if got.InputSchema.AdditionalProperties == nil || *got.InputSchema.AdditionalProperties {
		t.Error("input_schema.additionalProperties must be false")
	}
	// The upstream must see a plain function tool — no server-tool "type",
	// and no proxy-enforced constraint fields leaking through.
	if got.Type != nil {
		t.Errorf("converted tool must not carry a server-tool type, got %q", *got.Type)
	}
	if got.MaxUses != nil || got.Model != nil || got.MaxTokens != nil {
		t.Error("constraint fields (max_uses/model/max_tokens) must be proxy-enforced, never sent upstream")
	}
}

// --- constraint capture ---

func TestParseAdvisorConstraints(t *testing.T) {
	t.Run("all fields present", func(t *testing.T) {
		got := anthropic.ParseAdvisorConstraints([]byte(
			`{"type":"advisor_20260301","name":"advisor","max_uses":3,"model":"m","max_tokens":4096}`))
		if got.MaxUses != 3 {
			t.Errorf("MaxUses: got %d, want 3", got.MaxUses)
		}
		if got.Model != "m" {
			t.Errorf("Model: got %q, want m", got.Model)
		}
		if got.MaxTokens != 4096 {
			t.Errorf("MaxTokens: got %d, want 4096", got.MaxTokens)
		}
		if !got.MaxTokensSet {
			t.Error("MaxTokensSet must be true when max_tokens is present")
		}
	})

	t.Run("absent fields leave zero values and MaxTokensSet false", func(t *testing.T) {
		got := anthropic.ParseAdvisorConstraints([]byte(`{"type":"advisor_20260301","name":"advisor"}`))
		if got.MaxUses != 0 || got.Model != "" || got.MaxTokens != 0 {
			t.Errorf("expected zero values, got %+v", got)
		}
		if got.MaxTokensSet {
			t.Error("MaxTokensSet must be false when max_tokens is absent (controls stop_reason echo)")
		}
	})

	t.Run("explicit zero max_tokens still records MaxTokensSet", func(t *testing.T) {
		// A client that explicitly sends max_tokens:0 has imposed a cap, so
		// the stop_reason echo applies even though the value gets floored.
		got := anthropic.ParseAdvisorConstraints([]byte(`{"type":"advisor_20260301","max_tokens":0}`))
		if !got.MaxTokensSet {
			t.Error("MaxTokensSet must be true when max_tokens is explicitly present, even at 0")
		}
	})
}

// --- prepareAdvisorToolsForUpstream ---

func TestPrepareAdvisorToolsForUpstream(t *testing.T) {
	clientTool := json.RawMessage(`{"name":"Bash","input_schema":{"type":"object"}}`)
	advisorDecl := json.RawMessage(`{"type":"advisor_20260301","name":"advisor","max_uses":2,"model":"strong-m"}`)
	lg := logger{prefix: "test"}

	t.Run("converts advisor tool and captures constraints", func(t *testing.T) {
		out, constraints := prepareAdvisorToolsForUpstream(
			[]json.RawMessage{clientTool, advisorDecl}, true, lg)
		if len(out) != 2 {
			t.Fatalf("expected 2 tools out, got %d", len(out))
		}
		// The client tool passes through untouched.
		if string(out[0]) != string(clientTool) {
			t.Errorf("client tool was modified: %s", out[0])
		}
		// The advisor tool is now a plain function tool.
		if anthropic.IsAdvisorTool(out[1]) {
			t.Errorf("advisor tool was not converted: %s", out[1])
		}
		if anthropic.ToolName(out[1]) != "advisor" {
			t.Errorf("converted tool name: got %q", anthropic.ToolName(out[1]))
		}
		// Constraints captured proxy-side.
		if !constraints.any() {
			t.Fatal("expected constraints to be captured")
		}
		c, ok := constraints["advisor"]
		if !ok {
			t.Fatalf("constraints not keyed by tool name: %v", constraints)
		}
		if c.MaxUses != 2 {
			t.Errorf("MaxUses: got %d, want 2", c.MaxUses)
		}
		if c.Model != "strong-m" {
			t.Errorf("Model: got %q, want strong-m", c.Model)
		}
	})

	t.Run("disabled strips the advisor tool entirely", func(t *testing.T) {
		out, constraints := prepareAdvisorToolsForUpstream(
			[]json.RawMessage{clientTool, advisorDecl}, false, lg)
		if len(out) != 1 {
			t.Fatalf("expected the advisor tool to be stripped, got %d tools", len(out))
		}
		if string(out[0]) != string(clientTool) {
			t.Errorf("surviving tool should be the client tool, got %s", out[0])
		}
		if constraints.any() {
			t.Error("no constraints should be captured when the feature is disabled")
		}
	})

	t.Run("no advisor tool is a pass-through no-op", func(t *testing.T) {
		in := []json.RawMessage{clientTool}
		out, constraints := prepareAdvisorToolsForUpstream(in, true, lg)
		if len(out) != 1 || string(out[0]) != string(clientTool) {
			t.Errorf("expected verbatim pass-through, got %v", out)
		}
		if constraints.any() {
			t.Error("expected no constraints")
		}
	})

	t.Run("empty tools is a no-op", func(t *testing.T) {
		out, constraints := prepareAdvisorToolsForUpstream(nil, true, lg)
		if len(out) != 0 {
			t.Errorf("expected empty out, got %v", out)
		}
		if constraints.any() {
			t.Error("expected no constraints")
		}
	})
}

// --- advisorToolSet helpers ---

func TestAdvisorToolSet_Helpers(t *testing.T) {
	var nilSet advisorToolSet
	if nilSet.any() {
		t.Error("nil set must not report any()")
	}
	if nilSet.isAdvisorToolUseName("advisor") {
		t.Error("nil set must not match any tool name")
	}

	set := advisorToolSet{"advisor": {}}
	if !set.any() {
		t.Error("populated set must report any()")
	}
	if !set.isAdvisorToolUseName("advisor") {
		t.Error("expected 'advisor' to match")
	}
	if set.isAdvisorToolUseName("Bash") {
		t.Error("client tool names must not match")
	}
}
