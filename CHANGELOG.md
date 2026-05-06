# Changelog

## [0.16.1](https://github.com/IceRhymers/databricks-claude/compare/v0.16.0...v0.16.1) (2026-05-06)


### Bug Fixes

* add missing sanitize patterns and reject bare integers for --idle-timeout ([ec2e95b](https://github.com/IceRhymers/databricks-claude/commit/ec2e95be100985df4d1a01bfa7bde1752bf2e183))
* broaden sanitize patterns to cover dapi tokens without hyphen and X-Databricks-Authorization ([7cccfd8](https://github.com/IceRhymers/databricks-claude/commit/7cccfd8f9d4a9d738d3b9e9bd7e3662c9ccecae1))
* properly wrap lifecycle on health watcher takeover ([17fb740](https://github.com/IceRhymers/databricks-claude/commit/17fb740b2141aef664d04ca04838ec92e2d605fa))
* remove stale proxy entries from settings.json on startup ([fd7fc35](https://github.com/IceRhymers/databricks-claude/commit/fd7fc3582db6f5cea133cf01649129b4a641fb02))
* remove stale proxy entries from settings.json on startup ([8a1856b](https://github.com/IceRhymers/databricks-claude/commit/8a1856bdf2be3def5b2b4b40bc144f4f82ae49c5))
* restore isExecutableFile shim in root package for desktop_config.go ([d1effbb](https://github.com/IceRhymers/databricks-claude/commit/d1effbb0e388b848579f3cf0a81456799bf402b1))
* return errors from NewServer and Ensure instead of log.Fatalf; resolve CLI path in authcheck ([eeaa65e](https://github.com/IceRhymers/databricks-claude/commit/eeaa65ec3ce55b032ca9fd6e5643c13b9cf4cd6b))
* skip Authorization header when token empty; fix WebSocket goroutine leak ([b3bf297](https://github.com/IceRhymers/databricks-claude/commit/b3bf2971d403f565194c1ae33c798362a5892657))
* use os.CreateTemp for unique tmp filenames in settings writers ([a2d97a0](https://github.com/IceRhymers/databricks-claude/commit/a2d97a0b993f21b841346416acf7f2518b320c00))

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
