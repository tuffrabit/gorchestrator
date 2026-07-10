package tools

import (
	"context"
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// WriteFileArgs are the arguments for the write_file tool.
type WriteFileArgs struct {
	Path    string `json:"path" jsonschema:"Workspace-relative path to the file"`
	Content string `json:"content" jsonschema:"File content to write"`
}

// WriteFileResult is the result of the write_file tool.
type WriteFileResult struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	Status string `json:"status"`
}

func newWriteFileTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "write_file",
		Description: "Write a file within the implementer's workspace.",
	}, func(ctx agent.Context, args WriteFileArgs) (WriteFileResult, error) {
		return writeFile(ctx, bt, args)
	})
}

func writeFile(ctx context.Context, bt *BoundTools, args WriteFileArgs) (WriteFileResult, error) {
	if bt.WorkspacePath == "" {
		return WriteFileResult{}, fmt.Errorf("workspace path not configured")
	}
	if args.Path == "" {
		return WriteFileResult{}, fmt.Errorf("path is required")
	}
	resolved, ok := resolveAllowedPath(args.Path, []string{bt.WorkspacePath}, bt.WorkspacePath)
	if !ok {
		return WriteFileResult{}, fmt.Errorf("path escapes workspace: %s", args.Path)
	}
	if err := bt.Storage.Write(ctx, resolved, []byte(args.Content)); err != nil {
		return WriteFileResult{}, err
	}
	return WriteFileResult{
		Path:   args.Path,
		Size:   len(args.Content),
		Status: "written",
	}, nil
}
