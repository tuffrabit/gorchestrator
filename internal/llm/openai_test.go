package llm

import (
	"context"
	"iter"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestNewOpenAIModel(t *testing.T) {
	m, err := NewOpenAIModel("gpt-4o-mini", "", "http://127.0.0.1:1/v1", 0)
	if err != nil {
		t.Fatalf("NewOpenAIModel: %v", err)
	}
	if m.Name() != "gpt-4o-mini" {
		t.Fatalf("Name() = %q, want gpt-4o-mini", m.Name())
	}
}

func TestNewOpenAIModel_EmptyModelName(t *testing.T) {
	if _, err := NewOpenAIModel("", "", "", 0); err == nil {
		t.Fatal("expected an error for an empty model name")
	}
}

func TestWithGenerationConfig(t *testing.T) {
	temp := 0.7
	tools := []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "finish_task",
			ParametersJsonSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}},
	}}
	original := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)},
		Config:   &genai.GenerateContentConfig{Tools: tools},
	}

	cloned := withGenerationConfig(original, &temp, 2048)

	if cloned == original {
		t.Fatal("withGenerationConfig returned the original request")
	}
	if cloned.Config == original.Config {
		t.Fatal("withGenerationConfig shared the original config")
	}
	if cloned.Config.Temperature == nil || *cloned.Config.Temperature != float32(temp) {
		t.Fatalf("Temperature = %v, want %v", cloned.Config.Temperature, temp)
	}
	if cloned.Config.MaxOutputTokens != 2048 {
		t.Fatalf("MaxOutputTokens = %d, want 2048", cloned.Config.MaxOutputTokens)
	}
	if len(cloned.Config.Tools) != 1 || cloned.Config.Tools[0] != tools[0] {
		t.Fatal("withGenerationConfig dropped the request's tools")
	}

	// The caller's request must be untouched: agents reuse LLMRequest configs
	// across calls.
	if original.Config.Temperature != nil {
		t.Fatalf("original config Temperature = %v, want nil", original.Config.Temperature)
	}
	if original.Config.MaxOutputTokens != 0 {
		t.Fatalf("original config MaxOutputTokens = %d, want 0", original.Config.MaxOutputTokens)
	}
}

func TestWithGenerationConfig_NilConfig(t *testing.T) {
	temp := 0.2
	original := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)},
	}
	cloned := withGenerationConfig(original, &temp, 0)
	if cloned.Config == nil {
		t.Fatal("withGenerationConfig left Config nil")
	}
	if cloned.Config.Temperature == nil || *cloned.Config.Temperature != float32(temp) {
		t.Fatalf("Temperature = %v, want %v", cloned.Config.Temperature, temp)
	}
	if cloned.Config.MaxOutputTokens != 0 {
		t.Fatalf("MaxOutputTokens = %d, want 0", cloned.Config.MaxOutputTokens)
	}
}

func TestOpenAIModel_GenerateContent_Delegates(t *testing.T) {
	// The plugin owns translation/streaming; this only pins that GenerateContent
	// runs through the wrapped model and injects configured parameters.
	inner := &fakePluginModel{resp: &model.LLMResponse{
		Content:      genai.NewContentFromText("ok", genai.RoleModel),
		TurnComplete: true,
	}}
	temp := 0.5
	m := &openAIModel{inner: inner, temperature: &temp, maxTokens: 100}

	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}
	var got *model.LLMResponse
	for resp, err := range m.GenerateContent(t.Context(), req, true) {
		if err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		got = resp
	}
	if got != inner.resp {
		t.Fatalf("response = %+v, want the inner model's response", got)
	}
	if inner.lastReq == nil {
		t.Fatal("inner model received no request")
	}
	if inner.lastReq.Config == nil || inner.lastReq.Config.Temperature == nil {
		t.Fatalf("inner request Config = %+v, want injected temperature", inner.lastReq.Config)
	}
	if inner.lastReq.Config.MaxOutputTokens != 100 {
		t.Fatalf("MaxOutputTokens = %d, want 100", inner.lastReq.Config.MaxOutputTokens)
	}
	if req.Config != nil {
		t.Fatalf("caller request Config = %+v, want nil (unchanged)", req.Config)
	}
}

type fakePluginModel struct {
	resp    *model.LLMResponse
	lastReq *model.LLMRequest
}

func (f *fakePluginModel) Name() string { return "fake" }

func (f *fakePluginModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	f.lastReq = req
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(f.resp, nil)
	}
}
