package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"

	"github.com/IceRhymers/databricks-agents/internal/core"
	"github.com/IceRhymers/databricks-agents/internal/core/authcheck"
	"github.com/IceRhymers/databricks-agents/internal/core/proxy"
)

// buildOpencodeLaunchPlan performs the opencode-specific pre-flight for wrapper
// mode and returns a neutral core.LaunchPlan for core.Run to execute, together
// with the field-bearing opencodeSettingsPatcher that OpencodeProfile wraps. It
// owns everything up to (but not including) the proxy port bind: logging setup,
// profile/model resolution (with state persistence), auth, port resolution, TLS
// validation + state save, startup security warnings, token seeding, host
// discovery, gateway URL construction (Anthropic + Gemini), and the
// LookPath("opencode") guard.
//
// The patcher is returned rather than reconstructed in main because it carries
// the exact resolved model / apiKey-placeholder values this function computed;
// single-sourcing them avoids opencode.json parity drift between wrapper mode
// and serve. core.LaunchPlan has no opencode-specific field to carry it, and
// OpencodeProfile(patcher) needs it — hence the extra return.
//
// opencode writes NO settings.json env block (its per-session config is the
// surgical opencode.json patch), so plan.BuildEnv is nil. Fatal conditions are
// returned as errors WITHOUT the "databricks-opencode:" prefix; the caller adds
// it via log.Fatalf, preserving the silent-in-non-verbose /
// visible-in-verbose behavior of the original inline Fatalfs via the shared log
// output global this function configures.
func buildOpencodeLaunchPlan(a *Args) (core.LaunchPlan, opencodeSettingsPatcher, error) {
	var plan core.LaunchPlan
	var patcher opencodeSettingsPatcher

	// Default: discard all logs (silent wrapper — identical to vanilla opencode).
	log.SetOutput(io.Discard)
	if a.Verbose {
		log.SetOutput(os.Stderr)
	}
	if a.LogFile != "" {
		f, err := os.OpenFile(a.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			log.SetOutput(os.Stderr) // ensure this fatal is visible
			return plan, patcher, fmt.Errorf("cannot open log file %q: %v", a.LogFile, err)
		}
		// Not closed explicitly: os.Exit skips defers and the process holds the
		// file until exit (matching the original inline defer-that-never-runs).
		if a.Verbose {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		} else {
			log.SetOutput(f)
		}
	}

	// --- Resolve profile (--profile flag saved to state) ---
	profileExplicit := a.Profile != ""
	profile := resolveProfile(a.Profile, loadState().Profile)
	if profileExplicit {
		saved := loadState()
		saved.Profile = profile
		if err := saveState(saved); err != nil {
			log.Printf("databricks-opencode: failed to save profile: %v", err)
		} else {
			log.Printf("databricks-opencode: saved profile %q for future sessions", profile)
		}
	}
	log.Printf("databricks-opencode: using profile: %s", profile)

	// --- Resolve model (--model flag saved to state) ---
	modelExplicit := a.Model != ""
	savedForModel := loadState()
	model := resolveModel(a.Model, savedForModel.Model)
	if !modelExplicit && savedForModel.Model != "" {
		log.Printf("databricks-opencode: using saved model: %s", savedForModel.Model)
	}
	if modelExplicit {
		savedForModel.Model = model
		if err := saveState(savedForModel); err != nil {
			log.Printf("databricks-opencode: failed to save model: %v", err)
		} else {
			log.Printf("databricks-opencode: saved model %q for future sessions", model)
		}
	}

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(profile, ""); err != nil {
		return plan, patcher, fmt.Errorf("auth failed: %v", err)
	}

	// --- Load state and resolve port ---
	state := loadState()
	port := resolvePort(a.Port, state)
	if a.Port > 0 {
		state.Port = port
		if err := saveState(state); err != nil {
			log.Printf("databricks-opencode: failed to save port: %v", err)
		} else {
			log.Printf("databricks-opencode: saved port %d for future sessions", port)
		}
	}
	log.Printf("databricks-opencode: using port: %d", port)

	// --- TLS validation ---
	if err := proxy.ValidateTLSConfig(a.TLSCert, a.TLSKey); err != nil {
		return plan, patcher, fmt.Errorf("%v", err)
	}

	// --- Save TLS config to state so headless-ensure can use the right scheme ---
	{
		s := loadState()
		if s.TLSCert != a.TLSCert || s.TLSKey != a.TLSKey {
			s.TLSCert = a.TLSCert
			s.TLSKey = a.TLSKey
			if err := saveState(s); err != nil {
				log.Printf("databricks-opencode: failed to save TLS config: %v", err)
			}
		}
	}

	// --- Startup security checks ---
	for _, w := range proxy.SecurityChecks() {
		fmt.Fprintln(os.Stderr, w)
	}

	// --- Seed token cache ---
	tp := NewTokenProvider("", profile)
	if _, err := tp.Token(context.Background()); err != nil {
		return plan, patcher, fmt.Errorf("failed to fetch initial token: %v", err)
	}

	// --- Discover host + construct gateway URLs ---
	host, err := DiscoverHost("", profile)
	if err != nil {
		return plan, patcher, fmt.Errorf("failed to discover host: %v\nRun 'databricks auth login' first", err)
	}
	log.Printf("databricks-opencode: discovered host: %s", host)

	gatewayURL := a.Upstream
	if gatewayURL == "" {
		gatewayURL = ConstructGatewayURL(host)
	}
	log.Printf("databricks-opencode: gateway URL: %s", gatewayURL)

	// Gemini Native upstream — always derived from the discovered host (no
	// --upstream override knob; the /v1beta route is path-prefixed off the same
	// local proxy port).
	geminiGatewayURL := ConstructGeminiGatewayURL(host)
	log.Printf("databricks-opencode: gemini gateway URL: %s", geminiGatewayURL)

	// Verify opencode is on PATH before proxy startup. Kept at its original
	// relative position (after host discovery) so "opencode not found" surfaces
	// only after auth/discovery errors. Excluded from serve mode and from
	// core.Run (which launches ChildBinary blindly).
	if _, err := exec.LookPath("opencode"); err != nil {
		return plan, patcher, fmt.Errorf("opencode binary not found on PATH — install from https://opencode.ai")
	}

	patcher = newOpencodePatcher(model, modelExplicit, a.ProxyAPIKey)

	plan = core.LaunchPlan{
		InferenceUpstream: gatewayURL,
		// opencode emits no telemetry — no OTEL upstream, no UC tables.
		OTELUpstream: "",
		Routes: []proxy.UpstreamRoute{
			{PathPrefix: "/v1beta", Upstream: geminiGatewayURL},
		},
		ResponsesRewrite: proxy.ResponsesRewriteSettings{Enabled: true},
		UCMetricsTable:   "",
		UCLogsTable:      "",
		UCTracesTable:    "",
		TokenProvider:    tp,
		Port:             port,
		PortFlag:         a.Port,
		ProfileName:      profile,
		TLSCert:          a.TLSCert,
		TLSKey:           a.TLSKey,
		ProxyAPIKey:      a.ProxyAPIKey,
		Verbose:          a.Verbose,
		Version:          Version,
		ToolName:         "databricks-opencode",
		RefcountPrefix:   ".databricks-opencode-sessions",
		ManagedEnvVar:    "DATABRICKS_OPENCODE_MANAGED=1",
		NoUpdateCheck:    a.NoUpdateCheck,
		UpdaterConfig:    buildUpdaterConfig(),
		BuildEnv:         nil,
	}
	return plan, patcher, nil
}
