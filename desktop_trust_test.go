package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeTestCert generates a self-signed RSA-2048 certificate suitable for
// exercising the trust-profile generator. Returns the DER bytes plus the
// parsed *x509.Certificate (so tests can compare round-tripped values).
func makeTestCert(t *testing.T) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(424242),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}
	return der, parsed
}

// writePEM writes a PEM-wrapped CERTIFICATE block to a temp file and returns
// the path. Centralised here so tests don't repeat the wrapping boilerplate.
func writePEM(t *testing.T, der []byte, blockType string) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "cert.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(tmp, pemBytes, 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	return tmp
}

func TestTrustMobileconfigPayloadIsDataNotString(t *testing.T) {
	der, want := makeTestCert(t)
	out, err := buildTrustMobileconfig(der)
	if err != nil {
		t.Fatalf("buildTrustMobileconfig: %v", err)
	}

	if !strings.Contains(out, "<data>") {
		t.Fatalf("expected <data> element in output, got:\n%s", out)
	}

	// Bound the search to the inner pkcs1 PayloadContent block by anchoring
	// on the structural keys around it. Anything between the inner
	// "<key>PayloadContent</key>" (the second one — the inner payload's)
	// and "</dict>" must contain <data>, never <string>.
	const innerKey = "<key>PayloadContent</key>"
	first := strings.Index(out, innerKey)
	if first < 0 {
		t.Fatal("first PayloadContent key not found")
	}
	second := strings.Index(out[first+len(innerKey):], innerKey)
	if second < 0 {
		t.Fatal("second (inner) PayloadContent key not found")
	}
	innerStart := first + len(innerKey) + second + len(innerKey)
	innerEnd := strings.Index(out[innerStart:], "</dict>")
	if innerEnd < 0 {
		t.Fatal("end of inner payload dict not found")
	}
	innerSection := out[innerStart : innerStart+innerEnd]
	if !strings.Contains(innerSection, "<data>") {
		t.Errorf("inner PayloadContent missing <data>: %q", innerSection)
	}
	if strings.Contains(innerSection, "<string>") {
		t.Errorf("inner PayloadContent must use <data>, not <string>: %q", innerSection)
	}

	// Round-trip: extract base64 between <data> and </data>, decode, parse.
	dataStart := strings.Index(out, "<data>")
	dataEnd := strings.Index(out, "</data>")
	if dataStart < 0 || dataEnd < 0 || dataEnd <= dataStart {
		t.Fatalf("malformed <data> element: start=%d end=%d", dataStart, dataEnd)
	}
	rawB64 := out[dataStart+len("<data>") : dataEnd]
	// Strip whitespace introduced by the 76-col line wrap.
	cleaned := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, rawB64)
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	got, err := x509.ParseCertificate(decoded)
	if err != nil {
		t.Fatalf("parse round-tripped cert: %v", err)
	}
	if got.SerialNumber.Cmp(want.SerialNumber) != 0 {
		t.Errorf("serial mismatch: got %v, want %v", got.SerialNumber, want.SerialNumber)
	}
	if got.Subject.CommonName != want.Subject.CommonName {
		t.Errorf("CN mismatch: got %q, want %q", got.Subject.CommonName, want.Subject.CommonName)
	}
}

func TestBuildDataPlistElement_LineWrap(t *testing.T) {
	t.Run("len_200_wraps_at_76", func(t *testing.T) {
		in := strings.Repeat("A", 200)
		out := buildDataPlistElement(in)
		if !strings.HasPrefix(out, "<data>\n") {
			t.Errorf("missing <data>\\n prefix: %q", out)
		}
		if !strings.HasSuffix(out, "</data>") {
			t.Errorf("missing </data> suffix: %q", out)
		}
		// Strip the wrapper and inspect the line shape.
		body := strings.TrimSuffix(strings.TrimPrefix(out, "<data>\n"), "</data>")
		body = strings.TrimRight(body, "\n")
		lines := strings.Split(body, "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 wrapped lines for 200 chars, got %d: %v", len(lines), lines)
		}
		if len(lines[0]) != 76 || len(lines[1]) != 76 || len(lines[2]) != 48 {
			t.Errorf("unexpected line lengths: %d %d %d", len(lines[0]), len(lines[1]), len(lines[2]))
		}
	})
	t.Run("empty_input", func(t *testing.T) {
		out := buildDataPlistElement("")
		// Either "<data></data>" or "<data>\n</data>" is acceptable per spec;
		// our implementation emits the latter so the data element is visually
		// consistent with the wrapped case.
		if out != "<data>\n</data>" && out != "<data></data>" {
			t.Errorf("unexpected empty output: %q", out)
		}
	})
	t.Run("exactly_76_one_line", func(t *testing.T) {
		in := strings.Repeat("B", 76)
		out := buildDataPlistElement(in)
		body := strings.TrimSuffix(strings.TrimPrefix(out, "<data>\n"), "</data>")
		body = strings.TrimRight(body, "\n")
		lines := strings.Split(body, "\n")
		if len(lines) != 1 {
			t.Fatalf("expected 1 line for 76 chars, got %d: %v", len(lines), lines)
		}
		if len(lines[0]) != 76 {
			t.Errorf("first line len = %d, want 76", len(lines[0]))
		}
	})
	t.Run("exactly_152_two_lines", func(t *testing.T) {
		in := strings.Repeat("C", 152)
		out := buildDataPlistElement(in)
		body := strings.TrimSuffix(strings.TrimPrefix(out, "<data>\n"), "</data>")
		body = strings.TrimRight(body, "\n")
		lines := strings.Split(body, "\n")
		if len(lines) != 2 {
			t.Fatalf("expected 2 lines for 152 chars, got %d: %v", len(lines), lines)
		}
		if len(lines[0]) != 76 || len(lines[1]) != 76 {
			t.Errorf("line lengths: %d %d, want 76 76", len(lines[0]), len(lines[1]))
		}
	})
}

func TestBuildTrustMobileconfig_PlistShape(t *testing.T) {
	der, _ := makeTestCert(t)
	out, err := buildTrustMobileconfig(der)
	if err != nil {
		t.Fatalf("buildTrustMobileconfig: %v", err)
	}

	wantSubstrings := []string{
		"<key>PayloadType</key>",
		"<string>Configuration</string>",
		"<string>com.apple.security.pkcs1</string>",
		"<key>PayloadIdentifier</key>",
		"com.databricks.databricks-claude.trust",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n---\n%s", s, out)
		}
	}

	// Two distinct UUIDs: extract every <string>...UUID-shaped...</string>
	// pair by scanning all PayloadUUID keys.
	const uuidKey = "<key>PayloadUUID</key>"
	var uuids []string
	rest := out
	for {
		i := strings.Index(rest, uuidKey)
		if i < 0 {
			break
		}
		rest = rest[i+len(uuidKey):]
		open := strings.Index(rest, "<string>")
		closeIdx := strings.Index(rest, "</string>")
		if open < 0 || closeIdx < 0 || closeIdx <= open {
			break
		}
		uuids = append(uuids, rest[open+len("<string>"):closeIdx])
		rest = rest[closeIdx+len("</string>"):]
	}
	if len(uuids) < 2 {
		t.Fatalf("expected at least 2 PayloadUUID values, got %d: %v", len(uuids), uuids)
	}
	if uuids[0] == uuids[1] {
		t.Errorf("inner and outer UUIDs must differ, both = %q", uuids[0])
	}
}

func TestRunGenerateTrustProfile_BadCert(t *testing.T) {
	t.Run("non_pem_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bogus.pem")
		if err := os.WriteFile(path, []byte("not a pem file"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		out := filepath.Join(dir, "trust.mobileconfig")
		err := runGenerateTrustProfile([]string{"--cert", path, "--output", out})
		if err == nil {
			t.Fatal("expected error for non-PEM input")
		}
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Errorf("output file should not exist on error, stat err = %v", statErr)
		}
	})
	t.Run("wrong_block_type", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "wrong.pem")
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("garbage")})
		if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		out := filepath.Join(dir, "trust.mobileconfig")
		err := runGenerateTrustProfile([]string{"--cert", path, "--output", out})
		if err == nil {
			t.Fatal("expected error for wrong PEM block type")
		}
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Errorf("output file should not exist on error, stat err = %v", statErr)
		}
	})
}

func TestRunGenerateTrustProfile_AtomicWrite(t *testing.T) {
	der, _ := makeTestCert(t)
	certPath := writePEM(t, der, "CERTIFICATE")

	dir := t.TempDir()
	out := filepath.Join(dir, "trust.mobileconfig")
	if err := runGenerateTrustProfile([]string{"--cert", certPath, "--output", out}); err != nil {
		t.Fatalf("runGenerateTrustProfile: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(body)
	for _, want := range []string{"Configuration", "com.apple.security.pkcs1"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}

	// No leftover .tmp files in the output dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestExtractCertFlag(t *testing.T) {
	t.Run("present_with_value", func(t *testing.T) {
		got, _ := extractCertFlag([]string{"--cert", "/tmp/c.pem"})
		if got != "/tmp/c.pem" {
			t.Errorf("got %q, want /tmp/c.pem", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		got, _ := extractCertFlag([]string{"--other", "x"})
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("equals_form", func(t *testing.T) {
		got, _ := extractCertFlag([]string{"--cert=/tmp/eq.pem"})
		if got != "/tmp/eq.pem" {
			t.Errorf("got %q, want /tmp/eq.pem", got)
		}
	})
	t.Run("mixed_with_other_args", func(t *testing.T) {
		got, rem := extractCertFlag([]string{"--profile", "foo", "--cert", "/p.pem", "--output", "/o"})
		if got != "/p.pem" {
			t.Errorf("got %q, want /p.pem", got)
		}
		// Sanity-check remaining args still contain the non-cert flags.
		joined := strings.Join(rem, " ")
		if !strings.Contains(joined, "--profile") || !strings.Contains(joined, "--output") {
			t.Errorf("remaining lost non-cert flags: %v", rem)
		}
		if strings.Contains(joined, "--cert") {
			t.Errorf("remaining still contains --cert: %v", rem)
		}
	})
}
