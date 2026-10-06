package llm

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"os"
	"strings"
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
// from genai.GenerateContentConfig, and — when captureReasoning is set —
// recovers chain-of-thought the plugin drops (see openai_reasoning.go).
type openAIModel struct {
	inner            model.LLM
	temperature      *float64
	maxTokens        int
	captureReasoning bool
}

// NewOpenAIModel creates an OpenAI-compatible model.LLM.
// If baseURL is empty, it defaults to https://api.openai.com/v1.
func NewOpenAIModel(modelName, apiKeyEnv, baseURL string, timeout time.Duration) (model.LLM, error) {
	return NewOpenAIModelWithOptions(modelName, apiKeyEnv, baseURL, timeout, nil, 0, false)
}

// NewOpenAIModelWithOptions creates an OpenAI model with temperature/max_tokens.
// captureReasoning enables recovery of reasoning_content from reasoning-parser
// servers as thought parts (off by default).
func NewOpenAIModelWithOptions(modelName, apiKeyEnv, baseURL string, timeout time.Duration, temperature *float64, maxTokens int, captureReasoning bool) (model.LLM, error) {
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
	httpClient := &http.Client{Timeout: timeout}
	if captureReasoning {
		httpClient.Transport = &reasoningTransport{next: http.DefaultTransport}
	}
	inner, err := openaimodel.NewModel(context.Background(), modelName, &openaimodel.ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		HTTPClient: httpClient,
		API:        openaimodel.APIChatCompletions,
	})
	if err != nil {
		return nil, fmt.Errorf("create openai model: %w", err)
	}
	return &openAIModel{inner: inner, temperature: temperature, maxTokens: maxTokens, captureReasoning: captureReasoning}, nil
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
	if !m.captureReasoning || !stream {
		return m.inner.GenerateContent(ctx, req, stream)
	}

	// The collector rides the call's context; the transport feeding it is on
	// the model's HTTP client. Only streamed calls are recovered — the tee
	// activates on SSE responses, so nothing is collected for the blocking
	// path and there is nothing to inject.
	col := &reasoningCollector{}
	ctx = context.WithValue(ctx, reasoningCollectorContextKey{}, col)
	inner := m.inner.GenerateContent(ctx, req, stream)

	return func(yield func(*model.LLMResponse, error) bool) {
		// drain forwards reasoning collected so far as one partial response.
		// Reasoning models emit all reasoning before content, so draining
		// just before each inner response yields the practical emission
		// order; the final drain after the iterator ends (the stream is then
		// fully consumed, hence the collector quiescent) catches the rest.
		drain := func() bool {
			deltas := col.drain()
			if len(deltas) == 0 {
				return true
			}
			thought := &model.LLMResponse{
				Partial: true,
				Content: &genai.Content{
					Role:  genai.RoleModel,
					Parts: []*genai.Part{{Text: strings.Join(deltas, ""), Thought: true}},
				},
			}
			return yield(thought, nil)
		}
		for resp, err := range inner {
			if !drain() {
				return
			}
			if !yield(resp, err) {
				return
			}
		}
		drain()
	}
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
