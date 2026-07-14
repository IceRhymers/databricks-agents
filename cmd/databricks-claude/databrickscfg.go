package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// readDatabricksCfgHost reads the host for the given profile from ~/.databrickscfg.
// Returns empty string if not found or file unreadable.
func readDatabricksCfgHost(profile string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(home, ".databrickscfg"))
	if err != nil {
		return ""
	}
	defer f.Close()
	return parseDatabricksCfgHost(f, profile)
}

// parseDatabricksCfgHost parses an INI-formatted Databricks config and returns
// the host value for the given profile. Returns empty string if not found.
func parseDatabricksCfgHost(r io.Reader, profile string) string {
	target := "[" + profile + "]"
	inSection := false
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inSection = strings.EqualFold(line, target)
			continue
		}
		if inSection {
			k, v, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(k) == "host" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}
