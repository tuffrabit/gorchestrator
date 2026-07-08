package tools

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// ListDirArgs are the arguments for the list_directory tool.
type ListDirArgs struct {
	Path string `json:"path" jsonschema:"Relative path to the directory"`
}

// ListDirResult is the result of the list_directory tool.
type ListDirResult struct {
	Path    string            `json:"path"`
	Entries []storage.DirEntry `json:"entries"`
}

func newListDirectoryTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "list_directory",
		Description: "List the contents of a directory. Path is relative to the storage root.",
	}, func(ctx agent.Context, args ListDirArgs) (ListDirResult, error) {
		path := args.Path
		if path == "" {
			path = "."
		}
		resolved, ok := resolveAllowedPath(path, bt.Allowlist)
		if !ok {
			return ListDirResult{}, fmt.Errorf("path not allowed: %s", path)
		}
		entries, err := bt.Storage.List(ctx, resolved)
		if err != nil {
			return ListDirResult{}, err
		}
		return ListDirResult{
			Path:    path,
			Entries: entries,
		}, nil
	})
}
