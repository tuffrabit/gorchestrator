package agents

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// finishTaskSchema is the output schema for task-mode agents.
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

// Task is a generic tool-loop agent. Instruction is the user's configured
// system_prompt; there is no built-in prompt.
type Task struct {
	ID          string
	Instruction string
}

// NewTask creates a Task agent with the given id and system prompt.
func NewTask(id, instruction string) *Task {
	return &Task{ID: id, Instruction: instruction}
}

// Build returns an ADK LLMAgent configured for task-mode completion.
func (t *Task) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:         t.ID,
		Description:  "Task-mode agent running the configured system prompt.",
		Instruction:  t.Instruction,
		Model:        model,
		Tools:        tools,
		Mode:         llmagent.ModeTask,
		OutputSchema: finishTaskSchema,
	})
}
