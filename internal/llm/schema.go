package llm

import "google.golang.org/genai"

// SchemaToMap converts a genai.Schema into a JSON-Schema-like map suitable for
// serializing tool declarations to task.json and for OpenAI request bodies.
func SchemaToMap(s *genai.Schema) map[string]any {
	if s == nil {
		return nil
	}
	m := map[string]any{
		"type": s.Type,
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
