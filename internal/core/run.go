// Package core hosts the tool-agnostic launch engine shared by the
// databricks-{claude,codex,opencode} wrappers. Run is the wrapper-mode launch
// shape: it owns the generic proxy bind → serve/watch → settings-patch →
// child-launch → refcount-teardown lifecycle. Everything tool-specific (env
// assembly, upstream discovery, token wiring, config-file shape) is assembled
// by the launcher into a LaunchPlan and handed here as data, with the single
// exception of BuildEnv — a closure invoked once the proxy port is bound,
// because a tool's settings env block may embed the live proxy URL.
package core

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/IceRhymers/databricks-agents/internal/core/childproc"
	"github.com/IceRhymers/databricks-agents/internal/core/health"
	"github.com/IceRhymers/databricks-agents/internal/core/portbind"
	"github.com/IceRhymers/databricks-agents/internal/core/proxy"
	"github.com/IceRhymers/databricks-agents/internal/core/refcount"
	"github.com/IceRhymers/databricks-agents/internal/core/updater"
	"github.com/IceRhymers/databricks-agents/internal/profile"
)

// LaunchPlan is the neutral, fully-resolved description of one wrapper launch.
// The launcher (e.g. cmd/databricks-claude) assembles it — resolving profile,
// auth, upstreams, tables, TLS, and the token source — then calls Run. Every
// field is plain data except BuildEnv (see below); Run never performs
// tool-specific resolution and never learns a tool's env-var names.
//
// There is deliberately no pre-assembled Env map: a tool's settings env block
// can embed the live proxy URL (e.g. Claude's OTEL endpoints), which is not
// known until Run binds the port. BuildEnv closes over the tool-specific
// assembly and is invoked by Run once the URL exists — so all of a tool's
// env-key knowledge stays in the launcher.
type LaunchPlan struct {
	// InferenceUpstream is the resolved upstream base URL for inference
	// requests (e.g. the AI Gateway URL). Required.
	InferenceUpstream string
	// OTELUpstream is the resolved upstream for /otel/* requests. May equal
	// InferenceUpstream when the tool emits no telemetry.
	OTELUpstream string
	// Routes are optional path-prefix upstream overrides (opencode's dual
	// Anthropic+Gemini routing). Nil for claude — byte-identical to no routes.
	Routes []proxy.UpstreamRoute
	// ResponsesRewrite gates the OpenAI Responses-API SSE rewriter (opencode).
	// Zero value for claude — byte-identical to disabled.
	ResponsesRewrite proxy.ResponsesRewriteSettings
	// UCMetricsTable / UCLogsTable / UCTracesTable are the Unity Catalog OTEL
	// table names; empty disables the corresponding signal's UC header.
	UCMetricsTable string
	UCLogsTable    string
	UCTracesTable  string
	// TokenProvider mints fresh upstream bearer tokens. Built by the launcher
	// (claude shells the Databricks CLI); Run only hands it to the proxy.
	TokenProvider proxy.TokenSource
	// WebSearch bundles the optional local web_search/web_fetch fulfillment.
	// Disabled (zero value) is byte-identical to no websearch.
	WebSearch proxy.WebSearchSettings
	// Port is the resolved bind port (post --port / state / default chain).
	Port int
	// PortFlag is the raw --port value (0 if absent); forwarded to Patch so the
	// patcher can persist a sticky port only when explicitly requested.
	PortFlag int
	// ProfileName is the resolved Databricks profile ("DEFAULT" if none),
	// forwarded to Patch.
	ProfileName string
	// TLSCert / TLSKey enable TLS on the proxy listener when both are set.
	TLSCert string
	TLSKey  string
	// ProxyAPIKey, when non-empty, requires Bearer auth on incoming requests.
	ProxyAPIKey string
	// Verbose toggles proxy request logging.
	Verbose bool
	// Version is the build version reported by /health.
	Version string
	// ToolName identifies this wrapper in /health, port-bind locking, the
	// non-owner watcher, and every log line Run emits (e.g. "databricks-claude").
	ToolName string
	// RefcountPrefix is the temp-file prefix for the cross-session reference
	// counter (e.g. ".databricks-claude-sessions"). Kept explicit rather than
	// derived so the on-disk lock identity is byte-stable across releases.
	RefcountPrefix string
	// ManagedEnvVar, when non-empty, is appended to the child's environment as
	// a "wrapped by this tool" marker (e.g. "DATABRICKS_CLAUDE_MANAGED=1").
	ManagedEnvVar string
	// NoUpdateCheck suppresses the synchronous pre-child update notice.
	NoUpdateCheck bool
	// UpdaterConfig drives the update notice when NoUpdateCheck is false.
	UpdaterConfig updater.Config
	// BuildEnv assembles the tool's settings env block given the live proxy
	// URL. Invoked once by Run after the port binds; the result is handed to
	// PatchSettings.Patch. Nil means "no env block" (empty map). This is the
	// single behavioral seam — all tool-specific env-key knowledge lives here.
	BuildEnv func(proxyURL string) map[string]string
}

// Run executes the wrapper-mode launch lifecycle for the given profile and
// plan, returning the child process's exit code. It binds the proxy port,
// serves (or watches for the owner when another session already owns the port),
// assembles the settings env block via plan.BuildEnv, patches the tool's
// settings file through profile.PatchSettings, launches the child binary, and
// releases the cross-session refcount on exit. Fatal setup errors terminate the
// process via log.Fatalf (matching the pre-extraction inline behavior); Run
// returns normally only with the child's exit code.
//
// Run is the wrapper shape only. The serve --session-mode and serve --daemon
// lifecycles are distinct sibling entrypoints (different teardown, no child)
// and do not route through Run.
func Run(p profile.Profile, plan LaunchPlan, childArgs []string) int {
	// --- Bind proxy port ---
	ln, isOwner, err := portbind.Bind(plan.ToolName, plan.Port)
	if err != nil {
		log.Fatalf("%s: %v", plan.ToolName, err)
	}

	scheme := "http"
	if plan.TLSCert != "" && plan.TLSKey != "" {
		scheme = "https"
		fmt.Fprintf(os.Stderr, "%s: TLS enabled\n", plan.ToolName)
	}
	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, portbind.ListenerPort(ln, plan.Port))

	// --- Build proxy handler (needed by both owner and watchProxy) ---
	handler, err := proxy.NewServer(&proxy.Config{
		InferenceUpstream: plan.InferenceUpstream,
		OTELUpstream:      plan.OTELUpstream,
		Routes:            plan.Routes,
		ResponsesRewrite:  plan.ResponsesRewrite,
		UCMetricsTable:    plan.UCMetricsTable,
		UCLogsTable:       plan.UCLogsTable,
		UCTracesTable:     plan.UCTracesTable,
		TokenSource:       plan.TokenProvider,
		Verbose:           plan.Verbose,
		APIKey:            plan.ProxyAPIKey,
		// TLS is applied at serve time via srv.ServeTLS with the raw cert/key
		// paths below; proxy.Config.TLSCertFile/TLSKeyFile are unconsumed by
		// NewServer, so — matching the former NewProxyServer facade — they are
		// intentionally left unset here.
		ToolName:  plan.ToolName,
		Version:   plan.Version,
		WebSearch: plan.WebSearch,
	})
	if err != nil {
		log.Fatalf("%s: %v", plan.ToolName, err)
	}
	if plan.ProxyAPIKey != "" {
		fmt.Fprintf(os.Stderr, "%s: proxy API key authentication enabled\n", plan.ToolName)
	}

	// --- Reference counting ---
	// Wrapper mode: the parent process acquires here and releases on exit.
	refcountPath := refcount.PathForPort(plan.RefcountPrefix, plan.Port)
	if err := refcount.Acquire(refcountPath); err != nil {
		log.Printf("%s: refcount acquire warning: %v", plan.ToolName, err)
	}

	// --- Start proxy if we own the port; otherwise watch for owner death ---
	// Wrapper mode does not register /shutdown — sessions exit by closing the
	// child process. The non-owner branch keeps a joining session's proxy view
	// alive by taking over if the owner dies.
	if isOwner {
		go func() {
			srv := &http.Server{Handler: handler}
			if plan.TLSCert != "" && plan.TLSKey != "" {
				if err := srv.ServeTLS(ln, plan.TLSCert, plan.TLSKey); err != nil && err != http.ErrServerClosed {
					log.Printf("%s: proxy serve error: %v", plan.ToolName, err)
				}
			} else {
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Printf("%s: proxy serve error: %v", plan.ToolName, err)
				}
			}
		}()
	} else {
		go health.WatchProxy(plan.Port, handler, plan.TLSCert, plan.TLSKey, plan.ToolName, func() {})
	}

	// --- Assemble the settings env block (may embed proxyURL) and patch ---
	env := map[string]string{}
	if plan.BuildEnv != nil {
		env = plan.BuildEnv(proxyURL)
	}
	if err := p.PatchSettings.Patch(profile.PatchRequest{
		PortFlag:    plan.PortFlag,
		ProfileName: plan.ProfileName,
		ProxyURL:    proxyURL,
		Env:         env,
	}); err != nil {
		log.Fatalf("%s: %v", plan.ToolName, err)
	}

	// --- Log startup info ---
	log.Printf("%s: proxy on %s (owner=%v), profile=%s, upstream=%s",
		plan.ToolName, proxyURL, isOwner, plan.ProfileName, plan.InferenceUpstream)

	// --- Synchronous update check (before child to avoid stderr interleaving) ---
	if !plan.NoUpdateCheck && os.Getenv("DATABRICKS_NO_UPDATE_CHECK") != "1" {
		updater.PrintUpdateNotice(plan.UpdaterConfig)
	}

	// --- Run child ---
	exitCode, err := childproc.Run(context.Background(), childproc.Config{
		BinaryName: p.ChildBinary,
		Args:       childArgs,
		Env:        managedChildEnv(plan.ManagedEnvVar),
	})
	if err != nil {
		log.Printf("%s: child error: %v", plan.ToolName, err)
	}

	// --- Release refcount; if last session and owner, close listener ---
	// Called explicitly because os.Exit skips defers.
	remaining, relErr := refcount.Release(refcountPath)
	if relErr != nil {
		log.Printf("%s: refcount release warning: %v", plan.ToolName, relErr)
	}
	if remaining == 0 && isOwner {
		ln.Close()
		log.Printf("%s: last session, proxy shut down", plan.ToolName)
	}

	return exitCode
}

// managedChildEnv returns the child's extra environment: the managed marker
// when set, else nil (childproc leaves os.Environ() untouched when empty).
func managedChildEnv(marker string) []string {
	if marker == "" {
		return nil
	}
	return []string{marker}
}
