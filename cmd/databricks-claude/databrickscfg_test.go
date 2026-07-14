package main

import (
	"strings"
	"testing"
)

func TestParseDatabricksCfgHost(t *testing.T) {
	cfg := `
[DEFAULT]
host = https://default.azuredatabricks.net
auth_type = databricks-cli

[prod]
host = https://prod.azuredatabricks.net
auth_type = databricks-cli

[staging]
host  =  https://staging.azuredatabricks.net
auth_type = databricks-cli
`

	tests := []struct {
		profile string
		want    string
	}{
		{"DEFAULT", "https://default.azuredatabricks.net"},
		{"prod", "https://prod.azuredatabricks.net"},
		{"PROD", "https://prod.azuredatabricks.net"}, // case-insensitive section match
		{"staging", "https://staging.azuredatabricks.net"},
		{"missing", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := parseDatabricksCfgHost(strings.NewReader(cfg), tt.profile)
		if got != tt.want {
			t.Errorf("profile=%q: got %q, want %q", tt.profile, got, tt.want)
		}
	}
}

func TestParseDatabricksCfgHost_NoHostKey(t *testing.T) {
	cfg := `
[myprofile]
auth_type = databricks-cli
`
	got := parseDatabricksCfgHost(strings.NewReader(cfg), "myprofile")
	if got != "" {
		t.Errorf("expected empty string when host key absent, got %q", got)
	}
}

func TestParseDatabricksCfgHost_EmptyFile(t *testing.T) {
	got := parseDatabricksCfgHost(strings.NewReader(""), "DEFAULT")
	if got != "" {
		t.Errorf("expected empty string for empty file, got %q", got)
	}
}

func TestParseDatabricksCfgHost_StopsAtFirstHost(t *testing.T) {
	cfg := `
[mypro]
host = https://first.azuredatabricks.net
host = https://second.azuredatabricks.net
`
	got := parseDatabricksCfgHost(strings.NewReader(cfg), "mypro")
	if got != "https://first.azuredatabricks.net" {
		t.Errorf("expected first host value, got %q", got)
	}
}
