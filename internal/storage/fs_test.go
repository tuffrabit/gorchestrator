package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFS_ResolveSiblingPrefixRejection(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}

	// Create a sibling directory whose name is a prefix-extension of the root.
	sibling := root + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("create sibling: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("create secret: %v", err)
	}

	// Path that would be admitted by a naive prefix check.
	rel := "../" + filepath.Base(root) + "-evil/secret.txt"
	_, err = fs.Read(context.Background(), rel)
	if err == nil {
		t.Fatalf("expected rejection for sibling-prefix path %q", rel)
	}
}

func TestFS_ResolveSymlinkFileEscapes(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatalf("create outside file: %v", err)
	}

	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err = fs.Read(context.Background(), "link.txt")
	if err == nil {
		t.Fatal("expected rejection for symlinked file escaping root")
	}
}

func TestFS_ResolveSymlinkDirectoryEscapes(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("create outside file: %v", err)
	}

	// Symlink an intermediate directory inside the root to a directory outside.
	linkDir := filepath.Join(root, "linkdir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatalf("create symlink dir: %v", err)
	}

	_, err = fs.Read(context.Background(), "linkdir/secret.txt")
	if err == nil {
		t.Fatal("expected rejection for symlinked directory escaping root")
	}
}

func TestFS_ResolveSymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	data, err := fs.Read(context.Background(), "link.txt")
	if err != nil {
		t.Fatalf("read inside symlink: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q, want hello", string(data))
	}
}

func TestFS_ListSurfaceError(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}

	// Reading a non-existent directory should surface an error.
	_, err = fs.List(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error listing non-existent directory")
	}
}
