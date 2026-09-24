// Package tools provides ADK-native function tools for agent runs.
//
// Each tool is constructed with functiontool.New and receives run-scoped
// dependencies through a BoundTools value captured in the handler closure.
// Path enforcement is performed before any call reaches the storage port.
package tools

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/tool"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// BoundTools carries the storage port and resolved paths for a single agent run.
type BoundTools struct {
	Storage   storage.Port
	RootPath  string
	Allowlist []string
	// BasePath is the default directory for short relative tool paths (empty,
	// ".", "source", …). Typically the issue directory for read-only steps, or
	// the issue workspace for steps that edit files. Full storage keys under
	// Allowlist still work.
	BasePath   string
	OutputPath string
	// WorkspacePath is the issue workspace (any editing agent).
	WorkspacePath string
	// WorkspaceHostPath is the absolute host path of the issue workspace
	// (for container bind-mounts). Empty when not set.
	WorkspaceHostPath string
	// Test holds project test config for run_test.
	Test             *TestConfig
	ReadFileMaxBytes int
	ReadFileMaxLines int
	// OutputWritten is set to true by the write_output tool when it executes.
	// The orchestrator uses this to decide whether to fall back to the model's
	// final text response as the phase output.
	OutputWritten *bool
}

// NewCoreRegistry returns every core tool: read_file, list_directory,
// grep_search, write_output, write_file, update_file, and run_test when
// bt.Test is set with a non-empty Command. Callers narrow with FilterByNames.
func NewCoreRegistry(bt *BoundTools) ([]tool.Tool, error) {
	readFile, err := newReadFileTool(bt)
	if err != nil {
		return nil, fmt.Errorf("read_file tool: %w", err)
	}
	listDir, err := newListDirectoryTool(bt)
	if err != nil {
		return nil, fmt.Errorf("list_directory tool: %w", err)
	}
	grep, err := newGrepTool(bt)
	if err != nil {
		return nil, fmt.Errorf("grep_search tool: %w", err)
	}
	writeOutput, err := newWriteOutputTool(bt)
	if err != nil {
		return nil, fmt.Errorf("write_output tool: %w", err)
	}
	writeFile, err := newWriteFileTool(bt)
	if err != nil {
		return nil, fmt.Errorf("write_file tool: %w", err)
	}
	updateFile, err := newUpdateFileTool(bt)
	if err != nil {
		return nil, fmt.Errorf("update_file tool: %w", err)
	}
	out := []tool.Tool{readFile, listDir, grep, writeOutput, writeFile, updateFile}
	if bt.Test != nil && strings.TrimSpace(bt.Test.Command) != "" {
		runTest, err := newRunTestTool(bt)
		if err != nil {
			return nil, fmt.Errorf("run_test tool: %w", err)
		}
		out = append(out, runTest)
	}
	return out, nil
}

// FilterByNames keeps only tools whose Name() is in allow. Empty allow returns all.
func FilterByNames(all []tool.Tool, allow []string) []tool.Tool {
	if len(allow) == 0 {
		return all
	}
	set := make(map[string]struct{}, len(allow))
	for _, n := range allow {
		set[n] = struct{}{}
	}
	var out []tool.Tool
	for _, t := range all {
		if _, ok := set[t.Name()]; ok {
			out = append(out, t)
		}
	}
	return out
}
