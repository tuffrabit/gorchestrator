package tools

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// hostFS carries the resolved host root and read caps for the host read-only
// tools. All path enforcement happens against root/evalRoot before any
// filesystem access.
type hostFS struct {
	root             string // cleaned, absolute host root
	evalRoot         string // root with symlinks resolved (containment target)
	readFileMaxBytes int
	readFileMaxLines int
}

// NewHostReadOnlyRegistry returns read-only tools rooted at an absolute host
// directory. The tools have the same names and argument/result shapes as the
// storage-backed ones (read_file, list_directory, grep_search), so existing
// AgentConfig.Tools allowlists and FilterByNames work unchanged.
//
// Requested paths resolve against rootPath like so: the path is cleaned, and
// absolute-looking requested paths are treated as root-relative (a leading "/"
// is stripped), mirroring resolveAllowedPath. Any path that escapes rootPath
// after cleaning — via ".." or via symlinks — is rejected. Symlink escapes
// are caught by evaluating the resolved path with filepath.EvalSymlinks (or,
// when the path does not exist yet, its nearest existing ancestor) and
// requiring the result to stay within the evaluated rootPath.
//
// read_file mirrors the storage-backed tool's whole-file (byte/line capped)
// and surgical (offset/limit) modes. list_directory lists entries relative to
// the root, skipping .git. grep_search honors .gitignore, skips .git and
// binary files, and caps results at maxGrepResults. No write tools are
// included.
//
// rootPath must be absolute and must exist.
func NewHostReadOnlyRegistry(rootPath string, readFileMaxBytes, readFileMaxLines int) ([]tool.Tool, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, fmt.Errorf("root path is required")
	}
	if !filepath.IsAbs(rootPath) {
		return nil, fmt.Errorf("root path must be absolute: %s", rootPath)
	}
	root := filepath.Clean(rootPath)
	evalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root %s: %w", rootPath, err)
	}

	h := &hostFS{
		root:             root,
		evalRoot:         evalRoot,
		readFileMaxBytes: readFileMaxBytes,
		readFileMaxLines: readFileMaxLines,
	}

	readFile, err := functiontool.New(functiontool.Config{
		Name:        "read_file",
		Description: hostReadFileDescription,
	}, func(_ agent.Context, args ReadFileArgs) (ReadFileResult, error) {
		return h.readFile(args)
	})
	if err != nil {
		return nil, fmt.Errorf("read_file tool: %w", err)
	}
	listDir, err := functiontool.New(functiontool.Config{
		Name:        "list_directory",
		Description: hostListDirDescription,
	}, func(_ agent.Context, args ListDirArgs) (ListDirResult, error) {
		return h.listDirectory(args)
	})
	if err != nil {
		return nil, fmt.Errorf("list_directory tool: %w", err)
	}
	grep, err := functiontool.New(functiontool.Config{
		Name:        "grep_search",
		Description: hostGrepDescription,
	}, func(_ agent.Context, args GrepArgs) (GrepResult, error) {
		return h.grepSearch(args)
	})
	if err != nil {
		return nil, fmt.Errorf("grep_search tool: %w", err)
	}

	return []tool.Tool{readFile, listDir, grep}, nil
}

const hostReadFileDescription = `Read the full or a precise line-number range of a file on the host filesystem.

Path is relative to the project root (e.g. "src/main.go"). Absolute paths are treated as root-relative (a leading "/" is stripped); paths escaping the project root are rejected. Prefer paths returned by grep_search / list_directory.

Two modes:
1. Whole-file (no offset/limit): returns the full file subject to a cap. If the cap is hit, truncated=true and total_lines reports the full line count so you can switch to surgical mode.
2. Surgical (offset + limit): reads exactly the requested 1-based line range. Use this after grep_search returns file paths and line numbers to read only the relevant region.

Prefer grep_search to locate content, then surgical read_file to inspect it.`

const hostListDirDescription = `List the contents of a directory on the host filesystem.

Path is relative to the project root (e.g. "src"); omit or "." for the root. Absolute paths are treated as root-relative (a leading "/" is stripped); paths escaping the project root are rejected. The .git directory is never listed.

Returns the resolved root-relative path and directory entries.`

const hostGrepDescription = `Search file contents for a pattern under the project root on the host filesystem. Returns matching lines with root-relative file paths suitable for read_file. Honors .gitignore, skips .git and binary files.

Path is relative to the project root; omit or "." for the root. Absolute paths are treated as root-relative (a leading "/" is stripped); paths escaping the project root are rejected.`

// resolveHostPath resolves an agent-provided path to an absolute host path
// and its root-relative slash form. Empty and "." resolve to the root itself.
// Absolute-looking requested paths are treated as root-relative (leading "/"
// stripped). Paths that escape the root lexically ("..") or through symlinks
// are rejected.
func (h *hostFS) resolveHostPath(agentPath string) (abs, rel string, err error) {
	p := strings.TrimSpace(agentPath)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")

	if p == "" || p == "." {
		return h.root, "", nil
	}

	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", fmt.Errorf("path escapes root: %s", agentPath)
	}

	abs = filepath.Join(h.root, filepath.FromSlash(clean))
	if abs != h.root && !strings.HasPrefix(abs, h.root+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path escapes root: %s", agentPath)
	}

	// Symlink containment: evaluate the path (or its nearest existing
	// ancestor) and require the result to stay under the evaluated root.
	ev, err := evalPathInRoot(abs)
	if err != nil {
		return "", "", err
	}
	if ev != h.evalRoot && !strings.HasPrefix(ev, h.evalRoot+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path escapes root via symlink: %s", agentPath)
	}

	return abs, clean, nil
}

// evalPathInRoot returns abs with symlinks resolved. When abs does not exist,
// the nearest existing ancestor is evaluated and the missing tail re-appended,
// so symlinked intermediate components are still resolved.
func evalPathInRoot(abs string) (string, error) {
	if ev, err := filepath.EvalSymlinks(abs); err == nil {
		return ev, nil
	}
	var tail []string
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("cannot resolve path %q", abs)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
		ev, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				ev = filepath.Join(ev, tail[i])
			}
			return ev, nil
		}
	}
}

func (h *hostFS) readFile(args ReadFileArgs) (ReadFileResult, error) {
	if args.Path == "" {
		return ReadFileResult{}, fmt.Errorf("path is required")
	}
	abs, rel, err := h.resolveHostPath(args.Path)
	if err != nil {
		return ReadFileResult{}, fmt.Errorf("path not allowed: %s", args.Path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return ReadFileResult{}, err
	}

	lines := splitLines(string(data))
	totalLines := len(lines)

	if args.Offset <= 0 && args.Limit <= 0 {
		content, truncated := applyCap(lines, h.readFileMaxBytes, h.readFileMaxLines)
		return ReadFileResult{
			Path:       rel,
			Content:    content,
			Size:       len(content),
			TotalLines: totalLines,
			Truncated:  truncated,
		}, nil
	}

	content := applyRange(lines, args.Offset, args.Limit)
	return ReadFileResult{
		Path:       rel,
		Content:    content,
		Size:       len(content),
		TotalLines: totalLines,
		Truncated:  false,
	}, nil
}

func (h *hostFS) listDirectory(args ListDirArgs) (ListDirResult, error) {
	abs, rel, err := h.resolveHostPath(args.Path)
	if err != nil {
		return ListDirResult{}, fmt.Errorf("path not allowed: %s", args.Path)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return ListDirResult{}, err
	}
	out := make([]storage.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return ListDirResult{}, err
		}
		out = append(out, storage.DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		})
	}
	return ListDirResult{Path: rel, Entries: out}, nil
}

func (h *hostFS) grepSearch(args GrepArgs) (GrepResult, error) {
	if args.Pattern == "" {
		return GrepResult{}, fmt.Errorf("pattern is required")
	}

	absRoot, resolved, err := h.resolveHostPath(args.Path)
	if err != nil {
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

	var matches []GrepMatch
	var stack []gitignoreFrame

	// Load root .gitignore if present.
	if m, err := loadGitignore(filepath.Join(absRoot, ".gitignore")); err == nil && m != nil {
		stack = append(stack, gitignoreFrame{dir: absRoot, matcher: m})
	}

	err = filepath.Walk(absRoot, func(filePath string, info os.FileInfo, err error) error {
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
		// Match paths are root-relative (slash form) so agents can pass them
		// straight to read_file.
		relSlash := filepath.ToSlash(rel)
		matchPath := relSlash
		if resolved != "" {
			matchPath = path.Join(resolved, relSlash)
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
		fileMatches, err := grepFile(filePath, matchPath, matcher)
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
