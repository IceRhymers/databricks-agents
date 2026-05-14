# Windows lacks `ln`, and Go on Windows expects an .exe suffix. Claude Desktop
# on Windows still needs the `databricks-claude-credential-helper.exe` alias
# (the .reg MDM artifact points at it), so on Windows we create a hard link
# via `fsutil hardlink create` — no admin/Developer-Mode required (unlike a
# true symlink). We pin SHELL to cmd.exe on Windows so the recipes work
# regardless of whether the user invokes make from PowerShell, cmd, or Git
# Bash — `rm`/`ln`/`/dev/null` can't be relied on across those.
ifeq ($(OS),Windows_NT)
SHELL              := cmd.exe
.SHELLFLAGS        := /c
EXE                := .exe
DEVNULL            := nul
GOPATH_BIN          := $(shell go env GOPATH)\bin
# Parens around the `if exist` block are load-bearing: without them, cmd
# parses the following `& fsutil ...` as part of the if's body and skips it
# whenever the alias doesn't already exist (i.e., every fresh build).
LINK_ALIAS          = (if exist databricks-claude-credential-helper.exe del /f /q databricks-claude-credential-helper.exe) & fsutil hardlink create databricks-claude-credential-helper.exe databricks-claude.exe
INSTALL_LINK_ALIAS  = (if exist "$(GOPATH_BIN)\databricks-claude-credential-helper.exe" del /f /q "$(GOPATH_BIN)\databricks-claude-credential-helper.exe") & fsutil hardlink create "$(GOPATH_BIN)\databricks-claude-credential-helper.exe" "$(GOPATH_BIN)\databricks-claude.exe"
else
EXE                :=
DEVNULL            := /dev/null
LINK_ALIAS          = ln -sf databricks-claude databricks-claude-credential-helper
INSTALL_LINK_ALIAS  = ln -sf databricks-claude "$$(go env GOPATH)/bin/databricks-claude-credential-helper"
endif

VERSION ?= $(shell git describe --tags --always --dirty 2>$(DEVNULL) || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)

# Subject identity for `make generate-signing-cert`. Override these for your
# org before rotating into production — admins forking this repo MUST NOT
# leave the defaults in place for a fleet rollout, as the defaults are
# deliberately template-y to avoid impersonating any real organization.
CERT_CN      ?= databricks-claude code signing (REPLACE FOR PROD)
CERT_ORG     ?= databricks-claude self-signed (REPLACE FOR PROD)
CERT_COUNTRY ?= US

.DEFAULT_GOAL := build

## Build the databricks-claude binary (and the credential-helper alias that
## the Claude Desktop MDM artifacts expect — symlink on Unix, hard link on
## Windows).
build:
	go build -ldflags="$(LDFLAGS)" -o databricks-claude$(EXE) .
	$(LINK_ALIAS)

## Install to GOPATH/bin (also drops the credential-helper alias so Claude
## Desktop's inferenceCredentialHelper can target a stable path).
install:
	go install -ldflags="$(LDFLAGS)" .
	$(INSTALL_LINK_ALIAS)

## Run tests with verbose output
test:
	go test ./... -v

## Cross-compile for linux/darwin/windows amd64 + arm64. Symlinks for the
## credential-helper alias are NOT generated here — packagers (brew, .pkg,
## .deb) are responsible for creating them at install time pointing at a
## predictable system path.
dist:
	mkdir -p dist
	GOOS=darwin  GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-darwin-arm64  .
	GOOS=darwin  GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-darwin-amd64  .
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-linux-amd64   .
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-linux-arm64   .
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-windows-amd64.exe .
	GOOS=windows GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-windows-arm64.exe .

## Build a universal2 macOS .pkg installer. Set APPLE_INTERNAL_SIGNING_IDENTITY
## to codesign the binary inside the pkg with hardened-runtime flags; otherwise
## the binary is ad-hoc signed. The .pkg itself is always unsigned — productsign
## requires an Apple-issued installer cert, which a self-signed cert can't satisfy.
pkg:
	rm -rf build root scripts/postinstall dist/databricks-claude*.pkg
	mkdir -p build dist scripts root/usr/local/bin
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o dist/databricks-claude-darwin-amd64 .
	lipo -create -output build/databricks-claude dist/databricks-claude-darwin-arm64 dist/databricks-claude-darwin-amd64
	@if [ -n "$$APPLE_INTERNAL_SIGNING_IDENTITY" ]; then \
		echo "Signing binary with identity: $$APPLE_INTERNAL_SIGNING_IDENTITY"; \
		codesign --force --options runtime --timestamp --sign "$$APPLE_INTERNAL_SIGNING_IDENTITY" build/databricks-claude; \
	else \
		echo "APPLE_INTERNAL_SIGNING_IDENTITY unset — ad-hoc signing"; \
		codesign --force --options runtime --sign - build/databricks-claude; \
	fi
	cp build/databricks-claude root/usr/local/bin/databricks-claude
	ln -sf databricks-claude root/usr/local/bin/databricks-claude-credential-helper
	printf '#!/bin/sh\nset -e\ncd /usr/local/bin\nln -sf databricks-claude databricks-claude-credential-helper\n' > scripts/postinstall
	chmod +x scripts/postinstall
	pkgbuild --root root --scripts scripts \
		--identifier com.databricks.databricks-claude \
		--version "$(VERSION)" \
		--install-location / \
		dist/databricks-claude-component.pkg
	productbuild --package dist/databricks-claude-component.pkg \
		--identifier com.databricks.databricks-claude.dist \
		--version "$(VERSION)" \
		dist/databricks-claude.pkg
	rm -f dist/databricks-claude-component.pkg
	@echo "Built dist/databricks-claude.pkg"

## Emit the MDM trust profile (.mobileconfig) that establishes the signing cert
## as a trusted root for code-signing on managed Macs. Requires
## dist/signing-cert.pem (run `make generate-signing-cert` first).
trust-profile: build
	./databricks-claude desktop generate-trust-profile \
		--cert dist/signing-cert.pem \
		--output dist/databricks-claude-trust.mobileconfig

## Generate a 5-year self-signed code-signing cert for the .pkg. Run once;
## paste the printed values into GitHub repo secrets. Rotate ≥60 days before
## expiry (see README rotation runbook).
generate-signing-cert:
	mkdir -p dist
	@if [ -z "$$P12_PASSWORD" ]; then \
		echo "ERROR: set P12_PASSWORD env var (a strong random password)"; \
		exit 1; \
	fi
	@if [ -f dist/signing-cert.key ]; then \
		echo "ERROR: dist/signing-cert.key already exists. Refusing to overwrite."; \
		echo "       Move/archive the existing key first if you intend to rotate."; \
		exit 1; \
	fi
	@echo "Generating cert with subject:"
	@echo "  CN=$(CERT_CN)"
	@echo "  O=$(CERT_ORG)"
	@echo "  C=$(CERT_COUNTRY)"
	@case "$(CERT_CN)$(CERT_ORG)" in *"REPLACE FOR PROD"*) \
		echo ""; \
		echo "WARNING: cert subject contains the placeholder 'REPLACE FOR PROD'."; \
		echo "         For a real fleet rollout, override CERT_CN, CERT_ORG, and"; \
		echo "         CERT_COUNTRY to your org's identity. Continuing in 3s — Ctrl-C to abort."; \
		sleep 3 ;; \
	esac
	openssl req -x509 -newkey rsa:2048 -days 1825 -nodes \
		-subj "/CN=$(CERT_CN)/O=$(CERT_ORG)/C=$(CERT_COUNTRY)" \
		-addext "keyUsage=critical,digitalSignature" \
		-addext "extendedKeyUsage=codeSigning,1.2.840.113635.100.4.13" \
		-keyout dist/signing-cert.key -out dist/signing-cert.pem
	openssl pkcs12 -export -legacy -out dist/signing-cert.p12 \
		-inkey dist/signing-cert.key -in dist/signing-cert.pem \
		-passout pass:"$$P12_PASSWORD"
	base64 -i dist/signing-cert.p12 -o dist/signing-cert.p12.b64
	@echo
	@echo "Cert generated. Paste the following into GitHub repo secrets:"
	@echo "  APPLE_INTERNAL_SIGNING_P12_BASE64    = (contents of dist/signing-cert.p12.b64)"
	@echo "  APPLE_INTERNAL_SIGNING_P12_PASSWORD  = (the value of P12_PASSWORD)"
	@echo "  APPLE_INTERNAL_SIGNING_IDENTITY      = $(CERT_CN)"
	@echo "  APPLE_INTERNAL_SIGNING_CERT_PEM      = (contents of dist/signing-cert.pem)"
	@echo
	@echo "Rotate this cert >=60 days before expiry."

## Remove build artifacts
clean:
	rm -f databricks-claude databricks-claude-credential-helper
	rm -rf dist/ build/ root/ scripts/postinstall

## Run go vet
lint:
	go vet ./...

## STUB for adopters: notarize a self-built macOS binary for fleet rollout.
## Upstream databricks-claude ships UNSIGNED — signing/notarization is the
## deployer's responsibility (see issue #54 and the README "Signing
## prerequisite" section). This target documents the contract so adopters who
## fork the repo can fill it in for their org. It is intentionally NOT wired
## end-to-end upstream.
##
## Expected env when implemented:
##   DEVELOPER_ID_APPLICATION  -- e.g. "Developer ID Application: Your Org (TEAMID)"
##   NOTARYTOOL_PROFILE        -- an `xcrun notarytool store-credentials` keychain profile
##
## Expected sequence (codesign -> notarize -> staple):
##   codesign --force --options runtime --timestamp \
##     --sign "$$DEVELOPER_ID_APPLICATION" build/databricks-claude
##   ditto -c -k --keepParent build/databricks-claude build/databricks-claude.zip
##   xcrun notarytool submit build/databricks-claude.zip \
##     --keychain-profile "$$NOTARYTOOL_PROFILE" --wait
##   xcrun stapler staple build/databricks-claude   # (or staple the .pkg / .dmg)
notarize:
	@echo "make notarize: STUB — upstream databricks-claude ships unsigned."
	@echo "  Signing/notarization is an adopter responsibility (see issue #54"
	@echo "  and the README 'Signing prerequisite' section)."
	@echo ""
	@echo "  To implement for your org: fork the repo, set DEVELOPER_ID_APPLICATION"
	@echo "  and NOTARYTOOL_PROFILE, and fill in the codesign -> notarytool -> stapler"
	@echo "  sequence documented in the Makefile recipe comment above this target."
	@exit 1

.PHONY: build install test dist clean lint pkg trust-profile generate-signing-cert notarize
