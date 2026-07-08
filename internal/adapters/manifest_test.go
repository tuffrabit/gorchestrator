package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscovery(t *testing.T) {
	dir := t.TempDir()
	bin := buildNoopAdapter(t)

	manifest := `name: noop
version: "1.0.0"
protocol: jsonrpc-stdio
port: storage
capabilities: [read]
`
	if err := os.WriteFile(filepath.Join(dir, "noop.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	// Copy built binary next to manifest.
	binDest := filepath.Join(dir, "noop")
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if err := os.WriteFile(binDest, data, 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	manifests, err := Discovery(dir)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(manifests))
	}
	m := manifests[0]
	if m.Name != "noop" {
		t.Fatalf("name = %q, want noop", m.Name)
	}
	if m.Port != "storage" {
		t.Fatalf("port = %q, want storage", m.Port)
	}
}
