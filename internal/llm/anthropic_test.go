package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/model"
)

func TestAnthropicModel_Translation(t *testing.T) {
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotRequest); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}

		if r.URL.Path != "/messages" {
			t.Fatalf("path = %q, want /messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("missing anthropic-version header")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Hello from Claude"},
				{"type": "tool_use", "id": "tu_1", "name": "write_file", "input": map[string]any{"path": "main.go"}},
			},
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		})
	}))
	defer server.Close()

	m := NewAnthropicModel("claude-3-5-sonnet-20241022", "", server.URL+"/", 0)
	// Override apiKey after construction for the test.
	if am, ok := m.(*AnthropicModel); ok {
		am.apiKey = "test-key"
	}

	req := &model.LLMRequest{
		Model: "claude-3-5-sonnet-20241022",
		Contents: []*genai.Content{
			genai.NewContentFromText("Issue: add auth", genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("You are a coder.", genai.RoleUser),
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "write_file",
					Description: "Write a file",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"path":    {Type: genai.TypeString},
							"content": {Type: genai.TypeString},
						},
					},
				}},
			}},
		},
	}

	var resp *model.LLMResponse
	for r, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("generate content: %v", err)
		}
		resp = r
	}
	if resp == nil {
		t.Fatal("no response")
	}

	if gotRequest["model"] != "claude-3-5-sonnet-20241022" {
		t.Fatalf("model = %v, want claude-3-5-sonnet-20241022", gotRequest["model"])
	}
	if gotRequest["system"] != "You are a coder." {
		t.Fatalf("system = %v", gotRequest["system"])
	}
	messages, ok := gotRequest["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v", gotRequest["messages"])
	}

	tools, ok := gotRequest["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", gotRequest["tools"])
	}

	if len(resp.Content.Parts) != 2 {
		t.Fatalf("response parts = %d, want 2", len(resp.Content.Parts))
	}
	if resp.Content.Parts[0].Text != "Hello from Claude" {
		t.Fatalf("text = %q", resp.Content.Parts[0].Text)
	}
	if resp.Content.Parts[1].FunctionCall == nil {
		t.Fatal("expected function call part")
	}
	if resp.Content.Parts[1].FunctionCall.Name != "write_file" {
		t.Fatalf("function name = %q", resp.Content.Parts[1].FunctionCall.Name)
	}
	if resp.UsageMetadata.TotalTokenCount != 15 {
		t.Fatalf("total tokens = %d, want 15", resp.UsageMetadata.TotalTokenCount)
	}
}

func TestAnthropicModel_Retry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{"error": "rate limited"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()

	m := NewAnthropicModel("claude", "", server.URL+"/", 0)
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)},
	}
	for _, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("generate content: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
