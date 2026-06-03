package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/proxy/anthropic"
	"github.com/IceRhymers/databricks-claude/pkg/websearch"
)

// WebSearchSettings bundles the optional --with-websearch knobs. Embedded in
// Config; when Enabled is false the websearch path is fully bypassed and the
// proxy forwards bytes verbatim (regression-tested for byte-identity).
type WebSearchSettings struct {
	Enabled     bool
	Backend     websearch.Backend
	Robots      websearch.RobotsChecker
	FetchBudget int
}

// fulfilledMemoryLimit caps the in-process map of tool_use_id → kind/input
// pairs used for tool_result substitution. 1024 entries is overkill for any
// real conversation but cheap to bound.
const fulfilledMemoryLimit = 1024

// inferenceHandler returns the http.Handler for the inference (default)
// route. When ws.Enabled is false this is a thin replacement for the
// httputil.ReverseProxy that previously lived here, byte-identical on the
// wire. When ws.Enabled is true it inspects /v1/messages bodies, rewrites
// web_search_* / web_fetch_* tool entries, and substitutes Claude Code's
// failed tool_result blocks with locally-fulfilled output.
func inferenceHandler(upstream *url.URL, config *Config, ws WebSearchSettings) http.Handler {
	transport := http.DefaultTransport
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			handleWebSocket(w, r, upstream, config)
			return
		}

		// Buffer the body so we can optionally inspect/mutate it. Bodies are
		// /v1/messages JSON requests, bounded by max_tokens budget — well
		// under a few MB. Cap defensively at 8 MiB.
		const maxBodyBytes = 8 * 1024 * 1024
		var bodyBytes []byte
		if r.Body != nil {
			lr := io.LimitReader(r.Body, maxBodyBytes+1)
			b, err := io.ReadAll(lr)
			if err != nil {
				http.Error(w, "read request body failed", http.StatusBadRequest)
				return
			}
			bodyBytes = b
			r.Body.Close()
		}

		var rewroteTools rewrittenTools
		if ws.Enabled && isMessagesPath(r.URL.Path) && len(bodyBytes) > 0 {
			if mutated, tools, ok := tryRewriteRequest(r.Context(), bodyBytes, ws, config.Verbose); ok {
				bodyBytes = mutated
				rewroteTools = tools
			} else if config.Verbose {
				log.Printf("databricks-claude: websearch: no rewrite applied to /v1/messages (no web_search_*/web_fetch_* tools and no annotated tool_results matched)")
			}
		}

		// Build outbound request.
		token, terr := config.TokenSource.Token(r.Context())
		if terr != nil {
			log.Printf("databricks-claude: token fetch error: %v", terr)
		}

		basePath := strings.TrimRight(upstream.Path, "/")
		outURL := *upstream
		outURL.Path = basePath + r.URL.Path
		outURL.RawPath = ""
		outURL.RawQuery = r.URL.RawQuery

		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, outURL.String(), bytes.NewReader(bodyBytes))
		if err != nil {
			http.Error(w, "build upstream request failed", http.StatusInternalServerError)
			return
		}
		// Copy headers verbatim, excluding hop-by-hop.
		for k, vv := range r.Header {
			if isHopByHop(k) {
				continue
			}
			for _, v := range vv {
				outReq.Header.Add(k, v)
			}
		}
		outReq.Header.Del("Authorization")
		outReq.Header.Del("X-Api-Key")
		if token != "" {
			outReq.Header.Set("Authorization", "Bearer "+token)
			outReq.Header.Set("x-api-key", token)
		}
		outReq.Header.Set("x-databricks-use-coding-agent-mode", "true")
		outReq.Host = upstream.Host
		// Content-Length must reflect the (possibly mutated) body.
		outReq.ContentLength = int64(len(bodyBytes))
		outReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))

		if config.Verbose {
			log.Printf("databricks-claude: inference → %s %s%s", outReq.Method, outReq.URL.Host, outReq.URL.Path)
		}

		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// PASSTHROUGH FAST PATH (also load-bearing for byte-identity):
		// If the request rewrite did not touch any web_search_*/web_fetch_*
		// tools, forward the response verbatim with a flushing copy. This
		// is the FIRST check on the response side — no JSON/SSE inspection,
		// no buffering. Required by TestProxy_WebSearchDisabled_ByteIdenticalForward.
		if !rewroteTools.Any() {
			// RESPONSES-API REWRITE: opencode (and other OpenAI-compatible
			// clients) run with WebSearch disabled, so they always land here.
			// When ResponsesRewrite is enabled and this is a Responses-API SSE
			// stream, reconcile mismatched item_ids (Databricks AI Gateway
			// re-encodes the stream with divergent ids, breaking @ai-sdk/openai).
			// Independent of ws.Enabled by design. Claude Code never hits
			// /responses, so this is byte-identical for it.
			if config.ResponsesRewrite.Enabled && isResponsesPath(r.URL.Path) && isSSEResponse(resp) {
				for k, vv := range resp.Header {
					if isHopByHop(k) {
						continue
					}
					for _, v := range vv {
						w.Header().Add(k, v)
					}
				}
				// Strip Content-Length: a rewritten frame changes the body
				// length on the wire.
				w.Header().Del("Content-Length")
				w.WriteHeader(resp.StatusCode)
				if err := pumpResponsesSSE(r.Context(), w, resp.Body, config.Verbose); err != nil && config.Verbose {
					log.Printf("databricks-claude: responses: SSE pump err: %v", err)
				}
				return
			}

			for k, vv := range resp.Header {
				if isHopByHop(k) {
					continue
				}
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(resp.StatusCode)
			flushingCopy(w, resp.Body, config.Verbose)
			return
		}

		// REWRITE PATHS: copy headers (excluding hop-by-hop), but strip
		// Content-Length so the rewritten body length is correct on the wire.
		// (Tool_use blocks are terminal in current Anthropic streams — once
		// content_block_stop fires for a tool_use, no further text deltas
		// are generated. Synchronous local-fulfillment inside pumpSSE
		// therefore does not block any other content. If Anthropic changes
		// behavior to interleave text + tool_use, liveness must be revisited.)
		for k, vv := range resp.Header {
			if isHopByHop(k) {
				continue
			}
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.Header().Del("Content-Length")

		switch {
		case isSSEResponse(resp):
			w.WriteHeader(resp.StatusCode)
			if err := pumpSSE(r.Context(), w, resp.Body, ws, rewroteTools, config.Verbose); err != nil && config.Verbose {
				log.Printf("databricks-claude: websearch: SSE pump err: %v", err)
			}
			return
		case isJSONResponse(resp):
			const maxRespBytes = 8 * 1024 * 1024
			limited := io.LimitReader(resp.Body, maxRespBytes+1)
			body, rerr := io.ReadAll(limited)
			if rerr != nil {
				http.Error(w, "read upstream JSON failed", http.StatusBadGateway)
				return
			}
			out, jerr := rewriteJSONResponse(r.Context(), body, ws, config.Verbose)
			if jerr != nil && config.Verbose {
				log.Printf("databricks-claude: websearch: JSON rewrite err: %v (forwarding original)", jerr)
				out = body
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(out)))
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(out)
			return
		default:
			// Unknown content-type: defensive passthrough with flushing copy.
			if config.Verbose {
				log.Printf("databricks-claude: websearch: unexpected Content-Type %q on rewrite path; passing through", resp.Header.Get("Content-Type"))
			}
			w.WriteHeader(resp.StatusCode)
			flushingCopy(w, resp.Body, config.Verbose)
			return
		}
	})
}

func isMessagesPath(p string) bool {
	return strings.HasSuffix(p, "/v1/messages") || strings.Contains(p, "/v1/messages?")
}

// flushingCopy streams src to w with per-chunk Flush so SSE chunks reach
// the client as they arrive. Used by the byte-identity passthrough fast
// path AND the unknown-content-type defensive branch — keep these two
// callers in lockstep.
func flushingCopy(w http.ResponseWriter, src io.Reader, verbose bool) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF && verbose {
				log.Printf("databricks-claude: response read err: %v", rerr)
			}
			return
		}
	}
}

// isHopByHop reports whether a header is in RFC 7230 hop-by-hop set.
func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-length":
		return true
	}
	return false
}

// tryRewriteRequest attempts to decode body as an Anthropic Messages
// request, rewrite web_search_*/web_fetch_* tools, and substitute any
// is_error tool_result blocks targeting our annotated tool_use's. Returns
// the (possibly mutated) body bytes, a rewrittenTools struct describing
// which kinds of server tools were touched (used by the response-side
// SSE/JSON rewriter), and ok=true if a successful rewrite was done.
// On ok=false the body is unchanged and rewrittenTools is zero.
func tryRewriteRequest(ctx context.Context, body []byte, ws WebSearchSettings, verbose bool) ([]byte, rewrittenTools, bool) {
	var rt rewrittenTools
	var req anthropic.Request
	if err := json.Unmarshal(body, &req); err != nil {
		if verbose {
			log.Printf("databricks-claude: websearch: body not JSON-decodable as Anthropic Request: %v", err)
		}
		return body, rt, false
	}

	if verbose {
		names := make([]string, 0, len(req.Tools))
		for _, t := range req.Tools {
			var probe struct {
				Name string `json:"name"`
				Type string `json:"type"`
			}
			_ = json.Unmarshal(t, &probe)
			if probe.Type != "" {
				names = append(names, probe.Type)
			} else {
				names = append(names, probe.Name)
			}
		}
		log.Printf("databricks-claude: websearch: /v1/messages tools=%d %v", len(req.Tools), names)
	}

	// Phase 1: walk Tools and rewrite web_search_*/web_fetch_*.
	rewroteSearch, rewroteFetch := 0, 0
	if len(req.Tools) > 0 {
		for i, t := range req.Tools {
			switch {
			case anthropic.IsWebSearchTool(t):
				req.Tools[i] = anthropic.RewriteWebSearchTool()
				rewroteSearch++
			case anthropic.IsWebFetchTool(t):
				req.Tools[i] = anthropic.RewriteWebFetchTool()
				rewroteFetch++
			}
		}
	}
	if rewroteSearch > 0 {
		rt.HasWebSearch = true
	}
	if rewroteFetch > 0 {
		rt.HasWebFetch = true
	}
	if verbose && (rewroteSearch+rewroteFetch) > 0 {
		log.Printf("databricks-claude: websearch: rewrote tools web_search=%d web_fetch=%d", rewroteSearch, rewroteFetch)
	}

	// Phase 2: walk Messages, build a map of tool_use_id → {name, input}
	// for assistant tool_use blocks targeting our annotated tools, then
	// substitute any subsequent user tool_result blocks whose IDs match.
	annotated := scanAnnotatedToolUses(req.Messages)
	substituted := 0
	if len(annotated) > 0 {
		for i, msg := range req.Messages {
			if mutated, ok := substituteToolResults(ctx, msg, annotated, ws); ok {
				req.Messages[i] = mutated
				substituted++
			}
		}
	}
	if verbose {
		log.Printf("databricks-claude: websearch: annotated tool_use_ids=%d substituted messages=%d", len(annotated), substituted)
	}

	// If nothing changed, signal "no rewrite" so the outer code can log that.
	if rewroteSearch+rewroteFetch+substituted == 0 {
		return body, rt, false
	}

	out, err := json.Marshal(&req)
	if err != nil {
		return body, rt, false
	}
	return out, rt, true
}

type annotatedToolUse struct {
	ID    string
	Name  string // web_search | web_fetch
	Input json.RawMessage
}

// scanAnnotatedToolUses returns a map of tool_use_id → annotatedToolUse for
// every assistant tool_use block whose name matches our annotated client
// tools (web_search/web_fetch).
func scanAnnotatedToolUses(messages []json.RawMessage) map[string]annotatedToolUse {
	out := map[string]annotatedToolUse{}
	for _, m := range messages {
		var msg struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(m, &msg); err != nil {
			continue
		}
		if msg.Role != "assistant" {
			continue
		}
		for _, block := range msg.Content {
			var probe struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(block, &probe); err != nil {
				continue
			}
			if probe.Type != "tool_use" {
				continue
			}
			if probe.Name != anthropic.ClientToolName_WebSearch && probe.Name != anthropic.ClientToolName_WebFetch {
				continue
			}
			if len(out) >= fulfilledMemoryLimit {
				return out
			}
			out[probe.ID] = annotatedToolUse{ID: probe.ID, Name: probe.Name, Input: probe.Input}
		}
	}
	return out
}

// substituteToolResults rewrites any is_error tool_result block in a user
// message whose tool_use_id matches one of our annotated tool_use's,
// replacing it with the locally-fulfilled output.
func substituteToolResults(ctx context.Context, msg json.RawMessage, annotated map[string]annotatedToolUse, ws WebSearchSettings) (json.RawMessage, bool) {
	var m struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return msg, false
	}
	if m.Role != "user" || len(m.Content) == 0 {
		return msg, false
	}
	mutated := false
	for i, block := range m.Content {
		var probe struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
		}
		if err := json.Unmarshal(block, &probe); err != nil {
			continue
		}
		if probe.Type != "tool_result" {
			continue
		}
		entry, ok := annotated[probe.ToolUseID]
		if !ok {
			continue
		}
		// We substitute regardless of is_error: Claude Code may return a
		// non-error result if a user happens to have a same-named local tool,
		// but the proxy is the source of truth for these annotated tools.
		newBlock, err := fulfillToolResult(ctx, probe.ToolUseID, entry, ws)
		if err == nil {
			m.Content[i] = newBlock
			mutated = true
		}
	}
	if !mutated {
		return msg, false
	}
	// Re-emit msg, preserving any extra top-level fields by parsing into a
	// generic map so we don't lose anything.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(msg, &raw); err != nil {
		return msg, false
	}
	cb, err := json.Marshal(m.Content)
	if err != nil {
		return msg, false
	}
	raw["content"] = cb
	out, err := json.Marshal(raw)
	if err != nil {
		return msg, false
	}
	return out, true
}

// fulfillToolResult performs the local search/fetch and returns a new
// tool_result content block matching the standard Anthropic shape.
func fulfillToolResult(ctx context.Context, toolUseID string, entry annotatedToolUse, ws WebSearchSettings) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var resultText string
	var isErr bool
	switch entry.Name {
	case anthropic.ClientToolName_WebSearch:
		var input struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(entry.Input, &input)
		if ws.Backend == nil {
			isErr = true
			resultText = "web_search backend not configured"
			break
		}
		results, err := ws.Backend.Search(ctx, input.Query, 5)
		if err != nil {
			isErr = true
			resultText = fmt.Sprintf("web_search error: %v", err)
			break
		}
		buf, _ := json.Marshal(results)
		resultText = string(buf)
	case anthropic.ClientToolName_WebFetch:
		var input struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(entry.Input, &input)
		fr, err := websearch.Fetch(ctx, input.URL, ws.FetchBudget, ws.Robots)
		if err != nil {
			isErr = true
			resultText = fmt.Sprintf("web_fetch error: %v", err)
			break
		}
		buf, _ := json.Marshal(fr)
		resultText = string(buf)
	default:
		isErr = true
		resultText = "unknown annotated tool"
	}

	out := map[string]any{
		"type":        "tool_result",
		"tool_use_id": toolUseID,
		"content":     []map[string]string{{"type": "text", "text": resultText}},
	}
	if isErr {
		out["is_error"] = true
	}
	return json.Marshal(out)
}
