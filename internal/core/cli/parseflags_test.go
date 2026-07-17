package cli

import (
	"reflect"
	"testing"
)

// fields collects the destinations a launcher-style spec binds. The tests build
// a spec over these and assert both the bound values and the residual sink.
type fields struct {
	profile  string
	port     int
	verbose  bool
	model    string
	modelSet bool
	residual []string
}

// fullSpec mirrors a realistic launcher spec: a string flag, an int flag, a
// bool flag, and a string flag with an OnSet side-effect, plus the -h/-v
// shorthands.
func fullSpec(f *fields) Spec {
	return Spec{
		Known: map[string]bool{
			"--profile": true,
			"--port":    true,
			"--verbose": true,
			"--model":   true,
		},
		Shorthands: map[string]string{"-h": "--help", "-v": "--verbose"},
		Residual:   &f.residual,
		Bindings: map[string]Binding{
			"--profile": {Str: &f.profile},
			"--port":    {Int: &f.port},
			"--verbose": {Bool: &f.verbose},
			"--model":   {Str: &f.model, OnSet: func() { f.modelSet = true }},
		},
	}
}

func TestParseFlags_Behaviour(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantProfile  string
		wantPort     int
		wantVerbose  bool
		wantModel    string
		wantModelSet bool
		wantResidual []string
	}{
		{
			name:         "separator forwards remainder verbatim",
			args:         []string{"--profile", "p", "--", "--profile", "child", "-v"},
			wantProfile:  "p",
			wantResidual: []string{"--profile", "child", "-v"},
		},
		{
			name:         "-v shorthand sets verbose",
			args:         []string{"-v"},
			wantVerbose:  true,
			wantResidual: nil,
		},
		{
			name:         "-h shorthand is unknown here, forwarded (no --help binding)",
			args:         []string{"-h"},
			wantResidual: []string{"-h"},
		},
		{
			name:         "--flag=value inline form",
			args:         []string{"--profile=prod"},
			wantProfile:  "prod",
			wantResidual: nil,
		},
		{
			name:         "--flag value space-separated form",
			args:         []string{"--profile", "prod"},
			wantProfile:  "prod",
			wantResidual: nil,
		},
		{
			name:         "unknown flag and bare word pass through",
			args:         []string{"--debug", "hello"},
			wantResidual: []string{"--debug", "hello"},
		},
		{
			name:         "int parse valid",
			args:         []string{"--port", "8080"},
			wantPort:     8080,
			wantResidual: nil,
		},
		{
			name:         "int parse junk becomes zero",
			args:         []string{"--port", "notanumber"},
			wantPort:     0,
			wantResidual: nil,
		},
		{
			name:         "bool ignores an =value and never consumes next arg",
			args:         []string{"--verbose=whatever", "trailing"},
			wantVerbose:  true,
			wantResidual: []string{"trailing"},
		},
		{
			name:         "embedded = in value preserved (first = only)",
			args:         []string{"--profile=a=b=c"},
			wantProfile:  "a=b=c",
			wantResidual: nil,
		},
		{
			name:         "OnSet fires on assignment",
			args:         []string{"--model", "opus"},
			wantModel:    "opus",
			wantModelSet: true,
			wantResidual: nil,
		},
		{
			name:         "OnSet does NOT fire on last-arg-no-value",
			args:         []string{"--model"},
			wantModel:    "",
			wantModelSet: false,
			wantResidual: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f fields
			if err := ParseFlags(tt.args, fullSpec(&f)); err != nil {
				t.Fatalf("ParseFlags returned error: %v", err)
			}
			if f.profile != tt.wantProfile {
				t.Errorf("profile = %q, want %q", f.profile, tt.wantProfile)
			}
			if f.port != tt.wantPort {
				t.Errorf("port = %d, want %d", f.port, tt.wantPort)
			}
			if f.verbose != tt.wantVerbose {
				t.Errorf("verbose = %v, want %v", f.verbose, tt.wantVerbose)
			}
			if f.model != tt.wantModel {
				t.Errorf("model = %q, want %q", f.model, tt.wantModel)
			}
			if f.modelSet != tt.wantModelSet {
				t.Errorf("modelSet = %v, want %v", f.modelSet, tt.wantModelSet)
			}
			if !reflect.DeepEqual(f.residual, tt.wantResidual) {
				t.Errorf("residual = %#v, want %#v", f.residual, tt.wantResidual)
			}
		})
	}
}

// TestParseFlags_ThinlyCoveredStrFlagRoundTrips exercises a string flag class
// that the launcher behavioural suites touch only lightly (--tls-key), ensuring
// both the inline and space-separated forms land in the destination.
func TestParseFlags_ThinlyCoveredStrFlagRoundTrips(t *testing.T) {
	for _, args := range [][]string{
		{"--tls-key", "/path/to/key.pem"},
		{"--tls-key=/path/to/key.pem"},
	} {
		var got string
		var residual []string
		spec := Spec{
			Known:    map[string]bool{"--tls-key": true},
			Residual: &residual,
			Bindings: map[string]Binding{"--tls-key": {Str: &got}},
		}
		if err := ParseFlags(args, spec); err != nil {
			t.Fatalf("ParseFlags(%v) error: %v", args, err)
		}
		if got != "/path/to/key.pem" {
			t.Errorf("ParseFlags(%v): tls-key = %q, want /path/to/key.pem", args, got)
		}
		if residual != nil {
			t.Errorf("ParseFlags(%v): residual = %#v, want nil", args, residual)
		}
	}
}

func TestParseFlags_DriftErrorWhenKnownUnbound(t *testing.T) {
	var residual []string
	spec := Spec{
		Known:    map[string]bool{"--profile": true, "--ghost": true},
		Residual: &residual,
		Bindings: map[string]Binding{"--profile": {Str: new(string)}},
	}
	err := ParseFlags([]string{"--profile", "p"}, spec)
	if err == nil {
		t.Fatal("expected drift error for known-but-unbound --ghost, got nil")
	}
	want := "internal: --ghost is a known flag but ParseFlags has no binding for it"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestParseFlags_AllNilBindingIsValidationError(t *testing.T) {
	var residual []string
	spec := Spec{
		Known:    map[string]bool{"--profile": true},
		Residual: &residual,
		Bindings: map[string]Binding{"--profile": {}},
	}
	err := ParseFlags([]string{"--profile", "p"}, spec)
	if err == nil {
		t.Fatal("expected validation error for all-nil binding, got nil")
	}
}

func TestParseFlags_TwoSetBindingIsValidationError(t *testing.T) {
	var residual []string
	spec := Spec{
		Known:    map[string]bool{"--x": true},
		Residual: &residual,
		Bindings: map[string]Binding{"--x": {Str: new(string), Int: new(int)}},
	}
	err := ParseFlags([]string{"--x", "y"}, spec)
	if err == nil {
		t.Fatal("expected validation error for two-set binding, got nil")
	}
}
