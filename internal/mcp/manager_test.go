package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
)

func buildFixtureMCP(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "fixture.go")
	code := `package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "echo a message",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		msg, _ := args["message"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + msg}},
		}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "query_database",
		Description: "run a query",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		sql, _ := args["sql"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "sql:" + sql}},
		}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "call_http",
		Description: "http call",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		u, _ := args["url"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "url:" + u}},
		}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
`
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fixture-mcp")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture mcp: %v\n%s", err, out)
	}
	return bin
}

func TestManager_ToolsForAgent(t *testing.T) {
	bin := buildFixtureMCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := NewManager(ctx, []config.MCPServerConfig{{
		Name:    "fixture",
		Command: []string{bin},
	}})
	defer m.Close()

	tools, err := m.ToolsForAgent(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("deny by default: got %d tools", len(tools))
	}

	tools, err = m.ToolsForAgent([]string{"fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3 (all tools when grant list empty)", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name()] = true
	}
	if !names["fixture__echo"] || !names["fixture__query_database"] {
		t.Fatalf("names = %v", names)
	}

	tools, err = m.ToolsForAgent([]string{"nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("unknown server should yield 0 tools, got %d", len(tools))
	}
}

func TestManager_PerToolAllowlist(t *testing.T) {
	bin := buildFixtureMCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := NewManager(ctx, []config.MCPServerConfig{{
		Name:    "fixture",
		Command: []string{bin},
		Tools: []config.MCPToolGrant{
			{Name: "echo"},
			{Name: "query_database", Constraints: []config.MCPToolConstraint{{
				Type: "arg_prefix", Arg: "sql", Prefix: "SELECT",
			}}},
		},
	}})
	defer m.Close()

	tools, err := m.ToolsForAgent([]string{"fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2 (echo + query_database)", len(tools))
	}
	for _, tl := range tools {
		if tl.Name() == "fixture__call_http" {
			t.Fatal("call_http must not be advertised")
		}
	}
}

func TestManager_ConstraintArgPrefix(t *testing.T) {
	bin := buildFixtureMCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := NewManager(ctx, []config.MCPServerConfig{{
		Name:    "fixture",
		Command: []string{bin},
		Tools: []config.MCPToolGrant{{
			Name: "query_database",
			Constraints: []config.MCPToolConstraint{{
				Type: "arg_prefix", Arg: "sql", Prefix: "SELECT",
			}},
		}},
	}})
	defer m.Close()

	tools, err := m.ToolsForAgent([]string{"fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	// Invoke via tool interface is awkward; test checkConstraints unit-style instead.
	if err := checkConstraints(map[string]any{"sql": "SELECT 1"}, []config.MCPToolConstraint{{
		Type: "arg_prefix", Arg: "sql", Prefix: "SELECT",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := checkConstraints(map[string]any{"sql": "DROP TABLE x"}, []config.MCPToolConstraint{{
		Type: "arg_prefix", Arg: "sql", Prefix: "SELECT",
	}}); err == nil {
		t.Fatal("expected denial for DROP")
	}
	_ = tools
}

func TestCheckConstraints_URLAllowlist(t *testing.T) {
	cons := []config.MCPToolConstraint{{
		Type: "url_allowlist", Arg: "url", Hosts: []string{"api.internal.example"},
	}}
	if err := checkConstraints(map[string]any{"url": "https://api.internal.example/v1"}, cons); err != nil {
		t.Fatal(err)
	}
	if err := checkConstraints(map[string]any{"url": "https://evil.example/"}, cons); err == nil {
		t.Fatal("expected deny")
	}
}

func TestCheckConstraints_DenySubstring(t *testing.T) {
	cons := []config.MCPToolConstraint{{
		Type: "arg_deny_substring", Arg: "sql", Values: []string{"drop", "delete"},
	}}
	if err := checkConstraints(map[string]any{"sql": "SELECT * FROM t"}, cons); err != nil {
		t.Fatal(err)
	}
	if err := checkConstraints(map[string]any{"sql": "DELETE FROM t"}, cons); err == nil {
		t.Fatal("expected deny")
	}
}

func TestManager_DiscoveredTools(t *testing.T) {
	bin := buildFixtureMCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := NewManager(ctx, []config.MCPServerConfig{{Name: "fixture", Command: []string{bin}}})
	defer m.Close()
	names := m.DiscoveredTools("fixture")
	if len(names) < 1 || !strings.Contains(strings.Join(names, ","), "echo") {
		t.Fatalf("discovered = %v", names)
	}
}
