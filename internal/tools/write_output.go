package tools

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// WriteOutputArgs are the arguments for the write_output tool.
type WriteOutputArgs struct {
	Content string `json:"content" jsonschema:"Markdown content to write"`
}

// WriteOutputResult is the result of the write_output tool.
type WriteOutputResult struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	Status string `json:"status"`
}

func newWriteOutputTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "write_output",
		Description: "Write the agent's final output to the designated output file for this phase.",
	}, func(ctx agent.Context, args WriteOutputArgs) (WriteOutputResult, error) {
		if bt.OutputPath == "" {
			return WriteOutputResult{}, fmt.Errorf("output path not configured")
		}
		if err := bt.Storage.Write(ctx, bt.OutputPath, []byte(args.Content)); err != nil {
			return WriteOutputResult{}, err
		}
		if bt.OutputWritten != nil {
			*bt.OutputWritten = true
		}
		return WriteOutputResult{
			Path:   bt.OutputPath,
			Size:   len(args.Content),
			Status: "written",
		}, nil
	})
}
