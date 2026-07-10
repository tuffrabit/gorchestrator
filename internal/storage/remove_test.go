package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFS_RemoveAll_IssueTree(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := "projects/1/issues/2"
	if err := fs.Write(ctx, dir+"/research/output.md", []byte("findings")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(ctx, dir+"/source/main.go", []byte("package main")); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveAll(ctx, dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	exists, err := fs.Exists(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("issue dir still exists")
	}
	// Parent projects/1/issues may remain empty; that's fine.
	if err := fs.RemoveAll(ctx, dir); err != nil {
		t.Fatalf("RemoveAll missing path: %v", err)
	}
}

func TestFS_RemoveAll_RefusesRoot(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range []string{"", ".", "/", "  "} {
		if err := fs.RemoveAll(ctx, p); err == nil {
			t.Fatalf("expected refuse for %q", p)
		}
	}
	// Root itself must still exist.
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("storage root missing: %v", err)
	}
	// A marker file must survive refuse attempts.
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveAll(ctx, ""); err == nil {
		t.Fatal("expected refuse")
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatal("keep.txt was deleted")
	}
}
