package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/model"
)

func TestOpenAIModel_GenerateContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("parse request body: %v", err)
		}

		if req["model"] != "gpt-4o-mini" {
			t.Fatalf("model = %v, want gpt-4o-mini", req["model"])
		}

		resp := openAIChatResponse{
			Choices: []choice{
				{
					Message: message{
						Role:    "assistant",
						Content: "Hello from OpenAI",
						ToolCalls: []toolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: function{
									Name:      "read_file",
									Arguments: `{"path":"test.go"}`,
								},
							},
						},
					},
				},
			},
			Usage: usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	m := NewOpenAIModel("gpt-4o-mini", "", server.URL, 0)
	if m.Name() != "gpt-4o-mini" {
		t.Fatalf("Name() = %q, want gpt-4o-mini", m.Name())
	}

	req := &model.LLMRequest{
		Model: "gpt-4o-mini",
		Contents: []*genai.Content{
			genai.NewContentFromText("Say hello", genai.RoleUser),
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("GenerateContent error: %v", err)
		}
		responses = append(responses, resp)
	}
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}

	resp := responses[0]
	if resp.Content == nil {
		t.Fatal("response content is nil")
	}
	if len(resp.Content.Parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(resp.Content.Parts))
	}

	textPart := resp.Content.Parts[0]
	if textPart.Text != "Hello from OpenAI" {
		t.Fatalf("text = %q, want Hello from OpenAI", textPart.Text)
	}

	fcPart := resp.Content.Parts[1]
	if fcPart.FunctionCall == nil {
		t.Fatal("expected function call part")
	}
	if fcPart.FunctionCall.Name != "read_file" {
		t.Fatalf("function name = %q, want read_file", fcPart.FunctionCall.Name)
	}
	if fcPart.FunctionCall.Args["path"] != "test.go" {
		t.Fatalf("function args = %v, want path=test.go", fcPart.FunctionCall.Args)
	}

	if resp.UsageMetadata == nil {
		t.Fatal("usage metadata is nil")
	}
	if resp.UsageMetadata.TotalTokenCount != 15 {
		t.Fatalf("total tokens = %d, want 15", resp.UsageMetadata.TotalTokenCount)
	}
}

func TestOpenAIModel_ToolSchemaUsesJSONSchemaTypes(t *testing.T) {
	// llama.cpp and strict OpenAI-compatible servers reject Google-style
	// uppercase types (BOOLEAN/STRING) in tool parameters.
	var sawTools atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		tools, _ := req["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("expected tools in request")
		}
		tool := tools[0].(map[string]any)
		fn := tool["function"].(map[string]any)
		params := fn["parameters"].(map[string]any)
		if params["type"] != "object" {
			t.Fatalf("parameters.type = %v, want object", params["type"])
		}
		props := params["properties"].(map[string]any)
		done := props["done"].(map[string]any)
		if done["type"] != "boolean" {
			t.Fatalf("done.type = %v, want boolean", done["type"])
		}
		rat := props["rationale"].(map[string]any)
		if rat["type"] != "string" {
			t.Fatalf("rationale.type = %v, want string", rat["type"])
		}
		sawTools.Store(true)

		_ = json.NewEncoder(w).Encode(openAIChatResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer server.Close()

	m := NewOpenAIModel("local-model", "", server.URL, 0)
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name: "finish_task",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"done":      {Type: genai.TypeBoolean, Description: "done?"},
							"rationale": {Type: genai.TypeString, Description: "why"},
						},
					},
				}},
			}},
		},
	}
	for _, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !sawTools.Load() {
		t.Fatal("server did not receive tools")
	}
}

func TestOpenAIModel_ToolSchemaFromParametersJsonSchema(t *testing.T) {
	// ADK functiontool.New sets ParametersJsonSchema, not Parameters.
	// Without reading it, tools ship with empty properties and llama.cpp
	// models loop calling list_directory with {}.
	var sawTools atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		tools, _ := req["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tools len = %d", len(tools))
		}
		fn := tools[0].(map[string]any)["function"].(map[string]any)
		if fn["name"] != "list_directory" {
			t.Fatalf("name = %v", fn["name"])
		}
		params := fn["parameters"].(map[string]any)
		props, ok := params["properties"].(map[string]any)
		if !ok || props["path"] == nil {
			t.Fatalf("parameters must include path property, got %#v", params)
		}
		path := props["path"].(map[string]any)
		if path["type"] != "string" {
			t.Fatalf("path.type = %v", path["type"])
		}
		sawTools.Store(true)
		_ = json.NewEncoder(w).Encode(openAIChatResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer server.Close()

	m := NewOpenAIModel("local-model", "", server.URL, 0)
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("list root", genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "list_directory",
					Description: "List a directory",
					// Parameters intentionally nil — like real ADK function tools.
					ParametersJsonSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{
								"type":        "string",
								"description": "Relative path",
							},
						},
					},
				}},
			}},
		},
	}
	for _, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !sawTools.Load() {
		t.Fatal("server did not receive tools")
	}
}

func TestOpenAIModel_convertContents_ToolRoundTripNoEmptyUser(t *testing.T) {
	// Simulates ADK history: user text → assistant tool_call → user function response only.
	m := &OpenAIModel{model: "local"}
	msgs, err := m.convertContents([]*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "investigate auth"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID:   "call_1",
				Name: "read_file",
				Args: map[string]any{"path": "main.go"},
			},
		}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				ID:   "call_1",
				Name: "read_file",
				Response: map[string]any{
					"content": "package main",
				},
			},
		}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Expect: user, assistant(+tool_calls), tool — no empty user between assistant and tool.
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3: %#v", len(msgs), msgs)
	}
	if msgs[0]["role"] != "user" || msgs[0]["content"] == nil || msgs[0]["content"] == "" {
		t.Fatalf("msg0 = %#v", msgs[0])
	}
	if msgs[1]["role"] != "assistant" {
		t.Fatalf("msg1 role = %v", msgs[1]["role"])
	}
	// content key must be present (even if empty) for strict servers
	if _, ok := msgs[1]["content"]; !ok {
		t.Fatal("assistant tool_calls message missing content key")
	}
	if _, ok := msgs[1]["tool_calls"]; !ok {
		t.Fatal("assistant missing tool_calls")
	}
	if msgs[2]["role"] != "tool" {
		t.Fatalf("msg2 role = %v want tool", msgs[2]["role"])
	}
	if _, ok := msgs[2]["content"]; !ok {
		t.Fatal("tool message missing content")
	}
	// Ensure no role=user without content slipped in.
	for i, msg := range msgs {
		role, _ := msg["role"].(string)
		if role == "assistant" {
			continue
		}
		if _, ok := msg["content"]; !ok {
			t.Fatalf("msg[%d] role=%s missing content: %#v", i, role, msg)
		}
	}
}

func TestOpenAIModel_GenerateContent_RetryAfter429(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		if count == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "rate limited"})
			return
		}

		resp := openAIChatResponse{
			Choices: []choice{
				{
					Message: message{
						Role:    "assistant",
						Content: "Retry succeeded",
					},
				},
			},
			Usage: usage{
				TotalTokens: 5,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	m := NewOpenAIModel("gpt-4o-mini", "", server.URL, 30*time.Second)
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("Hi", genai.RoleUser),
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("GenerateContent error: %v", err)
		}
		responses = append(responses, resp)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	if responses[0].Content.Parts[0].Text != "Retry succeeded" {
		t.Fatalf("text = %q, want Retry succeeded", responses[0].Content.Parts[0].Text)
	}
}
