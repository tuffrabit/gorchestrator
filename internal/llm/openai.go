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
	"time"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/model"
)

// OpenAIModel is an ADK model.LLM that calls the OpenAI chat completions API
// (or any OpenAI-compatible endpoint). It translates between genai types and
// OpenAI request/response formats.
type OpenAIModel struct {
	model   string
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewOpenAIModel creates an OpenAI-compatible model.LLM.
// If baseURL is empty, it defaults to https://api.openai.com/v1.
func NewOpenAIModel(modelName, apiKeyEnv, baseURL string, timeout time.Duration) model.LLM {
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
	return &OpenAIModel{
		model:   modelName,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

// Name implements model.LLM.
func (m *OpenAIModel) Name() string {
	return m.model
}

// GenerateContent implements model.LLM.
func (m *OpenAIModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		resp, err := m.generate(ctx, req)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(resp, nil)
	}
}

func (m *OpenAIModel) generate(ctx context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	body, err := m.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build openai request: %w", err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", m.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create openai request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var apiResp openAIChatResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("openai returned no choices")
	}

	return m.convertResponse(&apiResp), nil
}

func (m *OpenAIModel) buildRequestBody(req *model.LLMRequest) (map[string]any, error) {
	messages, err := m.convertContents(req.Contents, req.Config)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": messages,
	}

	if tools := functionDeclarations(req.Config); len(tools) > 0 {
		openAITools, err := m.convertTools(tools)
		if err != nil {
			return nil, err
		}
		body["tools"] = openAITools
	}

	return body, nil
}

func (m *OpenAIModel) convertContents(contents []*genai.Content, cfg *genai.GenerateContentConfig) ([]map[string]any, error) {
	var messages []map[string]any

	// System instruction from config.
	if cfg != nil && cfg.SystemInstruction != nil {
		text := textFromContent(cfg.SystemInstruction)
		if text != "" {
			messages = append(messages, map[string]any{
				"role":    "system",
				"content": text,
			})
		}
	}

	for _, c := range contents {
		role := c.Role
		if role == genai.RoleUser {
			role = "user"
		} else if role == genai.RoleModel {
			role = "assistant"
		}

		msg := map[string]any{"role": role}

		// Separate text, function calls, and function responses.
		var textParts []string
		var toolCalls []map[string]any
		var toolResponses []map[string]any

		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				textParts = append(textParts, p.Text)
			}
			if p.FunctionCall != nil {
				toolCalls = append(toolCalls, map[string]any{
					"id":   p.FunctionCall.ID,
					"type": "function",
					"function": map[string]any{
						"name":      p.FunctionCall.Name,
						"arguments": argsJSON(p.FunctionCall.Args),
					},
				})
			}
			if p.FunctionResponse != nil {
				respText := ""
				if p.FunctionResponse.Response != nil {
					b, _ := json.Marshal(p.FunctionResponse.Response)
					respText = string(b)
				}
				toolResponses = append(toolResponses, map[string]any{
					"role":         "tool",
					"tool_call_id": p.FunctionResponse.ID,
					"content":      respText,
				})
			}
		}

		if len(textParts) > 0 {
			msg["content"] = joinStrings(textParts, "\n")
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}

		messages = append(messages, msg)

		// Tool responses must be their own messages in OpenAI format.
		for _, tr := range toolResponses {
			messages = append(messages, tr)
		}
	}

	return messages, nil
}

func functionDeclarations(cfg *genai.GenerateContentConfig) []*genai.FunctionDeclaration {
	if cfg == nil {
		return nil
	}
	var decls []*genai.FunctionDeclaration
	for _, t := range cfg.Tools {
		if t == nil {
			continue
		}
		decls = append(decls, t.FunctionDeclarations...)
	}
	return decls
}

func (m *OpenAIModel) convertTools(decls []*genai.FunctionDeclaration) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(decls))
	for _, d := range decls {
		params := schemaToMap(d.Parameters)
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        d.Name,
				"description": d.Description,
				"parameters":  params,
			},
		})
	}
	return out, nil
}

func (m *OpenAIModel) convertResponse(apiResp *openAIChatResponse) *model.LLMResponse {
	choice := apiResp.Choices[0]
	msg := choice.Message

	content := &genai.Content{
		Role:  genai.RoleModel,
		Parts: []*genai.Part{},
	}

	if msg.Content != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: msg.Content})
	}

	for _, tc := range msg.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		content.Parts = append(content.Parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			},
		})
	}

	return &model.LLMResponse{
		Content:      content,
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(apiResp.Usage.PromptTokens),
			CandidatesTokenCount: int32(apiResp.Usage.CompletionTokens),
			TotalTokenCount:      int32(apiResp.Usage.TotalTokens),
		},
	}
}

func textFromContent(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var parts []string
	for _, p := range c.Parts {
		if p != nil && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return joinStrings(parts, "\n")
}

func argsJSON(args map[string]any) string {
	b, _ := json.Marshal(args)
	return string(b)
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += sep + parts[i]
	}
	return out
}

func schemaToMap(s *genai.Schema) map[string]any {
	if s == nil {
		return nil
	}
	m := map[string]any{
		"type": s.Type,
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if s.Format != "" {
		m["format"] = s.Format
	}
	if s.Nullable != nil && *s.Nullable {
		m["nullable"] = true
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Items != nil {
		m["items"] = schemaToMap(s.Items)
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for k, v := range s.Properties {
			props[k] = schemaToMap(v)
		}
		m["properties"] = props
	}
	return m
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}
