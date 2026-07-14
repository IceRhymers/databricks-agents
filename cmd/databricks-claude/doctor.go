package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/IceRhymers/databricks-claude/internal/cmd"
	"github.com/IceRhymers/databricks-claude/pkg/modeldiscovery"
)

// modelDelta is the per-family comparison between the current settings.json
// model pin and what model discovery resolved. It is the unit-tested surface
// of the doctor command (mirrors config.go's resolveConfigOTEL testability).
type modelDelta struct {
	Family     string
	Current    string
	Discovered string
	Status     string // "ok" | "drift" | "stale-legacy" | "unresolved" | "new"
}

// diffModelRouting compares the current settings.json pins against discovery.
// Status rules per family:
//   - discovered=="" (unresolved): "unresolved" (leave current alone).
//   - current=="" and discovered!="": "new".
//   - current==discovered: "ok".
//   - current has prefix "databricks-" and differs: "stale-legacy" (migrate).
//   - otherwise differs: "drift".
//
// Pure function: no I/O, no global state, so the status matrix can be driven
// exhaustively in doctor_test.go.
func diffModelRouting(current, discovered ModelRouting, unresolved []modeldiscovery.Unresolved) []modelDelta {
	unres := map[string]bool{}
	for _, u := range unresolved {
		unres[u.Family] = true
	}

	fams := []struct {
		name string
		cur  string
		disc string
	}{
		{"opus", current.Opus, discovered.Opus},
		{"sonnet", current.Sonnet, discovered.Sonnet},
		{"haiku", current.Haiku, discovered.Haiku},
	}

	deltas := make([]modelDelta, 0, len(fams))
	for _, f := range fams {
		d := modelDelta{Family: f.name, Current: f.cur, Discovered: f.disc}
		switch {
		case f.disc == "" || unres[f.name]:
			// Discovery could not resolve this family — never blank the user's
			// working pin. The --fix path preserves Current for this status.
			d.Status = "unresolved"
		case f.cur == "":
			d.Status = "new"
		case f.cur == f.disc:
			d.Status = "ok"
		case strings.HasPrefix(f.cur, "databricks-"):
			d.Status = "stale-legacy"
		default:
			d.Status = "drift"
		}
		deltas = append(deltas, d)
	}
	return deltas
}

// runDoctor implements `databricks-claude doctor` — a non-interactive
// diagnostic that runs model discovery, diffs the current settings.json model
// pins against the discovered models, prints the delta, and rewrites
// settings.json ONLY under --fix (through bootstrapSettings, the sanctioned
// atomic writer). It is the sanctioned recovery path for the hook/daemon flow
// that can't prompt.
//
// Exit codes:
//   0   all pins are up to date (or --fix applied the discovered models)
//   1   drift detected without --fix (so scripts/hooks can detect and remediate)
func runDoctor(args []string) {
	r, _ := doctorCommand.Parse(args)
	if r.Bools["help"] {
		_ = cmd.Render(os.Stdout, doctorCommand, nil)
		os.Exit(0)
	}

	profile := r.Strings["profile"]
	saved := loadState()
	if profile == "" {
		profile = saved.Profile
	}
	if profile == "" {
		profile = "DEFAULT"
	}

	portFlag := atoiOrZero(r.Strings["port"])
	port := resolvePort(portFlag, saved)
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	host, err := DiscoverHost(profile, "")
	if err != nil {
		log.Fatalf("databricks-claude: doctor: failed to discover host for profile %q: %v\n"+
			"Run 'databricks auth login --profile %s' first", profile, err, profile)
	}

	tp := NewTokenProvider(profile, "")
	token, err := tp.Token(context.Background())
	if err != nil {
		log.Fatalf("databricks-claude: doctor: failed to fetch token for profile %q: %v", profile, err)
	}

	ms, unresolved, derr := modeldiscovery.Discover(context.Background(), modeldiscovery.NewClient(), host, token, modeldiscovery.Pins{})
	if derr != nil {
		log.Fatalf("databricks-claude: doctor: model discovery failed: %v", derr)
	}

	// "Current" is what the launch path will actually emit: the wrapper
	// regenerates settings.json's model keys from persistentState.Models via
	// launchModelRouting on every launch (main.go / serve_session.go), so
	// state.Models — not the possibly-stale settings.json — is the source of
	// truth to diff against. Diffing settings.json directly could report "ok"
	// while the wrapper launches a different model on the next run.
	current := launchModelRouting(saved)
	discovered := ModelRouting{Opus: ms.Opus.FQN, Sonnet: ms.Sonnet.FQN, Haiku: ms.Haiku.FQN}

	deltas := diffModelRouting(current, discovered, unresolved)

	fmt.Fprintf(os.Stderr, "databricks-claude: doctor: model routing diff (profile=%s, host=%s)\n", profile, host)
	drift := false
	for _, d := range deltas {
		if d.Status != "ok" {
			drift = true
		}
		fmt.Fprintf(os.Stderr, "  %-7s %-12s %s -> %s\n",
			d.Family, d.Status, displayTableOrNone(d.Current), displayTableOrNone(d.Discovered))
	}

	if r.Bools["fix"] {
		// Build the new routing: discovered for resolved families; for an
		// unresolved family, PRESERVE the current pin (don't blank a working
		// pin the user set), else omit.
		routing := ModelRouting{}
		for _, d := range deltas {
			val := d.Discovered
			if d.Status == "unresolved" {
				val = d.Current
			}
			switch d.Family {
			case "opus":
				routing.Opus = val
			case "sonnet":
				routing.Sonnet = val
			case "haiku":
				routing.Haiku = val
			}
		}

		// Guard against persisting a non-nil-but-empty ModelRouting: that would
		// violate the "nil == never discovered" contract in state.go and write a
		// settings.json with no model keys. Mirrors config write's zero-resolved
		// hard-fail. (Resolved families or preserved current pins keep routing
		// non-empty, so this only fires on a truly empty result.)
		if routing.Opus == "" && routing.Sonnet == "" && routing.Haiku == "" {
			log.Fatalf("databricks-claude: doctor: --fix: nothing to apply — discovery resolved no models and there are no current pins to preserve. Grant EXECUTE on a model-service, then re-run.")
		}

		// Load-then-mutate so the whole-struct save preserves every other
		// persisted field.
		saved = loadState()
		saved.Models = &routing
		if err := saveState(saved); err != nil {
			log.Fatalf("databricks-claude: doctor: could not persist model routing: %v", err)
		}

		// ensureConfig preserves all unrelated env keys (including OTEL), so we
		// pass only the model-routing env block — mirrors runConfigWrite.
		if err := bootstrapSettings(portFlag, profile, proxyURL, databricksFullSetupEnv(routing)); err != nil {
			log.Fatalf("databricks-claude: doctor: %v", err)
		}
		fmt.Fprintf(os.Stderr, "databricks-claude: doctor: applied discovered models to ~/.claude/settings.json (base_url=%s)\n", proxyURL)
		return
	}

	if drift {
		fmt.Fprintln(os.Stderr, "Run 'databricks-claude doctor --fix' to apply.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "settings.json models are up to date.")
}
