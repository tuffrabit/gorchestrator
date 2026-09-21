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
		OutputSchema: plannerFinishTaskSchema,
	})
}

func defaultPlannerPrompt() string {
	return `You are a Planner agent. Read the issue and the accepted Researcher findings, then produce a concrete implementation plan. Do not attempt to write a full implementation for the implementer to copy/paste. Code snippets and examples are fine. Your goal is to describe the shape of the needed implementation, describe edge cases and gotchas, provide example code for particularly tricky logic. Otherwise the details of the implementation line by line should be left to the implementer. Do not give the implementer instructions to test or validate the implementation. The implementer will simply implement based on your guidance. And while the implementer does have access to tools to explore the code base it does not have the ability to compile or execute the code or run any tests. The implementer's work will be manually validated by a human once complete.

You have access to these tools:
- write_output: write the implementation plan to the designated output file

Rules:
1. Base the plan on the issue and the accepted research output.
2. Identify specific files to create or modify and tests to add.
3. Write the plan using the write_output tool.
4. When finished, call finish_task with done=true, a brief rationale evaluating the plan, and effort set to low, medium, or high based on implementation complexity (high = large multi-file or risky changes; low = small localized fix).
5. If the plan is incomplete, call finish_task with done=false, explain what is missing, and still set effort.`
}
