package modeldiscovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The fixtures below are trimmed VERBATIM captures from a live Databricks
// workspace (Unity AI Gateway v2). They exist because hand-written fixtures
// encoded a wire shape the API does not actually use, so the whole suite stayed
// green against a client that could not parse a single real response:
//
//   - the resource name arrives as `name`, not `full_name`, and carries a
//     "model-services/" prefix that the gateway rejects as a model id;
//   - routing lives under `config.routing`, not a top-level `routing`;
//   - LIST omits routing.destinations for EVERY service (including system.ai),
//     and omits supported_api_types for user-deployed services.
//
// Any change to the parsing structs must keep these passing.

const realListPage = `{"model_services":[
 {"name":"model-services/workspace.default.twendland-claude-opus-4-8",
  "securable_type":"MODEL_SERVICE",
  "config":{"usage_tracking":{"enabled":true},"routing":{}},
  "etag":"CAESCAAAAZ9CzmnH"},
 {"name":"model-services/system.ai.claude-opus-4-8",
  "securable_type":"MODEL_SERVICE",
  "config":{"usage_tracking":{"enabled":true},"routing":{}},
  "supported_api_types":["mlflow/v1/chat/completions","anthropic/v1/messages","cursor/v1/chat/completions"]},
 {"name":"model-services/system.ai.claude-opus-4-7",
  "securable_type":"MODEL_SERVICE",
  "config":{"routing":{}},
  "supported_api_types":["mlflow/v1/chat/completions","anthropic/v1/messages"]},
 {"name":"model-services/system.ai.claude-haiku-4-5",
  "securable_type":"MODEL_SERVICE",
  "config":{"routing":{}},
  "supported_api_types":["mlflow/v1/chat/completions","anthropic/v1/messages"]},
 {"name":"model-services/system.ai.qwen3-next-80b-a3b-instruct",
  "securable_type":"MODEL_SERVICE",
  "config":{"routing":{}},
  "supported_api_types":["mlflow/v1/chat/completions"]}
]}`

// realGet maps an FQN to its verbatim individual-GET response.
var realGet = map[string]string{
	"workspace.default.twendland-claude-opus-4-8": `{
 "name":"model-services/workspace.default.twendland-claude-opus-4-8",
 "config":{"routing":{"destinations":[
   {"name":"system.ai.databricks-claude-opus-4-8","type":"DESTINATION_TYPE_PAY_PER_TOKEN_FOUNDATION_MODEL",
    "traffic_percentage":100,"pay_per_token_config":{"model":"models/system.ai.databricks-claude-opus-4-8"},
    "is_deleted":false}]}},
 "supported_api_types":["mlflow/v1/chat/completions","anthropic/v1/messages","cursor/v1/chat/completions"]}`,

	"system.ai.claude-opus-4-8": `{
 "name":"model-services/system.ai.claude-opus-4-8",
 "config":{"routing":{"destinations":[
   {"name":"system.ai.databricks-claude-opus-4-8","traffic_percentage":100,"is_deleted":false}]}},
 "supported_api_types":["mlflow/v1/chat/completions","anthropic/v1/messages"]}`,

	"system.ai.claude-opus-4-7": `{
 "name":"model-services/system.ai.claude-opus-4-7",
 "config":{"routing":{"destinations":[
   {"name":"system.ai.databricks-claude-opus-4-7","traffic_percentage":100,"is_deleted":false}]}},
 "supported_api_types":["mlflow/v1/chat/completions","anthropic/v1/messages"]}`,

	"system.ai.claude-haiku-4-5": `{
 "name":"model-services/system.ai.claude-haiku-4-5",
 "config":{"routing":{"destinations":[
   {"name":"system.ai.databricks-claude-haiku-4-5","traffic_percentage":100,"is_deleted":false}]}},
 "supported_api_types":["mlflow/v1/chat/completions","anthropic/v1/messages"]}`,
}

// realServer serves the captured LIST/GET responses and counts GET calls.
func realServer(t *testing.T, gets *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		if path == modelServicesPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(realListPage))
			return
		}
		fqn := strings.TrimPrefix(path, modelServicesPath+"/")
		atomic.AddInt32(gets, 1)
		body, ok := realGet[fqn]
		if !ok {
			t.Errorf("unexpected GetService for %q", fqn)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// TestListServicesParsesRealWireShape pins the `name` + "model-services/" prefix
// and `config.routing` decoding against a captured response.
func TestListServicesParsesRealWireShape(t *testing.T) {
	var gets int32
	srv := realServer(t, &gets)
	defer srv.Close()

	services, err := ListServices(context.Background(), srv.Client(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(services) != 5 {
		t.Fatalf("got %d services, want 5", len(services))
	}
	// FQN must be the bare UC name — the gateway 400s on the prefixed form.
	if got := services[0].FQN; got != "workspace.default.twendland-claude-opus-4-8" {
		t.Errorf("FQN = %q, want the model-services/ prefix stripped", got)
	}
	if got := services[0].Catalog; got != "workspace" {
		t.Errorf("Catalog = %q, want %q", got, "workspace")
	}
	// The real LIST carries destinations for NO service.
	for _, s := range services {
		if len(s.Destinations) != 0 {
			t.Errorf("service %q: LIST unexpectedly carried destinations", s.FQN)
		}
	}
	// api_types absent for the user-catalog service, present for system.ai.
	if len(services[0].SupportedAPITypes) != 0 {
		t.Errorf("user-catalog service should have no api_types in LIST")
	}
	if !services[1].supportsMessages() {
		t.Errorf("system.ai service should advertise anthropic/v1/messages in LIST")
	}
}

// TestDiscoverResolvesFromRealWireShape is the end-to-end regression for the bug
// that made discovery return opus-4-7 (the offline fallback) on a workspace
// where opus-4-8 exists: destinations are absent from LIST, so every candidate
// must be enriched via an individual GET before family classification.
func TestDiscoverResolvesFromRealWireShape(t *testing.T) {
	var gets int32
	srv := realServer(t, &gets)
	defer srv.Close()

	set, unresolved, err := Discover(context.Background(), srv.Client(), srv.URL, "tok", Pins{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// The user-catalog opus wins over system.ai at the same version, and 4.8 is
	// 1M-eligible so the emitted FQN carries the client-side [1m] annotation.
	wantOpus := "workspace.default.twendland-claude-opus-4-8" + oneMSuffix
	if set.Opus.FQN != wantOpus {
		t.Errorf("Opus = %q, want %q", set.Opus.FQN, wantOpus)
	}
	if set.Haiku.FQN != "system.ai.claude-haiku-4-5" {
		t.Errorf("Haiku = %q, want system.ai.claude-haiku-4-5", set.Haiku.FQN)
	}
	if set.Haiku.OneM {
		t.Error("haiku must never be 1M-eligible")
	}
	// No sonnet service in this capture.
	if len(unresolved) != 1 || unresolved[0].Family != "sonnet" {
		t.Errorf("unresolved = %+v, want exactly [sonnet]", unresolved)
	}

	// Exactly the 4 anthropic-capable/unknown services are enriched; the qwen
	// service has a known non-anthropic capability and must not cost a GET.
	if n := atomic.LoadInt32(&gets); n != 4 {
		t.Errorf("GetService called %d times, want 4 (skip known-non-anthropic)", n)
	}
}

// TestResolveMajorOnlyVersionBeatsOlderMinor pins the second real-world bug:
// system.ai.claude-sonnet-5 is live and is the NEWEST sonnet, but a parser that
// requires major-minor drops it, silently resolving the older sonnet-4-6.
func TestResolveMajorOnlyVersionBeatsOlderMinor(t *testing.T) {
	services := []Service{
		svc("system.ai.claude-sonnet-4-6", messages, "system.ai.databricks-claude-sonnet-4-6"),
		svc("system.ai.claude-sonnet-5", messages, "system.ai.databricks-claude-sonnet-5"),
		svc("system.ai.claude-sonnet-4", messages, "system.ai.databricks-claude-sonnet-4"),
	}
	set, _ := Resolve(services, Pins{})
	want := "system.ai.claude-sonnet-5" + oneMSuffix
	if set.Sonnet.FQN != want {
		t.Errorf("Sonnet = %q, want %q (5.0 > 4.6 > 4.0)", set.Sonnet.FQN, want)
	}
}

// TestToServiceIgnoresDeletedDestinations ensures a retired destination cannot
// make a service look ambiguous or pin it to an old version.
func TestToServiceIgnoresDeletedDestinations(t *testing.T) {
	var w wireService
	raw := `{"name":"model-services/a.b.c","config":{"routing":{"destinations":[
	  {"name":"system.ai.databricks-claude-sonnet-4-5","is_deleted":true},
	  {"name":"system.ai.databricks-claude-opus-4-8","is_deleted":false}]}},
	 "supported_api_types":["anthropic/v1/messages"]}`
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	svc := w.toService()
	if len(svc.Destinations) != 1 || svc.Destinations[0].Family != "opus" {
		t.Fatalf("destinations = %+v, want only the live opus dest", svc.Destinations)
	}
	family, major, minor, ok := svc.familyAndNewest()
	if !ok || family != "opus" || major != 4 || minor != 8 {
		t.Errorf("familyAndNewest = (%q,%d,%d,%v), want (opus,4,8,true)", family, major, minor, ok)
	}
}
