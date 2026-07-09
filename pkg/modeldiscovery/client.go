package modeldiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// ErrForbidden is returned (wrapped) by GetService when the Unity Catalog API
// responds with 403. The orchestrator uses it to skip a service rather than
// hard-fail, hedging the assumption that the LIST endpoint is filtered by the
// caller's EXECUTE grants.
var ErrForbidden = errors.New("forbidden")

// modelServicesPath is the Unity Catalog model-services collection endpoint.
const modelServicesPath = "/api/2.1/unity-catalog/model-services"

// listPageSize is the page_size requested when listing model-services. It is
// cheap insurance against a low default; pagination is still followed.
const listPageSize = 200

// defaultTimeout bounds a single discovery HTTP request. Discovery runs only at
// config/desktop/doctor time, never on the launch hot path, so a generous but
// finite ceiling is right: it prevents a hung gateway from blocking those
// commands forever (context.Background carries no deadline of its own).
const defaultTimeout = 30 * time.Second

// maxResponseBody caps how much of a model-services response body is buffered
// before JSON decoding. Model-services lists are small; the cap defends against
// a compromised or misbehaving gateway returning an unbounded body.
const maxResponseBody = 8 << 20 // 8 MiB

// NewClient returns the HTTP client discovery callers should use. It bounds each
// request with a timeout and refuses cross-host redirects so the OAuth bearer
// token can never be replayed against an attacker-chosen host (Go already
// strips the Authorization header on a cross-host redirect, but refusing the
// redirect outright also blocks the SSRF probe). Prefer this over
// http.DefaultClient, which has no timeout and follows redirects.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: defaultTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("model-services: refusing cross-host redirect to %s", req.URL.Host)
			}
			return nil
		},
	}
}

// resourcePrefix is the collection prefix the API stamps on every
// model-service resource name ("model-services/system.ai.claude-opus-4-8").
// It is NOT part of the Unity Catalog name: the gateway rejects a model id that
// carries it ("Invalid Unity Catalog name"), so it is stripped on the way in.
const resourcePrefix = "model-services/"

// wireDest mirrors a single routing destination entry on the wire.
type wireDest struct {
	Name      string `json:"name"`
	IsDeleted bool   `json:"is_deleted"`
}

// wireService mirrors a model-service object on the wire, used for both LIST
// entries and the individual GET response.
//
// The resource name arrives as `name` ("model-services/{catalog}.{schema}.{svc}").
// `full_name` is accepted as a fallback for forward/backward compatibility.
type wireService struct {
	Name              string     `json:"name"`
	FullName          string     `json:"full_name"`
	SupportedAPITypes []string   `json:"supported_api_types"`
	Config            wireConfig `json:"config"`
}

// wireConfig mirrors the config block; routing lives under it.
type wireConfig struct {
	Routing wireRouting `json:"routing"`
}

// wireRouting mirrors the routing block of a model-service config.
type wireRouting struct {
	Destinations []wireDest `json:"destinations"`
}

// wireListResponse mirrors the LIST response envelope.
type wireListResponse struct {
	ModelServices []wireService `json:"model_services"`
	NextPageToken string        `json:"next_page_token"`
}

// fqn returns the Unity Catalog name of the service, with the API's
// "model-services/" resource prefix stripped.
func (w wireService) fqn() string {
	name := w.Name
	if name == "" {
		name = w.FullName
	}
	return strings.TrimPrefix(name, resourcePrefix)
}

// toService converts a wire representation into the exported Service, computing
// the catalog and parsing every live routing destination. Deleted destinations
// are ignored — they no longer describe where traffic goes, so letting one
// participate in family classification could make a service look ambiguous or
// pin it to a retired version.
func (w wireService) toService() Service {
	fqn := w.fqn()
	svc := Service{
		FQN:               fqn,
		Catalog:           catalogOf(fqn),
		SupportedAPITypes: w.SupportedAPITypes,
	}
	for _, d := range w.Config.Routing.Destinations {
		if d.IsDeleted {
			continue
		}
		svc.Destinations = append(svc.Destinations, parseDestination(d.Name))
	}
	return svc
}

// catalogOf returns the first dot-segment of a fully-qualified name.
func catalogOf(fqn string) string {
	if i := strings.IndexByte(fqn, '.'); i >= 0 {
		return fqn[:i]
	}
	return fqn
}

// ListServices returns every model-service visible to the caller, following
// next_page_token pagination. Non-2xx responses yield an error that includes the
// status code. An empty result is not itself an error.
func ListServices(ctx context.Context, client *http.Client, host, token string) ([]Service, error) {
	var services []Service
	pageToken := ""
	// seen guards against a misbehaving/compromised gateway that returns the
	// same non-empty next_page_token forever, which would otherwise loop
	// unboundedly and grow services until OOM. maxPages is a belt-and-suspenders
	// hard cap (model-services lists are small; 1000 pages is far past any real
	// metastore).
	seen := map[string]bool{}
	const maxPages = 1000
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return nil, fmt.Errorf("model-services: pagination exceeded %d pages; aborting", maxPages)
		}
		url := fmt.Sprintf("%s%s?page_size=%d", strings.TrimRight(host, "/"), modelServicesPath, listPageSize)
		if pageToken != "" {
			// pageToken is echoed from the gateway response — escape it so a
			// crafted value cannot inject extra query parameters.
			url += "&page_token=" + neturl.QueryEscape(pageToken)
		}

		body, err := doGet(ctx, client, url, token)
		if err != nil {
			return nil, err
		}

		var resp wireListResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("model-services: decode list response: %w", err)
		}
		for _, w := range resp.ModelServices {
			services = append(services, w.toService())
		}
		if resp.NextPageToken == "" {
			break
		}
		if seen[resp.NextPageToken] {
			return nil, fmt.Errorf("model-services: gateway repeated page_token %q; aborting to avoid an infinite loop", resp.NextPageToken)
		}
		seen[resp.NextPageToken] = true
		pageToken = resp.NextPageToken
	}
	return services, nil
}

// GetService fetches a single model-service by FQN. A 403 response is returned
// wrapped around ErrForbidden so callers can skip the service; other non-2xx
// responses yield an error including the status code.
func GetService(ctx context.Context, client *http.Client, host, token, fqn string) (Service, error) {
	// An empty FQN would request the collection endpoint with a trailing slash
	// and 404. Fail with a name rather than a confusing status code.
	if fqn == "" {
		return Service{}, fmt.Errorf("model-services: GetService requires a non-empty fqn")
	}
	// fqn comes verbatim from the LIST response — escape it as a single path
	// segment so a crafted service name cannot traverse to another API path.
	url := fmt.Sprintf("%s%s/%s", strings.TrimRight(host, "/"), modelServicesPath, neturl.PathEscape(fqn))
	body, err := doGet(ctx, client, url, token)
	if err != nil {
		return Service{}, err
	}

	var w wireService
	if err := json.Unmarshal(body, &w); err != nil {
		return Service{}, fmt.Errorf("model-services: decode get response for %q: %w", fqn, err)
	}
	return w.toService(), nil
}

// doGet performs an authenticated GET and returns the response body. It maps a
// 403 to ErrForbidden and any other non-2xx status to a status-bearing error.
func doGet(ctx context.Context, client *http.Client, url, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("model-services: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model-services: request %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("model-services: read response body: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("model-services: GET %s: %w", url, ErrForbidden)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("model-services: GET %s: unexpected status %d", url, resp.StatusCode)
	}
	return body, nil
}

// needsEnrichment reports whether an individual GetService is required to make a
// LIST entry resolvable.
//
// The LIST endpoint omits routing.destinations for EVERY service and omits
// supported_api_types for user-deployed ones, so a LIST entry alone can never be
// classified into a family. Anything that is anthropic-capable — or whose
// capability is still unknown — must be fetched individually.
//
// A service with a KNOWN, non-anthropic capability is skipped: it can never be a
// candidate, so paying for a GET would be wasted (the metastore lists dozens of
// gpt/gemini/qwen services).
func needsEnrichment(svc Service) bool {
	if len(svc.SupportedAPITypes) > 0 && !svc.supportsMessages() {
		return false
	}
	return len(svc.Destinations) == 0 || len(svc.SupportedAPITypes) == 0
}

// listEnriched lists model-services and fills in the fields the LIST endpoint
// omits (supported_api_types, routing.destinations) with an individual
// GetService per candidate. A GetService that returns ErrForbidden causes that
// service to be skipped with a logged note; any other GetService error is
// propagated. A ListServices failure is returned directly.
func listEnriched(ctx context.Context, client *http.Client, host, token string) ([]Service, error) {
	services, err := ListServices(ctx, client, host, token)
	if err != nil {
		return nil, err
	}

	enriched := make([]Service, 0, len(services))
	for _, svc := range services {
		if !needsEnrichment(svc) {
			enriched = append(enriched, svc)
			continue
		}
		full, gerr := GetService(ctx, client, host, token, svc.FQN)
		if gerr != nil {
			if errors.Is(gerr, ErrForbidden) {
				log.Printf("modeldiscovery: skipping service %q: %v", svc.FQN, gerr)
				continue
			}
			return nil, gerr
		}
		enriched = append(enriched, full)
	}
	return enriched, nil
}

// Discover lists model-services, enriches each candidate via an individual
// GetService call, and resolves the newest model per family.
func Discover(ctx context.Context, client *http.Client, host, token string, pins Pins) (ModelSet, []Unresolved, error) {
	enriched, err := listEnriched(ctx, client, host, token)
	if err != nil {
		return ModelSet{}, nil, err
	}
	set, unresolved := Resolve(enriched, pins)
	return set, unresolved, nil
}
