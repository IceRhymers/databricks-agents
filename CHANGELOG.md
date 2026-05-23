# Changelog

## [1.1.0](https://github.com/IceRhymers/databricks-claude/compare/v1.0.2...v1.1.0) (2026-05-23)


### Features

* **proxy:** add path-prefix multi-upstream routing to pkg/proxy ([b0f1279](https://github.com/IceRhymers/databricks-claude/commit/b0f1279f6ebb68eb1332aab250485a400e3e4f45))
* **proxy:** add path-prefix multi-upstream routing to pkg/proxy ([3bb18ea](https://github.com/IceRhymers/databricks-claude/commit/3bb18ea92d7a24c2bdbd6595731acba7e18e8049)), closes [#188](https://github.com/IceRhymers/databricks-claude/issues/188)

## [1.0.2](https://github.com/IceRhymers/databricks-claude/compare/v1.0.1...v1.0.2) (2026-05-18)


### Bug Fixes

* **completion:** declare desktop action keywords as subcommands ([c33068a](https://github.com/IceRhymers/databricks-claude/commit/c33068ac5c5d656b68642f247572308a607c07ca))

## [1.0.1](https://github.com/IceRhymers/databricks-claude/compare/v1.0.0...v1.0.1) (2026-05-18)


### Bug Fixes

* **completion:** escape apostrophes in zsh flag/subcommand descriptions ([8642725](https://github.com/IceRhymers/databricks-claude/commit/8642725d97bc08ddd7adb6ba91d7311c7a90b7c4))
* **completion:** escape apostrophes in zsh flag/subcommand descriptions ([b9bd3a4](https://github.com/IceRhymers/databricks-claude/commit/b9bd3a469fae67fce720d6b0469f8e7170fc2b5a))

## [1.0.0](https://github.com/IceRhymers/databricks-claude/compare/v0.19.0...v1.0.0) (2026-05-18)


### ⚠ BREAKING CHANGES

* removes the --headless and --idle-timeout root flags. The session-scoped standalone proxy (refcounted, /shutdown, idle-timeout, settings.json restore-on-exit) is now `databricks-claude serve --session-mode`. --idle-timeout becomes a `serve` flag. The long-lived daemon now requires explicit `serve --daemon` (bare `serve` with no mode flag or sub-subcommand is a hard error — eliminates silent-degradation hazard at the hooks spawn site). serve install|uninstall|status unchanged. The hooks deployment mode ([#173](https://github.com/IceRhymers/databricks-claude/issues/173)) now spawns `serve --session-mode` instead of `--headless`; the SessionStart/SessionEnd lifecycle is verified intact.
* removes 4 root flags. Replacements:   --install-hooks     → hooks install   --uninstall-hooks   → hooks uninstall   --headless-ensure   → hooks session-start  (hook-invoked internal)   --headless-release  → hooks session-end    (hook-invoked internal) Old flags are removed, not aliased. The generated SessionStart/SessionEnd hook JSON now invokes the new command names. No back-compat for already-installed hooks (none deployed). The hooks deployment mode itself is unchanged — proxy still spins up on SessionStart and tears down on SessionEnd.
* removes 14 root flags. Replacements:   --otel / --otel-*-table          → config otel enable [...]   --no-otel / --no-otel-{m,l,t}    → config otel disable [...]   --with-websearch / --websearch-* → config websearch enable [...]   --write-claude-config            → config write   --print-env                      → config show Old flags are removed, not aliased — they now pass through to claude. Storage semantics (two-store model, sentinel guards, state preservation on disable, OTEL section removal) are unchanged.

### Features

* **cli:** --write-claude-config flag for scriptable settings.json bootstrap ([d3b9a3b](https://github.com/IceRhymers/databricks-claude/commit/d3b9a3b83d11ad35c0922e727b4c20585ebd00d8))
* **cli:** --write-claude-config flag for scriptable settings.json bootstrap ([8da1957](https://github.com/IceRhymers/databricks-claude/commit/8da195783efd9a02bf232fafec3e95d59bd03db9)), closes [#162](https://github.com/IceRhymers/databricks-claude/issues/162)
* **cli:** scope --help to wrapper; advertise -- passthrough ([7d63ba4](https://github.com/IceRhymers/databricks-claude/commit/7d63ba4704101a680f9924f8a983826affa9755e))
* consolidate --headless proxy mode into `serve --session-mode` ([89c952a](https://github.com/IceRhymers/databricks-claude/commit/89c952a30c03e7e984bcceac1a658843235af3b6))
* consolidate hooks lifecycle flags under a `hooks` subcommand ([5b4a496](https://github.com/IceRhymers/databricks-claude/commit/5b4a4961ad203dacdd459d09b11acee2340ccafb))
* consolidate persistent-config flags under a `config` subcommand ([92b56d0](https://github.com/IceRhymers/databricks-claude/commit/92b56d01fd0c717551a9a02389c174ee566955fe))
* **daemon:** add serve subcommand for long-lived MDM-deployed instances ([52931b2](https://github.com/IceRhymers/databricks-claude/commit/52931b21aca3aad5d9e5f3a76f3f2eaf5ab88db0))
* **daemon:** add serve subcommand for long-lived MDM-deployed instances ([b6431af](https://github.com/IceRhymers/databricks-claude/commit/b6431afb7888e76829750656412cab5838af7b0c)), closes [#153](https://github.com/IceRhymers/databricks-claude/issues/153)
* **daemon:** cross-platform serve install/uninstall/status ([f8978f2](https://github.com/IceRhymers/databricks-claude/commit/f8978f245008d3dc48b6361ed4574418ed05fe1e))
* **daemon:** cross-platform serve install/uninstall/status ([b2a1f37](https://github.com/IceRhymers/databricks-claude/commit/b2a1f37e11df7824fff23a20e03950e822513d61)), closes [#155](https://github.com/IceRhymers/databricks-claude/issues/155)
* **desktop:** --daemon mode for generate-config ([03aa065](https://github.com/IceRhymers/databricks-claude/commit/03aa065cdd69b083616d7207b2dc1fdda4f62c3e)), closes [#164](https://github.com/IceRhymers/databricks-claude/issues/164)
* **desktop:** --daemon mode for generate-config (gatewayBaseUrl + omitted helper) ([b147c40](https://github.com/IceRhymers/databricks-claude/commit/b147c40c1992130226228e83ccd8cd1a3dc6f6a1))
* **desktop:** databricksCliPath MDM key for endpoint CLI discovery ([eac6ba7](https://github.com/IceRhymers/databricks-claude/commit/eac6ba7e06a1fb402b1a12be0cb54a9dc46e0be8))
* **desktop:** databricksCliPath MDM key for endpoint CLI discovery ([005eebd](https://github.com/IceRhymers/databricks-claude/commit/005eebd6a6d02f183c263fcbb810afe7f51024d5)), closes [#150](https://github.com/IceRhymers/databricks-claude/issues/150)
* **hooks:** daemon-aware no-op behavior when serve is running ([d314974](https://github.com/IceRhymers/databricks-claude/commit/d314974e718b768a2b18157ea777bce77df775cb))
* **hooks:** daemon-aware no-op behavior when serve is running ([1800b8d](https://github.com/IceRhymers/databricks-claude/commit/1800b8ddd5c3bf8c6de9fa861c938d654b85e6a8)), closes [#154](https://github.com/IceRhymers/databricks-claude/issues/154)
* introduce internal/cmd command-tree registry; migrate root command ([15068bb](https://github.com/IceRhymers/databricks-claude/commit/15068bb0776b9fd83cb9653f1d3682fed4364599)), closes [#170](https://github.com/IceRhymers/databricks-claude/issues/170)
* migrate serve/setup/desktop subcommands onto the command tree ([5579cfd](https://github.com/IceRhymers/databricks-claude/commit/5579cfdad2058a115a5577da60fe0eaff7bea4bd)), closes [#171](https://github.com/IceRhymers/databricks-claude/issues/171)


### Bug Fixes

* **build:** make Makefile work from PowerShell/cmd on Windows ([e4297ed](https://github.com/IceRhymers/databricks-claude/commit/e4297ede8a2cccb208bf0c97ddce3dd0d70142e5))
* daemon-mode otlpEndpoint must carry /otel path prefix ([b79739b](https://github.com/IceRhymers/databricks-claude/commit/b79739bb983ecdd4051c51240611c28e161938e4))
* **daemon:** address code review nits on PR [#159](https://github.com/IceRhymers/databricks-claude/issues/159) follow-up ([23b02f3](https://github.com/IceRhymers/databricks-claude/commit/23b02f3755d66b00e68bc2b9ce1278e38f71fedb))
* **daemon:** install-time pre-auth + daemon-safe serve + status accuracy ([6cedf58](https://github.com/IceRhymers/databricks-claude/commit/6cedf5814b8d43b03c7a34c998cfc34f6d2c0a18))
* **serve:** wire --with-websearch into daemon proxy config ([4f9c2a2](https://github.com/IceRhymers/databricks-claude/commit/4f9c2a294264717d9288b7876d51a0ea2e051d46))

## [0.19.0](https://github.com/IceRhymers/databricks-claude/compare/v0.18.0...v0.19.0) (2026-05-11)


### Features

* add setup subcommand for fleet init scripts ([30d6f43](https://github.com/IceRhymers/databricks-claude/commit/30d6f438201590baab89a70bd0c1508451d658cc))
* **authcheck:** add EnsureAuthenticatedWithStdout for stdout-sensitive callers ([e723dce](https://github.com/IceRhymers/databricks-claude/commit/e723dced7e65748daa5c18bedfbcf8686f332fc5))
* Desktop MDM setup subcommand + endpoint profile key + helper auto-recovery ([3ccb00a](https://github.com/IceRhymers/databricks-claude/commit/3ccb00a371ca1137961ecc4d13e0c9131c745e4c))
* **desktop:** credential helper auto-recovers via authcheck on token miss ([24fd3cc](https://github.com/IceRhymers/databricks-claude/commit/24fd3ccfd9b11940b9c965cd30bf3db6985c617b))
* **desktop:** emit com.icerhymers.databricks-claude MDM payload in artifacts ([50af602](https://github.com/IceRhymers/databricks-claude/commit/50af6025e0cbdf4a811ba656c61dde65ea0824a6))
* **mdmprofile:** add darwin/windows/other MDM profile readers ([7951f2a](https://github.com/IceRhymers/databricks-claude/commit/7951f2ada54701cd1c20b2c3b09cdd25e2d9ec2d))


### Bug Fixes

* **desktop:** persist resolved profile to state in generate-config ([1f98eb0](https://github.com/IceRhymers/databricks-claude/commit/1f98eb030e3ff7a836c3fe7316953d19048b5321))
* **desktop:** treat state.Profile=DEFAULT as unset for MDM fall-through ([c7f5b90](https://github.com/IceRhymers/databricks-claude/commit/c7f5b906a3c4ac913d92030f3bf42011f11c8dad))
* **desktop:** treat state.Profile=DEFAULT as unset for MDM fall-through; stop persisting DEFAULT ([c2dcfc9](https://github.com/IceRhymers/databricks-claude/commit/c2dcfc9ebd756ed49e47f406e140b5ac3e079596)), closes [#148](https://github.com/IceRhymers/databricks-claude/issues/148)

## [0.18.0](https://github.com/IceRhymers/databricks-claude/compare/v0.17.0...v0.18.0) (2026-05-07)


### Features

* --with-websearch — local fulfillment of web_search/web_fetch (workaround) ([0a0e978](https://github.com/IceRhymers/databricks-claude/commit/0a0e9786c0551f5c85937512d6bb93a17a9c3d9e))
* --with-websearch — local fulfillment of web_search/web_fetch (workaround) ([dcb8eb9](https://github.com/IceRhymers/databricks-claude/commit/dcb8eb9f28eeeca476cb3dd88a602a8e4d615696)), closes [#141](https://github.com/IceRhymers/databricks-claude/issues/141)
* enhance web_search/web_fetch handling with SSE rewriter and local fulfillment ([78edb38](https://github.com/IceRhymers/databricks-claude/commit/78edb38798f05e63ab988047155a4243ae47d09d))


### Bug Fixes

* surface --with-websearch flags in --help output ([9cb767c](https://github.com/IceRhymers/databricks-claude/commit/9cb767c3e611da572cf5ee942e0d478c836dfac6))
* **websearch:** emit input:{} on server_tool_use start, inject error on overflow ([4c32b0e](https://github.com/IceRhymers/databricks-claude/commit/4c32b0e21173ae9f9402b42643fa6b4b5a3c9218))

## [0.17.0](https://github.com/IceRhymers/databricks-claude/compare/v0.16.0...v0.17.0) (2026-05-06)


### Refactors

* replace `parseArgs` 31-value tuple with `Args` struct — all flag values now accessed as `a.FieldName`; callers updated throughout ([6843844](https://github.com/IceRhymers/databricks-claude/commit/6843844f1c9d8c28e69e2b70b3ee78c15f49e5b))


### Bug Fixes

* remove stale proxy entries from settings.json on startup — probes existing `127.0.0.1:<port>` entries with a 200ms TCP dial and removes dead ones before writing the new URL ([8a1856b](https://github.com/IceRhymers/databricks-claude/commit/8a1856bdf2be3def5b2b4b40bc144f4f82ae49c5))
* `pkg/proxy.NewServer` and `pkg/headless.Ensure` now return errors instead of calling `log.Fatalf`; `authcheck` resolves CLI binary via fallback dirs (fixes silent false-negative on GUI-launched sessions) ([eeaa65e](https://github.com/IceRhymers/databricks-claude/commit/eeaa65ec3ce55b032ca9fd6e5643c13b9cf4cd6b))
* add missing sanitize patterns: case-insensitive Bearer, Basic auth, `access_token` JSON field, `DATABRICKS_TOKEN` env-var; `--idle-timeout` now rejects bare integers with a clear error ([ec2e95b](https://github.com/IceRhymers/databricks-claude/commit/ec2e95be100985df4d1a01bfa7bde1752bf2e183))
* use `os.CreateTemp` for unique tmp filenames in settings writers — eliminates concurrent-write corruption when two processes start simultaneously ([a2d97a0](https://github.com/IceRhymers/databricks-claude/commit/a2d97a0b993f21b841346416acf7f2518b320c00))
* broaden sanitize patterns to cover `dapi` tokens without hyphen and `X-Databricks-Authorization` header ([7cccfd8](https://github.com/IceRhymers/databricks-claude/commit/7cccfd8f9d4a9d738d3b9e9bd7e3662c9ccecae1))
* skip `Authorization` header injection when token is empty; fix WebSocket goroutine leak (single `<-done` replaced with explicit conn close + second receive) ([b3bf297](https://github.com/IceRhymers/databricks-claude/commit/b3bf2971d403f565194c1ae33c798362a5892657))
* properly wrap lifecycle handler on health-watcher takeover so `/shutdown` triggers clean shutdown after promotion ([17fb740](https://github.com/IceRhymers/databricks-claude/commit/17fb740b2141aef664d04ca04838ec92e2d605fa))


### Improvements

* replace hand-rolled slice-bounds prefix checks with `strings.HasPrefix`; add `https://` variant for localhost suppression; fix orphan comment in `pkg/refcount` ([219a29b](https://github.com/IceRhymers/databricks-claude/commit/219a29b))
* align README with current CLI flags and `--print-env` output ([46a4d9b](https://github.com/IceRhymers/databricks-claude/commit/46a4d9b))

## [0.16.0](https://github.com/IceRhymers/databricks-claude/compare/v0.15.0...v0.16.0) (2026-05-04)


### Features

* simplify `ConstructGatewayURL`: use host-relative AI Gateway path (`{host}/ai-gateway/anthropic`), removing SCIM workspace-ID lookup, token parameter, and fallback ([#116](https://github.com/IceRhymers/databricks-claude/issues/116)) ([8fa432a](https://github.com/IceRhymers/databricks-claude/commit/8fa432aca093239fbcc0c31c772d5b36819e42ba))
* require conventional commit prefix in agent instructions ([a6db721](https://github.com/IceRhymers/databricks-claude/commit/a6db72118d8979bf5122004ba7287318059cff69))

## [0.15.0](https://github.com/IceRhymers/databricks-claude/compare/v0.14.0...v0.15.0) (2026-05-01)


### Features

* **otel:** per-signal export control; `--otel-traces` exports Claude Code traces independently of metrics/logs ([#102](https://github.com/IceRhymers/databricks-claude/issues/102)) ([f0f1455](https://github.com/IceRhymers/databricks-claude/commit/f0f145525b5a2332db8cf5f79a1bd7066f9d56f3))
* **desktop:** `generate-trust-profile` subcommand with `--for-pkg` flag for pkg-scoped trust profiles ([fce4dee](https://github.com/IceRhymers/databricks-claude/commit/fce4deeca59d628cee2c45d9e6c9f9be3ec2cc36))


### Bug Fixes

* macOS `.pkg` ships unsigned — `productsign` requires an Apple-issued installer cert; build pipeline now asserts unsigned rather than attempting signing ([5e94ea2](https://github.com/IceRhymers/databricks-claude/commit/5e94ea20ff45dd5dff89d87332bc7822a41cf261))
* credential-helper symlink included in pkg payload; archive expanded before payload assertion ([eb3dc78](https://github.com/IceRhymers/databricks-claude/commit/eb3dc788e42d0eaa0f3805fb0fefd09ec2acc60b))
* explicitly unlock keychain and list identities before `codesign` ([9af5fea](https://github.com/IceRhymers/databricks-claude/commit/9af5fea5452daf2da1b3166e8a88a787c16d1a9c))

## [0.14.0](https://github.com/IceRhymers/databricks-claude/compare/v0.13.0...v0.14.0) (2026-04-28)


### Features

* --install-hooks performs first-run env setup; hook–desktop coexist ([da43ab9](https://github.com/IceRhymers/databricks-claude/commit/da43ab944e5cf4384061b6f08d17a7b59e372496))
* --install-hooks performs first-run env setup; hook–desktop coexist ([abf9871](https://github.com/IceRhymers/databricks-claude/commit/abf9871e585ac730e15a88d5e4e8165bd61e893b))
* emit databricks-claude-desktop.json for Claude Desktop dev mode ([313ae51](https://github.com/IceRhymers/databricks-claude/commit/313ae51ba0cbdbb3d32affc5d3ac9e13281f6f95))
* emit databricks-claude-desktop.json for Claude Desktop dev mode ([9c1af01](https://github.com/IceRhymers/databricks-claude/commit/9c1af01934ac001512b648fc2757b4548248ec1a))


### Bug Fixes

* update README to clarify Claude Desktop's inference management ([cdb61d0](https://github.com/IceRhymers/databricks-claude/commit/cdb61d0a0573d1d3160b8afdb603221ca44d1e57))

## [0.13.0](https://github.com/IceRhymers/databricks-claude/compare/v0.12.1...v0.13.0) (2026-04-28)


### Features

* --credential-helper and --generate-desktop-config for Claude Desktop ([12ef44c](https://github.com/IceRhymers/databricks-claude/commit/12ef44cea5cd4577ce6c800cb3ffba6ad3f17b4f))
* add --credential-helper and --generate-desktop-config for Claude Desktop ([00e05c1](https://github.com/IceRhymers/databricks-claude/commit/00e05c1c85c61235129b39136cd3140664c21b8e)), closes [#92](https://github.com/IceRhymers/databricks-claude/issues/92)
* document release-please conventional commit requirements in AGENTS.md ([bd3e865](https://github.com/IceRhymers/databricks-claude/commit/bd3e865162f291a6f07549041306356547425da9))


### Bug Fixes

* address PR [#93](https://github.com/IceRhymers/databricks-claude/issues/93) review — generate both artifacts, MDM CLI pinning, README docs ([ba7c7b3](https://github.com/IceRhymers/databricks-claude/commit/ba7c7b386bf662d656a15058ec686a066aae9c83))
* make credential helper work from Claude Desktop's GUI subprocess ([c58b3f0](https://github.com/IceRhymers/databricks-claude/commit/c58b3f02d86e076b64d96d13b3191efbd30445c3))

## [0.12.1](https://github.com/IceRhymers/databricks-claude/compare/v0.12.0...v0.12.1) (2026-04-10)


### Bug Fixes

* scheme-aware health check and TLS arg forwarding in headless.Ensure ([#84](https://github.com/IceRhymers/databricks-claude/issues/84)) ([fd3b0aa](https://github.com/IceRhymers/databricks-claude/commit/fd3b0aaf0c13100abd8000c11348846444bf62da))

## [0.12.0](https://github.com/IceRhymers/databricks-claude/compare/v0.11.0...v0.12.0) (2026-04-10)


### Bug Fixes

* treat missing settings.json as empty document ([#81](https://github.com/IceRhymers/databricks-claude/issues/81)) ([e957c4f](https://github.com/IceRhymers/databricks-claude/commit/e957c4f0db558e797db1609878c111dacb2919c1))


### Refactoring

* extract shared proxy utilities into pkg/ ([#79](https://github.com/IceRhymers/databricks-claude/issues/79)) — extracts health, lifecycle, state, and headless packages from the top-level package; adds ListenerPort, PathForPort, and PrintUpdateNotice helpers; ~185 lines removed from main package with no behavior changes

## [0.11.0](https://github.com/IceRhymers/databricks-claude/compare/v0.10.1...v0.11.0) (2026-04-10)


### Features

* add pkg/updater — GitHub release checker with cache and numeric semver comparison ([#75](https://github.com/IceRhymers/databricks-claude/issues/75)) ([32e58cf](https://github.com/IceRhymers/databricks-claude/commit/32e58cf52fa7837f59283d4c921d9472f855dd63))
* add update subcommand and startup update check ([#77](https://github.com/IceRhymers/databricks-claude/issues/77)) ([0e9b525](https://github.com/IceRhymers/databricks-claude/commit/0e9b525b0da0cff2246daa12d685b4352192af57))

## [0.10.1](https://github.com/IceRhymers/databricks-claude/compare/v0.10.0...v0.10.1) (2026-04-09)


### Bug Fixes

* accept --shell=bash form from Homebrew generate_completions_from_executable ([#71](https://github.com/IceRhymers/databricks-claude/issues/71)) ([4ec2a08](https://github.com/IceRhymers/databricks-claude/commit/4ec2a082879d483b212f3bcf7e022535dc303c62))

## [0.10.0](https://github.com/IceRhymers/databricks-claude/compare/v0.9.1...v0.10.0) (2026-04-09)


### Features

* add --install-hooks, --uninstall-hooks, and proxy lifecycle hooks ([#63](https://github.com/IceRhymers/databricks-claude/issues/63)) ([1e2b84f](https://github.com/IceRhymers/databricks-claude/commit/1e2b84f2f008dab16bbe4eeee673f9a72a29a46d))
* add POST /shutdown endpoint and idle timeout for headless lifecycle management ([#61](https://github.com/IceRhymers/databricks-claude/issues/61)) ([fe184d1](https://github.com/IceRhymers/databricks-claude/commit/fe184d142019c0348793dd8a1538dd166c987557))
* add shell tab completions (bash/zsh/fish) ([#69](https://github.com/IceRhymers/databricks-claude/issues/69)) ([7b011e4](https://github.com/IceRhymers/databricks-claude/commit/7b011e471f05a320d1407372248ebe0d8adbdad7))


### Bug Fixes

* remove DATABRICKS_HOST and DATABRICKS_CONFIG_PROFILE from settings.json env ([#67](https://github.com/IceRhymers/databricks-claude/issues/67)) ([22649e7](https://github.com/IceRhymers/databricks-claude/commit/22649e7acfc2545803f844a99c85399da5f0689a))

## [0.9.1](https://github.com/IceRhymers/databricks-claude/compare/v0.9.0...v0.9.1) (2026-04-07)


### Bug Fixes

* normalize trailing newline in main.go ([#52](https://github.com/IceRhymers/databricks-claude/issues/52)) ([f2ae5cf](https://github.com/IceRhymers/databricks-claude/commit/f2ae5cfa1da9ab851388fdeb6b0ad5e24b9962d4))

## [0.9.0](https://github.com/IceRhymers/databricks-claude/compare/v0.8.1...v0.9.0) (2026-04-07)


### Features

* dispatch Homebrew formula update on release ([#48](https://github.com/IceRhymers/databricks-claude/issues/48)) ([f960290](https://github.com/IceRhymers/databricks-claude/commit/f96029008e9fe1132a0449715b150b205928d80a))


### Bug Fixes

* correct YAML syntax in release.yml (missing newline before update-homebrew job) ([#49](https://github.com/IceRhymers/databricks-claude/issues/49)) ([412c01e](https://github.com/IceRhymers/databricks-claude/commit/412c01eff6e1565003730f2d28fe8472db174d23))

## [0.8.1](https://github.com/IceRhymers/databricks-claude/compare/v0.8.0...v0.8.1) (2026-04-07)


### Bug Fixes

* use platform-specific builds for syscall.Flock and syscall.Umask ([16af70b](https://github.com/IceRhymers/databricks-claude/commit/16af70bf566b0a585fa09c38ab41ad0942e8ff7a))

## [0.8.0](https://github.com/IceRhymers/databricks-claude/compare/v0.7.2...v0.8.0) (2026-04-07)


### Features

* add --headless flag for proxy-only startup ([#43](https://github.com/IceRhymers/databricks-claude/issues/43)) ([3141f92](https://github.com/IceRhymers/databricks-claude/commit/3141f926f95bf89b8faed083433512cba2de09d3))
