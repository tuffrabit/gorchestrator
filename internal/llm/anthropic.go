package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/model"
)

// AnthropicModel is an ADK model.LLM that calls the Anthropic Messages API.
type AnthropicModel struct {
	model   string
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewAnthropicModel creates an Anthropic model.LLM.
// If baseURL is empty, it defaults to https://api.anthropic.com/v1.
func NewAnthropicModel(modelName, apiKeyEnv, baseURL string, timeout time.Duration) model.LLM {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	apiKey := ""
	if apiKeyEnv != "" {
		apiKey = os.Getenv(apiKeyEnv)
	}
	return &AnthropicModel{
		model:   modelName,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

// Name implements model.LLM.
func (m *AnthropicModel) Name() string {
	return m.model
}

// GenerateContent implements model.LLM.
func (m *AnthropicModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		resp, err := m.generate(ctx, req)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(resp, nil)
	}
}

func (m *AnthropicModel) generate(ctx context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	body, err := m.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build anthropic request: %w", err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	const maxRetries = 3
	var lastErr error
	backoff := time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", m.baseURL+"/messages", bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("create anthropic request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-api-key", m.apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")

		httpResp, err := m.client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("anthropic request: %w", err)
		}

		respBody, err := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read anthropic response: %w", err)
		}

		if httpResp.StatusCode == http.StatusOK {
			var apiResp anthropicMessagesResponse
			if err := json.Unmarshal(respBody, &apiResp); err != nil {
				return nil, fmt.Errorf("parse anthropic response: %w", err)
			}
			return m.convertResponse(&apiResp), nil
		}

		lastErr = fmt.Errorf("anthropic status %d: %s", httpResp.StatusCode, string(respBody))
		if !isRetryableStatus(httpResp.StatusCode) {
			break
		}

		if attempt < maxRetries {
			wait := retryAfter(httpResp.Header.Get("Retry-After"), backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			backoff = addJitter(backoff * 2)
		}
	}

	return nil, lastErr
}

func (m *AnthropicModel) buildRequestBody(req *model.LLMRequest) (map[string]any, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = m.model
	}

	messages, system, err := m.convertContents(req.Contents, req.Config)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":      modelName,
		"max_tokens": 4096,
		"messages":   messages,
	}
	if system != "" {
		body["system"] = system
	}

	if tools := functionDeclarations(req.Config); len(tools) > 0 {
		anthropicTools, err := m.convertTools(tools)
		if err != nil {
			return nil, err
		}
		body["tools"] = anthropicTools
	}

	return body, nil
}

func (m *AnthropicModel) convertContents(contents []*genai.Content, cfg *genai.GenerateContentConfig) ([]map[string]any, string, error) {
	var system string
	if cfg != nil && cfg.SystemInstruction != nil {
		system = textFromContent(cfg.SystemInstruction)
	}

	var messages []map[string]any
	for _, c := range contents {
		role := "user"
		if c.Role == genai.RoleModel {
			role = "assistant"
		}

		var contentBlocks []map[string]any
		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "text",
					"text": p.Text,
				})
			}
			if p.FunctionCall != nil {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "tool_use",
					"id":   p.FunctionCall.ID,
					"name": p.FunctionCall.Name,
					"input": p.FunctionCall.Args,
				})
			}
			if p.FunctionResponse != nil {
				resultText := ""
				if p.FunctionResponse.Response != nil {
					b, _ := json.Marshal(p.FunctionResponse.Response)
					resultText = string(b)
				}
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "tool_result",
					"tool_use_id": p.FunctionResponse.ID,
					"content":     resultText,
				})
			}
		}

		if len(contentBlocks) > 0 {
			messages = append(messages, map[string]any{
				"role":    role,
				"content": contentBlocks,
			})
		}
	}

	return messages, system, nil
}

func (m *AnthropicModel) convertTools(decls []*genai.FunctionDeclaration) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(decls))
	for _, d := range decls {
		params := SchemaToMap(d.Parameters)
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":        d.Name,
			"description": d.Description,
			"input_schema": params,
		})
	}
	return out, nil
}

func (m *AnthropicModel) convertResponse(apiResp *anthropicMessagesResponse) *model.LLMResponse {
	content := &genai.Content{
		Role:  genai.RoleModel,
		Parts: []*genai.Part{},
	}

	for _, block := range apiResp.Content {
		switch block.Type {
		case "text":
			content.Parts = append(content.Parts, &genai.Part{Text: block.Text})
		case "tool_use":
			content.Parts = append(content.Parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{
					ID:   block.ID,
					Name: block.Name,
					Args: block.Input,
				},
			})
		}
	}

	return &model.LLMResponse{
		Content:      content,
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(apiResp.Usage.InputTokens),
			CandidatesTokenCount: int32(apiResp.Usage.OutputTokens),
			TotalTokenCount:      int32(apiResp.Usage.InputTokens + apiResp.Usage.OutputTokens),
		},
	}
}

type anthropicMessagesResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Usage   anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
