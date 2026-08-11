package main

import (
	"testing"
)

// TestConstructGatewayURL: verifies the host-relative AI Gateway URL format.
func TestConstructGatewayURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{
			name: "plain host",
			host: "https://dbc-abc123.cloud.databricks.com",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/openai/v1",
		},
		{
			name: "host with trailing slash is trimmed",
			host: "https://dbc-abc123.cloud.databricks.com/",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/openai/v1",
		},
		{
			name: "host with multiple trailing slashes is trimmed",
			host: "https://dbc-abc123.cloud.databricks.com///",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/openai/v1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ConstructGatewayURL(tc.host)
			if got != tc.want {
				t.Errorf("ConstructGatewayURL(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

// TestConstructModelsURL: the discovery endpoint is under /ai-gateway/v1/models
// (NOT /ai-gateway/openai/v1/models — Databricks 400s on that path).
func TestConstructModelsURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{
			name: "plain host",
			host: "https://dbc-abc123.cloud.databricks.com",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/v1/models",
		},
		{
			name: "trailing slash trimmed",
			host: "https://dbc-abc123.cloud.databricks.com/",
			want: "https://dbc-abc123.cloud.databricks.com/ai-gateway/v1/models",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ConstructModelsURL(tc.host)
			if got != tc.want {
				t.Errorf("ConstructModelsURL(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}
