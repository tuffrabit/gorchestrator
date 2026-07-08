package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxGrepResults = 100

type grepTool struct {
	bt *BoundTools
}

func (t *grepTool) Name() string { return "grep_search" }

func (t *grepTool) Description() string {
	return "Search file contents for a pattern. Returns matching lines with file paths."
}

func (t *grepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative directory path to search",
			},
			"pattern": map[string]any{
				"type":        "string",
				"description": "Pattern to search for",
			},
			"regex": map[string]any{
				"type":        "boolean",
				"description": "If true, treat pattern as a Go regular expression",
			},
		},
		"required": []string{"path", "pattern"},
	}
}

func (t *grepTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	useRegex, _ := args["regex"].(bool)

	resolved, ok := resolveAllowedPath(path, t.bt.Allowlist)
	if !ok {
		return nil, fmt.Errorf("path not allowed: %s", path)
	}

	root := filepath.Join("/", resolved) // we need absolute root for walking
	_ = root

	var matcher func(line string) bool
	var re *regexp.Regexp
	if useRegex {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		matcher = re.MatchString
	} else {
		matcher = func(line string) bool { return strings.Contains(line, pattern) }
	}

	// Build ignore patterns from .gitignore files encountered.
	ignores := map[string][]string{}
	_ = ignores

	var results []map[string]any
	absRoot := filepath.Join(t.bt.RootPath, resolved)

	err := filepath.Walk(absRoot, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(absRoot, filePath)
		if err != nil {
			return nil
		}
		if isBinary(filePath) {
			return nil
		}
		matches, err := grepFile(filePath, rel, matcher)
		if err != nil {
			return nil
		}
		results = append(results, matches...)
		if len(results) >= maxGrepResults {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}

	return map[string]any{
		"path":    path,
		"pattern": pattern,
		"regex":   useRegex,
		"matches": results,
		"truncated": len(results) >= maxGrepResults,
	}, nil
}

func grepFile(absPath, relPath string, matcher func(string) bool) ([]map[string]any, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var matches []map[string]any
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if matcher(line) {
			matches = append(matches, map[string]any{
				"path": relPath,
				"line": lineNum,
				"text": line,
			})
		}
	}
	return matches, scanner.Err()
}

func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return true
	}
	return bytes.IndexByte(buf[:n], 0) != -1
}

var _ Tool = (*grepTool)(nil)
