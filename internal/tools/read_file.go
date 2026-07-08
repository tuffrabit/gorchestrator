package tools

import (
	"context"
	"fmt"
)

type readFileTool struct {
	bt *BoundTools
}

func (t *readFileTool) Name() string { return "read_file" }

func (t *readFileTool) Description() string {
	return "Read the full or partial content of a file. Path is relative to the storage root."
}

func (t *readFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the file",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Line offset to start reading (1-based)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of lines to read",
			},
		},
		"required": []string{"path"},
	}
}

func (t *readFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("path is required")
	}
	resolved, ok := resolveAllowedPath(path, t.bt.Allowlist)
	if !ok {
		return nil, fmt.Errorf("path not allowed: %s", path)
	}
	data, err := t.bt.Storage.Read(ctx, resolved)
	if err != nil {
		return nil, err
	}
	content := string(data)
	return map[string]any{
		"path":    path,
		"content": content,
		"size":    len(data),
	}, nil
}

var _ Tool = (*readFileTool)(nil)
