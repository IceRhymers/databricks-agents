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

// buildCodexLaunchPlan performs the codex-specific pre-flight for wrapper mode
// and returns a neutral core.LaunchPlan for core.Run to execute, together with
// the field-bearing codexSettingsPatcher that CodexProfile wraps. It owns
// everything up to (but not including) the proxy port bind: logging setup,
// profile/model resolution (with state persistence), auth, port resolution,
// TLS validation + state save, startup security warnings, token seeding, host
// discovery, gateway URL construction, OTEL resolution, and the LookPath("codex")
// guard.
//
// The patcher is returned rather than reconstructed in main because it carries
// the exact resolved model / OTEL-table values this function computed; single-
// sourcing them avoids config.toml parity drift between what the proxy headers
// use and what config.toml is patched to. core.LaunchPlan has no codex-specific
// field to carry it, and CodexProfile(patcher) needs it — hence the extra return.
//
// codex writes NO settings.json env block (its per-session config is the
// surgical config.toml patch), so plan.BuildEnv is nil. Fatal conditions are
// returned as errors WITHOUT the "databricks-codex:" prefix; the caller adds it
// via log.Fatalf, preserving the silent-in-non-verbose / visible-in-verbose
// behavior of the original inline Fatalfs via the shared log output global this
// function configures.
func buildCodexLaunchPlan(a *Args) (core.LaunchPlan, codexSettingsPatcher, error) {
	var plan core.LaunchPlan
	var patcher codexSettingsPatcher

	// Default: discard all logs (silent wrapper — identical to vanilla codex).
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
			log.Printf("databricks-codex: failed to save profile: %v", err)
		} else {
			log.Printf("databricks-codex: saved profile %q for future sessions", profile)
		}
	}
	log.Printf("databricks-codex: using profile: %s", profile)

	// --- Resolve model (--model flag saved to state) ---
	modelExplicit := a.ModelSet
	savedForModel := loadState()
	model := resolveModel(a.Model, savedForModel.Model)
	switch {
	case a.Model != "":
		// flag-supplied; logged below
	case savedForModel.Model != "":
		log.Printf("databricks-codex: using saved model: %s", savedForModel.Model)
	}
	if modelExplicit {
		savedForModel.Model = model
		if err := saveState(savedForModel); err != nil {
			log.Printf("databricks-codex: failed to save model: %v", err)
		} else {
			log.Printf("databricks-codex: saved model %q for future sessions", model)
		}
	}
	log.Printf("databricks-codex: using model: %s", model)

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(profile, ""); err != nil {
		return plan, patcher, fmt.Errorf("auth failed: %v", err)
	}

	// --- Load state and resolve port ---
	state := loadState()
	port := resolvePort(a.PortFlag, state)
	if a.PortFlag > 0 {
		state.Port = port
		if err := saveState(state); err != nil {
			log.Printf("databricks-codex: failed to save port: %v", err)
		} else {
			log.Printf("databricks-codex: saved port %d for future sessions", port)
		}
	}
	log.Printf("databricks-codex: using port: %d", port)

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
				log.Printf("databricks-codex: failed to save TLS config: %v", err)
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

	// --- Discover host + construct gateway URL ---
	host, err := DiscoverHost("", profile)
	if err != nil {
		return plan, patcher, fmt.Errorf("failed to discover host: %v\nRun 'databricks auth login' first", err)
	}
	log.Printf("databricks-codex: discovered host: %s", host)

	gatewayURL := a.Upstream
	if gatewayURL == "" {
		gatewayURL = ConstructGatewayURL(host)
	}
	log.Printf("databricks-codex: gateway URL: %s", gatewayURL)

	// --- OTEL tables (read-only over state; the config editor is the only writer) ---
	saved := loadState()
	otel, otelMetricsTable, otelLogsTable := resolveOtel(saved)
	if otelMetricsTable != "" {
		log.Printf("databricks-codex: using saved otel-metrics-table: %s", otelMetricsTable)
	}
	if otelLogsTable != "" {
		log.Printf("databricks-codex: using saved otel-logs-table: %s", otelLogsTable)
	}

	// Verify codex is on PATH before proxy startup. Kept at its original
	// relative position (after host discovery) so "codex not found" surfaces
	// only after auth/discovery errors. Excluded from serve mode and from
	// core.Run (which launches ChildBinary blindly).
	if _, err := exec.LookPath("codex"); err != nil {
		return plan, patcher, fmt.Errorf("codex binary not found on PATH — install from https://openai.com/codex")
	}

	// --- Determine OTEL upstream (empty when OTEL is off, matching the
	// pre-unification ProxyConfig.OTELUpstream) ---
	otelUpstream := ""
	if otel {
		otelUpstream = host + "/api/2.0/otel"
		log.Printf("databricks-codex: OTEL enabled, upstream: %s", otelUpstream)
	}

	patcher = newCodexPatcher(model, modelExplicit, otelMetricsTable, otelLogsTable, otel)

	plan = core.LaunchPlan{
		InferenceUpstream: gatewayURL,
		OTELUpstream:      otelUpstream,
		UCMetricsTable:    otelMetricsTable,
		UCLogsTable:       otelLogsTable,
		UCTracesTable:     "",
		TokenProvider:     tp,
		Port:              port,
		PortFlag:          a.PortFlag,
		ProfileName:       profile,
		TLSCert:           a.TLSCert,
		TLSKey:            a.TLSKey,
		ProxyAPIKey:       a.ProxyAPIKey,
		Verbose:           a.Verbose,
		Version:           Version,
		ToolName:          "databricks-codex",
		RefcountPrefix:    ".databricks-codex-sessions",
		ManagedEnvVar:     "DATABRICKS_CODEX_MANAGED=1",
		NoUpdateCheck:     a.NoUpdateCheck,
		UpdaterConfig:     buildUpdaterConfig(),
		BuildEnv:          nil,
	}
	return plan, patcher, nil
}
