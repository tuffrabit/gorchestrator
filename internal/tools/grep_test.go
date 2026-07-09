package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestGrepTool_HonorsGitignore(t *testing.T) {
	root := t.TempDir()
	store, err := storage.NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}

	// Write a tracked file and an ignored file.
	if err := store.Write(context.Background(), "tracked.txt", []byte("hello world\n")); err != nil {
		t.Fatalf("write tracked: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatalf("mkdir ignored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "secret.txt"), []byte("hello secret\n"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}

	bt := &BoundTools{
		Storage:   store,
		RootPath:  root,
		Allowlist: []string{""},
	}

	res, err := grepSearch(context.Background(), bt, GrepArgs{Path: ".", Pattern: "hello"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(res.Matches))
	}
	if res.Matches[0].Path != "tracked.txt" {
		t.Fatalf("path = %q, want tracked.txt", res.Matches[0].Path)
	}
}
