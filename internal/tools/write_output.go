package tools

import (
	"context"
	"fmt"
)

type writeOutputTool struct {
	bt *BoundTools
}

func (t *writeOutputTool) Name() string { return "write_output" }

func (t *writeOutputTool) Description() string {
	return "Write the agent's final output to the designated output file for this phase."
}

func (t *writeOutputTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "Markdown content to write",
			},
		},
		"required": []string{"content"},
	}
}

func (t *writeOutputTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("content is required")
	}
	if t.bt.OutputPath == "" {
		return nil, fmt.Errorf("output path not configured")
	}
	if err := t.bt.Storage.Write(ctx, t.bt.OutputPath, []byte(content)); err != nil {
		return nil, err
	}
	return map[string]any{
		"path":   t.bt.OutputPath,
		"size":   len(content),
		"status": "written",
	}, nil
}

var _ Tool = (*writeOutputTool)(nil)
