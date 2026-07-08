// Package tools provides ADK-native function tools for agent runs.
//
// Each tool is constructed with functiontool.New and receives run-scoped
// dependencies through a BoundTools value captured in the handler closure.
// Path enforcement is performed before any call reaches the storage port.
package tools

import (
	"google.golang.org/adk/v2/tool"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// BoundTools carries the storage port and resolved paths for a single agent run.
type BoundTools struct {
	Storage       storage.Port
	RootPath      string
	Allowlist     []string
	OutputPath    string
	WorkspacePath string
	// OutputWritten is set to true by the write_output tool when it executes.
	// The orchestrator uses this to decide whether to fall back to the model's
	// final text response as the phase output.
	OutputWritten *bool
}

// NewResearcherRegistry creates the core toolset for the Researcher and Planner agents.
func NewResearcherRegistry(bt *BoundTools) []tool.Tool {
	return []tool.Tool{
		mustTool(newReadFileTool(bt)),
		mustTool(newListDirectoryTool(bt)),
		mustTool(newGrepTool(bt)),
		mustTool(newWriteOutputTool(bt)),
	}
}

// NewImplementerRegistry creates the core toolset for the Implementer agent.
func NewImplementerRegistry(bt *BoundTools) []tool.Tool {
	return []tool.Tool{
		mustTool(newReadFileTool(bt)),
		mustTool(newListDirectoryTool(bt)),
		mustTool(newGrepTool(bt)),
		mustTool(newWriteFileTool(bt)),
		mustTool(newUpdateFileTool(bt)),
	}
}

func mustTool(t tool.Tool, err error) tool.Tool {
	if err != nil {
		panic(err)
	}
	return t
}

