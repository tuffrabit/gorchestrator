package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func newDigestEngine(t *testing.T) (*Engine, storage.Port) {
	t.Helper()
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	return &Engine{store: store}, store
}

func writeSourceFile(t *testing.T, ctx context.Context, store storage.Port, rel, content string) {
	t.Helper()
	key := storage.SourcePath(1, 2) + "/" + rel
	if err := store.Write(ctx, key, []byte(content)); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

func TestBuildRepoDigestAuto(t *testing.T) {
	ctx := context.Background()
	e, store := newDigestEngine(t)
	writeSourceFile(t, ctx, store, "main.go", "package main\n\nfunc main() {}\n")
	writeSourceFile(t, ctx, store, "internal/x.go", "package internal\n")
	writeSourceFile(t, ctx, store, "node_modules/big.js", "module.exports = 1\n")
	writeSourceFile(t, ctx, store, "logo.bin", "PNG\x00\x01\x02LOGODATA")
	writeSourceFile(t, ctx, store, ".gitignore", "node_modules/\n")

	digest, err := e.buildRepoDigest(ctx, 1, 2, config.AgentConfig{SingleShotContextBytes: 64 * 1024})
	if err != nil {
		t.Fatalf("buildRepoDigest: %v", err)
	}
	if !strings.Contains(digest, "package main") || !strings.Contains(digest, "package internal") {
		t.Fatalf("digest missing go sources:\n%s", digest)
	}
	if strings.Contains(digest, "module.exports") {
		t.Fatal("digest must exclude gitignored node_modules content")
	}
	if strings.Contains(digest, "node_modules/big.js") {
		t.Fatal("listing must exclude gitignored files")
	}
	if strings.Contains(digest, "LOGODATA") {
		t.Fatal("digest must exclude binary file content")
	}
	if !strings.Contains(digest, "logo.bin") {
		t.Fatal("listing should still name binary files")
	}
	if !strings.Contains(digest, "1 file(s) omitted") {
		t.Fatalf("digest should note the omitted binary:\n%s", digest)
	}
}

func TestBuildRepoDigestBudget(t *testing.T) {
	ctx := context.Background()
	e, store := newDigestEngine(t)
	writeSourceFile(t, ctx, store, "a.txt", strings.Repeat("A", 100))
	writeSourceFile(t, ctx, store, "b.txt", strings.Repeat("B", 100))

	// Budget fits exactly one file section.
	budget := len("\n## a.txt\n```\n" + strings.Repeat("A", 100) + "\n```\n")
	digest, err := e.buildRepoDigest(ctx, 1, 2, config.AgentConfig{SingleShotContextBytes: budget})
	if err != nil {
		t.Fatalf("buildRepoDigest: %v", err)
	}
	if !strings.Contains(digest, strings.Repeat("A", 100)) {
		t.Fatal("digest should contain a.txt")
	}
	if strings.Contains(digest, strings.Repeat("B", 100)) {
		t.Fatal("digest should have omitted b.txt for budget")
	}
	if !strings.Contains(digest, "1 file(s) omitted") {
		t.Fatalf("digest should note the omission:\n%s", digest)
	}
	// The listing still names both files so the model knows what exists.
	if !strings.Contains(digest, "- a.txt") || !strings.Contains(digest, "- b.txt") {
		t.Fatalf("listing incomplete:\n%s", digest)
	}
}

func TestBuildRepoDigestContextFiles(t *testing.T) {
	ctx := context.Background()
	e, store := newDigestEngine(t)
	writeSourceFile(t, ctx, store, "a.go", "package a\n")
	writeSourceFile(t, ctx, store, "b.go", "package b\n")

	// Budget 0 + explicit list → default 64 KiB cap, exactly the listed files.
	digest, err := e.buildRepoDigest(ctx, 1, 2, config.AgentConfig{
		ContextFiles: []string{"a.go", "missing.go"},
	})
	if err != nil {
		t.Fatalf("buildRepoDigest: %v", err)
	}
	if !strings.Contains(digest, "package a") {
		t.Fatal("digest should contain a.go")
	}
	if strings.Contains(digest, "package b") {
		t.Fatal("digest must not contain unlisted b.go")
	}
	if !strings.Contains(digest, "## missing.go") || !strings.Contains(digest, "(not found in source snapshot)") {
		t.Fatalf("digest should mark missing explicit files:\n%s", digest)
	}
}

func TestBuildRepoDigestUnconfigured(t *testing.T) {
	ctx := context.Background()
	e, store := newDigestEngine(t)
	writeSourceFile(t, ctx, store, "a.go", "package a\n")

	digest, err := e.buildRepoDigest(ctx, 1, 2, config.AgentConfig{})
	if err != nil {
		t.Fatalf("buildRepoDigest: %v", err)
	}
	if digest != "" {
		t.Fatalf("no budget and no context_files should yield no digest, got %q", digest)
	}
}

func TestBuildRepoDigestNoSnapshot(t *testing.T) {
	ctx := context.Background()
	e, _ := newDigestEngine(t)

	digest, err := e.buildRepoDigest(ctx, 1, 2, config.AgentConfig{SingleShotContextBytes: 4096})
	if err != nil {
		t.Fatalf("buildRepoDigest: %v", err)
	}
	if digest != "" {
		t.Fatalf("missing snapshot should yield no digest, got %q", digest)
	}
}
