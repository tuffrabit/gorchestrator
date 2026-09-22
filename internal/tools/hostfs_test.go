package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupHostTree builds a small project tree under a temp dir and returns the
// root and a hostFS rooted there (64-byte / 4-line read caps).
func setupHostTree(t *testing.T) (string, *hostFS) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("main.go", "package main\n\nfunc main() {}\n")
	write("sub/nested.txt", "hello nested\n")
	write("big.txt", "a\nb\nc\nd\ne\nf\n")
	write("ignored.txt", "hello ignored\n")
	write(".gitignore", "ignored.txt\n")
	write(".git/config", "gitmarker secret\n")

	evalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root: %v", err)
	}
	return root, &hostFS{
		root:             root,
		evalRoot:         evalRoot,
		readFileMaxBytes: 64,
		readFileMaxLines: 4,
	}
}

func TestNewHostReadOnlyRegistry(t *testing.T) {
	root := t.TempDir()
	toolsOut, err := NewHostReadOnlyRegistry(root, 64, 4)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	want := []string{"read_file", "list_directory", "grep_search"}
	if len(toolsOut) != len(want) {
		t.Fatalf("tools = %d, want %d", len(toolsOut), len(want))
	}
	for i, name := range want {
		if toolsOut[i].Name() != name {
			t.Errorf("tool %d name = %q, want %q", i, toolsOut[i].Name(), name)
		}
	}

	if _, err := NewHostReadOnlyRegistry("", 64, 4); err == nil {
		t.Error("expected error for empty root")
	}
	if _, err := NewHostReadOnlyRegistry("relative/path", 64, 4); err == nil {
		t.Error("expected error for relative root")
	}
	if _, err := NewHostReadOnlyRegistry(filepath.Join(root, "missing"), 64, 4); err == nil {
		t.Error("expected error for nonexistent root")
	}
}

func TestHostReadFile_WholeFileUnderCap(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.readFile(ReadFileArgs{Path: "main.go"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if res.Path != "main.go" {
		t.Errorf("path = %q, want main.go", res.Path)
	}
	if res.Truncated {
		t.Error("expected not truncated")
	}
	if res.TotalLines != 3 {
		t.Errorf("total_lines = %d, want 3", res.TotalLines)
	}
	if strings.Count(res.Content, "\n") != 3 {
		t.Errorf("content = %q, want 3 newlines", res.Content)
	}
}

func TestHostReadFile_AbsolutePathIsRootRelative(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.readFile(ReadFileArgs{Path: "/main.go"})
	if err != nil {
		t.Fatalf("read file with leading slash: %v", err)
	}
	if res.Path != "main.go" {
		t.Errorf("path = %q, want main.go", res.Path)
	}
}

func TestHostReadFile_LineCap(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.readFile(ReadFileArgs{Path: "big.txt"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !res.Truncated {
		t.Error("expected truncated")
	}
	if res.TotalLines != 6 {
		t.Errorf("total_lines = %d, want 6", res.TotalLines)
	}
	if strings.Count(res.Content, "\n") != 4 {
		t.Errorf("content = %q, want 4 newlines", res.Content)
	}
}

func TestHostReadFile_ByteCap(t *testing.T) {
	root, h := setupHostTree(t)
	long := strings.Repeat("x", 100) + "\n"
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := h.readFile(ReadFileArgs{Path: "long.txt"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !res.Truncated {
		t.Error("expected truncated by byte cap")
	}
	if res.Size > 64 {
		t.Errorf("size = %d, want <= 64", res.Size)
	}
}

func TestHostReadFile_Surgical(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.readFile(ReadFileArgs{Path: "big.txt", Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if want := "b\nc\n"; res.Content != want {
		t.Errorf("content = %q, want %q", res.Content, want)
	}
	if res.TotalLines != 6 {
		t.Errorf("total_lines = %d, want 6", res.TotalLines)
	}
	if res.Truncated {
		t.Error("surgical mode should not report truncation")
	}
}

func TestHostReadFile_PastEOF(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.readFile(ReadFileArgs{Path: "big.txt", Offset: 10, Limit: 5})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if res.Content != "" {
		t.Errorf("content = %q, want empty", res.Content)
	}
}

func TestHostReadFile_RequiresPath(t *testing.T) {
	_, h := setupHostTree(t)
	if _, err := h.readFile(ReadFileArgs{}); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestHostListDirectory_Root(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.listDirectory(ListDirArgs{Path: "."})
	if err != nil {
		t.Fatalf("list directory: %v", err)
	}
	if res.Path != "" {
		t.Errorf("path = %q, want empty for root", res.Path)
	}
	names := map[string]bool{}
	for _, e := range res.Entries {
		names[e.Name] = true
	}
	for _, want := range []string{"main.go", "sub", "big.txt", "ignored.txt", ".gitignore"} {
		if !names[want] {
			t.Errorf("missing entry %q", want)
		}
	}
	if names[".git"] {
		t.Error(".git must never be listed")
	}
	for _, e := range res.Entries {
		if e.Name == "main.go" {
			if e.IsDir {
				t.Error("main.go reported as dir")
			}
			if want := int64(len("package main\n\nfunc main() {}\n")); e.Size != want {
				t.Errorf("main.go size = %d, want %d", e.Size, want)
			}
		}
		if e.Name == "sub" && !e.IsDir {
			t.Error("sub reported as file")
		}
	}
}

func TestHostListDirectory_Subdir(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.listDirectory(ListDirArgs{Path: "sub"})
	if err != nil {
		t.Fatalf("list directory: %v", err)
	}
	if res.Path != "sub" {
		t.Errorf("path = %q, want sub", res.Path)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "nested.txt" {
		t.Fatalf("entries = %+v, want only nested.txt", res.Entries)
	}
}

func TestHostGrep_Matches(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.grepSearch(GrepArgs{Path: ".", Pattern: "hello"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want exactly 1", res.Matches)
	}
	m := res.Matches[0]
	if m.Path != "sub/nested.txt" || m.Line != 1 || m.Text != "hello nested" {
		t.Errorf("match = %+v, want sub/nested.txt:1", m)
	}
	if res.Truncated {
		t.Error("expected not truncated")
	}
}

func TestHostGrep_HonorsGitignoreAndSkipsGit(t *testing.T) {
	_, h := setupHostTree(t)
	// "hello" appears in both sub/nested.txt and the gitignored ignored.txt;
	// only the tracked file may match (proves .gitignore is honored).
	res, err := h.grepSearch(GrepArgs{Path: ".", Pattern: "hello"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	for _, m := range res.Matches {
		if m.Path == "ignored.txt" {
			t.Errorf("match in gitignored file: %+v", m)
		}
		if strings.HasPrefix(m.Path, ".git/") {
			t.Errorf("match inside .git: %+v", m)
		}
	}
	// "gitmarker" and "secret" exist only inside .git/config; .git must never
	// be searched.
	for _, pattern := range []string{"gitmarker", "secret"} {
		res, err := h.grepSearch(GrepArgs{Path: ".", Pattern: pattern})
		if err != nil {
			t.Fatalf("grep %q: %v", pattern, err)
		}
		if len(res.Matches) != 0 {
			t.Errorf("grep %q matches = %+v, want none (.git skipped)", pattern, res.Matches)
		}
	}
}

func TestHostGrep_RegexMode(t *testing.T) {
	_, h := setupHostTree(t)
	res, err := h.grepSearch(GrepArgs{Path: ".", Pattern: `hello (nest\w+)`, Regex: true})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(res.Matches) != 1 || !res.Regex {
		t.Fatalf("matches = %+v, want 1 regex match", res.Matches)
	}
}

func TestHostGrep_ResultCap(t *testing.T) {
	root, h := setupHostTree(t)
	var sb strings.Builder
	for i := 0; i < maxGrepResults+50; i++ {
		sb.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := h.grepSearch(GrepArgs{Path: ".", Pattern: "needle"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !res.Truncated {
		t.Error("expected truncated at result cap")
	}
	if len(res.Matches) != maxGrepResults {
		t.Errorf("matches = %d, want cap %d", len(res.Matches), maxGrepResults)
	}
}

func TestHostTools_DotDotEscapeRejected(t *testing.T) {
	_, h := setupHostTree(t)
	for _, p := range []string{"..", "../main.go", "sub/../../main.go", "..\\main.go"} {
		if _, err := h.readFile(ReadFileArgs{Path: p}); err == nil {
			t.Errorf("read_file(%q): expected escape rejection", p)
		}
		if _, err := h.listDirectory(ListDirArgs{Path: p}); err == nil {
			t.Errorf("list_directory(%q): expected escape rejection", p)
		}
		if _, err := h.grepSearch(GrepArgs{Path: p, Pattern: "x"}); err == nil {
			t.Errorf("grep_search(%q): expected escape rejection", p)
		}
	}
}

func TestHostTools_SymlinkEscapeRejected(t *testing.T) {
	root, h := setupHostTree(t)

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "escape.txt"), []byte("escaped"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := h.readFile(ReadFileArgs{Path: "link/escape.txt"}); err == nil {
		t.Error("read through escaping symlink: expected rejection")
	}
	if _, err := h.readFile(ReadFileArgs{Path: "link/missing.txt"}); err == nil {
		t.Error("read through escaping symlink (missing leaf): expected rejection")
	}
	if _, err := h.listDirectory(ListDirArgs{Path: "link"}); err == nil {
		t.Error("list through escaping symlink: expected rejection")
	}
	if _, err := h.grepSearch(GrepArgs{Path: "link", Pattern: "escaped"}); err == nil {
		t.Error("grep through escaping symlink: expected rejection")
	}
}

func TestHostTools_SymlinkWithinRootAllowed(t *testing.T) {
	root, h := setupHostTree(t)
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "sublink")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	res, err := h.readFile(ReadFileArgs{Path: "sublink/nested.txt"})
	if err != nil {
		t.Fatalf("read through in-root symlink: %v", err)
	}
	if res.Content != "hello nested\n" {
		t.Errorf("content = %q, want %q", res.Content, "hello nested\n")
	}
}
