package adapters

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func writeTempSource(t *testing.T, name, code string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return src
}

func buildEchoAdapter(t *testing.T) string {
	t.Helper()
	src := writeTempSource(t, "echo.go", `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		id, _ := req["id"].(float64)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      int64(id),
			"result":  req["params"],
		}
		_ = enc.Encode(resp)
	}
}
`)
	bin := filepath.Join(t.TempDir(), "echo")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build echo adapter: %v\n%s", err, out)
	}
	return bin
}

func buildSleepAdapter(t *testing.T) string {
	t.Helper()
	src := writeTempSource(t, "sleep.go", `package main

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	initialized := false
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id, _ := req["id"].(float64)
		if method == "initialize" && !initialized {
			initialized = true
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      int64(id),
				"result":  map[string]any{"ok": true},
			})
			// Stop reading stdin and sleep forever so that Close() must kill us.
			time.Sleep(60 * time.Second)
		}
	}
}
`)
	bin := filepath.Join(t.TempDir(), "sleep")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sleep adapter: %v\n%s", err, out)
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

	resp, err := client.Call(ctx, "storage.read", map[string]any{})
	if err != nil {
		t.Fatalf("call storage.read: %v", err)
	}
	if string(resp.Result) != `{"content":"noop","exists":true,"size":4}` {
		t.Fatalf("unexpected result: %s", resp.Result)
	}
}

func TestJSONRPCClient_LargePayloadRoundTrip(t *testing.T) {
	bin := buildEchoAdapter(t)
	client, err := NewClient(bin)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	large := strings.Repeat("x", 128*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Call(ctx, "echo", map[string]any{"payload": large})
	if err != nil {
		t.Fatalf("call echo: %v", err)
	}
	if !strings.Contains(string(resp.Result), large) {
		t.Fatal("large payload was not returned intact")
	}
}

func TestJSONRPCClient_CloseWithTimeout(t *testing.T) {
	bin := buildSleepAdapter(t)
	client, err := NewClient(bin)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	start := time.Now()
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("close took too long: %v", elapsed)
	}
}
