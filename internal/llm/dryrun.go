package llm

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// DryRunModel is a model.LLM that returns a canned response without calling an LLM.
// It is used for tests and dry-run CLI mode. In task mode it simulates a real
// agent: it emits a work tool (write_output/write_file/read_file) on the first
// turn and a finish_task call on the second turn.
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
		prompt := extractPrompt(req)

		if strings.Contains(prompt, "[empty]") {
			// Simulate a loop that produces neither a tool call nor finish_task.
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

		if strings.Contains(prompt, "[block]") {
			// Block until context cancellation. Used by crash-recovery tests.
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
			}
			return
		}

		hasFuncResponse := hasFunctionResponse(req)
		if hasFuncResponse {
			// Second turn: finish the task.
			if strings.Contains(prompt, "[reject]") && !strings.Contains(prompt, "Adjudicator feedback:") {
				yield(m.finishTaskResponseWithDone(req, false, "missing tests"), nil)
				return
			}
			yield(m.finishTaskResponseWithDone(req, true, "dry-run self-check passed"), nil)
			return
		}

		// First turn: emit a work tool call if one is available.
		tools := functionDeclarations(req.Config)
		if hasTool(tools, "write_output") {
			yield(m.writeOutputCall(prompt), nil)
			return
		}
		if hasTool(tools, "write_file") {
			yield(m.writeFileCall(), nil)
			return
		}
		if strings.Contains(prompt, "[multiturn]") {
			yield(m.readFileCall(), nil)
			return
		}

		// No work tool available: finish immediately.
		yield(m.finishTaskResponseWithDone(req, true, "dry-run self-check passed"), nil)
	}
}

func extractPrompt(req *model.LLMRequest) string {
	var prompt string
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p != nil && p.Text != "" {
				prompt += p.Text
			}
		}
	}
	return prompt
}

func hasFunctionResponse(req *model.LLMRequest) bool {
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p != nil && p.FunctionResponse != nil {
				return true
			}
		}
	}
	return false
}

func hasTool(tools []*genai.FunctionDeclaration, name string) bool {
	for _, d := range tools {
		if d != nil && d.Name == name {
			return true
		}
	}
	return false
}

func (m *DryRunModel) writeOutputCall(prompt string) *model.LLMResponse {
	content := fmt.Sprintf("## Dry-run output\n\nPrompt summary: %s\n\nThis is a canned phase output for testing.", truncate(prompt, 200))
	return &model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call_write_output",
					Name: "write_output",
					Args: map[string]any{"content": content},
				},
			}},
		},
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     5,
			CandidatesTokenCount: 5,
			TotalTokenCount:      10,
		},
	}
}

func (m *DryRunModel) writeFileCall() *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call_write_file",
					Name: "write_file",
					Args: map[string]any{
						"path":    "dryrun.go",
						"content": "package dryrun\n\n// Canned implementation output.\n",
					},
				},
			}},
		},
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     5,
			CandidatesTokenCount: 5,
			TotalTokenCount:      10,
		},
	}
}

func (m *DryRunModel) readFileCall() *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call_read_file",
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
	}
}

func (m *DryRunModel) finishTaskResponse(rationale string) *model.LLMResponse {
	// Used by tests that don't have a request context; no effort field.
	return m.finishTaskArgs(true, rationale, "")
}

func (m *DryRunModel) finishTaskResponseWithDone(req *model.LLMRequest, done bool, rationale string) *model.LLMResponse {
	effort := ""
	// Only include effort when the finish_task declaration accepts it
	// (planner OutputSchema). Extra fields fail ADK validation and loop forever.
	if finishTaskHasEffort(req) {
		effort = "low"
		prompt := extractPrompt(req)
		if strings.Contains(prompt, "[effort:high]") {
			effort = "high"
		} else if strings.Contains(prompt, "[effort:medium]") {
			effort = "medium"
		}
	}
	return m.finishTaskArgs(done, rationale, effort)
}

func (m *DryRunModel) finishTaskArgs(done bool, rationale, effort string) *model.LLMResponse {
	args := map[string]any{
		"done":      done,
		"rationale": rationale,
	}
	if effort != "" {
		args["effort"] = effort
	}
	return &model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call_finish_task",
					Name: "finish_task",
					Args: args,
				},
			}},
		},
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     7,
			CandidatesTokenCount: 8,
			TotalTokenCount:      15,
		},
	}
}

// finishTaskHasEffort reports whether the request's finish_task tool schema
// includes an effort property (planner).
func finishTaskHasEffort(req *model.LLMRequest) bool {
	if req == nil {
		return false
	}
	for _, d := range functionDeclarations(req.Config) {
		if d == nil || d.Name != "finish_task" {
			continue
		}
		// Parameters (genai.Schema) path used by hand-built finish_task.
		if d.Parameters != nil && d.Parameters.Properties != nil {
			if _, ok := d.Parameters.Properties["effort"]; ok {
				return true
			}
		}
		// ParametersJsonSchema map path.
		if m, ok := d.ParametersJsonSchema.(map[string]any); ok {
			if props, ok := m["properties"].(map[string]any); ok {
				if _, ok := props["effort"]; ok {
					return true
				}
			}
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
