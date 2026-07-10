package llm

import (
	"encoding/json"
	"strings"

	"google.golang.org/genai"
)

// emptyObjectSchema is the fallback when a function declaration has no
// parameter schema. Prefer DeclarationParameters, which also handles ADK
// function tools that populate ParametersJsonSchema instead of Parameters.
func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// DeclarationParameters returns an OpenAI/Anthropic-compatible JSON Schema map
// for a function declaration's parameters.
//
// ADK functiontool.New stores the inferred schema in ParametersJsonSchema
// (*jsonschema.Schema), not Parameters (*genai.Schema). finish_task and other
// hand-built tools still use Parameters. Both must be handled or tools are
// sent with empty properties and models (especially llama.cpp) call them with {}.
func DeclarationParameters(d *genai.FunctionDeclaration) map[string]any {
	if d == nil {
		return emptyObjectSchema()
	}
	if d.ParametersJsonSchema != nil {
		if m := anySchemaToMap(d.ParametersJsonSchema); m != nil {
			return m
		}
	}
	if m := SchemaToMap(d.Parameters); m != nil {
		return m
	}
	return emptyObjectSchema()
}

// anySchemaToMap converts ParametersJsonSchema (any) into a plain map.
// Values are typically *jsonschema.Schema from google/jsonschema-go, which
// already marshals to standard lowercase JSON Schema types.
func anySchemaToMap(s any) map[string]any {
	if s == nil {
		return nil
	}
	if m, ok := s.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil
	}
	return m
}

// SchemaToMap converts a genai.Schema into a JSON-Schema map suitable for
// OpenAI/Anthropic tool declarations and task.json.
//
// genai.Type uses Google-style uppercase values (STRING, BOOLEAN, OBJECT, …).
// OpenAI-compatible servers (including llama.cpp) expect standard JSON Schema
// lowercase types (string, boolean, object, …). We always emit lowercase.
func SchemaToMap(s *genai.Schema) map[string]any {
	if s == nil {
		return nil
	}
	m := map[string]any{}
	if t := jsonSchemaType(s.Type); t != "" {
		m["type"] = t
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if s.Format != "" {
		m["format"] = s.Format
	}
	if s.Nullable != nil && *s.Nullable {
		m["nullable"] = true
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Items != nil {
		m["items"] = SchemaToMap(s.Items)
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for k, v := range s.Properties {
			props[k] = SchemaToMap(v)
		}
		m["properties"] = props
	}
	return m
}

// jsonSchemaType maps genai.Type (often uppercase) to JSON Schema type strings.
func jsonSchemaType(t genai.Type) string {
	switch strings.ToUpper(string(t)) {
	case "STRING", "TYPE_STRING":
		return "string"
	case "NUMBER", "TYPE_NUMBER":
		return "number"
	case "INTEGER", "TYPE_INTEGER":
		return "integer"
	case "BOOLEAN", "TYPE_BOOLEAN":
		return "boolean"
	case "ARRAY", "TYPE_ARRAY":
		return "array"
	case "OBJECT", "TYPE_OBJECT":
		return "object"
	case "NULL", "TYPE_NULL":
		return "null"
	case "":
		return ""
	default:
		// Already lowercase or unknown — pass through lowercased.
		return strings.ToLower(string(t))
	}
}
