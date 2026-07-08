package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// OpenAIProvider calls the OpenAI chat completions API.
type OpenAIProvider struct {
	model   string
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates an OpenAI-compatible provider.
// If baseURL is empty, it defaults to the OpenAI API.
func NewOpenAIProvider(model, apiKeyEnv, baseURL string, timeout time.Duration) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		model:   model,
		apiKey:  os.Getenv(apiKeyEnv),
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

// Generate implements Provider.
func (p *OpenAIProvider) Generate(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (*Response, error) {
	reqBody := map[string]any{
		"model":    p.model,
		"messages": buildMessages(systemPrompt, userPrompt),
	}
	if len(tools) > 0 {
		reqBody["tools"] = buildOpenAITools(tools)
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai status %d: %s", resp.StatusCode, string(body))
	}

	var result openAIChatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned")
	}
	choice := result.Choices[0]
	out := &Response{
		Content: choice.Message.Content,
		Usage: Usage{
			InputTokens:  result.Usage.PromptTokens,
			OutputTokens: result.Usage.CompletionTokens,
			TotalTokens:  result.Usage.TotalTokens,
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return out, nil
}

func buildMessages(systemPrompt, userPrompt string) []map[string]string {
	msgs := []map[string]string{}
	if systemPrompt != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": systemPrompt})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": userPrompt})
	return msgs
}

func buildOpenAITools(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	return out
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
				Type string `json:"type"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}
