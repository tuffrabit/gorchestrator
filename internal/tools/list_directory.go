package tools

import (
	"context"
	"fmt"
)

type listDirTool struct {
	bt *BoundTools
}

func (t *listDirTool) Name() string { return "list_directory" }

func (t *listDirTool) Description() string {
	return "List the contents of a directory. Path is relative to the storage root."
}

func (t *listDirTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the directory",
			},
		},
		"required": []string{"path"},
	}
}

func (t *listDirTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, ok := args["path"].(string)
	if !ok {
		path = "."
	}
	resolved, ok := resolveAllowedPath(path, t.bt.Allowlist)
	if !ok {
		return nil, fmt.Errorf("path not allowed: %s", path)
	}
	entries, err := t.bt.Storage.List(ctx, resolved)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":    path,
		"entries": entries,
	}, nil
}

var _ Tool = (*listDirTool)(nil)
