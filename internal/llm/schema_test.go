package llm

import (
	"testing"

	"google.golang.org/genai"
)

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
