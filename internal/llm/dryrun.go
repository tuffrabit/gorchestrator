package llm

import (
	"context"
	"fmt"
	"iter"
	"strings"

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
				if p != nil {
					prompt += p.Text
				}
			}
		}

		if strings.Contains(prompt, "[multiturn]") {
			m.generateMultiTurn(ctx, req, yield)
			return
		}
		if strings.Contains(prompt, "[empty]") {
			// Simulate a loop that produces neither a tool call nor final text.
			yield(&model.LLMResponse{
				Content: &genai.Content{
					Role:  genai.RoleModel,
					Parts: []*genai.Part{},
				},
				TurnComplete: true,
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
					PromptTokenCount:     2,
					CandidatesTokenCount: 1,
					TotalTokenCount:      3,
				},
			}, nil)
			return
		}

		text := fmt.Sprintf("## Dry-run response\n\n**Prompt:** %s\n\nThis is a canned response for testing.", prompt)
		resp := &model.LLMResponse{
			Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: []*genai.Part{{Text: text}},
			},
			TurnComplete: true,
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     5,
				CandidatesTokenCount: 5,
				TotalTokenCount:      10,
			},
		}
		yield(resp, nil)
	}
}

// generateMultiTurn simulates a tool-call loop: model calls read_file, then
// returns final text. Each model call reports token usage so token accounting
// can be verified.
func (m *DryRunModel) generateMultiTurn(ctx context.Context, req *model.LLMRequest, yield func(*model.LLMResponse, error) bool) {
	// If the conversation already contains a function response, return the
	// final text turn.
	hasFunctionResponse := false
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p != nil && p.FunctionResponse != nil {
				hasFunctionResponse = true
				break
			}
		}
	}

	if hasFunctionResponse {
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: []*genai.Part{{Text: "Final answer after tool use."}},
			},
			TurnComplete: true,
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     7,
				CandidatesTokenCount: 8,
				TotalTokenCount:      15,
			},
		}, nil)
		return
	}

	// First turn: emit a function call.
	yield(&model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call_1",
					Name: "read_file",
					Args: map[string]any{"path": "test.txt"},
				},
			}},
		},
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     4,
			CandidatesTokenCount: 6,
			TotalTokenCount:      10,
		},
	}, nil)
}
