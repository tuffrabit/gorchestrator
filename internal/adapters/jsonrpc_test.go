package adapters

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func buildNoopAdapter(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test directory")
	}
	src := filepath.Join(filepath.Dir(here), "..", "..", "adapters", "noop", "main.go")
	bin := filepath.Join(t.TempDir(), "noop")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build noop adapter: %v\n%s", err, out)
	}
	return bin
}

func TestJSONRPCClient(t *testing.T) {
	bin := buildNoopAdapter(t)
	client, err := NewClient(bin)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Call(ctx, "initialize", map[string]any{})
	if err != nil {
		t.Fatalf("call initialize: %v", err)
	}
	if string(resp.Result) != `{"ok":true}` {
		t.Fatalf("unexpected result: %s", resp.Result)
	}
}
