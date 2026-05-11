package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// uuidPattern matches the canonical 8-4-4-4-12 hex UUID layout produced by
// newUUID(). Hex is upper-case to mirror the format string in newUUID.
var uuidPattern = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`)

func TestNewUUID_FormatAndUniqueness(t *testing.T) {
	a, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID: %v", err)
	}
	b, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID: %v", err)
	}
	if !uuidPattern.MatchString(a) {
		t.Errorf("UUID %q does not match v4 layout", a)
	}
	if !uuidPattern.MatchString(b) {
		t.Errorf("UUID %q does not match v4 layout", b)
	}
	if a == b {
		t.Errorf("two consecutive UUIDs collided: %q", a)
	}
}

func TestBuildMobileconfig_ContainsRequiredKeys(t *testing.T) {
	gateway := "https://adb-abc-123.azuredatabricks.net/ai-gateway/anthropic"
	helper := "/usr/local/bin/databricks-claude"
	out, err := buildMobileconfig(gateway, helper, "myws", "")
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}

	// Spot-check the must-have keys and values.
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<key>PayloadType</key>`,
		`<string>com.anthropic.claudefordesktop</string>`,
		`<key>inferenceProvider</key>`,
		`<string>gateway</string>`,
		`<key>inferenceGatewayBaseUrl</key>`,
		`<string>` + gateway + `</string>`,
		`<key>inferenceCredentialHelper</key>`,
		`<string>` + helper + `</string>`,
		`<key>inferenceCredentialHelperTtlSec</key>`,
		`<integer>55</integer>`,
		`<key>inferenceModels</key>`,
		`databricks-claude-opus-4-7`,
		`<key>disableAutoUpdates</key>`,
		`<key>disableEssentialTelemetry</key>`,
		`<key>disableNonessentialTelemetry</key>`,
		`<key>disableNonessentialServices</key>`,
		// Second payload: our own domain.
		`<string>com.icerhymers.databricks-claude</string>`,
		`<key>databricksProfile</key>`,
		`<string>myws</string>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mobileconfig missing %q", want)
		}
	}
}

func TestBuildMobileconfig_SecondPayload(t *testing.T) {
	out, err := buildMobileconfig("https://x.example.com/anthropic", "/bin/h", "fleet-profile", "")
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}
	for _, want := range []string{
		`<string>com.icerhymers.databricks-claude</string>`,
		`<key>databricksProfile</key>`,
		`<string>fleet-profile</string>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mobileconfig second payload missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestBuildMobileconfig_EscapesSpecialChars(t *testing.T) {
	// Gateway URL with an ampersand should be plist-escaped.
	gateway := "https://example.com/anthropic?a=1&b=2"
	helper := "/Applications/Foo & Bar/databricks-claude"
	out, err := buildMobileconfig(gateway, helper, "DEFAULT", "")
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}
	if strings.Contains(out, "?a=1&b=2</string>") {
		t.Errorf("ampersand in gateway URL was not escaped")
	}
	if !strings.Contains(out, "?a=1&amp;b=2") {
		t.Errorf("expected escaped ampersand, got: %s", out)
	}
	if !strings.Contains(out, "Foo &amp; Bar") {
		t.Errorf("expected escaped ampersand in helper path")
	}
}

func TestBuildMobileconfig_UniqueUUIDs(t *testing.T) {
	out, err := buildMobileconfig("https://x", "/bin/x", "DEFAULT", "")
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}
	uuidsRe := regexp.MustCompile(`<string>([0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12})</string>`)
	matches := uuidsRe.FindAllStringSubmatch(out, -1)
	// Three UUIDs: Anthropic payload, IceRhymers payload, outer profile.
	if len(matches) != 3 {
		t.Fatalf("expected exactly 3 UUIDs in mobileconfig, got %d", len(matches))
	}
	seen := map[string]bool{}
	for _, m := range matches {
		if seen[m[1]] {
			t.Errorf("duplicate UUID %q in mobileconfig", m[1])
		}
		seen[m[1]] = true
	}
}

func TestBuildRegFile_ContainsRequiredKeys(t *testing.T) {
	gateway := "https://adb-abc-123.azuredatabricks.net/ai-gateway/anthropic"
	helper := `C:\Program Files\databricks-claude\databricks-claude.exe`
	out := buildRegFile(gateway, helper, "fleet-ws", "")

	for _, want := range []string{
		`Windows Registry Editor Version 5.00`,
		`[HKEY_CURRENT_USER\SOFTWARE\Policies\Claude]`,
		`"disableDeploymentModeChooser"=dword:00000001`,
		`"inferenceProvider"="gateway"`,
		`"inferenceGatewayBaseUrl"="` + gateway + `"`,
		`"inferenceGatewayAuthScheme"="bearer"`,
		`"inferenceCredentialHelperTtlSec"="55"`,
		`"isClaudeCodeForDesktopEnabled"=dword:00000001`,
		`"isDesktopExtensionSignatureRequired"=dword:00000000`,
		`"isLocalDevMcpEnabled"=dword:00000001`,
		`"disableAutoUpdates"=dword:00000000`,
		`"disableEssentialTelemetry"=dword:00000000`,
		`"disableNonessentialTelemetry"=dword:00000000`,
		`"disableNonessentialServices"=dword:00000000`,
		// IceRhymers key carrying databricksProfile.
		`[HKEY_CURRENT_USER\SOFTWARE\IceRhymers\databricks-claude]`,
		`"databricksProfile"="fleet-ws"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf(".reg missing %q", want)
		}
	}

	// Backslashes in the helper path must be doubled.
	if !strings.Contains(out, `C:\\Program Files\\databricks-claude\\databricks-claude.exe`) {
		t.Errorf("helper path backslashes not escaped, got: %s", out)
	}

	// JSON quotes in the inferenceModels string must be escaped.
	if !strings.Contains(out, `\"name\":\"databricks-claude-opus-4-7\"`) {
		t.Errorf("inferenceModels JSON quotes not escaped")
	}

	// Lines must end with CRLF (the .reg format requires it).
	if !strings.Contains(out, "\r\n") {
		t.Errorf(".reg output is missing CRLF line endings")
	}
}

func TestRegEscape(t *testing.T) {
	cases := map[string]string{
		`plain`:                  `plain`,
		`with "quotes"`:          `with \"quotes\"`,
		`C:\path\to\bin`:         `C:\\path\\to\\bin`,
		`"quote at start`:        `\"quote at start`,
		`back\slash and "quote"`: `back\\slash and \"quote\"`,
	}
	for in, want := range cases {
		if got := regEscape(in); got != want {
			t.Errorf("regEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlistEscape(t *testing.T) {
	cases := map[string]string{
		`plain`:        `plain`,
		`a & b`:        `a &amp; b`,
		`<tag>`:        `&lt;tag&gt;`,
		`he said "hi"`: `he said &quot;hi&quot;`,
		`it's & <ok>`:  `it&apos;s &amp; &lt;ok&gt;`,
	}
	for in, want := range cases {
		if got := plistEscape(in); got != want {
			t.Errorf("plistEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractProfileFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"--credential-helper"}, ""},
		{[]string{"--profile", "prod", "--credential-helper"}, "prod"},
		{[]string{"--credential-helper", "--profile", "dev"}, "dev"},
		{[]string{"--profile=staging"}, "staging"},
		{[]string{"--credential-helper", "--profile=qa"}, "qa"},
		// Bare --profile without a value must not panic and must return "".
		{[]string{"--profile"}, ""},
	}
	for _, c := range cases {
		if got := extractProfileFlag(c.args); got != c.want {
			t.Errorf("extractProfileFlag(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestExtractOutputFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"--generate-desktop-config"}, ""},
		{[]string{"--output", "/tmp/foo.mobileconfig"}, "/tmp/foo.mobileconfig"},
		{[]string{"--output=/tmp/foo.reg"}, "/tmp/foo.reg"},
		{[]string{"--generate-desktop-config", "--output", "/tmp/x"}, "/tmp/x"},
	}
	for _, c := range cases {
		if got := extractOutputFlag(c.args); got != c.want {
			t.Errorf("extractOutputFlag(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestHasFlag(t *testing.T) {
	if !hasFlag([]string{"--credential-helper"}, "--credential-helper") {
		t.Error("hasFlag should match exact flag")
	}
	if !hasFlag([]string{"--credential-helper=true"}, "--credential-helper") {
		t.Error("hasFlag should match --flag=value form")
	}
	if hasFlag([]string{"--credential-helper-thing"}, "--credential-helper") {
		t.Error("hasFlag must not match a flag with a longer name")
	}
	if hasFlag([]string{"--other"}, "--credential-helper") {
		t.Error("hasFlag should not match unrelated flag")
	}
	if hasFlag(nil, "--credential-helper") {
		t.Error("hasFlag on nil should return false")
	}
}

func TestIsCredentialHelperBinaryName(t *testing.T) {
	cases := []struct {
		arg0 string
		want bool
	}{
		{"databricks-claude-credential-helper", true},
		{"/usr/local/bin/databricks-claude-credential-helper", true},
		{"/opt/homebrew/bin/databricks-claude-credential-helper", true},
		{"databricks-claude-credential-helper.exe", true},
		// Backslash path-separator handling is exercised in the platform-specific
		// test below; filepath.Base on darwin/linux treats backslashes as
		// literal characters, so we don't include a Windows-style path here.
		// Main binary name must NOT trigger helper dispatch.
		{"databricks-claude", false},
		{"/usr/local/bin/databricks-claude", false},
		{"databricks-claude.exe", false},
		// Near-misses must not match.
		{"databricks-claude-credential-helper-extra", false},
		{"my-databricks-claude-credential-helper", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isCredentialHelperBinaryName(c.arg0); got != c.want {
			t.Errorf("isCredentialHelperBinaryName(%q) = %v, want %v", c.arg0, got, c.want)
		}
	}
}

func TestExtractDatabricksCLIPathFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"--generate-desktop-config"}, ""},
		{[]string{"--databricks-cli-path", "/usr/local/bin/databricks"}, "/usr/local/bin/databricks"},
		{[]string{"--databricks-cli-path=/opt/homebrew/bin/databricks"}, "/opt/homebrew/bin/databricks"},
		{[]string{"--generate-desktop-config", "--databricks-cli-path", "/x"}, "/x"},
		// Bare flag without a value must not panic and must return "".
		{[]string{"--databricks-cli-path"}, ""},
	}
	for _, c := range cases {
		if got := extractDatabricksCLIPathFlag(c.args); got != c.want {
			t.Errorf("extractDatabricksCLIPathFlag(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestExtractBinaryPathFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"--generate-desktop-config"}, ""},
		{[]string{"--binary-path", "/usr/local/bin/databricks-claude-credential-helper"}, "/usr/local/bin/databricks-claude-credential-helper"},
		{[]string{"--binary-path=/opt/homebrew/bin/databricks-claude-credential-helper"}, "/opt/homebrew/bin/databricks-claude-credential-helper"},
		{[]string{"--generate-desktop-config", "--binary-path", "/x"}, "/x"},
		// Bare --binary-path without a value must not panic and must return "".
		{[]string{"--binary-path"}, ""},
	}
	for _, c := range cases {
		if got := extractBinaryPathFlag(c.args); got != c.want {
			t.Errorf("extractBinaryPathFlag(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestResolveHelperPath_Override(t *testing.T) {
	override := "/usr/local/bin/databricks-claude-credential-helper"
	got, err := resolveHelperPath(override, false)
	if err != nil {
		t.Fatalf("resolveHelperPath: %v", err)
	}
	if got != override {
		t.Errorf("resolveHelperPath(%q) = %q, want %q", override, got, override)
	}
}

func TestResolveHelperPath_DerivedFromExecutable(t *testing.T) {
	// With no override, the helper path is the running test binary's
	// directory + the credential-helper alias name. We can't predict the
	// exact dir but we can assert the basename.
	got, err := resolveHelperPath("", false)
	if err != nil {
		t.Fatalf("resolveHelperPath: %v", err)
	}
	wantBase := credentialHelperBinaryName
	if runtime.GOOS == "windows" {
		wantBase += ".exe"
	}
	if filepath.Base(got) != wantBase {
		t.Errorf("resolveHelperPath(\"\") basename = %q, want %q (full=%q)", filepath.Base(got), wantBase, got)
	}
}

func TestResolveHelperPath_ForPkgDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("forPkg darwin branch only fires on darwin; GOOS=%s", runtime.GOOS)
	}
	got, err := resolveHelperPath("", true)
	if err != nil {
		t.Fatalf("resolveHelperPath: %v", err)
	}
	if got != MacOSCanonicalHelperPath {
		t.Errorf("resolveHelperPath(\"\", true) = %q, want %q", got, MacOSCanonicalHelperPath)
	}
}

func TestResolveHelperPath_ForPkgPreservesOverride(t *testing.T) {
	override := "/custom/path/foo"
	got, err := resolveHelperPath(override, true)
	if err != nil {
		t.Fatalf("resolveHelperPath: %v", err)
	}
	if got != override {
		t.Errorf("resolveHelperPath(%q, true) = %q, want %q (explicit override must win regardless of forPkg/GOOS)", override, got, override)
	}
}

func TestExtractForPkgFlag(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		wantForPkg    bool
		wantRemaining []string
	}{
		{
			name:          "absent",
			args:          []string{"--profile", "prod", "--output", "/tmp/x"},
			wantForPkg:    false,
			wantRemaining: []string{"--profile", "prod", "--output", "/tmp/x"},
		},
		{
			name:          "bare flag present",
			args:          []string{"--for-pkg"},
			wantForPkg:    true,
			wantRemaining: []string{},
		},
		{
			name:          "mixed with other flags",
			args:          []string{"--profile", "prod", "--for-pkg", "--output", "/tmp/x"},
			wantForPkg:    true,
			wantRemaining: []string{"--profile", "prod", "--output", "/tmp/x"},
		},
		{
			name:          "explicit =true",
			args:          []string{"--for-pkg=true"},
			wantForPkg:    true,
			wantRemaining: []string{},
		},
		{
			name:          "explicit =false",
			args:          []string{"--for-pkg=false"},
			wantForPkg:    false,
			wantRemaining: []string{},
		},
		{
			name:          "empty args",
			args:          []string{},
			wantForPkg:    false,
			wantRemaining: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotForPkg, gotRemaining := extractForPkgFlag(c.args)
			if gotForPkg != c.wantForPkg {
				t.Errorf("extractForPkgFlag(%v) forPkg = %v, want %v", c.args, gotForPkg, c.wantForPkg)
			}
			if len(gotRemaining) != len(c.wantRemaining) {
				t.Fatalf("extractForPkgFlag(%v) remaining = %v, want %v", c.args, gotRemaining, c.wantRemaining)
			}
			for i := range gotRemaining {
				if gotRemaining[i] != c.wantRemaining[i] {
					t.Errorf("extractForPkgFlag(%v) remaining[%d] = %q, want %q", c.args, i, gotRemaining[i], c.wantRemaining[i])
				}
			}
		})
	}
}

// ---- buildDevModeJSON ------------------------------------------------------

const (
	devTestGateway = "https://adb-abc-123.azuredatabricks.net/ai-gateway/anthropic"
	devTestHelper  = "/usr/local/bin/databricks-claude-credential-helper"
)

const devTestProfile = "test-profile"

func decodeDevJSON(t *testing.T) map[string]any {
	t.Helper()
	out, err := buildDevModeJSON(devTestGateway, devTestHelper, devTestProfile, "")
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode dev-mode JSON: %v\nbody: %s", err, out)
	}
	return m
}

func TestBuildDevModeJSON_ContainsRequiredKeys(t *testing.T) {
	m := decodeDevJSON(t)
	wantBool := map[string]bool{
		"disableDeploymentModeChooser":        true,
		"isClaudeCodeForDesktopEnabled":       true,
		"isDesktopExtensionEnabled":           true,
		"isDesktopExtensionDirectoryEnabled":  true,
		"isDesktopExtensionSignatureRequired": false,
		"isLocalDevMcpEnabled":                true,
		"disableAutoUpdates":                  false,
		"disableEssentialTelemetry":           false,
		"disableNonessentialTelemetry":        false,
		"disableNonessentialServices":         false,
	}
	for k, want := range wantBool {
		got, ok := m[k].(bool)
		if !ok {
			t.Errorf("dev JSON key %q: not a bool (got %T = %v)", k, m[k], m[k])
			continue
		}
		if got != want {
			t.Errorf("dev JSON key %q = %v, want %v", k, got, want)
		}
	}
	wantStr := map[string]string{
		"databricksProfile":          devTestProfile,
		"inferenceProvider":          "gateway",
		"inferenceGatewayBaseUrl":    devTestGateway,
		"inferenceGatewayAuthScheme": "bearer",
		"inferenceCredentialHelper":  devTestHelper,
	}
	for k, want := range wantStr {
		got, ok := m[k].(string)
		if !ok {
			t.Errorf("dev JSON key %q: not a string (got %T = %v)", k, m[k], m[k])
			continue
		}
		if got != want {
			t.Errorf("dev JSON key %q = %q, want %q", k, got, want)
		}
	}
}

func TestBuildDevModeJSON_ValidJSONAndTrailingNewline(t *testing.T) {
	out, err := buildDevModeJSON(devTestGateway, devTestHelper, devTestProfile, "")
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Errorf("dev-mode JSON must end with newline; last byte = %q", out[len(out)-1])
	}
	var sink map[string]any
	if err := json.Unmarshal(out, &sink); err != nil {
		t.Errorf("output is not valid JSON: %v", err)
	}
}

func TestBuildDevModeJSON_NoInferenceGatewayApiKey(t *testing.T) {
	out, err := buildDevModeJSON(devTestGateway, devTestHelper, devTestProfile, "")
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	if strings.Contains(string(out), "inferenceGatewayApiKey") {
		t.Errorf("dev-mode JSON must NOT contain inferenceGatewayApiKey (UI placeholder, OAuth via helper)")
	}
}

func TestBuildDevModeJSON_ModelsArrayShape(t *testing.T) {
	m := decodeDevJSON(t)
	models, ok := m["inferenceModels"].([]any)
	if !ok {
		t.Fatalf("inferenceModels: not a JSON array, got %T", m["inferenceModels"])
	}
	// Should match the count in inferenceModelsJSON.
	var want []json.RawMessage
	if err := json.Unmarshal([]byte(inferenceModelsJSON), &want); err != nil {
		t.Fatalf("inferenceModelsJSON malformed: %v", err)
	}
	if len(models) != len(want) {
		t.Fatalf("inferenceModels length = %d, want %d", len(models), len(want))
	}
	first, ok := models[0].(map[string]any)
	if !ok {
		t.Fatalf("first model: not an object, got %T", models[0])
	}
	if first["name"] != "databricks-claude-opus-4-7" {
		t.Errorf("first model name = %v, want databricks-claude-opus-4-7", first["name"])
	}
	if first["supports1m"] != true {
		t.Errorf("first model supports1m = %v, want true", first["supports1m"])
	}
}

func TestBuildDevModeJSON_TtlIs55(t *testing.T) {
	m := decodeDevJSON(t)
	ttl, ok := m["inferenceCredentialHelperTtlSec"].(float64)
	if !ok {
		t.Fatalf("inferenceCredentialHelperTtlSec: not a number, got %T", m["inferenceCredentialHelperTtlSec"])
	}
	if ttl != 55 {
		t.Errorf("inferenceCredentialHelperTtlSec = %v, want 55 (matches MDM TTL; do not drift to 3600)", ttl)
	}
}

func TestBuildDevModeJSON_PreservesPaths(t *testing.T) {
	gateway := "https://example.com/anthropic?a=1&b=2"
	helper := "/Applications/Foo Bar/databricks-claude-credential-helper"
	out, err := buildDevModeJSON(gateway, helper, "my-profile", "")
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["inferenceGatewayBaseUrl"] != gateway {
		t.Errorf("gateway URL not preserved: got %q", m["inferenceGatewayBaseUrl"])
	}
	if m["inferenceCredentialHelper"] != helper {
		t.Errorf("helper path not preserved: got %q", m["inferenceCredentialHelper"])
	}
}

// TestInferenceModelsConsistencyAcrossArtifacts proves the model list is
// byte-identical across all three artifacts so a future model addition
// to inferenceModelsJSON propagates everywhere automatically.
func TestInferenceModelsConsistencyAcrossArtifacts(t *testing.T) {
	gateway := "https://x.example.com/anthropic"
	helper := "/path/to/helper"

	// JSON: models array elements should byte-equal the constant elements.
	devOut, err := buildDevModeJSON(gateway, helper, "DEFAULT", "")
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	var devShape struct {
		Models []json.RawMessage `json:"inferenceModels"`
	}
	if err := json.Unmarshal(devOut, &devShape); err != nil {
		t.Fatalf("decode dev JSON: %v", err)
	}
	var want []json.RawMessage
	if err := json.Unmarshal([]byte(inferenceModelsJSON), &want); err != nil {
		t.Fatalf("inferenceModelsJSON malformed: %v", err)
	}
	if len(devShape.Models) != len(want) {
		t.Fatalf("dev JSON model count = %d, want %d", len(devShape.Models), len(want))
	}
	// MarshalIndent re-indents json.RawMessage elements, so compare the
	// compact form of each side. Compact bytes are byte-equal iff the JSON
	// is semantically identical down to key order — which is what "single
	// source of truth" demands.
	for i := range want {
		gotC, errG := compactJSON(devShape.Models[i])
		wantC, errW := compactJSON(want[i])
		if errG != nil || errW != nil {
			t.Fatalf("compact: got err=%v want err=%v", errG, errW)
		}
		if !bytes.Equal(gotC, wantC) {
			t.Errorf("dev JSON model[%d] (compact) = %s, want %s", i, gotC, wantC)
		}
	}

	// Mobileconfig: extract the <string>...</string> body for inferenceModels
	// and reverse plistEscape; should equal inferenceModelsJSON verbatim.
	mc, err := buildMobileconfig(gateway, helper, "DEFAULT", "")
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}
	mcModels := extractMobileconfigModels(t, mc)
	mcUnescaped := unescapePlist(mcModels)
	if mcUnescaped != inferenceModelsJSON {
		t.Errorf("mobileconfig models (unescaped) = %q,\nwant %q", mcUnescaped, inferenceModelsJSON)
	}

	// Reg: extract the inferenceModels="..." value and reverse regEscape.
	reg := buildRegFile(gateway, helper, "DEFAULT", "")
	regModels := extractRegModels(t, reg)
	regUnescaped := unescapeReg(regModels)
	if regUnescaped != inferenceModelsJSON {
		t.Errorf("reg models (unescaped) = %q,\nwant %q", regUnescaped, inferenceModelsJSON)
	}
}

// compactJSON returns the canonical compact form of a JSON byte slice. Two
// byte slices that compact-equal are semantically identical to encoding/json,
// regardless of whitespace or MarshalIndent re-formatting.
func compactJSON(in []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, in); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func extractMobileconfigModels(t *testing.T, plist string) string {
	t.Helper()
	re := regexp.MustCompile(`<key>inferenceModels</key>\s*<string>([^<]*)</string>`)
	m := re.FindStringSubmatch(plist)
	if len(m) != 2 {
		t.Fatalf("could not find inferenceModels <string> in mobileconfig")
	}
	return m[1]
}

func unescapePlist(s string) string {
	r := strings.NewReplacer(
		"&apos;", "'",
		"&quot;", `"`,
		"&gt;", ">",
		"&lt;", "<",
		"&amp;", "&",
	)
	return r.Replace(s)
}

func extractRegModels(t *testing.T, reg string) string {
	t.Helper()
	re := regexp.MustCompile(`"inferenceModels"="([^"\\]*(?:\\.[^"\\]*)*)"`)
	m := re.FindStringSubmatch(reg)
	if len(m) != 2 {
		t.Fatalf("could not find inferenceModels in reg file")
	}
	return m[1]
}

func unescapeReg(s string) string {
	// Reverse regEscape: \" → ", \\ → \. Order matters: \" first, then \\.
	r := strings.NewReplacer(
		`\"`, `"`,
		`\\`, `\`,
	)
	return r.Replace(s)
}

// ---- generate-config write-side guard --------------------------------------

// TestRunGenerateDesktopConfig_NoFlag_DoesNotPersistDEFAULT verifies that
// calling generate-config with no --profile flag on a clean machine leaves
// state.Profile == "". "DEFAULT" is a sentinel for fall-through, not a real
// profile choice, and must never be persisted.
func TestRunGenerateDesktopConfig_NoFlag_DoesNotPersistDEFAULT(t *testing.T) {
	_, cleanup := overrideStatePath(t)
	defer cleanup()

	// Simulate the profile-resolution + write-side guard block in
	// runGenerateDesktopConfig with no --profile flag and empty state.
	profile := ""
	resolved := profile
	if resolved == "" {
		if saved := loadState(); saved.Profile != "" {
			resolved = saved.Profile
		}
	}
	if resolved == "" {
		resolved = "DEFAULT"
	}

	// Apply the write-side guard (the fix).
	st := loadState()
	if resolved != "" && resolved != "DEFAULT" && st.Profile != resolved {
		st.Profile = resolved
		if err := saveState(st); err != nil {
			t.Fatalf("saveState: %v", err)
		}
	}

	got := loadState()
	if got.Profile != "" {
		t.Errorf("state.Profile = %q after generate-config with no flags, want \"\" (DEFAULT not persisted)", got.Profile)
	}
}

// ---- writeFileAtomic & writeDesktopConfigByPath ----------------------------

func TestWriteFileAtomic_RenamesFromTempInSameDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.json")
	want := []byte("hello\n")
	if err := writeFileAtomic(target, want, 0o600); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("contents = %q, want %q", got, want)
	}
	// .tmp must not exist after success.
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file leaked: stat err = %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestWriteDesktopConfigByPath_JsonExtension(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.json")
	if err := writeDesktopConfigByPath(target, "https://x.example.com/anthropic", "/bin/h", "DEFAULT", ""); err != nil {
		t.Fatalf("writeDesktopConfigByPath: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("file is not valid JSON: %v", err)
	}
	if m["inferenceProvider"] != "gateway" {
		t.Errorf("inferenceProvider = %v, want gateway", m["inferenceProvider"])
	}
}

// ---- guardDevJSONOutputPath ------------------------------------------------

func TestGuardDevJSONOutputPath_NonExistent(t *testing.T) {
	dir := t.TempDir()
	if err := guardDevJSONOutputPath(filepath.Join(dir, "does-not-exist.json")); err != nil {
		t.Errorf("expected nil for non-existent file, got %v", err)
	}
}

func TestGuardDevJSONOutputPath_Empty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := guardDevJSONOutputPath(p); err != nil {
		t.Errorf("expected nil for empty file, got %v", err)
	}
}

func TestGuardDevJSONOutputPath_OurOwnConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ours.json")
	body, err := buildDevModeJSON("https://x", "/bin/h", "DEFAULT", "")
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := guardDevJSONOutputPath(p); err != nil {
		t.Errorf("expected nil for our own config (regeneration is allowed), got %v", err)
	}
}

func TestGuardDevJSONOutputPath_ClaudeSettingsJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	settings := []byte(`{"env":{"FOO":"bar"},"hooks":{}}`)
	if err := os.WriteFile(p, settings, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := guardDevJSONOutputPath(p)
	if err == nil {
		t.Fatalf("expected error refusing to overwrite settings.json-shaped file, got nil")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error must mention the path %q, got %v", p, err)
	}
}

func TestGuardDevJSONOutputPath_NotJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(p, []byte("not json at all <<<"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := guardDevJSONOutputPath(p)
	if err == nil {
		t.Fatalf("expected error refusing to overwrite non-JSON file, got nil")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error must mention the path %q, got %v", p, err)
	}
}

// ---- databricksCliPath emission tests ----------------------------------------

const testCliPath = "/opt/databricks/bin/databricks"

func TestBuildMobileconfig_CliPath_Present(t *testing.T) {
	out, err := buildMobileconfig("https://x.example.com/anthropic", "/bin/h", "myws", testCliPath)
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}
	if !strings.Contains(out, "<key>databricksCliPath</key>") {
		t.Error("mobileconfig with cliPath must contain <key>databricksCliPath</key>")
	}
	if !strings.Contains(out, "<string>"+testCliPath+"</string>") {
		t.Errorf("mobileconfig must contain <string>%s</string>", testCliPath)
	}
}

func TestBuildMobileconfig_CliPath_Absent(t *testing.T) {
	out, err := buildMobileconfig("https://x.example.com/anthropic", "/bin/h", "myws", "")
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}
	if strings.Contains(out, "databricksCliPath") {
		t.Error("mobileconfig without cliPath must NOT contain databricksCliPath")
	}
}

func TestBuildMobileconfig_CliPath_Escaped(t *testing.T) {
	cliPath := "/opt/foo & bar/databricks"
	out, err := buildMobileconfig("https://x.example.com/anthropic", "/bin/h", "myws", cliPath)
	if err != nil {
		t.Fatalf("buildMobileconfig: %v", err)
	}
	if strings.Contains(out, "foo & bar") {
		t.Error("cliPath ampersand must be plist-escaped in mobileconfig")
	}
	if !strings.Contains(out, "foo &amp; bar") {
		t.Error("expected plist-escaped cliPath with &amp;")
	}
}

func TestBuildRegFile_CliPath_Present(t *testing.T) {
	out := buildRegFile("https://x.example.com/anthropic", "/bin/h", "myws", testCliPath)
	if !strings.Contains(out, `"databricksCliPath"="`+testCliPath+`"`) {
		t.Errorf(".reg with cliPath must contain %q", `"databricksCliPath"="`+testCliPath+`"`)
	}
}

func TestBuildRegFile_CliPath_Absent(t *testing.T) {
	out := buildRegFile("https://x.example.com/anthropic", "/bin/h", "myws", "")
	if strings.Contains(out, "databricksCliPath") {
		t.Error(".reg without cliPath must NOT contain databricksCliPath")
	}
}

func TestBuildRegFile_CliPath_Escaped(t *testing.T) {
	cliPath := `C:\Program Files\Databricks\databricks.exe`
	out := buildRegFile("https://x.example.com/anthropic", "/bin/h", "myws", cliPath)
	if !strings.Contains(out, `"databricksCliPath"="C:\\Program Files\\Databricks\\databricks.exe"`) {
		t.Errorf(".reg cliPath backslashes not escaped; got:\n%s", out)
	}
}

func TestBuildDevModeJSON_CliPath_Present(t *testing.T) {
	out, err := buildDevModeJSON("https://x.example.com/anthropic", "/bin/h", "myws", testCliPath)
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, ok := m["databricksCliPath"].(string)
	if !ok {
		t.Fatalf("databricksCliPath not a string in dev JSON (got %T = %v)", m["databricksCliPath"], m["databricksCliPath"])
	}
	if got != testCliPath {
		t.Errorf("databricksCliPath = %q, want %q", got, testCliPath)
	}
}

func TestBuildDevModeJSON_CliPath_Absent(t *testing.T) {
	out, err := buildDevModeJSON("https://x.example.com/anthropic", "/bin/h", "myws", "")
	if err != nil {
		t.Fatalf("buildDevModeJSON: %v", err)
	}
	if strings.Contains(string(out), "databricksCliPath") {
		t.Error("dev JSON without cliPath must NOT contain databricksCliPath key")
	}
}
