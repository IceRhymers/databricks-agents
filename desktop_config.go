package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
	"github.com/IceRhymers/databricks-claude/pkg/cli"
	"github.com/IceRhymers/databricks-claude/pkg/mdmprofile"
)

// uuidGenerator is the UUID factory used by buildMobileconfig. Overridable in
// tests to produce deterministic output for golden-file comparison.
var uuidGenerator = newUUID

// daemonFakeKeyDefault is the fleet-shared static fake gateway key used in
// daemon-mode when --daemon-fake-key is not set. The daemon validates the
// proxy connection via localhost IP binding, not a real credential.
// This value is intentionally public — it is a localhost gate, not a secret.
const daemonFakeKeyDefault = "databricks-claude-daemon-localhost-key"

// daemonFakeKeyWarning is printed to stderr when --daemon is set but
// --daemon-fake-key is not, alerting the admin that the default constant is in use.
const daemonFakeKeyWarning = `databricks-claude: WARNING: --daemon-fake-key not set, using default fleet-shared constant.
  The fake key is a localhost gate, not a real credential — Claude Desktop just needs
  ANY non-empty value to satisfy its gatewayApiKey schema. For fleet rollouts you may
  want to set a custom value via --daemon-fake-key so per-fleet artifacts differ.
`

// modeKeys holds the inference-provider key set for one deployment mode.
// Construct via helperModeKeys or daemonModeKeys — never fill directly.
// All three artifact builders (buildMobileconfig, buildRegFile,
// buildDevModeJSON) consume this struct as their single source of truth,
// so adding or removing a key here propagates automatically to every format.
type modeKeys struct {
	GatewayBaseURL string // mandatory in both modes
	GatewayAPIKey  string // daemon-mode only; "" → omit
	CredHelper     string // helper-mode only; "" → signals daemon-mode
	CredHelperTTL  int    // helper-mode: 55; daemon-mode: 0 → omit
	OTELEndpoint   string // daemon+otel only; "" → omit
	OTELProtocol   string // daemon+otel only; "" → omit
}

// helperModeKeys returns the inference key set for helper-mode (the default).
// Changing this function changes ALL three artifact formats simultaneously,
// which is the point: it is the single source of truth for helper-mode output.
func helperModeKeys(gatewayURL, helperPath string) modeKeys {
	return modeKeys{
		GatewayBaseURL: gatewayURL,
		CredHelper:     helperPath,
		CredHelperTTL:  55,
	}
}

// daemonModeKeys returns the inference key set for daemon-mode (--daemon opt-in).
// withOTEL adds OTLP endpoint/protocol keys pointing at the same localhost port.
func daemonModeKeys(port int, fakeKey string, withOTEL bool) modeKeys {
	k := modeKeys{
		GatewayBaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		GatewayAPIKey:  fakeKey,
	}
	if withOTEL {
		k.OTELEndpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		k.OTELProtocol = "http/protobuf"
	}
	return k
}

// helperDebugLog appends a single diagnostic line to
// ~/Library/Logs/databricks-claude/credential-helper.log (or the platform
// equivalent). Best-effort: failures are silent. Used only to diagnose how
// Claude Desktop spawns the helper.
func helperDebugLog(format string, args ...any) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	dir := filepath.Join(home, "Library", "Logs", "databricks-claude")
	if runtime.GOOS != "darwin" {
		dir = filepath.Join(home, ".cache", "databricks-claude")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "credential-helper.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pid=%d ppid=%d ", time.Now().Format(time.RFC3339Nano), os.Getpid(), os.Getppid())
	fmt.Fprintf(f, format, args...)
	fmt.Fprintln(f)
}

// inferenceModelsJSON is the JSON-encoded model list embedded in the generated
// Claude Desktop configuration. Kept as a single source of truth so the macOS,
// Windows, and developer-mode JSON generators stay aligned.
const inferenceModelsJSON = `[{"name":"databricks-claude-opus-4-7","supports1m":true},{"name":"databricks-claude-opus-4-6","supports1m":true},{"name":"databricks-claude-sonnet-4-6","supports1m":true},{"name":"databricks-claude-sonnet-4-5","supports1m":true},{"name":"databricks-claude-haiku-4-5"}]`

// desktopDeveloperModeArticleURL is the canonical Anthropic support article
// describing how to import a JSON config into Claude Desktop's developer mode.
// Pinned in one place so updates are a single-line edit.
const desktopDeveloperModeArticleURL = "https://support.claude.com/en/articles/14680741-install-and-configure-claude-cowork-with-third-party-platforms"

// credentialHelperBinaryName is the basename used to dispatch into
// runCredentialHelper via argv[0]. Each install method is expected to install
// a symlink (or hard copy) at this name pointing at the main binary so that
// Claude Desktop's mobileconfig — which can only specify a path, not args —
// can target it directly.
const credentialHelperBinaryName = "databricks-claude-credential-helper"

// MacOSCanonicalBinaryDir is the canonical install dir for the .pkg-shipped binary on macOS.
// It is the source of truth for the path baked into the postinstall script and the
// mobileconfig generated with --for-pkg. Changing this requires updating
// .github/workflows/release.yml and the locking test in desktop_path_test.go.
const MacOSCanonicalBinaryDir = "/usr/local/bin"
const MacOSCanonicalHelperPath = MacOSCanonicalBinaryDir + "/" + credentialHelperBinaryName

// isCredentialHelperBinaryName returns true if the program was invoked under
// the credential-helper alias. Pass os.Args[0] (or any argv[0]-like value).
func isCredentialHelperBinaryName(arg0 string) bool {
	base := filepath.Base(arg0)
	// On Windows the symlink name will carry an .exe suffix.
	base = strings.TrimSuffix(base, ".exe")
	return base == credentialHelperBinaryName
}

// runDesktopCommand handles the `databricks-claude desktop ...` subcommand.
// args is everything after the literal "desktop" token in os.Args.
func runDesktopCommand(args []string) {
	if len(args) == 0 {
		printDesktopHelp()
		os.Exit(2)
	}
	switch args[0] {
	case "-h", "--help":
		printDesktopHelp()
		os.Exit(0)
	case "generate-config":
		forPkg, _ := extractForPkgFlag(args[1:])
		daemon := extractDaemonFlag(args[1:])
		port := extractDesktopPortFlag(args[1:])
		fakeKey := extractDaemonFakeKeyFlag(args[1:])
		withOTEL := hasFlag(args[1:], "--otel")
		runGenerateDesktopConfig(
			extractProfileFlag(args[1:]),
			extractOutputFlag(args[1:]),
			extractBinaryPathFlag(args[1:]),
			extractDatabricksCLIPathFlag(args[1:]),
			forPkg, daemon, port, fakeKey, withOTEL,
		)
	case "credential-helper":
		runCredentialHelper(extractProfileFlag(args[1:]))
	case "generate-trust-profile":
		if err := runGenerateTrustProfile(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "databricks-claude: unknown desktop action %q\n\n", args[0])
		printDesktopHelp()
		os.Exit(1)
	}
}

func printDesktopHelp() {
	fmt.Fprint(os.Stderr, `Usage: databricks-claude desktop <action> [flags]

Set up Claude Desktop's third-party-inference integration with Databricks.

Actions:
  generate-config     Write Claude Desktop configuration artifacts. Without
                      --output, writes three files into the current directory.
                      All three encode the same Databricks gateway and
                      credential-helper defaults:
                        databricks-claude-desktop.mobileconfig (install on macOS)
                        databricks-claude-desktop.reg          (install on Windows)
                        databricks-claude-desktop.json         (editable source —
                                                                import into Claude
                                                                Desktop developer
                                                                mode to customize
                                                                further, then
                                                                re-export for MDM)
  credential-helper   Print a fresh Databricks token to stdout — the same code
                      path Claude Desktop's inferenceCredentialHelper invokes
                      via the databricks-claude-credential-helper symlink.
                      Useful for scripting and debug.
  generate-trust-profile
                      Emit a Configuration Profile (.mobileconfig) that
                      establishes the .pkg signing certificate as a trusted
                      root for code-signing on managed Macs. Pair with the
                      signed .pkg in your MDM rollout so Gatekeeper accepts
                      the installer without per-device prompts.

Flags:
  --profile string              Databricks CLI profile (default: state file > DEFAULT)
  --output string               Single output path for generate-config; format
                                inferred from .mobileconfig/.reg/.json extension
                                or host OS. Also the output path for
                                generate-trust-profile (default:
                                dist/databricks-claude-trust.mobileconfig).
  --binary-path string          generate-config: credential-helper path embedded in
                                the generated config (default: derived from the
                                running binary). Use this for MDM rollouts so one
                                config works on every endpoint.
  --databricks-cli-path string  generate-config: pin the absolute path of the
                                'databricks' CLI used by the credential helper.
                                Persisted to ~/.claude/.databricks-claude.json.
  --cert string                 generate-trust-profile: path to a PEM-encoded
                                x509 certificate (the .pkg signing cert) to
                                wrap as a trusted root.
  --daemon                      generate-config: emit daemon-mode artifacts pointing
                                at a local 'databricks-claude serve' daemon instead
                                of the Databricks AI Gateway. Default: helper-mode.
  --port int                    generate-config --daemon: daemon port for
                                gatewayBaseUrl (default: state file > 49153).
  --daemon-fake-key string      generate-config --daemon: static API key embedded
                                in artifacts (localhost gate, not a real credential).
                                Default: built-in constant with a banner warning.

Examples:
  # First-time setup on your Mac.
  databricks-claude desktop generate-config --profile myws

  # MDM rollout — bake fleet-wide paths into one config.
  databricks-claude desktop generate-config --profile myws \
    --binary-path /usr/local/bin/databricks-claude-credential-helper \
    --databricks-cli-path /usr/local/bin/databricks

  # Print a token directly (debug; equivalent to invoking the helper symlink).
  databricks-claude desktop credential-helper --profile myws

  # Emit a code-signing trust profile for MDM (pairs with a signed .pkg).
  databricks-claude desktop generate-trust-profile \
    --cert ./codesign-cert.pem \
    --output dist/databricks-claude-trust.mobileconfig
`)
}

// mdmReader is the MDM profile-reader function. Overridable in tests so the
// credential-helper resolution chain can be exercised on linux/CI without
// requiring a real darwin/windows managed-prefs surface.
var mdmReader = mdmprofile.Read

// resolveCredHelperProfile implements the flag → state → MDM → "DEFAULT"
// resolution chain. A "DEFAULT" value in state.Profile is treated as empty so
// a stale state file cannot permanently bypass the MDM tier.
func resolveCredHelperProfile(profile string) string {
	state := loadState()
	resolved := profile
	if resolved == "" {
		if state.Profile != "" && state.Profile != "DEFAULT" {
			resolved = state.Profile
			helperDebugLog("profile from state file=%q", resolved)
		} else if state.Profile == "DEFAULT" {
			helperDebugLog("state.Profile=DEFAULT skipped (sentinel)")
		}
	}
	if resolved == "" {
		if mdmProfile, err := mdmReader("com.icerhymers.databricks-claude"); err == nil && mdmProfile != "" {
			resolved = mdmProfile
			helperDebugLog("profile from MDM=%q", resolved)
		}
	}
	if resolved == "" {
		resolved = "DEFAULT"
	}
	return resolved
}

// runCredentialHelper fetches a fresh Databricks OAuth token and writes only
// the raw token to stdout. Intended to be called by Claude Desktop via the
// inferenceCredentialHelper MDM key. Stays silent on stderr on success.
//
// Profile resolution: explicit --profile flag > saved state file (skipping the
// "DEFAULT" sentinel) > MDM managed preferences > "DEFAULT".
func runCredentialHelper(profile string) {
	// Wire the helper-specific MDM logger so the resolution chain's per-tier
	// outcomes land in ~/Library/Logs/databricks-claude/credential-helper.log.
	// The reader itself is wired in main.go above the early-exit dispatchers,
	// so it's already populated by the time we get here.
	cli.SetMDMLogger(helperDebugLog)

	// Suppress all stdlib logging so the upstream tokencache cannot leak
	// anything onto stderr while Claude Desktop is watching.
	log.SetOutput(io.Discard)

	helperDebugLog("invoked args=%q HOME=%q PATH=%q USER=%q",
		os.Args, os.Getenv("HOME"), os.Getenv("PATH"), os.Getenv("USER"))

	resolved := resolveCredHelperProfile(profile)
	state := loadState()
	helperDebugLog("profile resolved=%q (input=%q) cli_path=%q", resolved, profile, state.DatabricksCLIPath)

	// state.DatabricksCLIPath ("" → fall through to PATH/fallback scan in
	// resolveDatabricksCLI) overrides the default "databricks" lookup.
	tp := NewTokenProvider(resolved, state.DatabricksCLIPath)
	helperDebugLog("tp.Token first attempt profile=%q", resolved)
	tok, err := tp.Token(context.Background())
	if err != nil {
		helperDebugLog("tp.Token first attempt FAIL profile=%q err=%v — invoking EnsureAuthenticated", resolved, err)
		// Route login subprocess stdout to our stderr so Desktop's bare-token
		// contract on our stdout is preserved.
		if authErr := authcheck.EnsureAuthenticatedWithStdout(resolved, state.DatabricksCLIPath, os.Stderr); authErr != nil {
			helperDebugLog("EnsureAuthenticated FAIL profile=%q err=%v", resolved, authErr)
			fmt.Fprintf(os.Stderr, "databricks-claude: credential helper authentication failed: %v\n", authErr)
			os.Exit(1)
		}
		helperDebugLog("tp.Token retry profile=%q", resolved)
		tok, err = tp.Token(context.Background())
		if err != nil {
			helperDebugLog("tp.Token retry FAIL profile=%q err=%v", resolved, err)
			fmt.Fprintf(os.Stderr, "databricks-claude: credential helper failed after re-authentication: %v\n", err)
			os.Exit(1)
		}
		helperDebugLog("tp.Token retry OK profile=%q", resolved)
	} else {
		helperDebugLog("tp.Token first attempt OK profile=%q", resolved)
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		helperDebugLog("FAIL profile=%q empty token", resolved)
		fmt.Fprintln(os.Stderr, "databricks-claude: credential helper got empty token")
		os.Exit(1)
	}
	helperDebugLog("OK profile=%q tok_len=%d tok_prefix=%q", resolved, len(tok), tok[:min(20, len(tok))])
	// Write raw token, no trailing newline. Desktop reads stdout verbatim.
	if _, err := io.WriteString(os.Stdout, tok); err != nil {
		helperDebugLog("FAIL stdout write err=%v", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runGenerateDesktopConfig discovers the AI Gateway URL for the active profile
// and writes a platform-appropriate Claude Desktop MDM config file.
//
// On darwin → .mobileconfig (Apple Configuration Profile).
// On windows → .reg (Windows Registry script).
// On other OSes both are written so the user can transfer them.
//
// If outputPath is non-empty, that single path is used (the platform is
// inferred from the file extension when present, otherwise from runtime.GOOS).
//
// binaryPathOverride lets MDM admins bake a fleet-wide path into the generated
// config (e.g. /usr/local/bin/databricks-claude-credential-helper) so the same
// .mobileconfig works on every endpoint regardless of the generating user's
// install layout. When empty, the path is derived from the running binary.
//
// databricksCLIPath, when non-empty, is persisted to the state file so the
// credential helper subprocess (which has no way to receive flags) can pin
// the `databricks` binary location. Useful when the CLI is installed at a
// non-standard path that the fallback dir scan in resolveDatabricksCLI
// wouldn't find.
//
// When outputPath is empty, both .mobileconfig and .reg are always written so
// one invocation produces artifacts for every supported Claude Desktop
// platform. Use --output to write a single specific file.
func runGenerateDesktopConfig(profile, outputPath, binaryPathOverride, databricksCLIPath string, forPkg, daemon bool, portOverride int, fakeKey string, withOTEL bool) {
	log.SetOutput(io.Discard)

	resolved := profile
	if resolved == "" {
		if saved := loadState(); saved.Profile != "" {
			resolved = saved.Profile
		}
	}
	if resolved == "" {
		resolved = "DEFAULT"
	}

	// Persist resolved profile so subsequent local databricks-claude invocations
	// on the generating machine (without --profile) use the same workspace.
	// Skip when resolved is "" or "DEFAULT" — that is a sentinel meaning
	// "fall through the chain", not a real user choice (mirrors ensureconfig.go:42).
	{
		st := loadState()
		if resolved != "" && resolved != "DEFAULT" && st.Profile != resolved {
			st.Profile = resolved
			if err := saveState(st); err != nil {
				fmt.Fprintf(os.Stderr, "databricks-claude: warning: failed to persist profile to state: %v\n", err)
			}
		}
	}

	// Validate and persist the databricks-cli-path BEFORE network calls so a
	// bad path fails fast.
	if databricksCLIPath != "" {
		if !filepath.IsAbs(databricksCLIPath) {
			fmt.Fprintf(os.Stderr, "databricks-claude: --databricks-cli-path must be absolute, got %q\n", databricksCLIPath)
			os.Exit(1)
		}
		if !isExecutableFile(databricksCLIPath) {
			fmt.Fprintf(os.Stderr, "databricks-claude: --databricks-cli-path %q is not an executable file\n", databricksCLIPath)
			os.Exit(1)
		}
		st := loadState()
		st.DatabricksCLIPath = databricksCLIPath
		if err := saveState(st); err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: failed to persist databricks-cli-path: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Pinned databricks CLI path: %s\n", databricksCLIPath)
	}

	// Resolve daemon port: flag > state.Port > defaultPort
	resolvedPort := resolvePort(portOverride, loadState())

	// Build mode keys and emit daemon warning if needed.
	var keys modeKeys
	if daemon {
		if fakeKey == "" {
			fakeKey = daemonFakeKeyDefault
			fmt.Fprint(os.Stderr, daemonFakeKeyWarning)
		}
		keys = daemonModeKeys(resolvedPort, fakeKey, withOTEL)
	} else {
		host, err := DiscoverHost(resolved, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: failed to discover host for profile %q: %v\n", resolved, err)
			fmt.Fprintf(os.Stderr, "Run 'databricks auth login --profile %s' first.\n", resolved)
			os.Exit(1)
		}
		gatewayURL := ConstructGatewayURL(host)

		helperPath, err := resolveHelperPath(binaryPathOverride, forPkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: %v\n", err)
			os.Exit(1)
		}
		keys = helperModeKeys(gatewayURL, helperPath)
	}

	daemonPort := 0
	if daemon {
		daemonPort = resolvedPort
	}

	if outputPath != "" {
		if err := writeDesktopConfigByPath(outputPath, keys, resolved, databricksCLIPath, daemonPort); err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Wrote Claude Desktop config: %s\n", outputPath)
		printInstallInstructions(outputPath)
		os.Exit(0)
	}

	// When --output isn't given, write all three artifacts so a single
	// invocation produces install-ready files for both desktop platforms
	// plus an editable source:
	//   - .mobileconfig: ready-to-install macOS configuration profile
	//   - .reg:          ready-to-merge Windows registry script
	//   - .json:         editable source for Claude Desktop developer mode.
	//                    Users who want to customize allow-lists, tools,
	//                    branding, etc. import this, edit in Desktop's UI,
	//                    and export back to .mobileconfig / .reg for MDM.
	// All three encode the same Databricks defaults; pick any one to
	// install or use the .json as the starting point for customization.
	type artifact struct {
		path    string
		content []byte
	}
	mc, err := buildMobileconfig(keys, resolved, databricksCLIPath, daemonPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude: %v\n", err)
		os.Exit(1)
	}
	dev, err := buildDevModeJSON(keys, resolved, databricksCLIPath, daemonPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "databricks-claude: %v\n", err)
		os.Exit(1)
	}
	arts := []artifact{
		{"databricks-claude-desktop.mobileconfig", []byte(mc)},
		{"databricks-claude-desktop.reg", []byte(buildRegFile(keys, resolved, databricksCLIPath, daemonPort))},
		{"databricks-claude-desktop.json", dev},
	}
	wrote := []string{}
	for _, a := range arts {
		if err := writeFileAtomic(a.path, a.content, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "databricks-claude: write %s: %v\n", a.path, err)
			os.Exit(1)
		}
		wrote = append(wrote, a.path)
	}

	for _, p := range wrote {
		fmt.Fprintf(os.Stderr, "Wrote Claude Desktop config: %s\n", p)
	}
	for _, p := range wrote {
		printInstallInstructions(p)
	}
	os.Exit(0)
}

// resolveHelperPath returns the absolute path embedded into the generated
// Claude Desktop config. Order:
//  1. explicit override (e.g. /usr/local/bin/databricks-claude-credential-helper)
//  2. when forPkg && darwin: the canonical .pkg install path
//     (MacOSCanonicalHelperPath). Used for MDM rollouts so the same artifact
//     works on every endpoint regardless of where the generating user ran the
//     binary from.
//  3. derived from the running binary: replace its basename with the
//     credential-helper alias name, preserving the install dir and any .exe
//     suffix on Windows.
func resolveHelperPath(override string, forPkg bool) (string, error) {
	if override != "" {
		return override, nil
	}
	if forPkg && runtime.GOOS == "darwin" {
		return MacOSCanonicalHelperPath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot resolve own executable path: %w", err)
	}
	dir := filepath.Dir(exe)
	name := credentialHelperBinaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name), nil
}

// writeDesktopConfigByPath chooses the format based on file extension (or the
// host OS when no recognised extension is present) and writes to outputPath
// atomically. For .json outputs, guardDevJSONOutputPath protects against
// accidentally clobbering ~/.claude/settings.json via a typo.
func writeDesktopConfigByPath(outputPath string, keys modeKeys, profile, cliPath string, daemonPort int) error {
	lower := strings.ToLower(outputPath)
	var data []byte
	var err error
	switch {
	case strings.HasSuffix(lower, ".mobileconfig"):
		var s string
		s, err = buildMobileconfig(keys, profile, cliPath, daemonPort)
		data = []byte(s)
	case strings.HasSuffix(lower, ".reg"):
		data = []byte(buildRegFile(keys, profile, cliPath, daemonPort))
	case strings.HasSuffix(lower, ".json"):
		if err := guardDevJSONOutputPath(outputPath); err != nil {
			return err
		}
		data, err = buildDevModeJSON(keys, profile, cliPath, daemonPort)
	default:
		// Fall back to host platform.
		if runtime.GOOS == "windows" {
			data = []byte(buildRegFile(keys, profile, cliPath, daemonPort))
		} else {
			var s string
			s, err = buildMobileconfig(keys, profile, cliPath, daemonPort)
			data = []byte(s)
		}
	}
	if err != nil {
		return err
	}
	return writeFileAtomic(outputPath, data, 0o600)
}

// printInstallInstructions writes user-facing guidance for the produced file
// to stderr (so it doesn't pollute stdout if anything is piping the binary).
func printInstallInstructions(path string) {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".mobileconfig"):
		fmt.Fprintf(os.Stderr, `
To install on macOS:
  1. Open the file:    open %q
  2. System Settings → Privacy & Security → Profiles → install the
     "Claude Desktop Third-Party Inference" profile.
  3. Restart Claude Desktop.
`, path)
	case strings.HasSuffix(lower, ".reg"):
		fmt.Fprintf(os.Stderr, `
To install on Windows:
  1. Double-click %q (or run: reg import "%s") to merge the keys
     into HKEY_CURRENT_USER\SOFTWARE\Policies\Claude.
  2. Restart Claude Desktop.
`, path, path)
	case strings.HasSuffix(lower, ".json"):
		libDir := "~/Library/Application Support/Claude-3p/configLibrary/"
		if runtime.GOOS == "windows" {
			libDir = "%APPDATA%\\Claude-3p\\configLibrary\\"
		}
		fmt.Fprintf(os.Stderr, `
To customize the configuration further (allow-lists, tools, branding, etc.),
load this JSON into Claude Desktop's developer mode:

  1. Enable developer mode:
     Help → Troubleshooting → Enable Developer mode
  2. Open the third-party inference UI:
     Developer → Configure third-party inference
  3. Click the configuration name in the top-right and choose
     "New configuration", then give it a name (e.g. "Databricks").
  4. From the same dropdown, choose "Reveal in Finder" (macOS) or
     "Reveal in Explorer" (Windows). This opens:
       %s
     The new configuration is stored as <uuid>.json inside that folder.
  5. Replace that <uuid>.json file's contents with %q
     (keep the original filename — only the contents change).
  6. Switch back to Claude Desktop, select your new configuration, and
     edit any Claude Desktop configuration keys in the UI.
  7. Use Desktop's "Export" action to write the edited config out as
     .mobileconfig (macOS) or .reg (Windows). Ship that to MDM
     (Jamf, Kandji, Intune, Group Policy).
  8. Restart Claude Desktop, or distribute the exported file to your fleet.

The defaults in this JSON are sufficient on their own; only use this flow
if you need to customize beyond the Databricks gateway + credential helper.

Note: Claude Desktop does not have a "Import JSON" UI today — file
replacement under configLibrary/ is the supported import path.

Reference (full list of Claude Desktop configuration keys): %s
`, libDir, path, desktopDeveloperModeArticleURL)
	}
}

// newUUID returns an RFC 4122 v4 UUID string built from 16 random bytes.
// Pure stdlib (crypto/rand) — no third-party dependency.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08X-%04X-%04X-%04X-%012X",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// buildMobileconfig renders the macOS Claude Desktop Configuration Profile.
// Dispatches to helper-mode or daemon-mode based on keys.CredHelper.
func buildMobileconfig(keys modeKeys, profile, cliPath string, daemonPort int) (string, error) {
	if keys.CredHelper != "" {
		return buildMobileconfigHelperMode(keys.GatewayBaseURL, keys.CredHelper, profile, cliPath)
	}
	return buildMobileconfigDaemonMode(keys, profile, cliPath, daemonPort)
}

// buildMobileconfigHelperMode is the original buildMobileconfig implementation,
// preserved verbatim to guarantee byte-identical output in helper-mode.
func buildMobileconfigHelperMode(gatewayURL, helperPath, profile, cliPath string) (string, error) {
	innerUUID, err := uuidGenerator()
	if err != nil {
		return "", err
	}
	ourUUID, err := uuidGenerator()
	if err != nil {
		return "", err
	}
	outerUUID, err := uuidGenerator()
	if err != nil {
		return "", err
	}

	cliPathXML := ""
	if cliPath != "" {
		cliPathXML = "\n\t\t\t\t<key>databricksCliPath</key>\n\t\t\t\t<string>" + plistEscape(cliPath) + "</string>"
	}

	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
	<dict>
		<key>PayloadContent</key>
		<array>
			<dict>
				<key>PayloadType</key>
				<string>com.anthropic.claudefordesktop</string>
				<key>PayloadIdentifier</key>
				<string>com.anthropic.claudefordesktop.settings</string>
				<key>PayloadUUID</key>
				<string>` + innerUUID + `</string>
				<key>PayloadVersion</key>
				<integer>1</integer>
				<key>PayloadDisplayName</key>
				<string>Claude Desktop</string>
				<key>disableDeploymentModeChooser</key>
				<true/>
				<key>inferenceProvider</key>
				<string>gateway</string>
				<key>inferenceGatewayBaseUrl</key>
				<string>` + plistEscape(gatewayURL) + `</string>
				<key>inferenceGatewayAuthScheme</key>
				<string>bearer</string>
				<key>inferenceModels</key>
				<string>` + plistEscape(inferenceModelsJSON) + `</string>
				<key>inferenceCredentialHelper</key>
				<string>` + plistEscape(helperPath) + `</string>
				<key>inferenceCredentialHelperTtlSec</key>
				<integer>55</integer>
				<key>isClaudeCodeForDesktopEnabled</key>
				<true/>
				<key>isDesktopExtensionEnabled</key>
				<true/>
				<key>isDesktopExtensionDirectoryEnabled</key>
				<true/>
				<key>isDesktopExtensionSignatureRequired</key>
				<false/>
				<key>isLocalDevMcpEnabled</key>
				<true/>
				<key>disableAutoUpdates</key>
				<false/>
				<key>disableEssentialTelemetry</key>
				<false/>
				<key>disableNonessentialTelemetry</key>
				<false/>
				<key>disableNonessentialServices</key>
				<false/>
			</dict>
			<dict>
				<key>PayloadType</key>
				<string>com.icerhymers.databricks-claude</string>
				<key>PayloadIdentifier</key>
				<string>com.icerhymers.databricks-claude.settings</string>
				<key>PayloadUUID</key>
				<string>` + ourUUID + `</string>
				<key>PayloadVersion</key>
				<integer>1</integer>
				<key>PayloadDisplayName</key>
				<string>Databricks Claude Settings</string>
				<key>databricksProfile</key>
				<string>` + plistEscape(profile) + `</string>` + cliPathXML + `
			</dict>
		</array>
		<key>PayloadDisplayName</key>
		<string>Claude Desktop Third-Party Inference</string>
		<key>PayloadIdentifier</key>
		<string>com.anthropic.claudefordesktop.profile</string>
		<key>PayloadType</key>
		<string>Configuration</string>
		<key>PayloadUUID</key>
		<string>` + outerUUID + `</string>
		<key>PayloadVersion</key>
		<integer>1</integer>
		<key>PayloadScope</key>
		<string>User</string>
	</dict>
</plist>
`, nil
}

// buildMobileconfigDaemonMode renders the daemon-mode variant of the macOS
// Configuration Profile. Omits inferenceCredentialHelper / TTL; emits
// gatewayBaseUrl pointing at 127.0.0.1:<port> with a static gatewayApiKey.
// Adds daemonPort + daemonMode to the com.icerhymers.databricks-claude payload.
// daemonPort is a future-use field: the daemon currently does not read it from
// MDM (it reads state.Port at startup), but endpoint tooling will use it in a
// future issue to cross-check that the daemon is listening on the expected port.
func buildMobileconfigDaemonMode(keys modeKeys, profile, cliPath string, daemonPort int) (string, error) {
	innerUUID, err := uuidGenerator()
	if err != nil {
		return "", err
	}
	ourUUID, err := uuidGenerator()
	if err != nil {
		return "", err
	}
	outerUUID, err := uuidGenerator()
	if err != nil {
		return "", err
	}

	var b strings.Builder

	// Anthropic payload inference section — mode-specific keys only.
	var inferenceXML strings.Builder
	fmt.Fprintf(&inferenceXML, "\t\t\t\t<key>inferenceGatewayBaseUrl</key>\n\t\t\t\t<string>%s</string>\n", plistEscape(keys.GatewayBaseURL))
	if keys.GatewayAPIKey != "" {
		fmt.Fprintf(&inferenceXML, "\t\t\t\t<key>inferenceGatewayApiKey</key>\n\t\t\t\t<string>%s</string>\n", plistEscape(keys.GatewayAPIKey))
	}
	inferenceXML.WriteString("\t\t\t\t<key>inferenceGatewayAuthScheme</key>\n\t\t\t\t<string>bearer</string>\n")
	fmt.Fprintf(&inferenceXML, "\t\t\t\t<key>inferenceModels</key>\n\t\t\t\t<string>%s</string>\n", plistEscape(inferenceModelsJSON))
	if keys.OTELEndpoint != "" {
		fmt.Fprintf(&inferenceXML, "\t\t\t\t<key>otlpEndpoint</key>\n\t\t\t\t<string>%s</string>\n", plistEscape(keys.OTELEndpoint))
		fmt.Fprintf(&inferenceXML, "\t\t\t\t<key>otlpProtocol</key>\n\t\t\t\t<string>%s</string>\n", plistEscape(keys.OTELProtocol))
	}

	// IceRhymers payload — same as helper-mode plus daemonPort/daemonMode.
	var ourXML strings.Builder
	fmt.Fprintf(&ourXML, "\t\t\t\t<key>databricksProfile</key>\n\t\t\t\t<string>%s</string>", plistEscape(profile))
	if cliPath != "" {
		fmt.Fprintf(&ourXML, "\n\t\t\t\t<key>databricksCliPath</key>\n\t\t\t\t<string>%s</string>", plistEscape(cliPath))
	}
	// daemonPort and daemonMode are emitted for endpoint tooling to cross-check.
	// pkg/mdmprofile does not yet read them (future issue); daemonMode is a
	// boolean sentinel for any future endpoint-side conditional.
	if daemonPort > 0 {
		fmt.Fprintf(&ourXML, "\n\t\t\t\t<key>daemonPort</key>\n\t\t\t\t<integer>%d</integer>", daemonPort)
		ourXML.WriteString("\n\t\t\t\t<key>daemonMode</key>\n\t\t\t\t<true/>")
	}

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
	<dict>
		<key>PayloadContent</key>
		<array>
			<dict>
				<key>PayloadType</key>
				<string>com.anthropic.claudefordesktop</string>
				<key>PayloadIdentifier</key>
				<string>com.anthropic.claudefordesktop.settings</string>
				<key>PayloadUUID</key>
				<string>` + innerUUID + `</string>
				<key>PayloadVersion</key>
				<integer>1</integer>
				<key>PayloadDisplayName</key>
				<string>Claude Desktop</string>
				<key>disableDeploymentModeChooser</key>
				<true/>
				<key>inferenceProvider</key>
				<string>gateway</string>
`)
	b.WriteString(inferenceXML.String())
	b.WriteString(`				<key>isClaudeCodeForDesktopEnabled</key>
				<true/>
				<key>isDesktopExtensionEnabled</key>
				<true/>
				<key>isDesktopExtensionDirectoryEnabled</key>
				<true/>
				<key>isDesktopExtensionSignatureRequired</key>
				<false/>
				<key>isLocalDevMcpEnabled</key>
				<true/>
				<key>disableAutoUpdates</key>
				<false/>
				<key>disableEssentialTelemetry</key>
				<false/>
				<key>disableNonessentialTelemetry</key>
				<false/>
				<key>disableNonessentialServices</key>
				<false/>
			</dict>
			<dict>
				<key>PayloadType</key>
				<string>com.icerhymers.databricks-claude</string>
				<key>PayloadIdentifier</key>
				<string>com.icerhymers.databricks-claude.settings</string>
				<key>PayloadUUID</key>
				<string>` + ourUUID + `</string>
				<key>PayloadVersion</key>
				<integer>1</integer>
				<key>PayloadDisplayName</key>
				<string>Databricks Claude Settings</string>
`)
	b.WriteString(ourXML.String())
	b.WriteString(`
			</dict>
		</array>
		<key>PayloadDisplayName</key>
		<string>Claude Desktop Third-Party Inference</string>
		<key>PayloadIdentifier</key>
		<string>com.anthropic.claudefordesktop.profile</string>
		<key>PayloadType</key>
		<string>Configuration</string>
		<key>PayloadUUID</key>
		<string>` + outerUUID + `</string>
		<key>PayloadVersion</key>
		<integer>1</integer>
		<key>PayloadScope</key>
		<string>User</string>
	</dict>
</plist>
`)
	return b.String(), nil
}

// plistEscape escapes characters that are illegal inside a plist <string>
// element: &, <, >. Quotes/apostrophes don't strictly need escaping inside
// element content but we encode them defensively for safety.
func plistEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

func buildRegFile(keys modeKeys, profile, cliPath string, daemonPort int) string {
	var b strings.Builder
	b.WriteString("Windows Registry Editor Version 5.00\r\n\r\n")
	b.WriteString("[HKEY_CURRENT_USER\\SOFTWARE\\Policies\\Claude]\r\n")
	b.WriteString(`"disableDeploymentModeChooser"=dword:00000001` + "\r\n")
	b.WriteString(`"inferenceProvider"="gateway"` + "\r\n")
	fmt.Fprintf(&b, "\"inferenceGatewayBaseUrl\"=\"%s\"\r\n", regEscape(keys.GatewayBaseURL))
	if keys.GatewayAPIKey != "" {
		fmt.Fprintf(&b, "\"inferenceGatewayApiKey\"=\"%s\"\r\n", regEscape(keys.GatewayAPIKey))
	}
	b.WriteString(`"inferenceGatewayAuthScheme"="bearer"` + "\r\n")
	fmt.Fprintf(&b, "\"inferenceModels\"=\"%s\"\r\n", regEscape(inferenceModelsJSON))
	if keys.CredHelper != "" {
		fmt.Fprintf(&b, "\"inferenceCredentialHelper\"=\"%s\"\r\n", regEscape(keys.CredHelper))
		fmt.Fprintf(&b, "\"inferenceCredentialHelperTtlSec\"=\"%d\"\r\n", keys.CredHelperTTL)
	}
	if keys.OTELEndpoint != "" {
		fmt.Fprintf(&b, "\"otlpEndpoint\"=\"%s\"\r\n", regEscape(keys.OTELEndpoint))
		fmt.Fprintf(&b, "\"otlpProtocol\"=\"%s\"\r\n", regEscape(keys.OTELProtocol))
	}
	b.WriteString(`"isClaudeCodeForDesktopEnabled"=dword:00000001` + "\r\n")
	b.WriteString(`"isDesktopExtensionEnabled"=dword:00000001` + "\r\n")
	b.WriteString(`"isDesktopExtensionDirectoryEnabled"=dword:00000001` + "\r\n")
	b.WriteString(`"isDesktopExtensionSignatureRequired"=dword:00000000` + "\r\n")
	b.WriteString(`"isLocalDevMcpEnabled"=dword:00000001` + "\r\n")
	b.WriteString(`"disableAutoUpdates"=dword:00000000` + "\r\n")
	b.WriteString(`"disableEssentialTelemetry"=dword:00000000` + "\r\n")
	b.WriteString(`"disableNonessentialTelemetry"=dword:00000000` + "\r\n")
	b.WriteString(`"disableNonessentialServices"=dword:00000000` + "\r\n")
	b.WriteString("\r\n[HKEY_CURRENT_USER\\SOFTWARE\\IceRhymers\\databricks-claude]\r\n")
	fmt.Fprintf(&b, "\"databricksProfile\"=\"%s\"\r\n", regEscape(profile))
	if cliPath != "" {
		fmt.Fprintf(&b, "\"databricksCliPath\"=\"%s\"\r\n", regEscape(cliPath))
	}
	// daemonPort is emitted for endpoint tooling cross-check; not yet consumed
	// by pkg/mdmprofile (future issue). daemonMode is a boolean sentinel.
	if daemonPort > 0 {
		fmt.Fprintf(&b, "\"daemonPort\"=dword:%08X\r\n", daemonPort)
		b.WriteString("\"daemonMode\"=dword:00000001\r\n")
	}
	return b.String()
}

// buildDevModeJSON renders the Claude Desktop developer-mode importable JSON.
// Use case: an individual user wants to customize allow-lists, tools, or
// branding via Desktop's developer-mode UI without touching the fleet MDM
// artifacts. The .mobileconfig/.reg are the right format for fleet rollout;
// this is the right format for per-user customization.
//
// inferenceCredentialHelperTtlSec is set to 55 (NOT the validated example's
// 3600) to match the MDM artifacts. The OAuth helper refreshes tokens with a
// 5-minute buffer, so a 55-second TTL forces Desktop to re-invoke the helper
// at a cadence that always sees a fresh token. Diverging from the MDM TTL
// would give dev-mode users different effective behavior than fleet users.
//
// inferenceGatewayApiKey is intentionally absent in helper-mode: the validated
// example shows "••••••••" which is Desktop's UI placeholder. Our auth flow
// uses the OAuth credential helper, so no static key is needed. In daemon-mode
// it is present as a localhost gate (not a real credential).
//
// inferenceModels is reused from inferenceModelsJSON via []json.RawMessage so
// the model list never drifts between the three artifacts.
func buildDevModeJSON(keys modeKeys, profile, cliPath string, daemonPort int) ([]byte, error) {
	var models []json.RawMessage
	if err := json.Unmarshal([]byte(inferenceModelsJSON), &models); err != nil {
		return nil, fmt.Errorf("inferenceModelsJSON is malformed: %w", err)
	}

	if keys.CredHelper != "" {
		// Helper-mode: same struct as today for byte-identical output.
		cfg := struct {
			DatabricksProfile                   string            `json:"databricksProfile"`
			DatabricksCliPath                   string            `json:"databricksCliPath,omitempty"`
			DisableDeploymentModeChooser        bool              `json:"disableDeploymentModeChooser"`
			InferenceProvider                   string            `json:"inferenceProvider"`
			InferenceGatewayBaseUrl             string            `json:"inferenceGatewayBaseUrl"`
			InferenceGatewayAuthScheme          string            `json:"inferenceGatewayAuthScheme"`
			InferenceModels                     []json.RawMessage `json:"inferenceModels"`
			InferenceCredentialHelper           string            `json:"inferenceCredentialHelper"`
			InferenceCredentialHelperTtlSec     int               `json:"inferenceCredentialHelperTtlSec"`
			IsClaudeCodeForDesktopEnabled       bool              `json:"isClaudeCodeForDesktopEnabled"`
			IsDesktopExtensionEnabled           bool              `json:"isDesktopExtensionEnabled"`
			IsDesktopExtensionDirectoryEnabled  bool              `json:"isDesktopExtensionDirectoryEnabled"`
			IsDesktopExtensionSignatureRequired bool              `json:"isDesktopExtensionSignatureRequired"`
			IsLocalDevMcpEnabled                bool              `json:"isLocalDevMcpEnabled"`
			DisableAutoUpdates                  bool              `json:"disableAutoUpdates"`
			DisableEssentialTelemetry           bool              `json:"disableEssentialTelemetry"`
			DisableNonessentialTelemetry        bool              `json:"disableNonessentialTelemetry"`
			DisableNonessentialServices         bool              `json:"disableNonessentialServices"`
		}{
			DatabricksProfile:                   profile,
			DatabricksCliPath:                   cliPath,
			DisableDeploymentModeChooser:        true,
			InferenceProvider:                   "gateway",
			InferenceGatewayBaseUrl:             keys.GatewayBaseURL,
			InferenceGatewayAuthScheme:          "bearer",
			InferenceModels:                     models,
			InferenceCredentialHelper:           keys.CredHelper,
			InferenceCredentialHelperTtlSec:     keys.CredHelperTTL,
			IsClaudeCodeForDesktopEnabled:       true,
			IsDesktopExtensionEnabled:           true,
			IsDesktopExtensionDirectoryEnabled:  true,
			IsDesktopExtensionSignatureRequired: false,
			IsLocalDevMcpEnabled:                true,
			DisableAutoUpdates:                  false,
			DisableEssentialTelemetry:           false,
			DisableNonessentialTelemetry:        false,
			DisableNonessentialServices:         false,
		}
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal dev-mode config: %w", err)
		}
		return append(out, '\n'), nil
	}

	// Daemon-mode: omit CredentialHelper/TTL; add GatewayApiKey; add
	// daemonPort/daemonMode in the IceRhymers section; optional OTLP keys.
	// daemonPort is future-use for endpoint cross-check (pkg/mdmprofile does
	// not yet consume it; see daemon-mode design notes in issue #164).
	type daemonCfg struct {
		DatabricksProfile                   string            `json:"databricksProfile"`
		DatabricksCliPath                   string            `json:"databricksCliPath,omitempty"`
		DaemonPort                          int               `json:"daemonPort,omitempty"`
		DaemonMode                          bool              `json:"daemonMode,omitempty"`
		DisableDeploymentModeChooser        bool              `json:"disableDeploymentModeChooser"`
		InferenceProvider                   string            `json:"inferenceProvider"`
		InferenceGatewayBaseUrl             string            `json:"inferenceGatewayBaseUrl"`
		InferenceGatewayApiKey              string            `json:"inferenceGatewayApiKey,omitempty"`
		InferenceGatewayAuthScheme          string            `json:"inferenceGatewayAuthScheme"`
		InferenceModels                     []json.RawMessage `json:"inferenceModels"`
		OtlpEndpoint                        string            `json:"otlpEndpoint,omitempty"`
		OtlpProtocol                        string            `json:"otlpProtocol,omitempty"`
		IsClaudeCodeForDesktopEnabled       bool              `json:"isClaudeCodeForDesktopEnabled"`
		IsDesktopExtensionEnabled           bool              `json:"isDesktopExtensionEnabled"`
		IsDesktopExtensionDirectoryEnabled  bool              `json:"isDesktopExtensionDirectoryEnabled"`
		IsDesktopExtensionSignatureRequired bool              `json:"isDesktopExtensionSignatureRequired"`
		IsLocalDevMcpEnabled                bool              `json:"isLocalDevMcpEnabled"`
		DisableAutoUpdates                  bool              `json:"disableAutoUpdates"`
		DisableEssentialTelemetry           bool              `json:"disableEssentialTelemetry"`
		DisableNonessentialTelemetry        bool              `json:"disableNonessentialTelemetry"`
		DisableNonessentialServices         bool              `json:"disableNonessentialServices"`
	}
	cfg := daemonCfg{
		DatabricksProfile:                   profile,
		DatabricksCliPath:                   cliPath,
		DaemonPort:                          daemonPort,
		DaemonMode:                          daemonPort > 0,
		DisableDeploymentModeChooser:        true,
		InferenceProvider:                   "gateway",
		InferenceGatewayBaseUrl:             keys.GatewayBaseURL,
		InferenceGatewayApiKey:              keys.GatewayAPIKey,
		InferenceGatewayAuthScheme:          "bearer",
		InferenceModels:                     models,
		OtlpEndpoint:                        keys.OTELEndpoint,
		OtlpProtocol:                        keys.OTELProtocol,
		IsClaudeCodeForDesktopEnabled:       true,
		IsDesktopExtensionEnabled:           true,
		IsDesktopExtensionDirectoryEnabled:  true,
		IsDesktopExtensionSignatureRequired: false,
		IsLocalDevMcpEnabled:                true,
		DisableAutoUpdates:                  false,
		DisableEssentialTelemetry:           false,
		DisableNonessentialTelemetry:        false,
		DisableNonessentialServices:         false,
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal daemon dev-mode config: %w", err)
	}
	return append(out, '\n'), nil
}

// writeFileAtomic writes data to path atomically: write to <path>.tmp in the
// same directory, then os.Rename. Mirrors the pattern used by pkg/settings to
// satisfy the CLAUDE.md "atomic file writes everywhere" mandate.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// guardDevJSONOutputPath refuses to overwrite a JSON file that does not look
// like a previously-generated dev-mode config. Specifically: if the target
// exists, parses as JSON, and lacks an `inferenceProvider:"gateway"` field,
// we abort. This protects against accidental clobbers of e.g.
// ~/.claude/settings.json via a typo on --output.
//
// Non-existent file → allow.
// Empty file        → allow (nothing to lose).
// Valid JSON with inferenceProvider == "gateway" → allow (regeneration).
// Any other case    → refuse with an explicit error mentioning the path.
func guardDevJSONOutputPath(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var probe struct {
		InferenceProvider string `json:"inferenceProvider"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("refusing to overwrite %q: not a databricks-claude dev-mode JSON (failed to parse as JSON: %v)", path, err)
	}
	if probe.InferenceProvider != "gateway" {
		return fmt.Errorf("refusing to overwrite %q: not a databricks-claude dev-mode JSON (inferenceProvider=%q, expected %q)", path, probe.InferenceProvider, "gateway")
	}
	return nil
}

// regEscape escapes a string for use inside a Windows .reg REG_SZ value:
// backslashes and quotes get backslash-escaped.
func regEscape(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
	)
	return r.Replace(s)
}

// extractProfileFlag scans args for --profile/--profile=value and returns the
// profile string if present. Used by the early-exit credential-helper and
// generate-desktop-config paths so they don't have to wait for parseArgs.
func extractProfileFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--profile" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--profile=") {
			return strings.TrimPrefix(a, "--profile=")
		}
	}
	return ""
}

// extractOutputFlag is the analogous helper for --output / --output=value.
func extractOutputFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--output" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--output=") {
			return strings.TrimPrefix(a, "--output=")
		}
	}
	return ""
}

// extractBinaryPathFlag is the analogous helper for --binary-path. Used by
// MDM admins to override the credential-helper path embedded in the generated
// Claude Desktop config.
func extractBinaryPathFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--binary-path" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--binary-path=") {
			return strings.TrimPrefix(a, "--binary-path=")
		}
	}
	return ""
}

// extractDatabricksCLIPathFlag is the analogous helper for --databricks-cli-path.
// Pins the absolute path to the `databricks` binary that the credential
// helper subprocess will exec. Persisted to the state file so the helper —
// which can't receive flags — picks it up on each invocation.
func extractDatabricksCLIPathFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--databricks-cli-path" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--databricks-cli-path=") {
			return strings.TrimPrefix(a, "--databricks-cli-path=")
		}
	}
	return ""
}

// extractForPkgFlag scans args for the boolean --for-pkg flag. Mirrors the
// other extract* helpers in shape but is presence-only: --for-pkg toggles
// forPkg=true, --for-pkg=true / --for-pkg=false honour the explicit value.
// Returns the parsed value plus a copy of args with the flag removed.
func extractForPkgFlag(args []string) (forPkg bool, remaining []string) {
	remaining = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--for-pkg" {
			forPkg = true
			continue
		}
		if strings.HasPrefix(a, "--for-pkg=") {
			v := strings.TrimPrefix(a, "--for-pkg=")
			forPkg = v == "true" || v == "1" || v == ""
			continue
		}
		remaining = append(remaining, a)
	}
	return forPkg, remaining
}

// extractDaemonFlag scans args for the boolean --daemon flag.
// Returns true if present. Same pattern as extractForPkgFlag but simpler
// (no explicit =false form needed for daemon-mode; absence means helper-mode).
func extractDaemonFlag(args []string) bool {
	for _, a := range args {
		if a == "--daemon" {
			return true
		}
		if strings.HasPrefix(a, "--daemon=") {
			v := strings.TrimPrefix(a, "--daemon=")
			return v == "true" || v == "1" || v == ""
		}
	}
	return false
}

// extractDesktopPortFlag scans args for --port / --port=N specifically for
// the desktop generate-config subcommand. Returns 0 if not present (the caller
// resolves the default from state or the package constant).
func extractDesktopPortFlag(args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--port" && i+1 < len(args) {
			v, _ := strconv.Atoi(args[i+1])
			return v
		}
		if strings.HasPrefix(a, "--port=") {
			v, _ := strconv.Atoi(strings.TrimPrefix(a, "--port="))
			return v
		}
	}
	return 0
}

// extractDaemonFakeKeyFlag scans args for --daemon-fake-key / --daemon-fake-key=value.
func extractDaemonFakeKeyFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--daemon-fake-key" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--daemon-fake-key=") {
			return strings.TrimPrefix(a, "--daemon-fake-key=")
		}
	}
	return ""
}

// hasFlag returns true if any element of args equals name (or starts with
// name+"="). Used for early-exit flag detection at the top of main().
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}
