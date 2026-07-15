// Package core is the module-private home of the shared, tool-agnostic engine
// that every launcher runs: the reverse proxy, OAuth token cache, auth
// pre-flight, child-process control, state persistence, session lifecycle,
// port binding, health endpoint, refcount, updater, completion, and CLI
// helpers. These packages were promoted from the public pkg/ surface into
// internal/core in #198 (no behavior change); the internal/ boundary makes
// "this is the engine, not a launcher" compiler-enforced.
//
// Deliberately NOT in core (Claude/Anthropic-coupled — they move later with
// the claude Profile in #E): pkg/modeldiscovery (Opus/Sonnet/Haiku discovery +
// the anthropic/v1/messages predicate), pkg/mdmprofile (Desktop MDM/trust), and
// pkg/websearch (Claude's web-search backends). Note that internal/core/proxy
// still imports pkg/websearch and still houses the anthropic wire bits
// (sse_rewriter.go, websearch_handler.go, responses_rewriter.go, anthropic/) —
// a documented, temporary internal/core -> pkg back-edge that #E resolves when
// those bits move out with the claude Profile.
package core
