package agents

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// Planner produces an implementation plan from the issue and research findings.
type Planner struct {
	SystemPrompt string
}

// NewPlanner creates a Planner with the default system prompt.
func NewPlanner() *Planner {
	return &Planner{
		SystemPrompt: defaultPlannerPrompt(),
	}
}

// Build returns an ADK LLMAgent configured for task-mode completion.
func (p *Planner) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:         "planner",
		Description:  "Produces a concrete implementation plan for the Implementer.",
		Instruction:  p.SystemPrompt,
		Model:        model,
		Tools:        tools,
		Mode:         llmagent.ModeTask,
		OutputSchema: finishTaskSchema,
	})
}

func defaultPlannerPrompt() string {
	return `You are a Planner agent. Read the issue and the accepted Researcher findings, then produce a concrete implementation plan.

You have access to these tools:
- read_file: read source files and previous phase outputs
- list_directory: explore the source tree
- grep_search: search file contents
- write_output: write the implementation plan to the designated output file

Rules:
1. Base the plan on the issue and the accepted research output.
2. Identify specific files to create or modify and tests to add.
3. Write the plan using the write_output tool.
4. When finished, call finish_task with done=true and a brief rationale evaluating the plan.
5. If the plan is incomplete, call finish_task with done=false and explain what is missing.`
}
