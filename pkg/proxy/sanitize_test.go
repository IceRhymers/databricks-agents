package proxy

import "testing"

func TestSanitizeLogOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bearer token in body",
			input: `{"Authorization": "Bearer dapi1234567890abcdef"}`,
			want:  `{"Authorization": "[REDACTED]"}`,
		},
		{
			name:  "dapi PAT token",
			input: `token was dapi-abc123XYZ in the request`,
			want:  `token was [REDACTED] in the request`,
		},
		{
			name:  "x-api-key header case insensitive",
			input: `X-Api-Key: secret-key-value`,
			want:  `[REDACTED]`,
		},
		{
			name:  "x-api-key lowercase",
			input: `x-api-key: another-secret`,
			want:  `[REDACTED]`,
		},
		{
			name:  "safe string unchanged",
			input: `{"status": "ok", "code": 200}`,
			want:  `{"status": "ok", "code": 200}`,
		},
		{
			name:  "multiple sensitive values",
			input: `Bearer tok123 and dapi-xyz456`,
			want:  `[REDACTED] and [REDACTED]`,
		},
		{
			name:  "bearer header word itself is redacted",
			input: `Bearer mysecrettoken`,
			want:  `[REDACTED]`,
		},
		{
			name:  "dapi token without leading hyphen",
			input: `token was dapi01234567890abcdef in the request`,
			want:  `token was [REDACTED] in the request`,
		},
		{
			name:  "dapi token with internal hyphens",
			input: `token was dapi-abc-123-xyz in the request`,
			want:  `token was [REDACTED] in the request`,
		},
		{
			name:  "x-databricks-authorization header",
			input: `X-Databricks-Authorization: dapi01234567890abcdef`,
			want:  `[REDACTED]`,
		},
		{
			name:  "x-databricks-authorization header lowercase",
			input: `x-databricks-authorization: some-pat-token`,
			want:  `[REDACTED]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLogOutput(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeLogOutput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
