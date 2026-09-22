package agents

import (
	"fmt"

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

// Build returns an ADK LLMAgent configured for chat-mode conversation.
func (c *Chat) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        "chat-" + c.AgentType,
		Description: fmt.Sprintf("Conversational variant of the %s for live chat about the project.", c.AgentType),
		Instruction: c.instruction(),
		Model:       model,
		Tools:       tools,
		Mode:        llmagent.ModeChat,
	})
}

func (c *Chat) instruction() string {
	return c.SystemPrompt + `

You are now chatting live with the user about the project. You have no task to complete and no finish_task output to produce. Just answer the user's questions directly and concisely.

You have access to these read-only tools:
- read_file: read a file's content (whole-file or surgical line range)
- list_directory: list a directory
- grep_search: search file contents

These tools point at the project's current source tree. You cannot modify files.

Rules:
1. Answer questions directly and concisely, in chat form.
2. Use tools when the answer depends on the current state of the code.`
}
