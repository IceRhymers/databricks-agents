package proxy

import (
	"errors"
	"fmt"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core/proxy/anthropic"
)

// AdvisorSettings bundles the optional --with-advisor knobs. Embedded in
// Config; when Enabled is false advisor tools are stripped from forwarded
// requests and the proxy's behavior is byte-identical to before.
type AdvisorSettings struct {
	Enabled bool
	// DefaultModel is the advisor model used when the tool declaration
	// omits "model". Empty = same model as the executor request.
	DefaultModel string
	// DefaultMaxTokens caps advisor output when the tool declaration omits
	// "max_tokens". Applied with a floor of advisorMinMaxTokens.
	DefaultMaxTokens int
}

// Compile-time constants (spec §10).
const (
	// maxAdvisorTurns bounds the server loop / streaming interleave's
	// continuation rounds. A model that calls the advisor every turn must
	// not spin the gateway indefinitely.
	maxAdvisorTurns = 4
	// advisorTimeout bounds each advisor sub-inference.
	advisorTimeout = 60 * time.Second
	// sseAdvisorPingInterval is the SSE keepalive cadence while the blocking
	// advisor sub-inference runs mid-stream.
	sseAdvisorPingInterval = 15 * time.Second
	// advisorMinMaxTokens floors the per-call max_tokens budget.
	advisorMinMaxTokens = 1024
	// advisorDefaultMaxTokens is the default output cap when neither the
	// tool declaration nor AdvisorSettings.DefaultMaxTokens specifies one.
	advisorDefaultMaxTokens = 2048
)

// Typed advisor errors (spec §7.3). Mapped to error_code strings in the
// client-facing advisor_tool_result_error block.
var (
	ErrTooManyRequests       = errors.New("advisor: too many requests")
	ErrOverloaded            = errors.New("advisor: overloaded")
	ErrPromptTooLong         = errors.New("advisor: prompt too long")
	ErrExecutionTimeExceeded = errors.New("advisor: execution time exceeded")
	ErrUnavailable           = errors.New("advisor: unavailable")
)

// advisorErrorCode maps a typed advisor error to its wire error_code string.
// Unknown errors map to "unavailable" (fail-open).
func advisorErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrTooManyRequests):
		return "too_many_requests"
	case errors.Is(err, ErrOverloaded):
		return "overloaded"
	case errors.Is(err, ErrPromptTooLong):
		return "prompt_too_long"
	case errors.Is(err, ErrExecutionTimeExceeded):
		return "execution_time_exceeded"
	default:
		return "unavailable"
	}
}

// resolvedAdvisorCall is the per-invocation plan: which model, which token
// cap, and whether the client explicitly set the cap (controls whether the
// success content echoes stop_reason — spec §3.2).
type resolvedAdvisorCall struct {
	Model        string
	MaxTokens    int
	MaxTokensSet bool
}

// resolveAdvisorCall applies the default model / max_tokens (floor 1024,
// default 2048) chain against the tool-declared constraints, the configured
// defaults, and the executor's own model.
func resolveAdvisorCall(c anthropic.AdvisorConstraints, s AdvisorSettings, executorModel string) resolvedAdvisorCall {
	model := c.Model
	if model == "" {
		model = s.DefaultModel
	}
	if model == "" {
		model = executorModel
	}
	maxTokens := c.MaxTokens
	maxTokensSet := c.MaxTokensSet
	if maxTokens == 0 {
		maxTokens = s.DefaultMaxTokens
	}
	if maxTokens == 0 {
		maxTokens = advisorDefaultMaxTokens
	}
	if maxTokens < advisorMinMaxTokens {
		maxTokens = advisorMinMaxTokens
	}
	return resolvedAdvisorCall{Model: model, MaxTokens: maxTokens, MaxTokensSet: maxTokensSet}
}

// mintAdvisorServerID ensures the client-facing server_tool_use id carries
// the srvtoolu_ prefix. Mirrors the websearch handler's convention.
func mintAdvisorServerID(toolUseID string) string {
	if len(toolUseID) >= 9 && toolUseID[:9] == "srvtoolu_" {
		return toolUseID
	}
	return "srvtoolu_" + toolUseID
}

// advisorUnavailableText formats the continuation-side tool_result body when
// the advisor fails. code may be empty.
func advisorUnavailableText(code string) string {
	if code == "" {
		return "[Advisor unavailable]"
	}
	return fmt.Sprintf("[Advisor unavailable: %s]", code)
}
