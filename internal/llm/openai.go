package llm

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"os"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

// openAIModel is an ADK model.LLM backed by ADK's official openai-go plugin,
// talking to the Chat Completions API — the surface OpenAI-compatible
// providers (llama.cpp, llama-swap, OpenRouter, ...) implement. Streaming,
// retries, and the genai↔OpenAI translation live in the plugin; this wrapper
// only injects the per-model generation parameters, which the plugin reads
// from genai.GenerateContentConfig.
//
// Note: chain-of-thought from reasoning-parser servers (reasoning_content)
// is not surfaced as thought parts — neither openai-go's typed structs nor
// the plugin carry that field.
type openAIModel struct {
	inner       model.LLM
	temperature *float64
	maxTokens   int
}

// NewOpenAIModel creates an OpenAI-compatible model.LLM.
// If baseURL is empty, it defaults to https://api.openai.com/v1.
func NewOpenAIModel(modelName, apiKeyEnv, baseURL string, timeout time.Duration) (model.LLM, error) {
	return NewOpenAIModelWithOptions(modelName, apiKeyEnv, baseURL, timeout, nil, 0)
}

// NewOpenAIModelWithOptions creates an OpenAI model with temperature/max_tokens.
func NewOpenAIModelWithOptions(modelName, apiKeyEnv, baseURL string, timeout time.Duration, temperature *float64, maxTokens int) (model.LLM, error) {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	apiKey := ""
	if apiKeyEnv != "" {
		apiKey = os.Getenv(apiKeyEnv)
	}
	inner, err := openaimodel.NewModel(context.Background(), modelName, &openaimodel.ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: timeout},
		API:        openaimodel.APIChatCompletions,
	})
	if err != nil {
		return nil, fmt.Errorf("create openai model: %w", err)
	}
	return &openAIModel{inner: inner, temperature: temperature, maxTokens: maxTokens}, nil
}

// Name implements model.LLM.
func (m *openAIModel) Name() string {
	return m.inner.Name()
}

// GenerateContent implements model.LLM.
func (m *openAIModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if m.temperature != nil || m.maxTokens > 0 {
		req = withGenerationConfig(req, m.temperature, m.maxTokens)
	}
	return m.inner.GenerateContent(ctx, req, stream)
}

// withGenerationConfig returns a shallow copy of req with Temperature and
// MaxOutputTokens set. The plugin takes generation parameters from
// genai.GenerateContentConfig, so the per-model values configured at
// construction are injected onto each request. The caller's request is never
// mutated.
func withGenerationConfig(req *model.LLMRequest, temperature *float64, maxTokens int) *model.LLMRequest {
	cloned := *req
	cfg := cloned.Config
	if cfg == nil {
		cfg = &genai.GenerateContentConfig{}
	} else {
		cfgCopy := *cfg
		cfg = &cfgCopy
	}
	if temperature != nil {
		t := float32(*temperature)
		cfg.Temperature = &t
	}
	if maxTokens > 0 {
		cfg.MaxOutputTokens = int32(maxTokens)
	}
	cloned.Config = cfg
	return &cloned
}
