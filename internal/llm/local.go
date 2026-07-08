package llm

import (
	"time"
)

// NewLocalProvider creates a provider for an OpenAI-compatible local endpoint.
func NewLocalProvider(model, apiKeyEnv, baseURL string, timeout time.Duration) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "http://localhost:8080/v1"
	}
	return NewOpenAIProvider(model, apiKeyEnv, baseURL, timeout)
}
