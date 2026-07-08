package agents

import (
	"encoding/json"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// Researcher is a requirements-analysis agent.
type Researcher struct {
	SystemPrompt string
}

// NewResearcher creates a Researcher with the default system prompt.
func NewResearcher() *Researcher {
	return &Researcher{
		SystemPrompt: defaultSystemPrompt(),
	}
}

// Build returns an ADK LLMAgent configured for the Researcher.
func (r *Researcher) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        "researcher",
		Description: "Investigates a software engineering issue and produces concise findings.",
		Instruction: r.SystemPrompt,
		Model:       model,
		Tools:       tools,
		Mode:        llmagent.ModeChat,
	})
}

// MarshalTask serializes a task.json payload.
func MarshalTask(task any) ([]byte, error) {
	return json.MarshalIndent(task, "", "  ")
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
