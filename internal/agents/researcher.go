package agents

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// finishTaskSchema is the output schema for researcher and implementer task mode.
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

// plannerFinishTaskSchema extends finish_task with a required effort tag for
// the pre-implementation effort gate (Phase 5).
var plannerFinishTaskSchema = &genai.Schema{
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
		"effort": {
			Type:        genai.TypeString,
			Description: "Implementation effort estimate. Must be exactly one of: low, medium, high.",
		},
	},
	Required: []string{"done", "rationale", "effort"},
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
	return `You are a context compiler agent. Your job is to compile the necessary project directory paths, filenames, function/method/class/variable names, and whatever else is needed to communicate the current shape of project in context of the task description at hand. You have access to the project filesystem along with tools to explore it. The planner does not. You must provide the planner with a task focused window into this project so it can do its job which is planning for the implementer who comes after. Do not attempt to solve the task or issue yourself. Do not attempt to reason about the issue outside of what is needed to create a context dump for the planner. The planner can't see the filesystem or make any tool calls to explore the project or perform discovery on its own. All the planner can do is reason about what is given. So give it a complete picture of the shape of the project in context of the issue description given to you.

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
