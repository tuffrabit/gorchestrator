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
			Choices: []struct {
				Message struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			}{
				{
					Message: struct {
						Role      string `json:"role"`
						Content   string `json:"content"`
						ToolCalls []struct {
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					}{
						Role:    "assistant",
						Content: "Hello from OpenAI",
						ToolCalls: []struct {
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						}{
							{
								ID:   "call_1",
								Type: "function",
								Function: struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								}{
									Name:      "read_file",
									Arguments: `{"path":"test.go"}`,
								},
							},
						},
					},
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
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
