package tools

import (
	"fmt"
	"path/filepath"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// UpdateFileArgs are the arguments for the update_file tool.
type UpdateFileArgs struct {
	Path    string `json:"path" jsonschema:"Workspace-relative path to the file"`
	Content string `json:"content" jsonschema:"New file content (overwrites existing file)"`
}

// UpdateFileResult is the result of the update_file tool.
type UpdateFileResult struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	Status string `json:"status"`
}

func newUpdateFileTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "update_file",
		Description: "Update (overwrite) a file within the implementer's workspace.",
	}, func(ctx agent.Context, args UpdateFileArgs) (UpdateFileResult, error) {
		if bt.WorkspacePath == "" {
			return UpdateFileResult{}, fmt.Errorf("workspace path not configured")
		}
		if args.Path == "" {
			return UpdateFileResult{}, fmt.Errorf("path is required")
		}
		fullPath := filepath.Join(bt.WorkspacePath, args.Path)
		resolved, ok := resolveAllowedPath(fullPath, []string{bt.WorkspacePath})
		if !ok {
			return UpdateFileResult{}, fmt.Errorf("path escapes workspace: %s", args.Path)
		}
		if err := bt.Storage.Write(ctx, resolved, []byte(args.Content)); err != nil {
			return UpdateFileResult{}, err
		}
		return UpdateFileResult{
			Path:   args.Path,
			Size:   len(args.Content),
			Status: "updated",
		}, nil
	})
}
