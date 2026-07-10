package llm

import (
	"testing"

	"google.golang.org/genai"
)

func TestDeclarationParameters_PrefersParametersJsonSchema(t *testing.T) {
	// ADK functiontool puts the real schema here; Parameters is nil.
	d := &genai.FunctionDeclaration{
		Name: "list_directory",
		ParametersJsonSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path",
				},
			},
			"required": []any{"path"},
		},
	}
	m := DeclarationParameters(d)
	if m["type"] != "object" {
		t.Fatalf("type = %v", m["type"])
	}
	props, ok := m["properties"].(map[string]any)
	if !ok || props["path"] == nil {
		t.Fatalf("properties = %#v, want path", m["properties"])
	}
}

func TestDeclarationParameters_FallsBackToParameters(t *testing.T) {
	// finish_task and hand-built tools use Parameters.
	d := &genai.FunctionDeclaration{
		Name: "finish_task",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"done":      {Type: genai.TypeBoolean},
				"rationale": {Type: genai.TypeString},
			},
			Required: []string{"done", "rationale"},
		},
	}
	m := DeclarationParameters(d)
	props := m["properties"].(map[string]any)
	if props["done"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("done type = %#v", props["done"])
	}
}

func TestDeclarationParameters_EmptyFallback(t *testing.T) {
	m := DeclarationParameters(&genai.FunctionDeclaration{Name: "noop"})
	if m["type"] != "object" {
		t.Fatalf("type = %v", m["type"])
	}
	props, ok := m["properties"].(map[string]any)
	if !ok || len(props) != 0 {
		t.Fatalf("properties = %#v", m["properties"])
	}
}

func TestSchemaToMap_LowercaseJSONSchemaTypes(t *testing.T) {
	s := &genai.Schema{
		Type:        genai.TypeObject,
		Description: "finish_task params",
		Required:    []string{"done", "rationale"},
		Properties: map[string]*genai.Schema{
			"done": {
				Type:        genai.TypeBoolean,
				Description: "Whether the task is complete and meets the rubric.",
			},
			"rationale": {
				Type:        genai.TypeString,
				Description: "Brief explanation of why the task is or is not complete.",
			},
			"tags": {
				Type: genai.TypeArray,
				Items: &genai.Schema{
					Type: genai.TypeString,
				},
			},
		},
	}

	m := SchemaToMap(s)
	if m["type"] != "object" {
		t.Fatalf("root type = %v, want object", m["type"])
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type %T", m["properties"])
	}
	done := props["done"].(map[string]any)
	if done["type"] != "boolean" {
		t.Fatalf("done type = %v, want boolean (not BOOLEAN)", done["type"])
	}
	rat := props["rationale"].(map[string]any)
	if rat["type"] != "string" {
		t.Fatalf("rationale type = %v, want string (not STRING)", rat["type"])
	}
	tags := props["tags"].(map[string]any)
	if tags["type"] != "array" {
		t.Fatalf("tags type = %v, want array", tags["type"])
	}
	items := tags["items"].(map[string]any)
	if items["type"] != "string" {
		t.Fatalf("tags.items type = %v, want string", items["type"])
	}
}

func TestJSONSchemaType_AllGenaiTypes(t *testing.T) {
	cases := []struct {
		in   genai.Type
		want string
	}{
		{genai.TypeString, "string"},
		{genai.TypeBoolean, "boolean"},
		{genai.TypeObject, "object"},
		{genai.TypeArray, "array"},
		{genai.TypeNumber, "number"},
		{genai.TypeInteger, "integer"},
		{genai.Type("string"), "string"}, // already lowercase
	}
	for _, tc := range cases {
		if got := jsonSchemaType(tc.in); got != tc.want {
			t.Errorf("jsonSchemaType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
