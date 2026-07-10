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
	// Path is optional; empty or "." lists BasePath (issue root / workspace).
	Path string `json:"path,omitempty" jsonschema:"Directory path relative to the issue root (e.g. \"source\") or a full allowlisted storage key; omit or \".\" for the default base"`
}

// ListDirResult is the result of the list_directory tool.
type ListDirResult struct {
	Path    string             `json:"path"`
	Entries []storage.DirEntry `json:"entries"`
}

func newListDirectoryTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "list_directory",
		Description: `List the contents of a directory.

Path may be:
- omitted or "." — the default base (issue root for research/plan; workspace for implementer)
- relative to the issue root (e.g. "source", "research")
- a full storage key under the allowlist (e.g. projects/1/issues/1/source)

Returns the resolved storage path and directory entries.`,
	}, func(ctx agent.Context, args ListDirArgs) (ListDirResult, error) {
		resolved, ok := resolveAllowedPath(args.Path, bt.Allowlist, bt.BasePath)
		if !ok {
			return ListDirResult{}, fmt.Errorf("path not allowed: %s", args.Path)
		}
		entries, err := bt.Storage.List(ctx, resolved)
		if err != nil {
			return ListDirResult{}, err
		}
		return ListDirResult{
			Path:    resolved,
			Entries: entries,
		}, nil
	})
}
