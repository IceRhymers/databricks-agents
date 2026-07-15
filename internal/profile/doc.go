// Package profile defines the per-tool abstraction that lets the databricks
// agent wrappers (databricks-claude, and future databricks-codex /
// databricks-opencode) share a single launcher shape while each keeps its own
// settings/daemon/hook behavior.
//
// # Types
//
// Profile is the descriptor a launcher fills in: Name, ChildBinary,
// ConfigPath, GatewayPath, plus three behavioral seams —
//
//   - SettingsPatcher — writes (and, in a future #E, would restore) the tool's
//     settings file so it routes through the local proxy.
//   - DaemonStrategy  — installs/uninstalls/queries the long-lived OS service.
//   - HookInstaller   — manages the session-lifecycle hook entries.
//
// Request/response structs (PatchRequest, RestoreRequest, DaemonInstallRequest,
// DaemonStatus) are neutral and lossless: no settings.json /
// map[string]interface{} / plist / installOptions type ever crosses an
// interface boundary, yet DaemonInstallRequest mirrors all 8 launcher install
// fields and DaemonStatus mirrors all 11 status fields.
//
// # Where implementations live
//
// Each launcher is its own `package main` and therefore cannot be imported. The
// interfaces live here; the concrete implementations live in the launcher's own
// package. The claude implementations are in
// cmd/databricks-claude/profile_claude.go (claudeSettingsPatcher, claudeDaemon,
// claudeHooks), constructed via ClaudeProfile(). Sibling tools implement the
// same three interfaces in their own main packages.
//
// # Registry
//
// Registry is a name→Profile lookup surface delivered as an API shape for the
// future multiplexer (#H). It intentionally has no package-level Default and no
// init()-time registration: because each launcher is a separate main package,
// init-registration into a shared Default cannot compose a multiplexer that
// lives in yet another main package. #H must choose an explicit registration
// mechanism; Registry only proves the lookup round-trip.
package profile
