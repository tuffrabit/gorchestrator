package llm

import (
	"context"
	"fmt"
)

// DryRunProvider returns a canned response without calling an LLM.
type DryRunProvider struct{}

// NewDryRunProvider creates a dry-run provider.
func NewDryRunProvider() *DryRunProvider {
	return &DryRunProvider{}
}

// Generate implements Provider.
func (p *DryRunProvider) Generate(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (*Response, error) {
	return &Response{
		Content: fmt.Sprintf("## Dry-run response\n\n**System prompt:** %s\n\n**User prompt:** %s\n\nThis is a canned response for testing.", systemPrompt, userPrompt),
		Usage:   Usage{},
	}, nil
}
