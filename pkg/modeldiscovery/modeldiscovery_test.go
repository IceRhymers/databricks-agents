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

// --- test helpers ------------------------------------------------------------

// svc builds a Service with the given FQN, api types, and destination FQNs
// (each parsed via parseDestination).
func svc(fqn string, apiTypes []string, destFQNs ...string) Service {
	s := Service{
		FQN:               fqn,
		Catalog:           catalogOf(fqn),
		SupportedAPITypes: apiTypes,
	}
	for _, d := range destFQNs {
		s.Destinations = append(s.Destinations, parseDestination(d))
	}
	return s
}

var messages = []string{anthropicMessagesAPIType}

// listHandler serves a single-page LIST response for the given wire services.
func writeList(t *testing.T, w http.ResponseWriter, services []wireService, nextToken string) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(wireListResponse{
		ModelServices: services,
		NextPageToken: nextToken,
	}); err != nil {
		t.Fatalf("encode list: %v", err)
	}
}

// wsvc builds a wire service in the REAL API shape: the resource name carries
// the "model-services/" prefix and routing lives under `config`.
func wsvc(fqn string, apiTypes []string, destFQNs ...string) wireService {
	w := wireService{Name: resourcePrefix + fqn, SupportedAPITypes: apiTypes}
	for _, d := range destFQNs {
		w.Config.Routing.Destinations = append(w.Config.Routing.Destinations, wireDest{Name: d})
	}
	return w
}

// --- parseDestination --------------------------------------------------------

func TestParseDestination(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		family string
		major  int
		minor  int
		parsed bool
	}{
		{"opus", "system.ai.databricks-claude-opus-4-8", "opus", 4, 8, true},
		{"sonnet", "system.ai.databricks-claude-sonnet-4-6", "sonnet", 4, 6, true},
		{"double-digit-minor", "x-claude-opus-4-10", "opus", 4, 10, true},
		// Major-only versions are REAL: system.ai.databricks-claude-sonnet-5 and
		// ...-claude-sonnet-4 both exist and serve traffic. An absent minor is .0.
		{"major-only", "system.ai.databricks-claude-opus-4", "opus", 4, 0, true},
		{"major-only-sonnet-5", "system.ai.databricks-claude-sonnet-5", "sonnet", 5, 0, true},
		{"garbage-suffix", "system.ai.databricks-claude-opusX", "", 0, 0, false},
		{"unknown-family", "system.ai.databricks-claude-turbo-4-8", "", 0, 0, false},
		// "claude-" must begin a name segment: an unrelated substring like
		// "notclaude-opus-2025-01" must NOT classify as opus 2025.1 (which would
		// otherwise dominate the version sort and mis-route the family).
		{"not-a-segment-boundary", "myorg.custom.notclaude-opus-2025-01", "", 0, 0, false},
		{"empty", "", "", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := parseDestination(c.in)
			if d.Parsed != c.parsed || d.Family != c.family || d.Major != c.major || d.Minor != c.minor {
				t.Fatalf("parseDestination(%q) = %+v; want family=%q major=%d minor=%d parsed=%v",
					c.in, d, c.family, c.major, c.minor, c.parsed)
			}
			if d.FQN != c.in {
				t.Fatalf("FQN not preserved: got %q want %q", d.FQN, c.in)
			}
		})
	}
}

// --- Resolve (pure, table-driven, no server) ---------------------------------

func TestResolve(t *testing.T) {
	t.Run("(a) zero services -> all unresolved", func(t *testing.T) {
		set, un := Resolve(nil, Pins{})
		if set.Opus.FQN != "" || set.Sonnet.FQN != "" || set.Haiku.FQN != "" {
			t.Fatalf("expected empty ModelSet, got %+v", set)
		}
		assertUnresolved(t, un, "opus", "sonnet", "haiku")
	})

	t.Run("(b) user-catalog-only opus; sonnet/haiku unresolved", func(t *testing.T) {
		services := []Service{
			svc("workspace.default.acme-claude-opus", messages, "system.ai.databricks-claude-opus-4-8"),
		}
		set, un := Resolve(services, Pins{})
		// opus-4-8 -> OneM, suffix appended.
		if set.Opus.FQN != "workspace.default.acme-claude-opus"+oneMSuffix || !set.Opus.OneM {
			t.Fatalf("opus = %+v", set.Opus)
		}
		assertUnresolved(t, un, "sonnet", "haiku")
	})

	t.Run("(c) mixed-family service is ambiguous -> excluded", func(t *testing.T) {
		services := []Service{
			svc("workspace.default.mixed", messages,
				"system.ai.databricks-claude-opus-4-8",
				"system.ai.databricks-claude-sonnet-4-6"),
		}
		set, un := Resolve(services, Pins{})
		if set.Opus.FQN != "" || set.Sonnet.FQN != "" {
			t.Fatalf("ambiguous service must not resolve: %+v", set)
		}
		assertUnresolved(t, un, "opus", "sonnet", "haiku")
	})

	t.Run("numeric sort picks 4-10 over 4-8", func(t *testing.T) {
		services := []Service{
			svc("workspace.default.opus-old", messages, "system.ai.databricks-claude-opus-4-8"),
			svc("workspace.default.opus-new", messages, "system.ai.databricks-claude-opus-4-10"),
		}
		set, _ := Resolve(services, Pins{})
		if !strings.HasPrefix(set.Opus.FQN, "workspace.default.opus-new") {
			t.Fatalf("expected opus-new (4-10), got %q", set.Opus.FQN)
		}
	})

	t.Run("unparseable destination -> service excluded, no panic", func(t *testing.T) {
		// NOTE: "...-claude-opus-4" is NOT unparseable — major-only versions are
		// real (claude-sonnet-5). Use destinations with no version at all.
		services := []Service{
			svc("workspace.default.broken", messages, "system.ai.databricks-claude-opus"),
			svc("workspace.default.brokenX", messages, "system.ai.databricks-claude-opusX"),
		}
		set, un := Resolve(services, Pins{})
		if set.Opus.FQN != "" {
			t.Fatalf("unparseable dest must not resolve: %+v", set.Opus)
		}
		assertUnresolved(t, un, "opus", "sonnet", "haiku")
	})

	t.Run("1M predicate boundaries", func(t *testing.T) {
		cases := []struct {
			family string
			dest   string
			oneM   bool
		}{
			{"opus", "x-claude-opus-4-5", false},
			{"opus", "x-claude-opus-4-6", true},
			{"sonnet", "x-claude-sonnet-4-6", true},
			{"haiku", "x-claude-haiku-4-5", false},
			{"haiku", "x-claude-haiku-9-9", false},
		}
		for _, c := range cases {
			t.Run(c.family+"-"+c.dest, func(t *testing.T) {
				services := []Service{svc("workspace.default.svc", messages, c.dest)}
				set, _ := Resolve(services, Pins{})
				var got Resolved
				switch c.family {
				case "opus":
					got = set.Opus
				case "sonnet":
					got = set.Sonnet
				case "haiku":
					got = set.Haiku
				}
				if got.OneM != c.oneM {
					t.Fatalf("%s %s OneM = %v, want %v", c.family, c.dest, got.OneM, c.oneM)
				}
				if c.oneM && !strings.HasSuffix(got.FQN, oneMSuffix) {
					t.Fatalf("expected [1m] suffix on %q", got.FQN)
				}
				if !c.oneM && strings.HasSuffix(got.FQN, oneMSuffix) {
					t.Fatalf("did not expect [1m] suffix on %q", got.FQN)
				}
			})
		}
	})

	t.Run("pin precedence: opus verbatim, others resolve", func(t *testing.T) {
		services := []Service{
			svc("workspace.default.sonnet", messages, "system.ai.databricks-claude-sonnet-4-6"),
			svc("workspace.default.haiku", messages, "system.ai.databricks-claude-haiku-3-5"),
		}
		set, un := Resolve(services, Pins{Opus: "my.custom.opus"})
		if set.Opus.FQN != "my.custom.opus" || set.Opus.OneM {
			t.Fatalf("pin must be verbatim without [1m]: %+v", set.Opus)
		}
		if !strings.HasPrefix(set.Sonnet.FQN, "workspace.default.sonnet") {
			t.Fatalf("sonnet should resolve: %+v", set.Sonnet)
		}
		if set.Haiku.FQN != "workspace.default.haiku" {
			t.Fatalf("haiku should resolve without suffix: %+v", set.Haiku)
		}
		if len(un) != 0 {
			t.Fatalf("expected no unresolved, got %+v", un)
		}
	})

	t.Run("prefer non-system.ai at same version", func(t *testing.T) {
		services := []Service{
			svc("system.ai.claude-opus", messages, "system.ai.databricks-claude-opus-4-8"),
			svc("workspace.governed.opus", messages, "system.ai.databricks-claude-opus-4-8"),
		}
		set, _ := Resolve(services, Pins{})
		if !strings.HasPrefix(set.Opus.FQN, "workspace.governed.opus") {
			t.Fatalf("expected workspace opus to win, got %q", set.Opus.FQN)
		}
	})

	t.Run("service without messages api-type excluded", func(t *testing.T) {
		services := []Service{
			svc("workspace.default.opus", []string{"openai/v1/chat"}, "system.ai.databricks-claude-opus-4-8"),
		}
		set, _ := Resolve(services, Pins{})
		if set.Opus.FQN != "" {
			t.Fatalf("non-messages service must not resolve: %+v", set.Opus)
		}
	})
}

// 1M suffix contract: the annotation is pure client-side; stripping it yields
// the wire FQN that a gateway would receive.
func TestOneMSuffixIsClientSideOnly(t *testing.T) {
	services := []Service{
		svc("workspace.default.acme-claude-opus", messages, "system.ai.databricks-claude-opus-4-8"),
	}
	set, _ := Resolve(services, Pins{})
	if !strings.HasSuffix(set.Opus.FQN, oneMSuffix) {
		t.Fatalf("expected [1m] suffix, got %q", set.Opus.FQN)
	}
	wire := strings.TrimSuffix(set.Opus.FQN, oneMSuffix)
	if wire != "workspace.default.acme-claude-opus" {
		t.Fatalf("stripped wire FQN = %q; want the raw service FQN with no annotation", wire)
	}
}

func assertUnresolved(t *testing.T, un []Unresolved, families ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, u := range un {
		got[u.Family] = true
		if u.PinCommand == "" {
			t.Errorf("family %q unresolved with empty PinCommand", u.Family)
		}
	}
	for _, f := range families {
		if !got[f] {
			t.Errorf("expected family %q to be unresolved; got %+v", f, un)
		}
	}
	if len(un) != len(families) {
		t.Errorf("unresolved count = %d, want %d (%+v)", len(un), len(families), un)
	}
}

// --- ListServices ------------------------------------------------------------

func TestListServicesPagination(t *testing.T) {
	var page int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, modelServicesPath) {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		token := r.URL.Query().Get("page_token")
		if token == "" {
			atomic.AddInt32(&page, 1)
			writeList(t, w, []wireService{
				wsvc("workspace.default.opus", messages, "system.ai.databricks-claude-opus-4-8"),
			}, "next")
			return
		}
		if token == "next" {
			atomic.AddInt32(&page, 1)
			writeList(t, w, []wireService{
				wsvc("workspace.default.sonnet", messages, "system.ai.databricks-claude-sonnet-4-6"),
			}, "")
			return
		}
		t.Errorf("unexpected page_token %q", token)
	}))
	defer srv.Close()

	services, err := ListServices(context.Background(), srv.Client(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("expected 2 merged services across pages, got %d: %+v", len(services), services)
	}
	if atomic.LoadInt32(&page) != 2 {
		t.Fatalf("expected 2 page fetches, got %d", page)
	}
}

// TestListServicesRepeatedPageTokenAborts verifies the pagination loop refuses
// a gateway that returns the same non-empty next_page_token forever, instead of
// looping unboundedly until OOM.
func TestListServicesRepeatedPageTokenAborts(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Always return the same non-empty token — a never-terminating stream.
		writeList(t, w, []wireService{
			wsvc("workspace.default.opus", messages, "system.ai.databricks-claude-opus-4-8"),
		}, "loop")
	}))
	defer srv.Close()

	_, err := ListServices(context.Background(), srv.Client(), srv.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "repeated page_token") {
		t.Fatalf("expected repeated page_token error, got %v", err)
	}
	// Must abort quickly (page 1 returns "loop", page 2 repeats it -> abort),
	// not spin thousands of times.
	if h := atomic.LoadInt32(&hits); h > 3 {
		t.Fatalf("expected abort within a few requests, got %d", h)
	}
}

func TestListServicesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := ListServices(context.Background(), srv.Client(), srv.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected status-bearing error, got %v", err)
	}
}

// --- GetService --------------------------------------------------------------

func TestGetService403IsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := GetService(context.Background(), srv.Client(), srv.URL, "tok", "workspace.default.x")
	if err == nil {
		t.Fatal("expected error")
	}
	if !isForbidden(err) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func isForbidden(err error) bool {
	return err != nil && strings.Contains(err.Error(), "forbidden")
}

func TestGetServiceParsesSingleObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(wsvc(
			"workspace.default.acme", messages, "system.ai.databricks-claude-opus-4-8")); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}))
	defer srv.Close()

	got, err := GetService(context.Background(), srv.Client(), srv.URL, "tok", "workspace.default.acme")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if got.FQN != "workspace.default.acme" || got.Catalog != "workspace" {
		t.Fatalf("unexpected service: %+v", got)
	}
	if len(got.Destinations) != 1 || got.Destinations[0].Family != "opus" {
		t.Fatalf("unexpected destinations: %+v", got.Destinations)
	}
	if !got.supportsMessages() {
		t.Fatalf("expected messages support")
	}
}

// --- Discover ----------------------------------------------------------------

// discoverServer wires a LIST handler plus a GET handler with a call counter.
func discoverServer(t *testing.T, list []wireService, gets map[string]http.HandlerFunc, getCount *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Individual GET has a path segment after the collection path.
		rest := strings.TrimPrefix(r.URL.Path, modelServicesPath)
		if rest == "" || rest == "/" {
			writeList(t, w, list, "")
			return
		}
		fqn := strings.TrimPrefix(rest, "/")
		if getCount != nil {
			atomic.AddInt32(getCount, 1)
		}
		if h, ok := gets[fqn]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected GET for %q", fqn)
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestDiscoverEnrichesOnlyWhenApiTypesAbsent(t *testing.T) {
	var getCount int32
	list := []wireService{
		// Absent api types -> must trigger exactly one GET.
		wsvc("workspace.default.opus", nil, "system.ai.databricks-claude-opus-4-8"),
		// Present api types -> must NOT trigger a GET.
		wsvc("system.ai.claude-sonnet", messages, "system.ai.databricks-claude-sonnet-4-6"),
	}
	gets := map[string]http.HandlerFunc{
		"workspace.default.opus": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(wsvc(
				"workspace.default.opus", messages, "system.ai.databricks-claude-opus-4-8"))
		},
	}
	srv := discoverServer(t, list, gets, &getCount)
	defer srv.Close()

	set, un, err := Discover(context.Background(), srv.Client(), srv.URL, "tok", Pins{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if atomic.LoadInt32(&getCount) != 1 {
		t.Fatalf("expected exactly 1 GetService call, got %d", getCount)
	}
	if !strings.HasPrefix(set.Opus.FQN, "workspace.default.opus") {
		t.Fatalf("opus should resolve from enrichment: %+v", set.Opus)
	}
	if !strings.HasPrefix(set.Sonnet.FQN, "system.ai.claude-sonnet") {
		t.Fatalf("sonnet should resolve from list: %+v", set.Sonnet)
	}
	assertUnresolved(t, un, "haiku")
}

func TestDiscoverForbiddenGetIsSkipped(t *testing.T) {
	var getCount int32
	list := []wireService{
		wsvc("workspace.default.secret", nil, "system.ai.databricks-claude-opus-4-8"),
		wsvc("system.ai.claude-sonnet", messages, "system.ai.databricks-claude-sonnet-4-6"),
	}
	gets := map[string]http.HandlerFunc{
		"workspace.default.secret": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
	}
	srv := discoverServer(t, list, gets, &getCount)
	defer srv.Close()

	set, un, err := Discover(context.Background(), srv.Client(), srv.URL, "tok", Pins{})
	if err != nil {
		t.Fatalf("Discover must not hard-fail on 403: %v", err)
	}
	if set.Opus.FQN != "" {
		t.Fatalf("forbidden service must be skipped, opus should be unresolved: %+v", set.Opus)
	}
	if !strings.HasPrefix(set.Sonnet.FQN, "system.ai.claude-sonnet") {
		t.Fatalf("sonnet should still resolve: %+v", set.Sonnet)
	}
	assertUnresolved(t, un, "opus", "haiku")
}

func TestDiscoverPropagatesNonForbiddenGetError(t *testing.T) {
	list := []wireService{
		wsvc("workspace.default.boom", nil, "system.ai.databricks-claude-opus-4-8"),
	}
	gets := map[string]http.HandlerFunc{
		"workspace.default.boom": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	}
	srv := discoverServer(t, list, gets, nil)
	defer srv.Close()

	_, _, err := Discover(context.Background(), srv.Client(), srv.URL, "tok", Pins{})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error propagated, got %v", err)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	srv := discoverServer(t, nil, nil, nil)
	defer srv.Close()

	set, un, err := Discover(context.Background(), srv.Client(), srv.URL, "tok", Pins{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if set.Opus.FQN != "" || set.Sonnet.FQN != "" || set.Haiku.FQN != "" {
		t.Fatalf("expected empty ModelSet: %+v", set)
	}
	assertUnresolved(t, un, "opus", "sonnet", "haiku")
}
