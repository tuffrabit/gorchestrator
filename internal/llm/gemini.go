package llm

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
)

// NewGeminiModel creates an ADK Gemini model.LLM from configuration.
func NewGeminiModel(ctx context.Context, modelName, apiKey string, timeout time.Duration) (model.LLM, error) {
	if modelName == "" {
		modelName = "gemini-2.0-flash"
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	httpOpts := genai.HTTPOptions{Timeout: &timeout}
	cfg := &genai.ClientConfig{
		APIKey:      apiKey,
		HTTPOptions: httpOpts,
	}
	m, err := gemini.NewModel(ctx, modelName, cfg)
	if err != nil {
		return nil, fmt.Errorf("create gemini model: %w", err)
	}
	return m, nil
}
