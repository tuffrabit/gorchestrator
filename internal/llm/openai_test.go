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
