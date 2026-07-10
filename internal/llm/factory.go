package llm

import (
	"context"
	"fmt"
	"os"
	"time"

	"google.golang.org/adk/v2/model"
)

// Config describes the model configuration passed to the factory.
type Config struct {
	Provider    string
	Model       string
	APIKeyEnv   string
	BaseURL     string
	Timeout     time.Duration
	Temperature *float64 // optional; provider-specific request field
	MaxTokens   int      // optional completion cap; 0 = provider default
}

// New builds a model.LLM from configuration.
func New(ctx context.Context, cfg Config) (model.LLM, error) {
	switch cfg.Provider {
	case "dryrun":
		return NewDryRunModel(cfg.Model), nil
	case "gemini":
		apiKey := ""
		if cfg.APIKeyEnv != "" {
			apiKey = os.Getenv(cfg.APIKeyEnv)
		}
		// Temperature/max_tokens for Gemini are left to ADK defaults for now.
		return NewGeminiModel(ctx, cfg.Model, apiKey, cfg.Timeout)
	case "openai":
		return NewOpenAIModelWithOptions(cfg.Model, cfg.APIKeyEnv, cfg.BaseURL, cfg.Timeout, cfg.Temperature, cfg.MaxTokens), nil
	case "anthropic":
		return NewAnthropicModelWithOptions(cfg.Model, cfg.APIKeyEnv, cfg.BaseURL, cfg.Timeout, cfg.Temperature, cfg.MaxTokens), nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}
}
