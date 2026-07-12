// Package mcp implements an MCP client manager with per-agent server allowlists
// and Phase 5 per-tool grants + simple argument constraints.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
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

// Configs returns a copy of configured MCP servers (for admin permissions view).
func (m *Manager) Configs() []config.MCPServerConfig {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]config.MCPServerConfig, 0, len(m.cfgs))
	for _, c := range m.cfgs {
		out = append(out, c)
	}
	return out
}

// DiscoveredTools returns live tool names for a connected server (empty if not connected).
func (m *Manager) DiscoveredTools(server string) []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ss, ok := m.sessions[server]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(ss.tools))
	for _, t := range ss.tools {
		if t != nil {
			out = append(out, t.Name)
		}
	}
	return out
}

// ToolsForAgent returns ADK tools for the servers listed in allowServers.
// Tool names are prefixed as {server}__{tool} to avoid collisions.
// When a server config lists Tools, only those tool names are wrapped.
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
		cfg := m.cfgs[name]
		allow := toolGrantIndex(cfg.Tools)
		for _, t := range ss.tools {
			if t == nil {
				continue
			}
			if allow != nil {
				if _, ok := allow[t.Name]; !ok {
					continue
				}
			}
			var grants []config.MCPToolConstraint
			if allow != nil {
				grants = allow[t.Name]
			}
			adkTool, err := wrapMCPTool(ss, t, grants)
			if err != nil {
				return nil, fmt.Errorf("wrap %s/%s: %w", name, t.Name, err)
			}
			out = append(out, adkTool)
		}
	}
	return out, nil
}

// toolGrantIndex maps tool name → constraints. nil means "all tools, no constraints".
func toolGrantIndex(grants []config.MCPToolGrant) map[string][]config.MCPToolConstraint {
	if len(grants) == 0 {
		return nil
	}
	out := make(map[string][]config.MCPToolConstraint, len(grants))
	for _, g := range grants {
		out[g.Name] = g.Constraints
	}
	return out
}

type mcpArgs struct {
	// Arguments is a JSON object of tool parameters (MCP input schema).
	Arguments json.RawMessage `json:"arguments" jsonschema:"JSON object matching the MCP tool input schema"`
}

type mcpResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

func wrapMCPTool(ss *serverSession, t *mcp.Tool, constraints []config.MCPToolConstraint) (tool.Tool, error) {
	prefixed := ss.name + "__" + t.Name
	desc := t.Description
	if desc == "" {
		desc = "MCP tool " + t.Name + " from server " + ss.name
	}
	desc += " (MCP server " + ss.name + "; pass arguments as a JSON object in the arguments field)"

	toolName := t.Name
	session := ss.session
	// copy constraints for closure
	cons := append([]config.MCPToolConstraint(nil), constraints...)

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
		if err := checkConstraints(argMap, cons); err != nil {
			return mcpResult{IsError: true, Content: "constraint denied: " + err.Error()}, nil
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

// checkConstraints evaluates simple arg constraints. Fail closed on violation.
func checkConstraints(args map[string]any, cons []config.MCPToolConstraint) error {
	for _, c := range cons {
		raw, ok := args[c.Arg]
		val, _ := raw.(string)
		if !ok || val == "" {
			// Missing required constrained arg is a denial.
			return fmt.Errorf("%s: argument %q is required", c.Type, c.Arg)
		}
		switch c.Type {
		case "arg_prefix":
			if !strings.HasPrefix(val, c.Prefix) {
				return fmt.Errorf("arg_prefix: %q must start with %q", c.Arg, c.Prefix)
			}
		case "arg_deny_substring":
			lower := strings.ToLower(val)
			for _, sub := range c.Values {
				if sub != "" && strings.Contains(lower, strings.ToLower(sub)) {
					return fmt.Errorf("arg_deny_substring: %q must not contain %q", c.Arg, sub)
				}
			}
		case "url_allowlist":
			u, err := url.Parse(val)
			if err != nil || u.Host == "" {
				return fmt.Errorf("url_allowlist: %q is not a valid URL with host", c.Arg)
			}
			host := strings.ToLower(u.Hostname())
			allowed := false
			for _, h := range c.Hosts {
				if strings.EqualFold(host, strings.TrimSpace(h)) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("url_allowlist: host %q not in allowlist", host)
			}
		default:
			return fmt.Errorf("unknown constraint type %q", c.Type)
		}
	}
	return nil
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
