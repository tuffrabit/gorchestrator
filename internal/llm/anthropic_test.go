package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
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

func TestAnthropicModel_ThinkingBlocksRoundTrip(t *testing.T) {
	// Extended-thinking responses carry "thinking" blocks. They must surface
	// as Thought parts (never as answer text) and must not be echoed back as
	// text blocks in later requests.
	var echoedThought atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)

		// No text block may carry the private reasoning on the wire.
		if msgs, ok := req["messages"].([]any); ok {
			for _, m := range msgs {
				blocks, _ := m.(map[string]any)["content"].([]any)
				for _, b := range blocks {
					if b.(map[string]any)["type"] == "text" &&
						b.(map[string]any)["text"] == "the user wants a listing, so call list_directory" {
						echoedThought.Store(true)
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "thinking", "thinking": "the user wants a listing, so call list_directory", "signature": "sig"},
				{"type": "text", "text": "Here is the listing."},
				{"type": "tool_use", "id": "tu_1", "name": "list_directory", "input": map[string]any{"path": "."}},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 6},
		})
	}))
	defer server.Close()

	newModel := NewAnthropicModel("claude", "", server.URL+"/", 0)
	am, ok := newModel.(*AnthropicModel)
	if !ok {
		t.Fatalf("model = %T, want *AnthropicModel", newModel)
	}
	am.apiKey = "test"

	// Round 1: a fresh request — the response must surface the thinking.
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("list it", genai.RoleUser)}}
	var resp *model.LLMResponse
	for r, err := range am.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		resp = r
	}
	if resp == nil || len(resp.Content.Parts) != 3 {
		t.Fatalf("parts = %+v, want thought, text, and tool call", resp)
	}
	if !resp.Content.Parts[0].Thought || resp.Content.Parts[0].Text != "the user wants a listing, so call list_directory" {
		t.Fatalf("part 0 = %+v, want a Thought part holding the thinking", resp.Content.Parts[0])
	}
	if resp.Content.Parts[1].Thought || resp.Content.Parts[1].Text != "Here is the listing." {
		t.Fatalf("part 1 = %+v, want the plain answer text", resp.Content.Parts[1])
	}
	if resp.Content.Parts[2].FunctionCall == nil {
		t.Fatalf("part 2 = %+v, want the tool call", resp.Content.Parts[2])
	}

	// Round 2: resend the previous turn as history over the wire. The handler
	// flags it if the private reasoning ever comes back as a text block.
	req2 := &model.LLMRequest{Contents: []*genai.Content{resp.Content, genai.NewContentFromText("thanks", genai.RoleUser)}}
	for _, err := range am.GenerateContent(context.Background(), req2, false) {
		if err != nil {
			t.Fatal(err)
		}
	}

	// The echo check runs in the handler goroutine, so assert the flag here,
	// on the test goroutine.
	if echoedThought.Load() {
		t.Fatal("thought was echoed back as a text block in a later request")
	}

	// The same history, built offline: thought stays out, the answer and the
	// tool_use stay in.
	got, _, err := am.convertContents(req2.Contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	var assistantMsg map[string]any
	for _, m := range got {
		if m["role"] == "assistant" {
			assistantMsg = m
		}
	}
	if assistantMsg == nil {
		t.Fatalf("messages = %+v, want an assistant message", got)
	}
	var blocks []map[string]any
	switch c := assistantMsg["content"].(type) {
	case []any: // wire shape after a JSON round trip
		for _, b := range c {
			blocks = append(blocks, b.(map[string]any))
		}
	case []map[string]any: // built offline, straight from convertContents
		blocks = c
	}
	var sawText, sawToolUse, sawThought bool
	for _, block := range blocks {
		switch block["type"] {
		case "text":
			sawText = block["text"] == "Here is the listing."
		case "tool_use":
			sawToolUse = true
		case "thinking":
			sawThought = true
		}
	}
	if !sawText {
		t.Fatalf("assistant blocks lost the answer: %+v", assistantMsg)
	}
	if !sawToolUse {
		t.Fatalf("assistant blocks lost the tool_use: %+v", assistantMsg)
	}
	if sawThought {
		t.Fatalf("assistant blocks echoed the thinking: %+v", assistantMsg)
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
