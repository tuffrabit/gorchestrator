package llm

import (
	"strings"

	"google.golang.org/genai"
)

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
