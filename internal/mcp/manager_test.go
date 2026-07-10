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
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	if tools[0].Name() != "fixture__echo" {
		t.Fatalf("name = %q", tools[0].Name())
	}
	if !strings.Contains(tools[0].Description(), "echo") {
		t.Fatalf("desc = %q", tools[0].Description())
	}

	tools, err = m.ToolsForAgent([]string{"nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("unknown server should yield 0 tools, got %d", len(tools))
	}
}
