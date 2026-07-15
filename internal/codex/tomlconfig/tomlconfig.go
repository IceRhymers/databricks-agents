// Package tomlconfig manages the Codex CLI config.toml file.
// It uses simple string-based manipulation rather than a full TOML parser,
// keeping the zero-external-dependency constraint.
package tomlconfig

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// PatchConfig holds the values to inject into config.toml.
type PatchConfig struct {
	ProxyURL            string // e.g., "http://127.0.0.1:54321"
	Model               string // e.g., "databricks-gpt-5-5"
	ModelExplicit       bool   // true when --model was explicitly passed
	OTELLogsEndpoint    string // e.g., "http://127.0.0.1:54321/otel/v1/logs"
	OTELMetricsEndpoint string // e.g., "http://127.0.0.1:54321/otel/v1/metrics"
}

// sentinel is stored in originals when a key/section was absent before patching.
const sentinel = "\x00nil"

// Manager reads, patches, and restores the Codex config.toml file.
type Manager struct {
	configPath string
	backupPath string
	original   []byte // saved original content for crash-recovery backup

	// Surgical restore state: tracks what we changed so Restore only undoes
	// what we touched. Keys map to original line/block content, or sentinel
	// if the key/section was absent before patching.
	origRootKeys    map[string]string // root key name -> original line or sentinel
	origSections    map[string]string // section header (e.g. "profiles.databricks-proxy") -> original block or sentinel
	origModelLine   string            // original "model = ..." line in [profiles.databricks-proxy], or sentinel
	patchedModelVal string            // model value we wrote (for preserve-if-present check)
}

// NewManager creates a new config.toml manager.
// configPath defaults to ~/.codex/config.toml if empty.
func NewManager(configPath string) *Manager {
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("databricks-codex: cannot determine home dir: %v", err)
			configPath = ".codex/config.toml"
		} else {
			configPath = filepath.Join(home, ".codex", "config.toml")
		}
	}
	return &Manager{
		configPath:   configPath,
		backupPath:   configPath + ".databricks-codex-backup",
		origRootKeys: make(map[string]string),
		origSections: make(map[string]string),
	}
}

// ConfigPath returns the path to config.toml.
func (m *Manager) ConfigPath() string {
	return m.configPath
}

// Backup reads the current config.toml and saves the original content
// both in memory and to a backup file for crash recovery.
func (m *Manager) Backup() error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			m.original = nil
			return nil
		}
		return fmt.Errorf("read config.toml: %w", err)
	}
	m.original = data

	if err := atomicWrite(m.backupPath, data); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

// managedRootKeys lists top-level keys we manage.
var managedRootKeys = []string{"profile"}

// managedSections lists section headers we manage.
var managedSections = []string{
	"profiles.databricks-proxy",
	"model_providers.databricks-proxy",
	"otel",
}

// Patch performs surgical patching of config.toml: it reads the existing file,
// saves originals for keys/sections it will touch, then injects/updates only
// managed keys and sections. All non-managed content is preserved byte-for-byte.
func (m *Manager) Patch(cfg PatchConfig) error {
	content := ""
	if m.original != nil {
		content = string(m.original)
	} else if data, err := os.ReadFile(m.configPath); err == nil {
		content = string(data)
	}

	// --- Save originals and patch root keys ---
	content = m.patchRootKey(content, "profile", `"databricks-proxy"`)

	// --- Parse sections for surgical section patching ---
	content = m.patchSection(content, "profiles.databricks-proxy",
		m.buildProfileSection(cfg))

	content = m.patchSection(content, "model_providers.databricks-proxy",
		m.buildProviderSection(cfg))

	// Always handle the [otel] section: when both endpoints are set, patch
	// it; when both are empty, remove it if it exists. This makes --no-otel
	// (or --no-otel-metrics/--no-otel-logs that clears the last remaining
	// signal) actually erase the section from config.toml — not just leave
	// stale exporter lines behind.
	if cfg.OTELLogsEndpoint != "" || cfg.OTELMetricsEndpoint != "" {
		content = m.patchSection(content, "otel",
			m.buildOTELSection(cfg))
	} else {
		content = m.removeSection(content, "otel")
	}

	if err := atomicWrite(m.configPath, []byte(content)); err != nil {
		return fmt.Errorf("write patched config.toml: %w", err)
	}
	return nil
}

// buildProfileSection builds the [profiles.databricks-proxy] section body.
// It implements preserve-if-present for the model key.
func (m *Manager) buildProfileSection(cfg PatchConfig) string {
	var b strings.Builder
	b.WriteString("model_provider = \"databricks-proxy\"\n")

	// Preserve-if-present: check if user already has a model line in this section.
	existingModel := m.findModelInSection(string(m.original), "profiles.databricks-proxy")
	if existingModel != "" {
		m.origModelLine = existingModel
	} else {
		m.origModelLine = sentinel
	}

	if cfg.ModelExplicit {
		// User explicitly passed --model: always write it.
		b.WriteString(fmt.Sprintf("model = %q\n", cfg.Model))
		m.patchedModelVal = cfg.Model
	} else if existingModel != "" {
		// Preserve user's existing model line in the profile section as-is.
		b.WriteString(existingModel + "\n")
	} else {
		// No explicit flag, no model in the profile section.
		// Check root-level model — since we switch the active profile to
		// databricks-proxy, the root-level model would be ignored by Codex.
		// Carry it into the profile section so the user's choice is respected.
		rootModel := m.findRootModel(string(m.original))
		if rootModel != "" {
			b.WriteString(fmt.Sprintf("model = %q\n", rootModel))
			m.patchedModelVal = rootModel
		} else if cfg.Model != "" {
			// Fall back to the resolved model (saved state or built-in default).
			b.WriteString(fmt.Sprintf("model = %q\n", cfg.Model))
			m.patchedModelVal = cfg.Model
		}
	}

	return b.String()
}

// buildProviderSection builds the [model_providers.databricks-proxy] section body.
func (m *Manager) buildProviderSection(cfg PatchConfig) string {
	var b strings.Builder
	b.WriteString("name = \"Databricks Proxy\"\n")
	b.WriteString(fmt.Sprintf("base_url = %q\n", cfg.ProxyURL))
	b.WriteString("api_key = \"databricks-proxy\"\n")
	b.WriteString("wire_api = \"responses\"\n")
	return b.String()
}

// buildOTELSection builds the [otel] section body.
// Emits `exporter` (logs) and/or `metrics_exporter` for whichever endpoints
// are non-empty. Both can coexist in the same [otel] block.
//
// Note: Codex's upstream `metrics_exporter` default is Statsig
// (https://ab.chatgpt.com/otlp/v1/metrics). We do NOT defensively rewrite
// this at the proxy layer; setting `metrics_exporter` here is the user's
// explicit opt-in to route metrics through Databricks instead.
func (m *Manager) buildOTELSection(cfg PatchConfig) string {
	var b strings.Builder
	b.WriteString("environment = \"production\"\n")
	if cfg.OTELLogsEndpoint != "" {
		b.WriteString(fmt.Sprintf("exporter = { otlp-http = { endpoint = %q, protocol = \"binary\" } }\n", cfg.OTELLogsEndpoint))
	}
	if cfg.OTELMetricsEndpoint != "" {
		b.WriteString(fmt.Sprintf("metrics_exporter = { otlp-http = { endpoint = %q, protocol = \"binary\" } }\n", cfg.OTELMetricsEndpoint))
	}
	return b.String()
}

// patchRootKey finds a root-level key in the content, saves its original value,
// and replaces or appends the managed value.
func (m *Manager) patchRootKey(content, key, value string) string {
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, key) && !inAnySection(lines, i) {
			m.origRootKeys[key] = line
			lines[i] = fmt.Sprintf("%s = %s", key, value)
			found = true
			break
		}
	}
	if !found {
		m.origRootKeys[key] = sentinel
		// Prepend the root key at the top (after any leading comments/blank lines
		// but before the first section).
		insertIdx := 0
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				insertIdx = i
				break
			}
			insertIdx = i + 1
		}
		newLine := fmt.Sprintf("%s = %s", key, value)
		lines = insertAt(lines, insertIdx, newLine)
	}
	return strings.Join(lines, "\n")
}

// removeSection finds a [section] in the content and removes it entirely
// (header + body up to the next section header or EOF). The original block
// is recorded in origSections so Restore() can put it back.
//
// If the section is not present, this is a no-op — and crucially, we do
// NOT record a sentinel, because there's nothing to undo on Restore().
func (m *Manager) removeSection(content, sectionName string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")

	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			startIdx = i
			break
		}
	}

	if startIdx == -1 {
		// Section absent — nothing to remove, nothing to track.
		return content
	}

	// Find section end (next section header or EOF).
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}

	// Save original block so Restore() can put it back if needed.
	m.origSections[sectionName] = strings.Join(lines[startIdx:endIdx], "\n")

	// Also drop the trailing blank line that typically separates sections,
	// so removing [otel] doesn't leave a double-blank gap behind. Only do
	// this if endIdx is followed by a blank line (i.e. we're removing a
	// mid-file section, not a trailing one).
	if endIdx < len(lines) && strings.TrimSpace(lines[endIdx-1]) == "" {
		// endIdx-1 is already a blank line inside the section we're
		// removing — nothing extra to do.
	} else if endIdx < len(lines) && strings.TrimSpace(lines[endIdx]) == "" {
		// The line right after the section is blank — consume it.
		endIdx++
	}
	// Also drop a blank line immediately BEFORE the section if present,
	// so we don't leave a dangling separator.
	if startIdx > 0 && strings.TrimSpace(lines[startIdx-1]) == "" {
		startIdx--
	}

	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:startIdx]...)
	newLines = append(newLines, lines[endIdx:]...)

	return strings.Join(newLines, "\n")
}

// patchSection finds a [section] in the content, saves its original block,
// and replaces or appends the managed section.
func (m *Manager) patchSection(content, sectionName, body string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")

	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			startIdx = i
			break
		}
	}

	if startIdx == -1 {
		// Section absent — record sentinel, append.
		m.origSections[sectionName] = sentinel
		var sb strings.Builder
		sb.WriteString(header + "\n")
		sb.WriteString(body)
		// Ensure content ends with newline before appending.
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		content += "\n" + sb.String()
		return content
	}

	// Find section end (next section header or EOF).
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}

	// Save original block.
	origBlock := strings.Join(lines[startIdx:endIdx], "\n")
	m.origSections[sectionName] = origBlock

	// Build replacement.
	var replacement []string
	replacement = append(replacement, header)
	for _, line := range strings.Split(body, "\n") {
		if line != "" {
			replacement = append(replacement, line)
		}
	}

	// Replace the section block.
	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:startIdx]...)
	newLines = append(newLines, replacement...)
	newLines = append(newLines, lines[endIdx:]...)

	return strings.Join(newLines, "\n")
}

// findRootModel returns the value of a root-level "model = ..." line (not inside any section).
// Returns empty string if not found.
func (m *Manager) findRootModel(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, "model") && !inAnySection(lines, i) {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"`)
				return val
			}
		}
	}
	return ""
}

// findModelInSection looks for a "model = ..." line inside a named section.
func (m *Manager) findModelInSection(content, sectionName string) string {
	if content == "" {
		return ""
	}
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if strings.HasPrefix(trimmed, "model") && strings.Contains(trimmed, "=") {
				// Distinguish "model" from "model_provider"
				afterKey := strings.TrimPrefix(trimmed, "model")
				if len(afterKey) > 0 && (afterKey[0] == ' ' || afterKey[0] == '=') {
					return line
				}
			}
		}
	}
	return ""
}

// Restore performs surgical restoration: only removes/restores keys and sections
// that we patched. Non-managed content is untouched.
func (m *Manager) Restore() error {
	// If we never had an original file and we added everything, remove the file.
	if m.original == nil && allSentinels(m.origRootKeys) && allSentinels(m.origSections) {
		os.Remove(m.configPath)
		os.Remove(m.backupPath)
		return nil
	}

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			os.Remove(m.backupPath)
			return nil
		}
		return fmt.Errorf("read config.toml for restore: %w", err)
	}
	content := string(data)

	// Restore root keys.
	for key, orig := range m.origRootKeys {
		content = m.restoreRootKey(content, key, orig)
	}

	// Restore sections.
	for sectionName, orig := range m.origSections {
		content = m.restoreSection(content, sectionName, orig)
	}

	// Restore model line if it was absent before.
	if m.origModelLine == sentinel {
		content = m.removeModelFromSection(content, "profiles.databricks-proxy")
	} else if m.origModelLine != "" && m.origModelLine != sentinel {
		content = m.restoreModelInSection(content, "profiles.databricks-proxy", m.origModelLine)
	}

	// Clean up trailing whitespace.
	content = strings.TrimRight(content, "\n") + "\n"

	if err := atomicWrite(m.configPath, []byte(content)); err != nil {
		return fmt.Errorf("restore config.toml: %w", err)
	}
	os.Remove(m.backupPath)
	return nil
}

// restoreRootKey restores a single root key to its original state.
func (m *Manager) restoreRootKey(content, key, orig string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, key) && !inAnySection(lines, i) {
			if orig == sentinel {
				// Was absent — remove the line.
				lines = removeAt(lines, i)
			} else {
				lines[i] = orig
			}
			return strings.Join(lines, "\n")
		}
	}
	return content
}

// restoreSection restores a section to its original state.
func (m *Manager) restoreSection(content, sectionName, orig string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")

	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return content
	}

	// Find section end.
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}

	if orig == sentinel {
		// Section was absent — remove the entire block.
		// Also remove a preceding blank line if present.
		removeStart := startIdx
		if removeStart > 0 && strings.TrimSpace(lines[removeStart-1]) == "" {
			removeStart--
		}
		newLines := make([]string, 0, len(lines))
		newLines = append(newLines, lines[:removeStart]...)
		newLines = append(newLines, lines[endIdx:]...)
		return strings.Join(newLines, "\n")
	}

	// Restore original block.
	origLines := strings.Split(orig, "\n")
	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:startIdx]...)
	newLines = append(newLines, origLines...)
	newLines = append(newLines, lines[endIdx:]...)
	return strings.Join(newLines, "\n")
}

// removeModelFromSection removes the model line from a section.
func (m *Manager) removeModelFromSection(content, sectionName string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")
	inSection := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if strings.HasPrefix(trimmed, "model") && strings.Contains(trimmed, "=") {
				afterKey := strings.TrimPrefix(trimmed, "model")
				if len(afterKey) > 0 && (afterKey[0] == ' ' || afterKey[0] == '=') {
					lines = removeAt(lines, i)
					return strings.Join(lines, "\n")
				}
			}
		}
	}
	return content
}

// restoreModelInSection restores the model line in a section to its original value.
func (m *Manager) restoreModelInSection(content, sectionName, origLine string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")
	inSection := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if strings.HasPrefix(trimmed, "model") && strings.Contains(trimmed, "=") {
				afterKey := strings.TrimPrefix(trimmed, "model")
				if len(afterKey) > 0 && (afterKey[0] == ' ' || afterKey[0] == '=') {
					lines[i] = origLine
					return strings.Join(lines, "\n")
				}
			}
		}
	}
	return content
}

// RestoreFromBackup recovers from a crash by restoring from the backup file.
// Returns false if no backup exists (clean state).
func (m *Manager) RestoreFromBackup() bool {
	data, err := os.ReadFile(m.backupPath)
	if err != nil {
		return false
	}
	log.Printf("databricks-codex: restoring config.toml from crash backup")
	m.original = data
	// For crash recovery, do a full restore from backup.
	if m.original == nil {
		os.Remove(m.configPath)
	} else {
		if err := atomicWrite(m.configPath, m.original); err != nil {
			log.Printf("databricks-codex: crash restore failed: %v", err)
		}
	}
	os.Remove(m.backupPath)
	return true
}

// UpdateProxyURL updates only the base_url in the managed config.toml.
// Used for multi-session handoff.
func (m *Manager) UpdateProxyURL(newURL string) error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("read config for proxy URL update: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "base_url") && strings.Contains(trimmed, "=") {
			lines[i] = fmt.Sprintf("base_url = %q", newURL)
			break
		}
	}

	return atomicWrite(m.configPath, []byte(strings.Join(lines, "\n")))
}

// --- Helpers ---

// isRootKey checks if a trimmed line is a root-level assignment for the given key.
func isRootKey(trimmed, key string) bool {
	return strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=")
}

// inAnySection returns true if line at idx is inside a [section] (i.e., there's
// a section header somewhere above it with no intervening root-level context).
func inAnySection(lines []string, idx int) bool {
	for i := idx - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			return true
		}
	}
	return false
}

// allSentinels returns true if all values in the map are sentinel.
func allSentinels(m map[string]string) bool {
	for _, v := range m {
		if v != sentinel {
			return false
		}
	}
	return true
}

// insertAt inserts a string at the given index in a slice.
func insertAt(lines []string, idx int, s string) []string {
	if idx >= len(lines) {
		return append(lines, s)
	}
	lines = append(lines, "")
	copy(lines[idx+1:], lines[idx:])
	lines[idx] = s
	return lines
}

// removeAt removes the element at idx from a slice.
func removeAt(lines []string, idx int) []string {
	return append(lines[:idx], lines[idx+1:]...)
}

// atomicWrite writes data to a temp file and renames it into place.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
