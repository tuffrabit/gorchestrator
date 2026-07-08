package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// Tool is a callable agent tool.
type Tool interface {
	// Name returns the tool name exposed to the model.
	Name() string

	// Description returns the tool description.
	Description() string

	// Schema returns the JSON schema for the tool parameters.
	Schema() map[string]any

	// Execute runs the tool with the provided arguments.
	Execute(ctx context.Context, args map[string]any) (any, error)
}

// ToLLM converts a Tool to the llm.Tool representation.
func ToLLM(t Tool) llm.Tool {
	return llm.Tool{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters:  t.Schema(),
	}
}

// Registry is a collection of tools available to an agent.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Add registers a tool.
func (r *Registry) Add(t Tool) {
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// LLMTools returns all tools as llm.Tool values.
func (r *Registry) LLMTools() []llm.Tool {
	out := make([]llm.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, ToLLM(t))
	}
	return out
}

// Run parses a tool call and executes it.
func (r *Registry) Run(ctx context.Context, call llm.ToolCall) (string, error) {
	t, ok := r.Get(call.Name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", call.Name)
	}
	result, err := t.Execute(ctx, call.Arguments)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// BoundTools carries the storage port and resolved paths for a single agent run.
type BoundTools struct {
	Storage    storage.Port
	RootPath   string
	Allowlist  []string
	OutputPath string
}

// NewResearcherRegistry creates the core toolset for the Researcher agent.
func NewResearcherRegistry(bt *BoundTools) *Registry {
	r := NewRegistry()
	r.Add(&readFileTool{bt: bt})
	r.Add(&listDirTool{bt: bt})
	r.Add(&grepTool{bt: bt})
	r.Add(&writeOutputTool{bt: bt})
	return r
}

// allowedPath rejects paths that fall outside the configured allowlist.
func allowedPath(path string, allowlist []string) bool {
	for _, prefix := range allowlist {
		// Storage paths are relative; the orchestrator constructs them.
		if path == prefix || len(path) > len(prefix) && path[:len(prefix)+1] == prefix+"/" {
			return true
		}
	}
	return false
}
