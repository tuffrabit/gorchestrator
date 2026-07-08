package llm

import (
	"context"
	"fmt"
	"iter"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/model"
)

// DryRunModel is a model.LLM that returns a canned response without calling an LLM.
// It is used for tests and dry-run CLI mode.
type DryRunModel struct {
	modelName string
}

// NewDryRunModel creates a dry-run model.
func NewDryRunModel(modelName string) model.LLM {
	if modelName == "" {
		modelName = "dryrun"
	}
	return &DryRunModel{modelName: modelName}
}

// Name implements model.LLM.
func (m *DryRunModel) Name() string {
	return m.modelName
}

// GenerateContent implements model.LLM.
func (m *DryRunModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Build a simple user prompt summary for the canned response.
		var prompt string
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				prompt += p.Text
			}
		}
		text := fmt.Sprintf("## Dry-run response\n\n**Prompt:** %s\n\nThis is a canned response for testing.", prompt)
		resp := &model.LLMResponse{
			Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: []*genai.Part{{Text: text}},
			},
			TurnComplete: true,
		}
		yield(resp, nil)
	}
}
