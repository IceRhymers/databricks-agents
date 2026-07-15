package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/IceRhymers/databricks-agents/internal/core/cli"
	"github.com/IceRhymers/databricks-agents/pkg/tokencache"
)

// resolveDatabricksCLI delegates to pkg/cli for CLI binary resolution.
// See pkg/cli.ResolveDatabricksCLI for the full lookup order.
func resolveDatabricksCLI(cmdName string) string {
	return cli.ResolveDatabricksCLI(cmdName)
}

// isExecutableFile delegates to pkg/cli.IsExecutableFile.
func isExecutableFile(path string) bool {
	return cli.IsExecutableFile(path)
}

// TokenProvider is an alias to the pkg type for backward compatibility.
type TokenProvider = tokencache.TokenProvider

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Expiry      string `json:"expiry"` // RFC3339 or Unix timestamp
}

// databricksFetcher implements tokencache.TokenFetcher using the Databricks CLI.
type databricksFetcher struct {
	profile string
	cmdName string
}

func (f *databricksFetcher) FetchToken(ctx context.Context) (string, time.Time, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(fetchCtx, resolveDatabricksCLI(f.cmdName), "auth", "token", "--profile", f.profile)
	out, err := cmd.Output()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("databricks auth token failed: %w", err)
	}

	resp, err := parseTokenResponse(out)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse token response: %w", err)
	}

	return resp.AccessToken, resp.expiryTime(), nil
}

// NewTokenProvider creates a new TokenProvider backed by the Databricks CLI.
// cmdName defaults to "databricks" if empty.
func NewTokenProvider(profile, cmdName string) *TokenProvider {
	if cmdName == "" {
		cmdName = "databricks"
	}
	return tokencache.NewTokenProvider(&databricksFetcher{
		profile: profile,
		cmdName: cmdName,
	})
}

// parseTokenResponse decodes the JSON output from "databricks auth token".
func parseTokenResponse(data []byte) (*tokenResponse, error) {
	var resp tokenResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if resp.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}
	return &resp, nil
}

// expiryTime parses the Expiry field, falling back to 55 minutes from now.
func (r *tokenResponse) expiryTime() time.Time {
	if r.Expiry != "" {
		// Try RFC3339 first
		if t, err := time.Parse(time.RFC3339, r.Expiry); err == nil {
			return t
		}
		// Try Unix timestamp (seconds)
		if secs, err := strconv.ParseInt(r.Expiry, 10, 64); err == nil {
			return time.Unix(secs, 0)
		}
	}
	// Conservative default: 55-minute expiry
	return time.Now().Add(55 * time.Minute)
}

type authEnvResponse struct {
	Env map[string]string `json:"env"`
}

// DiscoverHost calls "databricks auth env --profile <profile> --output json"
// and extracts the DATABRICKS_HOST value from the response.
func DiscoverHost(profile, cmdName string) (string, error) {
	if cmdName == "" {
		cmdName = "databricks"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, resolveDatabricksCLI(cmdName), "auth", "env", "--profile", profile, "--output", "json")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("databricks auth env failed: %w", err)
	}

	var resp authEnvResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("failed to parse auth env response: %w", err)
	}

	host, ok := resp.Env["DATABRICKS_HOST"]
	if !ok || host == "" {
		return "", fmt.Errorf("DATABRICKS_HOST not found in auth env response")
	}
	return host, nil
}

// ConstructGatewayURL returns the AI Gateway base URL for the given Databricks host.
func ConstructGatewayURL(host string) string {
	host = strings.TrimRight(host, "/")
	return host + "/ai-gateway/anthropic"
}
