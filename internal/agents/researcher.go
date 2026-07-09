package agents

import (
	"google.golang.org/genai"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// finishTaskSchema is the output schema used by all task-mode agents to
// self-evaluate before finishing.
var finishTaskSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"done": {
			Type:        genai.TypeBoolean,
			Description: "Whether the task is complete and meets the rubric.",
		},
		"rationale": {
			Type:        genai.TypeString,
			Description: "Brief explanation of why the task is or is not complete.",
		},
	},
	Required: []string{"done", "rationale"},
}

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

// Build returns an ADK LLMAgent configured for task-mode completion.
func (r *Researcher) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:         "researcher",
		Description:  "Investigates a software engineering issue and produces concise findings.",
		Instruction:  r.SystemPrompt,
		Model:        model,
		Tools:        tools,
		Mode:         llmagent.ModeTask,
		OutputSchema: finishTaskSchema,
	})
}

func defaultSystemPrompt() string {
	return `You are a Researcher agent. Your job is to investigate a software engineering issue, gather context from the source snapshot, and produce a concise findings document.

You have access to these tools:
- read_file: read a file's content (whole-file or surgical line range)
- list_directory: list a directory
- grep_search: search file contents
- write_output: write your final findings to the designated output file

Rules:
1. Use tools to gather information from the allowed paths.
2. Write your final findings using the write_output tool.
3. Be concise and actionable for the next phase (Planner).
4. When you are finished, call finish_task with done=true and a brief rationale evaluating whether your findings are complete and accurate.
5. If your findings are incomplete, call finish_task with done=false and explain what is missing.`
}
