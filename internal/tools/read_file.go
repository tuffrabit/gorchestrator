package tools

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ReadFileArgs are the arguments for the read_file tool.
type ReadFileArgs struct {
	Path   string `json:"path" jsonschema:"Relative path to the file"`
	Offset int    `json:"offset" jsonschema:"Line offset to start reading (1-based)"`
	Limit  int    `json:"limit" jsonschema:"Maximum number of lines to read"`
}

// ReadFileResult is the result of the read_file tool.
type ReadFileResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int    `json:"size"`
}

func newReadFileTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "read_file",
		Description: "Read the full or partial content of a file. Path is relative to the storage root.",
	}, func(ctx agent.Context, args ReadFileArgs) (ReadFileResult, error) {
		if args.Path == "" {
			return ReadFileResult{}, fmt.Errorf("path is required")
		}
		resolved, ok := resolveAllowedPath(args.Path, bt.Allowlist)
		if !ok {
			return ReadFileResult{}, fmt.Errorf("path not allowed: %s", args.Path)
		}
		data, err := bt.Storage.Read(ctx, resolved)
		if err != nil {
			return ReadFileResult{}, err
		}
		content := applyOffsetLimit(string(data), args.Offset, args.Limit)
		return ReadFileResult{
			Path:    args.Path,
			Content: content,
			Size:    len(data),
		}, nil
	})
}

func applyOffsetLimit(content string, offset, limit int) string {
	if offset <= 0 && limit <= 0 {
		return content
	}
	lines := splitLines(content)
	if offset > 0 {
		offset-- // convert to 0-based
		if offset >= len(lines) {
			return ""
		}
		lines = lines[offset:]
	}
	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}
	return joinLines(lines)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func joinLines(lines []string) string {
	var out string
	for _, l := range lines {
		out += l
	}
	return out
}
