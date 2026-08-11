package main

import "github.com/IceRhymers/databricks-agents/internal/core/dbxauth"

// gatewayPath is codex's AI Gateway suffix — the OpenAI-shaped route, distinct
// from claude's /ai-gateway/anthropic. Per-tool value; the token/auth machinery
// lives in internal/core/dbxauth (#218).
const gatewayPath = "/ai-gateway/openai/v1"

// modelsPath is the working OpenAI-shaped model-discovery endpoint. Note it is
// NOT under gatewayPath: Databricks serves the models list at /ai-gateway/v1/models
// (no /openai/v1 segment) and 400s on /ai-gateway/openai/v1/models. The proxy
// reroutes GET /models here and transforms the body to the OpenAI shape so
// OpenAI-shaped clients (e.g. JetBrains IntelliJ) can enumerate models.
const modelsPath = "/ai-gateway/v1/models"

// ConstructGatewayURL builds the AI Gateway URL for the Codex proxy endpoint.
// Format: {host}/ai-gateway/openai/v1
func ConstructGatewayURL(host string) string {
	return dbxauth.GatewayURL(host, gatewayPath)
}

// ConstructModelsURL builds the OpenAI-shaped model-discovery URL.
// Format: {host}/ai-gateway/v1/models
func ConstructModelsURL(host string) string {
	return dbxauth.GatewayURL(host, modelsPath)
}
