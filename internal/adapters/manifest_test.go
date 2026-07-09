package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest_Valid(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "noop")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho noop"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	manifest := `name: noop
version: "1.0.0"
protocol: jsonrpc-stdio
port: storage
binary: ./noop
capabilities: [read]
`
	path := filepath.Join(dir, "noop.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if m.Name != "noop" {
		t.Fatalf("name = %q, want noop", m.Name)
	}
	if m.Port != "storage" {
		t.Fatalf("port = %q, want storage", m.Port)
	}
	if m.Binary != bin {
		t.Fatalf("binary = %q, want %q", m.Binary, bin)
	}
}

func TestLoadManifest_RejectDirectoryBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "noop"), 0o755); err != nil {
		t.Fatalf("mkdir binary dir: %v", err)
	}

	manifest := `name: noop
binary: ./noop
`
	path := filepath.Join(dir, "noop.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for directory binary")
	}
}

func TestLoadManifest_RejectNonExecutable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "noop")
	if err := os.WriteFile(bin, []byte("data"), 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	manifest := `name: noop
binary: ./noop
`
	path := filepath.Join(dir, "noop.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for non-executable binary")
	}
}

func TestLoadManifest_RejectMissingBinaryField(t *testing.T) {
	dir := t.TempDir()
	manifest := `name: noop
`
	path := filepath.Join(dir, "noop.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for missing binary field")
	}
}
