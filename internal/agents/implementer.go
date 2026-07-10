package agents

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// Implementer edits the workspace to implement the plan.
type Implementer struct {
	SystemPrompt string
}

// NewImplementer creates an Implementer with the default system prompt.
func NewImplementer() *Implementer {
	return &Implementer{
		SystemPrompt: defaultImplementerPrompt(),
	}
}

// Build returns an ADK LLMAgent configured for task-mode completion.
func (i *Implementer) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:         "implementer",
		Description:  "Implements the accepted plan by editing files in the workspace.",
		Instruction:  i.SystemPrompt,
		Model:        model,
		Tools:        tools,
		Mode:         llmagent.ModeTask,
		OutputSchema: finishTaskSchema,
	})
}

func defaultImplementerPrompt() string {
	return `You are an Implementer agent. Read the issue, the accepted Researcher findings, and the accepted Planner output, then edit the workspace to implement the changes.

You have access to these tools:
- read_file: read files in the workspace or source snapshot
- list_directory: explore the workspace
- grep_search: search file contents
- write_file: create new files in the workspace
- update_file: overwrite existing files in the workspace
- run_test: execute the project's immutable test command in a container sandbox; use it for test-and-fix loops

Rules:
1. Edit only within the implementer's workspace.
2. Match the existing code style and include tests where the plan asks for them.
3. Prefer run_test after meaningful changes when tests are configured; fix failures and re-run.
4. When finished, call finish_task with done=true and a brief rationale evaluating the implementation.
5. If the implementation is incomplete, call finish_task with done=false and explain what is missing.`
}
