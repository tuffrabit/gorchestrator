package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/tools"
)

// Researcher runs a requirements-analysis agent loop.
type Researcher struct {
	Provider llm.Provider
	SystemPrompt string
}

// NewResearcher creates a Researcher with the default system prompt.
func NewResearcher(provider llm.Provider) *Researcher {
	return &Researcher{
		Provider: provider,
		SystemPrompt: defaultSystemPrompt(),
	}
}

// Run executes one loop of the Researcher.
// It writes the final output to storage via the write_output tool.
func (r *Researcher) Run(ctx context.Context, input string, registry *tools.Registry) (*llm.Usage, error) {
	userPrompt := fmt.Sprintf("Issue: %s\n\nInvestigate the issue and write your findings to the output file using the write_output tool.", input)

	maxSteps := 10
	usage := &llm.Usage{}

	for step := 0; step < maxSteps; step++ {
		resp, err := r.Provider.Generate(ctx, r.SystemPrompt, userPrompt, registry.LLMTools())
		if err != nil {
			return usage, fmt.Errorf("llm generate: %w", err)
		}
		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		usage.TotalTokens += resp.Usage.TotalTokens

		if len(resp.ToolCalls) == 0 {
			// No tool calls; assume the model provided its answer directly.
			// We still require write_output to have been used. If it wasn't, write the content ourselves.
			if resp.Content != "" {
				_, err := registry.Run(ctx, llm.ToolCall{
					Name: "write_output",
					Arguments: map[string]any{"content": resp.Content},
				})
				if err != nil {
					return usage, fmt.Errorf("fallback write_output: %w", err)
				}
			}
			return usage, nil
		}

		// Execute all requested tool calls and append results to the prompt.
		results := make([]string, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			result, err := registry.Run(ctx, tc)
			if err != nil {
				result = fmt.Sprintf(`{"error": %q}`, err.Error())
			}
			results = append(results, fmt.Sprintf("Tool %s result: %s", tc.Name, result))
			if tc.Name == "write_output" && err == nil {
				// Output written; finish loop.
				return usage, nil
			}
		}
		userPrompt = fmt.Sprintf("Issue: %s\n\nPrevious tool results:\n%s\n\nContinue investigating and use write_output when ready.", input, strings.Join(results, "\n"))
	}

	return usage, fmt.Errorf("researcher exceeded maximum steps")
}

func defaultSystemPrompt() string {
	return `You are a Researcher agent. Your job is to investigate a software engineering issue, gather context, and produce a concise findings document.

You have access to these tools:
- read_file: read a file's content
- list_directory: list a directory
- grep_search: search file contents
- write_output: write your final findings to the designated output file

Rules:
1. Use tools to gather information from the allowed paths.
2. Write your final findings using the write_output tool.
3. Be concise and actionable for the next phase (Planner).`
}

// MarshalTask serializes a task.json payload.
func MarshalTask(task any) ([]byte, error) {
	return json.MarshalIndent(task, "", "  ")
}
