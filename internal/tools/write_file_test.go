package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestWriteFile_WorksInWorkspace(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	bt := &BoundTools{
		Storage:       store,
		RootPath:      tmp,
		WorkspacePath: "workspace",
	}
	if err := store.Mkdir(ctx, "workspace"); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	res, err := writeFile(ctx, bt, WriteFileArgs{Path: "main.go", Content: "package main\n"})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if res.Status != "written" {
		t.Fatalf("status = %q, want written", res.Status)
	}

	data, err := store.Read(ctx, "workspace/main.go")
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("content = %q", string(data))
	}
}

func TestWriteFile_RejectsEscape(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	bt := &BoundTools{
		Storage:       store,
		RootPath:      tmp,
		WorkspacePath: "workspace",
	}
	if err := store.Mkdir(ctx, "workspace"); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	_, err = writeFile(ctx, bt, WriteFileArgs{Path: "../escaped.go", Content: "evil"})
	if err == nil {
		t.Fatal("expected error for path escape")
	}
}

func TestWriteFile_RejectsSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	// Create a workspace with a symlink pointing outside the storage root.
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	// Place the symlink target outside the storage root entirely.
	outside := filepath.Join(filepath.Dir(tmp), "outside-"+filepath.Base(tmp))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	bt := &BoundTools{
		Storage:       store,
		RootPath:      tmp,
		WorkspacePath: "workspace",
	}

	_, err = writeFile(ctx, bt, WriteFileArgs{Path: "link/escaped.go", Content: "evil"})
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
}
