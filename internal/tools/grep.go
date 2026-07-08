package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const maxGrepResults = 100

// GrepArgs are the arguments for the grep_search tool.
type GrepArgs struct {
	Path    string `json:"path" jsonschema:"Relative directory path to search"`
	Pattern string `json:"pattern" jsonschema:"Pattern to search for"`
	Regex   bool   `json:"regex" jsonschema:"If true, treat pattern as a Go regular expression"`
}

// GrepMatch is a single grep result.
type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// GrepResult is the result of the grep_search tool.
type GrepResult struct {
	Path      string      `json:"path"`
	Pattern   string      `json:"pattern"`
	Regex     bool        `json:"regex"`
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
}

func newGrepTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "grep_search",
		Description: "Search file contents for a pattern. Returns matching lines with file paths.",
	}, func(ctx agent.Context, args GrepArgs) (GrepResult, error) {
		path := args.Path
		if path == "" {
			path = "."
		}
		if args.Pattern == "" {
			return GrepResult{}, fmt.Errorf("pattern is required")
		}

		resolved, ok := resolveAllowedPath(path, bt.Allowlist)
		if !ok {
			return GrepResult{}, fmt.Errorf("path not allowed: %s", path)
		}

		var matcher func(line string) bool
		var re *regexp.Regexp
		if args.Regex {
			var err error
			re, err = regexp.Compile(args.Pattern)
			if err != nil {
				return GrepResult{}, fmt.Errorf("invalid regex: %w", err)
			}
			matcher = re.MatchString
		} else {
			matcher = func(line string) bool { return strings.Contains(line, args.Pattern) }
		}

		var matches []GrepMatch
		absRoot := filepath.Join(bt.RootPath, resolved)

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
			fileMatches, err := grepFile(filePath, rel, matcher)
			if err != nil {
				return nil
			}
			matches = append(matches, fileMatches...)
			if len(matches) >= maxGrepResults {
				return io.EOF
			}
			return nil
		})
		if err != nil && err != io.EOF {
			return GrepResult{}, err
		}

		truncated := len(matches) >= maxGrepResults
		if truncated {
			matches = matches[:maxGrepResults]
		}

		return GrepResult{
			Path:      path,
			Pattern:   args.Pattern,
			Regex:     args.Regex,
			Matches:   matches,
			Truncated: truncated,
		}, nil
	})
}

func grepFile(absPath, relPath string, matcher func(string) bool) ([]GrepMatch, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var matches []GrepMatch
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if matcher(line) {
			matches = append(matches, GrepMatch{
				Path: relPath,
				Line: lineNum,
				Text: line,
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
