package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func setupReadFile(t *testing.T, content string) (*BoundTools, context.Context) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}
	if err := store.Write(context.Background(), "test.txt", []byte(content)); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	bt := &BoundTools{
		Storage:          store,
		RootPath:         root,
		Allowlist:        []string{""},
		ReadFileMaxBytes: 64,
		ReadFileMaxLines: 4,
	}
	return bt, context.Background()
}

func TestReadFile_WholeFileUnderCap(t *testing.T) {
	bt, ctx := setupReadFile(t, "line1\nline2\nline3\n")
	res, err := readFile(ctx, bt, ReadFileArgs{Path: "test.txt"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if res.Truncated {
		t.Fatal("expected not truncated")
	}
	if res.TotalLines != 3 {
		t.Fatalf("total_lines = %d, want 3", res.TotalLines)
	}
	if strings.Count(res.Content, "\n") != 3 {
		t.Fatalf("content = %q, want 3 newlines", res.Content)
	}
}

func TestReadFile_WholeFileOverCapByLines(t *testing.T) {
	bt, ctx := setupReadFile(t, "a\nb\nc\nd\ne\n")
	res, err := readFile(ctx, bt, ReadFileArgs{Path: "test.txt"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !res.Truncated {
		t.Fatal("expected truncated")
	}
	if res.TotalLines != 5 {
		t.Fatalf("total_lines = %d, want 5", res.TotalLines)
	}
	if strings.Count(res.Content, "\n") != 4 {
		t.Fatalf("content = %q, want 4 newlines", res.Content)
	}
}

func TestReadFile_WholeFileOverCapByBytes(t *testing.T) {
	bt, ctx := setupReadFile(t, strings.Repeat("x", 100))
	res, err := readFile(ctx, bt, ReadFileArgs{Path: "test.txt"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !res.Truncated {
		t.Fatal("expected truncated")
	}
	if res.Size > bt.ReadFileMaxBytes {
		t.Fatalf("size = %d, want <= %d", res.Size, bt.ReadFileMaxBytes)
	}
}

func TestReadFile_SurgicalMidFile(t *testing.T) {
	bt, ctx := setupReadFile(t, "line1\nline2\nline3\nline4\nline5\n")
	res, err := readFile(ctx, bt, ReadFileArgs{Path: "test.txt", Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	want := "line2\nline3\n"
	if res.Content != want {
		t.Fatalf("content = %q, want %q", res.Content, want)
	}
	if res.TotalLines != 5 {
		t.Fatalf("total_lines = %d, want 5", res.TotalLines)
	}
}

func TestReadFile_SurgicalPastEOF(t *testing.T) {
	bt, ctx := setupReadFile(t, "line1\nline2\n")
	res, err := readFile(ctx, bt, ReadFileArgs{Path: "test.txt", Offset: 10, Limit: 5})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if res.Content != "" {
		t.Fatalf("content = %q, want empty", res.Content)
	}
	if res.TotalLines != 2 {
		t.Fatalf("total_lines = %d, want 2", res.TotalLines)
	}
}

func TestReadFile_CapOverrideFromConfig(t *testing.T) {
	bt, ctx := setupReadFile(t, "a\nb\nc\nd\ne\nf\n")
	bt.ReadFileMaxLines = 10
	res, err := readFile(ctx, bt, ReadFileArgs{Path: "test.txt"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if res.Truncated {
		t.Fatal("expected not truncated with higher cap")
	}
	if res.TotalLines != 6 {
		t.Fatalf("total_lines = %d, want 6", res.TotalLines)
	}
}

func TestReadFile_RequiresPath(t *testing.T) {
	bt, ctx := setupReadFile(t, "x")
	_, err := readFile(ctx, bt, ReadFileArgs{})
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestReadFile_AllowlistEnforced(t *testing.T) {
	root := t.TempDir()
	store, err := storage.NewFS(root)
	if err != nil {
		t.Fatalf("new fs: %v", err)
	}
	if err := store.Write(context.Background(), "allowed.txt", []byte("ok")); err != nil {
		t.Fatalf("write allowed file: %v", err)
	}
	if err := store.Write(context.Background(), "secret.txt", []byte("secret")); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	bt := &BoundTools{
		Storage:          store,
		RootPath:         root,
		Allowlist:        []string{"allowed.txt"},
		ReadFileMaxBytes: 1024,
		ReadFileMaxLines: 100,
	}
	_, err = readFile(context.Background(), bt, ReadFileArgs{Path: "secret.txt"})
	if err == nil {
		t.Fatal("expected error for disallowed path")
	}
}
