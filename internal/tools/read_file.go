package tools

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ReadFileArgs are the arguments for the read_file tool.
//
// Two explicit modes:
//   1. Whole-file: leave Offset and Limit at zero. The file is returned subject
//      to a configurable cap (default 64KB / 2,000 lines, whichever is hit first).
//      If truncated, Truncated is true and TotalLines holds the full line count.
//   2. Surgical: set Offset (1-based line) and Limit (line count) to read exactly
//      that range. This is the intended follow-up to grep_search, which returns
//      file paths and line numbers.
type ReadFileArgs struct {
	Path   string `json:"path" jsonschema:"Relative path to the file"`
	Offset int    `json:"offset" jsonschema:"1-based line offset for surgical mode; omit for whole-file mode"`
	Limit  int    `json:"limit" jsonschema:"Maximum number of lines for surgical mode; omit for whole-file mode"`
}

// ReadFileResult is the result of the read_file tool.
type ReadFileResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Size       int    `json:"size"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

func newReadFileTool(bt *BoundTools) (tool.Tool, error) {
	description := `Read the full or a precise line-number range of a file. Path is relative to the storage root.

Two modes:
1. Whole-file (no offset/limit): returns the full file subject to a cap. If the cap is hit, truncated=true and total_lines reports the full line count so you can switch to surgical mode.
2. Surgical (offset + limit): reads exactly the requested 1-based line range. Use this after grep_search returns file paths and line numbers to read only the relevant region.

Prefer grep_search to locate content, then surgical read_file to inspect it.`

	return functiontool.New(functiontool.Config{
		Name:        "read_file",
		Description: description,
	}, func(ctx agent.Context, args ReadFileArgs) (ReadFileResult, error) {
		return readFile(ctx, bt, args)
	})
}

func readFile(ctx context.Context, bt *BoundTools, args ReadFileArgs) (ReadFileResult, error) {
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

	lines := splitLines(string(data))
	totalLines := len(lines)

	if args.Offset <= 0 && args.Limit <= 0 {
		content, truncated := applyCap(lines, bt.ReadFileMaxBytes, bt.ReadFileMaxLines)
		return ReadFileResult{
			Path:       args.Path,
			Content:    content,
			Size:       len(content),
			TotalLines: totalLines,
			Truncated:  truncated,
		}, nil
	}

	content := applyRange(lines, args.Offset, args.Limit)
	return ReadFileResult{
		Path:       args.Path,
		Content:    content,
		Size:       len(content),
		TotalLines: totalLines,
		Truncated:  false,
	}, nil
}

// applyCap returns the content truncated to the more restrictive of the byte and
// line caps. It returns the content and whether truncation occurred.
func applyCap(lines []string, maxBytes, maxLines int) (string, bool) {
	if len(lines) == 0 {
		return "", false
	}

	totalBytes := 0
	for _, line := range lines {
		totalBytes += len(line)
	}

	underBytes := maxBytes <= 0 || totalBytes <= maxBytes
	underLines := maxLines <= 0 || len(lines) <= maxLines
	if underBytes && underLines {
		return joinLines(lines), false
	}

	byteLimit := len(lines)
	if maxBytes > 0 {
		byteLimit = truncateByBytes(lines, maxBytes)
	}

	lineLimit := len(lines)
	if maxLines > 0 {
		lineLimit = maxLines
		if lineLimit > len(lines) {
			lineLimit = len(lines)
		}
	}

	limit := byteLimit
	if lineLimit < limit {
		limit = lineLimit
	}
	return joinLines(lines[:limit]), true
}

func truncateByBytes(lines []string, maxBytes int) int {
	n := 0
	for i, line := range lines {
		if n+len(line) > maxBytes {
			return i
		}
		n += len(line)
	}
	return len(lines)
}

func applyRange(lines []string, offset, limit int) string {
	if offset <= 0 {
		offset = 1
	}
	start := offset - 1
	if start >= len(lines) {
		return ""
	}
	end := start + limit
	if limit <= 0 || end > len(lines) {
		end = len(lines)
	}
	return joinLines(lines[start:end])
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
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
	}
	return b.String()
}
