package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseFlags is a table-driven root-flag parser shared by all three launchers
// (databricks-claude / -codex / -opencode). Each launcher owns a small set of
// wrapper flags (--profile, --port, …) and forwards everything else — including
// unknown flags — verbatim to the wrapped child CLI (claude/codex/opencode).
//
// Why not the stdlib flag package: the launchers require *transparent
// passthrough* of every unrecognized flag/arg to the child CLI. Go's flag
// package errors on unknown flags and has no way to express "leave this token
// untouched and forward it", so a hand-rolled parser is mandatory. This file is
// that parser, extracted once from the three byte-identical loops that used to
// live in each launcher's main.go (issue #219).

// Binding maps one --flag to its destination field. Exactly one of Str/Int/Bool
// must be set (enforced by Spec.validate). OnSet, if non-nil, fires on each
// value assignment — used by codex's --model to set ModelSet=true.
type Binding struct {
	Str   *string
	Int   *int
	Bool  *bool
	OnSet func()
}

// Spec drives ParseFlags for one launcher.
type Spec struct {
	// Known is the authoritative known-vs-passthrough gate, derived from
	// rootCommand.KnownFlags(). A name here with no matching Bindings entry is a
	// drift error.
	Known map[string]bool
	// Bindings maps "--flag" → destination. Must cover Known exactly.
	Bindings map[string]Binding
	// Shorthands rewrites a whole arg to its long form, e.g. "-h" → "--help".
	Shorthands map[string]string
	// Residual is the passthrough sink (ClaudeArgs / CodexArgs / OpencodeArgs).
	Residual *[]string
}

// validate enforces the exactly-one-of-Str/Int/Bool invariant for every Known
// flag, and that every Known flag has a binding at all. Both illegal states
// would otherwise silently swallow a flag (an all-nil binding consumes the flag
// and its value while writing nowhere), so they are rejected up front.
func (spec Spec) validate() error {
	for name := range spec.Known {
		b, ok := spec.Bindings[name]
		if !ok {
			return fmt.Errorf("internal: %s is a known flag but ParseFlags has no binding for it", name)
		}
		set := 0
		if b.Str != nil {
			set++
		}
		if b.Int != nil {
			set++
		}
		if b.Bool != nil {
			set++
		}
		if set != 1 {
			return fmt.Errorf("internal: binding for %s must set exactly one of Str/Int/Bool (got %d)", name, set)
		}
	}
	return nil
}

// ParseFlags parses args against spec, populating the bound fields and appending
// every passthrough token (unknown flags, bare words, and everything after an
// explicit "--") to *spec.Residual. It is a mechanical, byte-for-byte
// reproduction of the loops it replaced.
func ParseFlags(args []string, spec Spec) error {
	if err := spec.validate(); err != nil {
		return err
	}

	i := 0
	for i < len(args) {
		orig := args[i]

		// Explicit separator: everything after "--" is forwarded verbatim.
		if orig == "--" {
			*spec.Residual = append(*spec.Residual, args[i+1:]...)
			return nil
		}

		// Whole-arg shorthand rewrite (e.g. "-h" → "--help").
		arg := orig
		if long, ok := spec.Shorthands[orig]; ok {
			arg = long
		}

		if strings.HasPrefix(arg, "--") {
			// Split on the FIRST "=" only; "=" inside the value is preserved
			// (e.g. --proxy-api-key=a=b).
			name, value, _ := strings.Cut(arg, "=")

			if spec.Known[name] {
				b, ok := spec.Bindings[name]
				if !ok {
					return fmt.Errorf("internal: %s is a known flag but ParseFlags has no binding for it", name)
				}
				switch {
				case b.Bool != nil:
					// Bool flags ignore any value and never consume the next arg.
					*b.Bool = true
				case b.Str != nil:
					if value != "" {
						*b.Str = value
						if b.OnSet != nil {
							b.OnSet()
						}
					} else if i+1 < len(args) {
						i++
						*b.Str = args[i]
						if b.OnSet != nil {
							b.OnSet()
						}
					}
				case b.Int != nil:
					if value != "" {
						*b.Int, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						*b.Int, _ = strconv.Atoi(args[i])
					}
				}
				i++
				continue
			}
		}

		// Not a known flag — pass through the ORIGINAL arg verbatim.
		*spec.Residual = append(*spec.Residual, orig)
		i++
	}
	return nil
}
