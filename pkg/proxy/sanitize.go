package proxy

import "regexp"

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`Bearer [^\s"]+`),
	regexp.MustCompile(`dapi[a-zA-Z0-9-]+`),
	regexp.MustCompile(`(?i)x-api-key:\s*[^\s"]+`),
	regexp.MustCompile(`(?i)x-databricks-authorization:\s*\S+`),
}

// sanitizeLogOutput redacts sensitive values (Bearer tokens, PATs, API keys)
// from strings before they are written to logs.
func sanitizeLogOutput(s string) string {
	for _, re := range sensitivePatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
