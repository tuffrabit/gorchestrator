// Package mcp implements an MCP client manager with per-agent server allowlists.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/tuffrabit/gorchestrator/internal/config"
)

// Manager holds long-lived MCP client sessions keyed by server name.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*serverSession
	cfgs     map[string]config.MCPServerConfig
}

type serverSession struct {
	name    string
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

// NewManager connects to all configured MCP servers (stdio). Failures to
// connect a single server are logged and skipped so one bad server does not
// block the daemon.
func NewManager(ctx context.Context, servers []config.MCPServerConfig) *Manager {
	m := &Manager{
		sessions: make(map[string]*serverSession),
		cfgs:     make(map[string]config.MCPServerConfig),
	}
	for _, s := range servers {
		if s.Name == "" || len(s.Command) == 0 {
			continue
		}
		m.cfgs[s.Name] = s
		if err := m.connect(ctx, s); err != nil {
			log.Printf("mcp: failed to connect server %q: %v", s.Name, err)
			continue
		}
		log.Printf("mcp: connected server %q (%d tools)", s.Name, len(m.sessions[s.Name].tools))
	}
	return m
}

func (m *Manager) connect(ctx context.Context, s config.MCPServerConfig) error {
	cmd := exec.CommandContext(ctx, s.Command[0], append(s.Command[1:], s.Args...)...)
	// Pass through selected env var names (values from host env — never logged).
	if len(s.Env) > 0 {
		env := os.Environ()
		for _, name := range s.Env {
			if v, ok := os.LookupEnv(name); ok {
				env = append(env, name+"="+v)
			}
		}
		cmd.Env = env
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "gorchestrator", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("list tools: %w", err)
	}
	m.mu.Lock()
	m.sessions[s.Name] = &serverSession{
		name:    s.Name,
		session: session,
		tools:   listed.Tools,
	}
	m.mu.Unlock()
	return nil
}

// Close shuts down all sessions.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for name, ss := range m.sessions {
		if err := ss.session.Close(); err != nil && first == nil {
			first = err
		}
		delete(m.sessions, name)
	}
	return first
}

// ToolsForAgent returns ADK tools for the servers listed in allowServers.
// Tool names are prefixed as {server}__{tool} to avoid collisions.
func (m *Manager) ToolsForAgent(allowServers []string) ([]tool.Tool, error) {
	if m == nil || len(allowServers) == 0 {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []tool.Tool
	for _, name := range allowServers {
		ss, ok := m.sessions[name]
		if !ok {
			continue
		}
		for _, t := range ss.tools {
			adkTool, err := wrapMCPTool(ss, t)
			if err != nil {
				return nil, fmt.Errorf("wrap %s/%s: %w", name, t.Name, err)
			}
			out = append(out, adkTool)
		}
	}
	return out, nil
}

type mcpArgs struct {
	// Arguments is a JSON object of tool parameters (MCP input schema).
	Arguments json.RawMessage `json:"arguments" jsonschema:"JSON object matching the MCP tool input schema"`
}

type mcpResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

func wrapMCPTool(ss *serverSession, t *mcp.Tool) (tool.Tool, error) {
	prefixed := ss.name + "__" + t.Name
	desc := t.Description
	if desc == "" {
		desc = "MCP tool " + t.Name + " from server " + ss.name
	}
	desc += " (MCP server " + ss.name + "; pass arguments as a JSON object in the arguments field)"

	toolName := t.Name
	session := ss.session

	return functiontool.New(functiontool.Config{
		Name:        prefixed,
		Description: desc,
	}, func(ctx agent.Context, args mcpArgs) (mcpResult, error) {
		var argMap map[string]any
		if len(args.Arguments) > 0 && string(args.Arguments) != "null" {
			if err := json.Unmarshal(args.Arguments, &argMap); err != nil {
				return mcpResult{IsError: true, Content: "invalid arguments JSON: " + err.Error()}, nil
			}
		}
		if argMap == nil {
			argMap = map[string]any{}
		}
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: argMap,
		})
		if err != nil {
			return mcpResult{IsError: true, Content: err.Error()}, nil
		}
		text := formatCallResult(res)
		return mcpResult{Content: text, IsError: res != nil && res.IsError}, nil
	})
}

func formatCallResult(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		default:
			b, _ := json.Marshal(c)
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}
