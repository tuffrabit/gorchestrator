package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
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
	// Path is optional; empty or "." searches BasePath (issue root / workspace).
	Path string `json:"path,omitempty" jsonschema:"Directory to search, relative to the issue root (e.g. \"source\") or a full allowlisted storage key; omit or \".\" for the default base"`
	// Pattern is required.
	Pattern string `json:"pattern" jsonschema:"Pattern to search for"`
	// Regex is optional; false means plain substring match.
	Regex bool `json:"regex,omitempty" jsonschema:"If true, treat pattern as a Go regular expression"`
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
		Name: "grep_search",
		Description: `Search file contents for a pattern. Returns matching lines with storage-relative file paths suitable for read_file. Honors .gitignore, skips .git and binary files.

Path may be omitted/"." (default base), relative to the issue root (e.g. "source"), or a full allowlisted storage key.`,
	}, func(ctx agent.Context, args GrepArgs) (GrepResult, error) {
		return grepSearch(ctx, bt, args)
	})
}

func grepSearch(ctx context.Context, bt *BoundTools, args GrepArgs) (GrepResult, error) {
	if args.Pattern == "" {
		return GrepResult{}, fmt.Errorf("pattern is required")
	}

	resolved, ok := resolveAllowedPath(args.Path, bt.Allowlist, bt.BasePath)
	if !ok {
		return GrepResult{}, fmt.Errorf("path not allowed: %s", args.Path)
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

	absRoot := filepath.Join(bt.RootPath, filepath.FromSlash(resolved))

	var matches []GrepMatch
	var stack []gitignoreFrame

	// Load root .gitignore if present.
	if m, err := loadGitignore(filepath.Join(absRoot, ".gitignore")); err == nil && m != nil {
		stack = append(stack, gitignoreFrame{dir: absRoot, matcher: m})
	}

	err := filepath.Walk(absRoot, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
		}

		rel, err := filepath.Rel(absRoot, filePath)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		// Storage keys use forward slashes; keep match paths storage-relative
		// so agents can pass them straight to read_file.
		relSlash := filepath.ToSlash(rel)
		storagePath := relSlash
		if resolved != "" {
			storagePath = path.Join(resolved, relSlash)
		}

		// Pop frames when leaving their directories.
		for len(stack) > 0 && !strings.HasPrefix(filePath, stack[len(stack)-1].dir+string(filepath.Separator)) {
			stack = stack[:len(stack)-1]
		}

		isDir := info.IsDir()

		// Load nested .gitignore when entering a directory.
		if isDir {
			if m, err := loadGitignore(filepath.Join(filePath, ".gitignore")); err == nil && m != nil {
				stack = append(stack, gitignoreFrame{dir: filePath, matcher: m})
			}
		}

		// Honor .gitignore (paths relative to search root).
		if ignoredByGitignore(stack, relSlash, isDir) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}

		if isDir {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		if isBinary(filePath) {
			return nil
		}
		fileMatches, err := grepFile(filePath, storagePath, matcher)
		if err != nil {
			return nil
		}
		matches = append(matches, fileMatches...)
		if len(matches) >= maxGrepResults {
			return stopWalk{}
		}
		return nil
	})

	truncated := false
	if _, ok := err.(stopWalk); ok {
		truncated = true
		err = nil
	}
	if err != nil {
		return GrepResult{}, err
	}
	if len(matches) > maxGrepResults {
		matches = matches[:maxGrepResults]
		truncated = true
	}

	return GrepResult{
		Path:      resolved,
		Pattern:   args.Pattern,
		Regex:     args.Regex,
		Matches:   matches,
		Truncated: truncated,
	}, nil
}

// stopWalk is a sentinel error used to stop walking once the result cap is hit.
type stopWalk struct{}

func (stopWalk) Error() string { return "max results reached" }

type gitignoreFrame struct {
	dir     string
	matcher *gitignoreMatcher
}

func ignoredByGitignore(stack []gitignoreFrame, rel string, isDir bool) bool {
	for _, frame := range stack {
		if frame.matcher.match(rel, isDir) {
			return true
		}
	}
	return false
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
