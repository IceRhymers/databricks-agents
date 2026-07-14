package main

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// defaultTrustProfileOutputPath is the default --output destination for the
// trust-profile generator. Mirrors the dist/ convention used elsewhere for
// release artifacts.
const defaultTrustProfileOutputPath = "dist/databricks-claude-trust.mobileconfig"

// runGenerateTrustProfile emits a Configuration Profile (.mobileconfig) that
// establishes the .pkg signing certificate as a trusted root for code-signing
// on managed Macs. Reads a PEM-encoded x509 certificate from --cert and writes
// the rendered profile to --output (default: dist/databricks-claude-trust.mobileconfig).
//
// The PayloadContent of the inner pkcs1 payload is the DER bytes of the cert,
// emitted as a plist <data> element (NOT a <string>) per Apple's profile spec.
func runGenerateTrustProfile(args []string) error {
	log.SetOutput(io.Discard)

	certPath, _ := extractCertFlag(args)
	if certPath == "" {
		return fmt.Errorf("--cert <path> is required")
	}
	outputPath := extractOutputFlag(args)
	if outputPath == "" {
		outputPath = defaultTrustProfileOutputPath
	}

	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read cert %q: %w", certPath, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return fmt.Errorf("decode cert %q: not a PEM file", certPath)
	}
	if block.Type != "CERTIFICATE" {
		return fmt.Errorf("decode cert %q: expected PEM block type %q, got %q", certPath, "CERTIFICATE", block.Type)
	}

	plist, err := buildTrustMobileconfig(block.Bytes)
	if err != nil {
		return fmt.Errorf("build trust mobileconfig: %w", err)
	}

	// Ensure the output directory exists. dist/ is the conventional staging
	// dir but callers may pass any path; create parents on demand.
	if dir := filepath.Dir(outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %q: %w", dir, err)
		}
	}

	if err := writeFileAtomic(outputPath, []byte(plist), 0o600); err != nil {
		return fmt.Errorf("write %q: %w", outputPath, err)
	}
	fmt.Fprintf(os.Stderr, "Wrote trust profile: %s\n", outputPath)
	return nil
}

// buildTrustMobileconfig renders a Configuration Profile that installs the
// given DER-encoded certificate as a trusted code-signing root. Two distinct
// UUIDs are generated: outer profile wrapper and inner pkcs1 payload.
//
// The DER bytes are validated via x509.ParseCertificate to fail fast on
// malformed input. The base64-of-DER is line-wrapped to 76 columns inside a
// <data> element per Apple convention — using <string> here would break the
// profile silently because Apple parses pkcs1 PayloadContent as raw bytes.
//
// PayloadScope is deliberately "System" (the desktop config profile in
// desktop_config.go uses "User"). Code-signing trust must be evaluated
// system-wide so productsign / pkgutil / installer all see the cert as
// trusted, not just the installing user — do not harmonize the two scopes.
func buildTrustMobileconfig(certDER []byte) (string, error) {
	if _, err := x509.ParseCertificate(certDER); err != nil {
		return "", fmt.Errorf("parse certificate DER: %w", err)
	}

	innerUUID, err := newUUID()
	if err != nil {
		return "", err
	}
	outerUUID, err := newUUID()
	if err != nil {
		return "", err
	}

	b64 := base64.StdEncoding.EncodeToString(certDER)
	dataElem := buildDataPlistElement(b64)

	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
	<dict>
		<key>PayloadContent</key>
		<array>
			<dict>
				<key>PayloadType</key>
				<string>com.apple.security.pkcs1</string>
				<key>PayloadIdentifier</key>
				<string>com.databricks.databricks-claude.trust</string>
				<key>PayloadUUID</key>
				<string>` + innerUUID + `</string>
				<key>PayloadVersion</key>
				<integer>1</integer>
				<key>PayloadDisplayName</key>
				<string>Databricks Claude code-signing trust</string>
				<key>PayloadContent</key>
				` + dataElem + `
			</dict>
		</array>
		<key>PayloadDisplayName</key>
		<string>Databricks Claude Code-Signing Trust</string>
		<key>PayloadIdentifier</key>
		<string>com.databricks.databricks-claude.trust.profile</string>
		<key>PayloadType</key>
		<string>Configuration</string>
		<key>PayloadUUID</key>
		<string>` + outerUUID + `</string>
		<key>PayloadVersion</key>
		<integer>1</integer>
		<key>PayloadScope</key>
		<string>System</string>
	</dict>
</plist>
`, nil
}

// buildDataPlistElement wraps a base64 string at 76 columns per line and
// returns a `<data>...</data>` plist element. Pure string manipulation, no
// I/O. Apple's profile parser tolerates whitespace inside <data>; the 76-col
// wrap matches the convention used by `security cms -D` and Apple's own
// Configuration Profile export.
//
// Empty input emits "<data>\n</data>" rather than "<data></data>" so the
// output remains visually consistent with non-empty payloads.
func buildDataPlistElement(b64 string) string {
	const cols = 76
	var b strings.Builder
	b.WriteString("<data>\n")
	for i := 0; i < len(b64); i += cols {
		end := i + cols
		if end > len(b64) {
			end = len(b64)
		}
		b.WriteString(b64[i:end])
		b.WriteString("\n")
	}
	b.WriteString("</data>")
	return b.String()
}

// extractCertFlag scans args for --cert / --cert=value and returns the path.
// The remaining slice strips the consumed tokens — kept for symmetry with the
// other extract* helpers, but callers that only need the value can ignore it.
func extractCertFlag(args []string) (certPath string, remaining []string) {
	remaining = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--cert" && i+1 < len(args) {
			certPath = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(a, "--cert=") {
			certPath = strings.TrimPrefix(a, "--cert=")
			continue
		}
		remaining = append(remaining, a)
	}
	return certPath, remaining
}
