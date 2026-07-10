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

func buildNotifyAdapter(t *testing.T) string {
	t.Helper()
	src := writeTempSource(t, "notify.go", `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id, hasID := req["id"]
		if method == "initialize" {
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"ok": true},
			})
			// Emit a notification after initialize.
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"method":  "trigger.issue",
				"params":  map[string]any{"title": "from-adapter"},
			})
			continue
		}
		if hasID {
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"method": method},
			})
		}
	}
}
`)
	bin := filepath.Join(t.TempDir(), "notify")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build notify adapter: %v\n%s", err, out)
	}
	return bin
}

func buildCrashOnceAdapter(t *testing.T, flagPath string) string {
	t.Helper()
	src := writeTempSource(t, "crashonce.go", `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

func main() {
	flag := os.Getenv("CRASH_FLAG")
	if flag != "" {
		if _, err := os.Stat(flag); err == nil {
			// Second start: stay alive.
		} else {
			// First start: create flag and exit after initialize.
			f, _ := os.Create(flag)
			if f != nil {
				_ = f.Close()
			}
			scanner := bufio.NewScanner(os.Stdin)
			enc := json.NewEncoder(os.Stdout)
			if scanner.Scan() {
				var req map[string]any
				_ = json.Unmarshal(scanner.Bytes(), &req)
				_ = enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"result":  map[string]any{"ok": true},
				})
			}
			os.Exit(1)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  map[string]any{"ok": true, "stable": true},
		})
	}
}
`)
	bin := filepath.Join(t.TempDir(), "crashonce")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build crashonce adapter: %v\n%s", err, out)
	}
	return bin
}

func TestJSONRPCClient_Notifications(t *testing.T) {
	bin := buildNotifyAdapter(t)
	client, err := NewClient(bin)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	select {
	case n := <-client.Notifications():
		if n.Method != "trigger.issue" {
			t.Fatalf("method: %q", n.Method)
		}
		if !strings.Contains(string(n.Params), "from-adapter") {
			t.Fatalf("params: %s", n.Params)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for notification")
	}
}

func TestJSONRPCClient_ExtraEnv(t *testing.T) {
	src := writeTempSource(t, "env.go", `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  map[string]any{"v": os.Getenv("GORCH_TEST_SECRET")},
		})
	}
}
`)
	bin := filepath.Join(t.TempDir(), "env")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build env adapter: %v\n%s", err, out)
	}

	client, err := NewClientWithOptions(bin, ClientOptions{
		ExtraEnv: []string{"GORCH_TEST_SECRET=s3cr3t"},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	// initialize already consumed; call again
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Call(ctx, "ping", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(resp.Result), "s3cr3t") {
		t.Fatalf("env not passed: %s", resp.Result)
	}
}

func TestSupervisor_RestartAfterCrash(t *testing.T) {
	flagPath := filepath.Join(t.TempDir(), "crashed")
	bin := buildCrashOnceAdapter(t, flagPath)

	sup, err := NewSupervisor(bin, SupervisorConfig{
		MinBackoff: 50 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond,
		MaxDown:    10 * time.Second,
		Client: ClientOptions{
			ExtraEnv: []string{"CRASH_FLAG=" + flagPath},
		},
	})
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	defer sup.Close()

	// First client exits after initialize; wait for restart.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := sup.Call(ctx, "ping", map[string]any{})
		cancel()
		if err == nil {
			if !strings.Contains(string(resp.Result), "stable") {
				t.Fatalf("expected stable result after restart, got %s", resp.Result)
			}
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("never recovered after crash: %v", lastErr)
}

func TestSupervisor_NotificationsAcrossRestart(t *testing.T) {
	bin := buildNotifyAdapter(t)
	sup, err := NewSupervisor(bin, SupervisorConfig{
		MinBackoff: 50 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	defer sup.Close()

	select {
	case n := <-sup.Notifications():
		if n.Method != "trigger.issue" {
			t.Fatalf("method: %q", n.Method)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for supervised notification")
	}
}
