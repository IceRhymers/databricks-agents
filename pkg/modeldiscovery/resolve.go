package modeldiscovery

import (
	"regexp"
	"strconv"
)

// anthropicMessagesAPIType is the supported_api_types value that marks a
// service as speaking the Anthropic Messages API.
const anthropicMessagesAPIType = "anthropic/v1/messages"

// oneMSuffix is the pure client-side annotation appended to an emitted FQN when
// a service qualifies for the 1M-context variant. It is never present on the
// wire FQN sent to the gateway.
const oneMSuffix = "[1m]"

// destPattern matches the claude-(opus|sonnet|haiku)-major[-minor] shape within
// a routing destination FQN.
//
// The leading (?:^|[./_-]) boundary ensures "claude-" begins a name segment, so
// an unrelated substring like "notclaude-opus-2025-01" is NOT misclassified as
// opus at version 2025 (which would otherwise dominate the numeric version sort
// and mis-route the family).
//
// The minor component is OPTIONAL because Databricks ships major-only
// destinations: system.ai.databricks-claude-sonnet-5 and
// ...-claude-sonnet-4 both exist and serve traffic. Requiring major-minor made
// the parser silently drop them, so a workspace with sonnet-5 resolved to the
// older sonnet-4-6. A missing minor is treated as .0 (5 => 5.0 > 4.6).
//
// The trailing (?:[^0-9]|$) boundary keeps the greedy minor group from taking
// only part of a digit run.
var destPattern = regexp.MustCompile(`(?:^|[./_-])claude-(opus|sonnet|haiku)-(\d+)(?:-(\d+))?(?:[^0-9]|$)`)

// families is the fixed set of Claude families, in resolution order.
var families = []string{"opus", "sonnet", "haiku"}

// parseDestination parses a routing destination FQN into a Dest. It never
// panics: if name does not match the claude-(opus|sonnet|haiku)-major[-minor]
// shape it returns a Dest with Parsed false and an empty Family. An absent minor
// component is treated as 0.
func parseDestination(name string) Dest {
	m := destPattern.FindStringSubmatch(name)
	if m == nil {
		return Dest{FQN: name, Parsed: false}
	}
	// The pattern guarantees m[2] and m[3] are digit runs, so Atoi cannot fail
	// under normal input; ignore the error defensively. m[3] is "" when the
	// destination carries a major-only version (e.g. claude-sonnet-5).
	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	return Dest{
		FQN:    name,
		Family: m[1],
		Major:  major,
		Minor:  minor,
		Parsed: true,
	}
}

// pinFor returns the pin FQN for the given family, or "" if unpinned.
func (p Pins) pinFor(family string) string {
	switch family {
	case "opus":
		return p.Opus
	case "sonnet":
		return p.Sonnet
	case "haiku":
		return p.Haiku
	default:
		return ""
	}
}

// set writes a Resolved into the ModelSet slot for the given family.
func (ms *ModelSet) set(family string, r Resolved) {
	switch family {
	case "opus":
		ms.Opus = r
	case "sonnet":
		ms.Sonnet = r
	case "haiku":
		ms.Haiku = r
	}
}

// supportsMessages reports whether the service advertises the Anthropic
// Messages API type.
func (s Service) supportsMessages() bool {
	for _, t := range s.SupportedAPITypes {
		if t == anthropicMessagesAPIType {
			return true
		}
	}
	return false
}

// familyAndNewest classifies a service to a single family and returns the
// numerically-newest destination version for it. A service is classified only
// when it has at least one destination and every destination parses to the same
// family; otherwise ok is false (ambiguous or unparseable service).
func (s Service) familyAndNewest() (family string, major, minor int, ok bool) {
	if len(s.Destinations) == 0 {
		return "", 0, 0, false
	}
	family = ""
	for _, d := range s.Destinations {
		if !d.Parsed {
			return "", 0, 0, false
		}
		if family == "" {
			family = d.Family
		} else if d.Family != family {
			return "", 0, 0, false
		}
		if d.Major > major || (d.Major == major && d.Minor > minor) {
			major, minor = d.Major, d.Minor
		}
	}
	return family, major, minor, true
}

// isOneM reports whether a family at the given version qualifies for the
// 1M-context variant. Opus and Sonnet at >= 4.6 qualify; Haiku never does.
func isOneM(family string, major, minor int) bool {
	if family != "opus" && family != "sonnet" {
		return false
	}
	return major > 4 || (major == 4 && minor >= 6)
}

// Resolve maps each Claude family to a concrete service FQN. It is a pure
// function with no I/O.
//
// For each family in {opus, sonnet, haiku}:
//   - If a pin is present it is used verbatim (no [1m] suffix) and discovery is
//     skipped for that family.
//   - Otherwise the candidate set is every service that advertises the Anthropic
//     Messages API and whose destinations all classify to that family. A service
//     with any destination in a different family, or any unparseable
//     destination, is excluded as ambiguous.
//   - Candidates in a non-"system" catalog are preferred over system.ai ones.
//     Within the winning tier the service whose newest destination has the
//     highest (Major, Minor) is chosen.
//   - When no candidate exists the family is reported as Unresolved; it is never
//     collapsed onto another family.
//
// When the chosen service qualifies for the 1M-context variant the literal
// "[1m]" suffix is appended to the emitted FQN.
func Resolve(services []Service, pins Pins) (ModelSet, []Unresolved) {
	var set ModelSet
	var unresolved []Unresolved

	for _, family := range families {
		if pin := pins.pinFor(family); pin != "" {
			set.set(family, Resolved{FQN: pin, OneM: false})
			continue
		}

		var (
			best        *Service
			bestMajor   int
			bestMinor   int
			bestSystem  bool
			bestPresent bool
		)
		for i := range services {
			svc := services[i]
			if !svc.supportsMessages() {
				continue
			}
			svcFamily, major, minor, ok := svc.familyAndNewest()
			if !ok || svcFamily != family {
				continue
			}
			isSystem := svc.Catalog == "system"
			if !bestPresent || better(bestSystem, bestMajor, bestMinor, isSystem, major, minor) {
				best = &services[i]
				bestMajor, bestMinor = major, minor
				bestSystem = isSystem
				bestPresent = true
			}
		}

		if !bestPresent {
			unresolved = append(unresolved, Unresolved{
				Family:     family,
				PinCommand: pinCommandFor(family),
			})
			continue
		}

		fqn := best.FQN
		oneM := isOneM(family, bestMajor, bestMinor)
		if oneM {
			fqn += oneMSuffix
		}
		set.set(family, Resolved{FQN: fqn, OneM: oneM})
	}

	return set, unresolved
}

// better reports whether the candidate (cand*) should replace the current best.
// A non-system catalog always beats a system one; within the same tier a higher
// (Major, Minor) wins.
func better(bestSystem bool, bestMajor, bestMinor int, candSystem bool, candMajor, candMinor int) bool {
	if bestSystem != candSystem {
		// A non-system candidate is preferred.
		return !candSystem
	}
	return candMajor > bestMajor || (candMajor == bestMajor && candMinor > bestMinor)
}

// pinCommandFor returns an actionable remediation hint for an unresolved
// family. A family is unresolved because the caller has no EXECUTE grant on a
// matching model-service, so the real fix is to grant access and re-run
// discovery via doctor. (There is deliberately no `config model` pin setter
// yet — do not point users at a command that does not exist.)
func pinCommandFor(family string) string {
	return "grant EXECUTE on a " + family + " model-service, then run: databricks-claude doctor --fix"
}
