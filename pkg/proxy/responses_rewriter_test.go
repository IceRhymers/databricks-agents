package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runResponsesPump runs pumpResponsesSSE on the given input and returns the
// written output as a string. Reuses flushRecorder from sse_rewriter_test.go.
func runResponsesPump(t *testing.T, input string) string {
	t.Helper()
	rec := newFlushRecorder()
	if err := pumpResponsesSSE(context.Background(), rec, strings.NewReader(input), false); err != nil {
		t.Fatalf("pumpResponsesSSE: %v", err)
	}
	return rec.body.String()
}

// TestResponsesRewriter_MismatchRewritten: a downstream output_text.delta
// carrying an item_id that disagrees with the output_item.added item.id is
// rewritten to the canonical id.
func TestResponsesRewriter_MismatchRewritten(t *testing.T) {
	input := strings.Join([]string{
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_canonical","type":"message"}}`),
		frame("response.content_part.added", `{"type":"response.content_part.added","output_index":0,"item_id":"item_WRONG","part":{"type":"output_text","text":""}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_WRONG","delta":"hello"}`),
		frame("response.output_text.done", `{"type":"response.output_text.done","output_index":0,"item_id":"item_WRONG","text":"hello"}`),
	}, "")

	out := runResponsesPump(t, input)

	if strings.Contains(out, "item_WRONG") {
		t.Errorf("mismatched id should have been rewritten away; output still contains item_WRONG:\n%s", out)
	}
	// The added event's own canonical id stays; downstream events now carry it.
	if c := strings.Count(out, "item_canonical"); c != 4 {
		t.Errorf("expected canonical id on the added event + 3 rewritten downstream events (4 total), got %d:\n%s", c, out)
	}
}

// TestResponsesRewriter_MatchingIdPassthrough: when downstream item_id already
// equals the canonical id, frames are byte-identical to the input.
func TestResponsesRewriter_MatchingIdPassthrough(t *testing.T) {
	input := strings.Join([]string{
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_ok","type":"message"}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_ok","delta":"hi"}`),
	}, "")

	out := runResponsesPump(t, input)
	if out != input {
		t.Errorf("conforming stream must be byte-identical.\n--- want ---\n%q\n--- got ---\n%q", input, out)
	}
}

// TestResponsesRewriter_ConformingByteIdentical: a full, well-formed Responses
// stream (added/delta/done/output_item.done/response.completed) is emitted
// byte-for-byte unchanged. Mirrors the websearch byte-identity guarantee.
func TestResponsesRewriter_ConformingByteIdentical(t *testing.T) {
	input := strings.Join([]string{
		frame("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`),
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_0","type":"message"}}`),
		frame("response.content_part.added", `{"type":"response.content_part.added","output_index":0,"item_id":"item_0","part":{"type":"output_text"}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_0","delta":"abc"}`),
		frame("response.output_text.done", `{"type":"response.output_text.done","output_index":0,"item_id":"item_0","text":"abc"}`),
		frame("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"id":"item_0","type":"message"}}`),
		frame("response.completed", `{"type":"response.completed","response":{"id":"resp_1"}}`),
		"data: [DONE]\n\n",
	}, "")

	out := runResponsesPump(t, input)
	if out != input {
		t.Errorf("conforming full stream must be byte-identical.\n--- want ---\n%q\n--- got ---\n%q", input, out)
	}
}

// TestResponsesRewriter_ReorderedDeltaBeforeAdded: a delta arriving before its
// output_item.added (cache miss) is passed through unchanged — we never invent
// a canonical id we haven't seen.
func TestResponsesRewriter_ReorderedDeltaBeforeAdded(t *testing.T) {
	input := strings.Join([]string{
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_early","delta":"x"}`),
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_late","type":"message"}}`),
	}, "")

	out := runResponsesPump(t, input)
	if out != input {
		t.Errorf("reordered delta-before-added must pass through unchanged.\n--- want ---\n%q\n--- got ---\n%q", input, out)
	}
}

// TestResponsesRewriter_ReusedIndexNoStaleId: after output_item.done retires an
// index, a new output_item.added reusing the same index installs a fresh
// canonical id; a stale id from the first item is NOT applied to the second.
func TestResponsesRewriter_ReusedIndexNoStaleId(t *testing.T) {
	input := strings.Join([]string{
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_first","type":"message"}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_first","delta":"a"}`),
		frame("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"id":"item_first","type":"message"}}`),
		// Index 0 reused for a new item.
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_second","type":"message"}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_WRONG","delta":"b"}`),
	}, "")

	out := runResponsesPump(t, input)
	// The second delta's wrong id must be rewritten to item_second, not the
	// retired item_first.
	if strings.Contains(out, "item_WRONG") {
		t.Errorf("reused-index delta should have been rewritten:\n%s", out)
	}
	// item_first legitimately appears on the first item's added/delta/done
	// frames; the failure mode we guard against is item_first leaking onto the
	// SECOND item's delta. Assert the second delta (the last frame) carries
	// item_second, proving the retired id was not reapplied.
	frames := strings.Split(strings.TrimRight(out, "\n"), "\n\n")
	lastFrame := frames[len(frames)-1]
	if !strings.Contains(lastFrame, `"item_id":"item_second"`) {
		t.Errorf("reused-index delta should carry item_second, not the retired id:\nlast frame: %s\nfull:\n%s", lastFrame, out)
	}
	if strings.Contains(lastFrame, "item_first") {
		t.Errorf("stale id from retired index leaked onto the reused index:\n%s", lastFrame)
	}
}

// TestResponsesRewriter_NonMessageItemIgnored: reasoning / function_call items
// are not cached, so a downstream event for that index is passed through (no
// canonical id to apply).
func TestResponsesRewriter_NonMessageItemIgnored(t *testing.T) {
	input := strings.Join([]string{
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_reason","type":"reasoning"}}`),
		frame("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"item_other","delta":"think"}`),
	}, "")

	out := runResponsesPump(t, input)
	if out != input {
		t.Errorf("non-message item should be ignored (passthrough).\n--- want ---\n%q\n--- got ---\n%q", input, out)
	}
}

// TestResponsesRewriter_DonePassthrough: the [DONE] sentinel and unparseable
// payloads pass through verbatim.
func TestResponsesRewriter_DonePassthrough(t *testing.T) {
	input := strings.Join([]string{
		"data: [DONE]\n\n",
		"data: not-json\n\n",
		": comment-only\n\n",
	}, "")

	out := runResponsesPump(t, input)
	if out != input {
		t.Errorf("DONE/non-JSON/comment frames must pass through.\n--- want ---\n%q\n--- got ---\n%q", input, out)
	}
}

// TestResponsesRewriter_IndexCacheCapped: an upstream that emits more than
// responsesIndexCacheLimit distinct output_index added-events (without
// output_item.done) does not grow the cache unbounded; indices past the cap
// are not cached and their downstream events degrade to passthrough.
func TestResponsesRewriter_IndexCacheCapped(t *testing.T) {
	var b strings.Builder
	// Emit cap+10 added events at distinct indices, each with a canonical id.
	for i := 0; i < responsesIndexCacheLimit+10; i++ {
		b.WriteString(frame("response.output_item.added",
			fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":{"id":"item_%d","type":"message"}}`, i, i)))
	}
	// A downstream delta for an index BEYOND the cap with a wrong id: since it
	// was never cached, it must pass through unchanged (no panic, no rewrite).
	overCapIdx := responsesIndexCacheLimit + 5
	deltaFrame := frame("response.output_text.delta",
		fmt.Sprintf(`{"type":"response.output_text.delta","output_index":%d,"item_id":"item_WRONG","delta":"x"}`, overCapIdx))
	b.WriteString(deltaFrame)

	out := runResponsesPump(t, b.String())

	// The over-cap delta should still contain item_WRONG (was never cached).
	if !strings.Contains(out, "item_WRONG") {
		t.Errorf("over-cap delta should pass through unchanged (uncached index), but item_WRONG was removed")
	}
}

// TestResponsesRewriter_OversizedFrameNoPanic: a frame larger than the 1 MiB
// scanner cap surfaces a scanner error rather than panicking.
func TestResponsesRewriter_OversizedFrameNoPanic(t *testing.T) {
	huge := strings.Repeat("a", 2*1024*1024)
	input := frame("response.output_text.delta",
		fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"item_id":"x","delta":"%s"}`, huge))

	rec := newFlushRecorder()
	// Must not panic; an error is acceptable (oversized frame).
	_ = pumpResponsesSSE(context.Background(), rec, strings.NewReader(input), false)
}

func TestIsResponsesPath(t *testing.T) {
	cases := map[string]bool{
		"/serving-endpoints/ws.aws.proxy.codex/openai/v1/responses": true,
		"/responses":           true,
		"/v1/responses":        true,
		"/v1/messages":         false,
		"/v1/chat/completions": false,
		"/responses/123":       true,
	}
	for path, want := range cases {
		if got := isResponsesPath(path); got != want {
			t.Errorf("isResponsesPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestResponsesRewriter_Integration_EndToEnd: full request -> fake upstream
// emitting mismatched-id Responses SSE -> proxy output carries canonical ids,
// Content-Length stripped, Content-Type preserved.
func TestResponsesRewriter_Integration_EndToEnd(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_real","type":"message"}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_bad","delta":"chunk"}`),
		frame("response.output_text.done", `{"type":"response.output_text.done","output_index":0,"item_id":"item_bad","text":"chunk"}`),
	}, "")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(upstreamSSE)))
		w.WriteHeader(200)
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		ResponsesRewrite:  ResponsesRewriteSettings{Enabled: true},
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	body := `{"model":"databricks-gpt-5-4","input":"hi","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/serving-endpoints/ws.aws.proxy.codex/openai/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	out := rec.Body.String()
	if strings.Contains(out, "item_bad") {
		t.Errorf("integration: mismatched id should be rewritten:\n%s", out)
	}
	if !strings.Contains(out, "item_real") {
		t.Errorf("integration: canonical id missing:\n%s", out)
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Errorf("integration: Content-Length should be stripped, got %q", rec.Header().Get("Content-Length"))
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("integration: Content-Type should be preserved, got %q", rec.Header().Get("Content-Type"))
	}
}

// TestResponsesRewriter_Integration_DisabledByteIdentical: with
// ResponsesRewrite disabled, the mismatched stream is forwarded byte-identically
// (the byte-identity guarantee for Claude Code and any opt-out).
func TestResponsesRewriter_Integration_DisabledByteIdentical(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"item_real","type":"message"}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"item_id":"item_bad","delta":"chunk"}`),
	}, "")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer upstream.Close()

	cfg := &Config{
		InferenceUpstream: upstream.URL,
		OTELUpstream:      upstream.URL,
		TokenSource:       warmToken("tok"),
		// ResponsesRewrite zero value: disabled.
	}
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/serving-endpoints/ws.aws.proxy.codex/openai/v1/responses", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "item_bad") {
		t.Errorf("disabled: stream must be forwarded verbatim (mismatched id preserved):\n%s", rec.Body.String())
	}
}
