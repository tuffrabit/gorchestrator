package tools

import (
	"bufio"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// GitignoreMatcher holds patterns loaded from a .gitignore file.
type GitignoreMatcher struct {
	patterns []gitignorePattern
}

type gitignorePattern struct {
	raw     string
	negated bool
	dirOnly bool
}

// ParseGitignore parses .gitignore content from r.
func ParseGitignore(r io.Reader) (*GitignoreMatcher, error) {
	m := &GitignoreMatcher{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := gitignorePattern{raw: line}
		if strings.HasPrefix(line, "!") {
			p.negated = true
			p.raw = strings.TrimPrefix(p.raw, "!")
		}
		if strings.HasSuffix(p.raw, "/") {
			p.dirOnly = true
			p.raw = strings.TrimSuffix(p.raw, "/")
		}
		m.patterns = append(m.patterns, p)
	}
	return m, scanner.Err()
}

// loadGitignore reads .gitignore patterns from path. It returns a nil matcher and
// no error if the file does not exist.
func loadGitignore(path string) (*GitignoreMatcher, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return ParseGitignore(f)
}

// Match reports whether rel (relative to the .gitignore file's directory) should
// be ignored. isDir indicates whether rel refers to a directory.
func (m *GitignoreMatcher) Match(rel string, isDir bool) bool {
	if m == nil {
		return false
	}
	matched := false
	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if matchGitignorePattern(p.raw, rel) {
			matched = !p.negated
		}
	}
	return matched
}

// MatchFile reports whether the file at rel is ignored, also considering
// dirOnly patterns against its parent directories (e.g. "node_modules/"
// ignores "node_modules/big.js").
func (m *GitignoreMatcher) MatchFile(rel string) bool {
	if m.Match(rel, false) {
		return true
	}
	for dir := path.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
		if m.Match(dir, true) {
			return true
		}
	}
	return false
}

// matchGitignorePattern implements a subset of .gitignore glob semantics sufficient
// for common repository patterns (node_modules, build dirs, *.log, etc.).
func matchGitignorePattern(pattern, rel string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}

	// Patterns containing a slash are anchored to the .gitignore directory.
	if strings.Contains(pattern, "/") {
		matched, err := filepath.Match(pattern, rel)
		if err == nil && matched {
			return true
		}
		// Also match if rel is inside a matched directory prefix.
		prefix := strings.TrimSuffix(pattern, "/") + "/"
		if strings.HasPrefix(rel, prefix) {
			return true
		}
		return false
	}

	// Patterns without a slash match against any path component.
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		matched, err := filepath.Match(pattern, part)
		if err == nil && matched {
			return true
		}
	}
	return false
}
