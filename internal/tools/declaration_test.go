package tools

import (
	"encoding/json"
	"testing"

	"google.golang.org/genai"
)

// TestFunctionToolDeclarationsHaveParameterSchemas guards against regressions
// where ADK tools expose parameters only via ParametersJsonSchema (not
// Parameters). Empty properties cause models to call tools with {} forever.
func TestFunctionToolDeclarationsHaveParameterSchemas(t *testing.T) {
	bt := &BoundTools{RootPath: "/tmp", Allowlist: []string{"."}, OutputPath: "out.md"}
	regs, err := NewResearcherRegistry(bt)
	if err != nil {
		t.Fatal(err)
	}

	wantProps := map[string][]string{
		"read_file":      {"path", "offset", "limit"},
		"list_directory": {"path"},
		"grep_search":    {"path", "pattern", "regex"},
		"write_output":   {"content"},
	}
	// Fields that must NOT be required (optional with defaults).
	mustNotRequire := map[string][]string{
		"read_file":      {"offset", "limit"},
		"list_directory": {"path"},
		"grep_search":    {"path", "regex"},
	}

	for _, tl := range regs {
		type declarer interface {
			Declaration() *genai.FunctionDeclaration
		}
		d, ok := tl.(declarer)
		if !ok {
			t.Fatalf("%s does not implement Declaration()", tl.Name())
		}
		decl := d.Declaration()
		if decl.Parameters != nil {
			t.Logf("%s: Parameters set (unusual for functiontool)", decl.Name)
		}
		if decl.ParametersJsonSchema == nil {
			t.Fatalf("%s: ParametersJsonSchema is nil", decl.Name)
		}

		b, err := json.Marshal(decl.ParametersJsonSchema)
		if err != nil {
			t.Fatalf("%s: marshal schema: %v", decl.Name, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatalf("%s: unmarshal schema: %v", decl.Name, err)
		}
		props, _ := schema["properties"].(map[string]any)
		if len(props) == 0 {
			t.Fatalf("%s: empty properties (models will call with {})", decl.Name)
		}
		for _, p := range wantProps[decl.Name] {
			if _, ok := props[p]; !ok {
				t.Errorf("%s: missing property %q", decl.Name, p)
			}
		}
		req, _ := schema["required"].([]any)
		reqSet := map[string]bool{}
		for _, r := range req {
			if s, ok := r.(string); ok {
				reqSet[s] = true
			}
		}
		for _, p := range mustNotRequire[decl.Name] {
			if reqSet[p] {
				t.Errorf("%s: %q should not be required (optional field)", decl.Name, p)
			}
		}
	}
}
