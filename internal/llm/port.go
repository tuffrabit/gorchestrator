package llm

import (
	"context"
)

// Usage captures token consumption for a generation.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// Response is the result of a generation request.
type Response struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

// Tool describes a tool available to the model.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Provider is the LLMProviderPort abstraction.
type Provider interface {
	// Generate sends a prompt and optional tools to the model.
	Generate(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (*Response, error)
}
