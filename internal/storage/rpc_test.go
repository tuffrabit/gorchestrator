package storage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/adapters"
)

func buildMemStorageAdapter(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "mem.go")
	code := `package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
)

func main() {
	store := map[string]string{}
	var mu sync.Mutex
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
			Method  string          ` + "`json:\"method\"`" + `
			Params  json.RawMessage ` + "`json:\"params\"`" + `
			ID      any             ` + "`json:\"id\"`" + `
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		var params map[string]any
		_ = json.Unmarshal(req.Params, &params)
		if params == nil {
			params = map[string]any{}
		}
		switch req.Method {
		case "initialize":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true, "port": "storage"}})
		case "storage.write":
			path, _ := params["path"].(string)
			content, _ := params["content"].(string)
			mu.Lock()
			store[path] = content
			mu.Unlock()
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true}})
		case "storage.read":
			path, _ := params["path"].(string)
			mu.Lock()
			c, ok := store[path]
			mu.Unlock()
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"exists": ok, "content_str": c, "size": len(c)}})
		case "storage.exists":
			path, _ := params["path"].(string)
			mu.Lock()
			_, ok := store[path]
			mu.Unlock()
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"exists": ok}})
		case "storage.list":
			path, _ := params["path"].(string)
			if path != "" && !strings.HasSuffix(path, "/") {
				path += "/"
			}
			var entries []map[string]any
			mu.Lock()
			for k, v := range store {
				if strings.HasPrefix(k, path) {
					name := strings.TrimPrefix(k, path)
					if i := strings.Index(name, "/"); i >= 0 {
						name = name[:i]
					}
					entries = append(entries, map[string]any{"name": name, "is_dir": false, "size": len(v)})
				}
			}
			mu.Unlock()
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"entries": entries}})
		case "storage.mkdir":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true}})
		default:
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "not found"}})
		}
	}
}
`
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "mem-storage")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestRPCPort_RoundTrip(t *testing.T) {
	bin := buildMemStorageAdapter(t)
	client, err := adapters.NewClient(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	port := NewRPCPort(client, "mem")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := "projects/1/issues/2/research/result.json"
	if err := port.Write(ctx, key, []byte(`{"status":"done"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	ok, err := port.Exists(ctx, key)
	if err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
	data, err := port.Read(ctx, key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != `{"status":"done"}` {
		t.Fatalf("data = %s", data)
	}
}
