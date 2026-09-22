package agents

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// Chat is a conversational variant of a core agent identity used by the
// dashboard chat drawer. It runs in ModeChat (no finish_task output schema).
type Chat struct {
	AgentType    string
	SystemPrompt string
}

// NewChat creates a Chat wrapping the given agent identity.
func NewChat(agentType, systemPrompt string) *Chat {
	return &Chat{
		AgentType:    agentType,
		SystemPrompt: systemPrompt,
	}
}

// Build returns an ADK LLMAgent configured for chat-mode conversation. The
// instruction is derived from the tools actually supplied, so a tool-less
// configuration is told it has no tools (removing the trigger for the model
// to imitate tool calls in prose).
func (c *Chat) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return llmagent.New(llmagent.Config{
		Name:        "chat-" + c.AgentType,
		Description: fmt.Sprintf("Conversational variant of the %s for live chat about the project.", c.AgentType),
		Instruction: c.instruction(names),
		Model:       model,
		Tools:       tools,
		Mode:        llmagent.ModeChat,
	})
}

func (c *Chat) instruction(names []string) string {
	var b strings.Builder
	b.WriteString(c.SystemPrompt)
	b.WriteString(`

You are now chatting live with the user about the project. You have no task to complete and no finish_task output to produce. Just answer the user's questions directly and concisely.

Rules:
1. Answer questions directly and concisely, in chat form.
2. Never write a tool call as text: if you need to inspect files, call the tool; if you have no tools, say so.
`)
	switch {
	case len(names) > 0:
		b.WriteString("\nYou have access to these read-only tools: " + strings.Join(names, ", ") + "\n\n")
		b.WriteString("These tools point at the project's current source tree. You cannot modify files. Use tools when the answer depends on the current state of the code.")
	case len(names) == 0:
		b.WriteString("\nYou have **no** tools and cannot inspect files. Say so and answer from what you know; never invent tool calls.")
	}
	return b.String()
}
