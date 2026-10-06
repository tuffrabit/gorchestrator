package llm

import (
	"context"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// reasoningSSEFixture mirrors what llama-swap / a reasoning-parser server
// streams for one turn: reasoning deltas first, then content, then a usage
// chunk and [DONE].
const reasoningSSEFixture = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me"}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":" think."}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}

data: [DONE]

`

type stubRoundTripper struct {
	resp *http.Response
	err  error
}

func (s *stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return s.resp, s.err
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReasoningTransport_ExtractsReasoningDeltas(t *testing.T) {
	tr := &reasoningTransport{next: &stubRoundTripper{resp: sseResponse(reasoningSSEFixture)}}
	col := &reasoningCollector{}
	ctx := context.WithValue(t.Context(), reasoningCollectorContextKey{}, col)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:1/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if string(body) != reasoningSSEFixture {
		t.Fatal("response body was not forwarded byte-for-byte")
	}

	deltas := col.drain()
	if got := strings.Join(deltas, ""); got != "Let me think." {
		t.Fatalf("collected reasoning = %q, want %q", got, "Let me think.")
	}
	if col.drain() != nil {
		t.Fatal("second drain returned data; want empty")
	}
}

func TestReasoningTransport_NoCollectorPassesThrough(t *testing.T) {
	tr := &reasoningTransport{next: &stubRoundTripper{resp: sseResponse(reasoningSSEFixture)}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:1/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != reasoningSSEFixture {
		t.Fatal("response body was not forwarded byte-for-byte")
	}
}

func TestReasoningTransport_NonSSENotScanned(t *testing.T) {
	tr := &reasoningTransport{next: &stubRoundTripper{resp: &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"reasoning_content":"hidden"}`)),
	}}}
	col := &reasoningCollector{}
	ctx := context.WithValue(t.Context(), reasoningCollectorContextKey{}, col)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:1/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if col.drain() != nil {
		t.Fatal("non-SSE response was scanned; want nothing collected")
	}
}

func TestReasoningTransport_IgnoresNoise(t *testing.T) {
	body := `: keepalive comment

event: message

data: {invalid json

data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":""}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"real"}}]}

data: [DONE]

data:
`
	tr := &reasoningTransport{next: &stubRoundTripper{resp: sseResponse(body)}}
	col := &reasoningCollector{}
	ctx := context.WithValue(t.Context(), reasoningCollectorContextKey{}, col)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:1/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := strings.Join(col.drain(), ""); got != "real" {
		t.Fatalf("collected reasoning = %q, want %q", got, "real")
	}
}

// tinyReader caps how much one Read returns, forcing the line assembler to
// operate across arbitrary chunk boundaries.
type tinyReader struct {
	r   io.Reader
	max int
}

func (t *tinyReader) Read(p []byte) (int, error) {
	if len(p) > t.max {
		p = p[:t.max]
	}
	return t.r.Read(p)
}

func TestReasoningSniffingBody_ChunkBoundaries(t *testing.T) {
	col := &reasoningCollector{}
	b := &reasoningSniffingBody{
		body: io.NopCloser(&tinyReader{r: strings.NewReader(reasoningSSEFixture), max: 2}),
		col:  col,
	}
	body, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != reasoningSSEFixture {
		t.Fatal("response body was not forwarded byte-for-byte")
	}
	if got := strings.Join(col.drain(), ""); got != "Let me think." {
		t.Fatalf("collected reasoning = %q, want %q", got, "Let me think.")
	}
}

// scriptedModel runs fn against the collector (if any) its GenerateContent
// context carries, so tests can interleave collector feeds with yields.
type scriptedModel struct {
	fn           func(col *reasoningCollector) iter.Seq2[*model.LLMResponse, error]
	sawCollector bool
}

func (s *scriptedModel) Name() string { return "scripted" }

func (s *scriptedModel) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	col, _ := ctx.Value(reasoningCollectorContextKey{}).(*reasoningCollector)
	s.sawCollector = col != nil
	return s.fn(col)
}

func collectResponses(seq iter.Seq2[*model.LLMResponse, error]) ([]*model.LLMResponse, error) {
	var out []*model.LLMResponse
	for resp, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, resp)
	}
	return out, nil
}

func textPartial(s string) *model.LLMResponse {
	return &model.LLMResponse{
		Partial: true,
		Content: &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Text: s}},
		},
	}
}

func TestOpenAIModel_GenerateContent_CaptureReasoning(t *testing.T) {
	final := &model.LLMResponse{
		Content:      genai.NewContentFromText("Hi", genai.RoleModel),
		TurnComplete: true,
	}
	inner := &scriptedModel{fn: func(col *reasoningCollector) iter.Seq2[*model.LLMResponse, error] {
		col.add("Let me think. ")
		yieldPart := textPartial("Hel")
		yieldPart2 := textPartial("lo")
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(yieldPart, nil)
			yield(yieldPart2, nil)
			col.add("One more thought.")
			yield(final, nil)
		}
	}}
	m := &openAIModel{inner: inner, captureReasoning: true}

	got, err := collectResponses(m.GenerateContent(t.Context(), &model.LLMRequest{}, true))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if !inner.sawCollector {
		t.Fatal("inner model context carried no reasoning collector")
	}
	if len(got) != 5 {
		t.Fatalf("got %d responses, want 5", len(got))
	}

	// Reasoning fed before the first inner yield leads; reasoning fed between
	// yields lands before the next response; nothing is duplicated.
	wantThoughts := map[int]string{0: "Let me think. ", 3: "One more thought."}
	for i, resp := range got {
		want, isThought := wantThoughts[i]
		if !isThought {
			if resp.Partial {
				continue // inner content partials
			}
			if resp != final {
				t.Fatalf("response %d is not the inner final response", i)
			}
			continue
		}
		if !resp.Partial {
			t.Fatalf("thought response %d: Partial = false", i)
		}
		if resp.TurnComplete {
			t.Fatalf("thought response %d: TurnComplete = true", i)
		}
		if resp.Content == nil || resp.Content.Role != genai.RoleModel || len(resp.Content.Parts) != 1 {
			t.Fatalf("thought response %d has wrong content shape: %+v", i, resp.Content)
		}
		p := resp.Content.Parts[0]
		if !p.Thought || p.Text != want {
			t.Fatalf("thought response %d = {Thought:%v Text:%q}, want {true %q}", i, p.Thought, p.Text, want)
		}
	}
}

func TestOpenAIModel_GenerateContent_CaptureReasoningDrainsAfterEnd(t *testing.T) {
	// Reasoning fed concurrently with the last inner yield must still land:
	// the wrapper drains once more after the iterator ends.
	final := &model.LLMResponse{
		Content:      genai.NewContentFromText("Hi", genai.RoleModel),
		TurnComplete: true,
	}
	inner := &scriptedModel{fn: func(col *reasoningCollector) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(final, nil)
			col.add("trailing")
		}
	}}
	m := &openAIModel{inner: inner, captureReasoning: true}

	got, err := collectResponses(m.GenerateContent(t.Context(), &model.LLMRequest{}, true))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d responses, want 2", len(got))
	}
	if got[0] != final {
		t.Fatal("response 0 is not the inner final response")
	}
	p := got[1].Content.Parts[0]
	if !got[1].Partial || !p.Thought || p.Text != "trailing" {
		t.Fatalf("trailing thought = {Partial:%v Thought:%v Text:%q}", got[1].Partial, p.Thought, p.Text)
	}
}

func TestOpenAIModel_GenerateContent_CaptureReasoningDisabled(t *testing.T) {
	final := &model.LLMResponse{
		Content:      genai.NewContentFromText("ok", genai.RoleModel),
		TurnComplete: true,
	}
	inner := &scriptedModel{fn: func(col *reasoningCollector) iter.Seq2[*model.LLMResponse, error] {
		if col != nil {
			t.Error("inner context carried a collector with captureReasoning off")
		}
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(final, nil)
		}
	}}
	m := &openAIModel{inner: inner, captureReasoning: false}

	got, err := collectResponses(m.GenerateContent(t.Context(), &model.LLMRequest{}, true))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if len(got) != 1 || got[0] != final {
		t.Fatalf("responses = %+v, want exactly the inner response", got)
	}
}

func TestOpenAIModel_GenerateContent_CaptureReasoningNonStreaming(t *testing.T) {
	final := &model.LLMResponse{
		Content:      genai.NewContentFromText("ok", genai.RoleModel),
		TurnComplete: true,
	}
	inner := &scriptedModel{fn: func(col *reasoningCollector) iter.Seq2[*model.LLMResponse, error] {
		if col != nil {
			t.Error("inner context carried a collector on a non-streamed call")
		}
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(final, nil)
		}
	}}
	m := &openAIModel{inner: inner, captureReasoning: true}

	got, err := collectResponses(m.GenerateContent(t.Context(), &model.LLMRequest{}, false))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if len(got) != 1 || got[0] != final {
		t.Fatalf("responses = %+v, want exactly the inner response", got)
	}
}

// TestOpenAIModel_CaptureReasoning_EndToEnd runs the real ADK plugin and
// openai-go against a stub reasoning-parser server: it pins the context-value
// chain that carries the collector from the wrapper through the plugin's HTTP
// request, and that recovered reasoning arrives as thought partials while the
// plugin's own text/usage handling is untouched.
func TestOpenAIModel_CaptureReasoning_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		write := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		write(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"reasoning-test","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me"}}]}`)
		write(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"reasoning-test","choices":[{"index":0,"delta":{"reasoning_content":" think."}}]}`)
		write(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"reasoning-test","choices":[{"index":0,"delta":{"content":"Hi"}}]}`)
		write(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"reasoning-test","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	run := func(capture bool) []*model.LLMResponse {
		t.Helper()
		m, err := NewOpenAIModelWithOptions("reasoning-test", "", srv.URL, 5*time.Second, nil, 0, capture)
		if err != nil {
			t.Fatalf("NewOpenAIModelWithOptions: %v", err)
		}
		req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}
		got, err := collectResponses(m.GenerateContent(t.Context(), req, true))
		if err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		return got
	}

	got := run(true)

	var thoughts []string
	var texts []string
	var final *model.LLMResponse
	for _, resp := range got {
		if resp.Content == nil {
			continue
		}
		for _, p := range resp.Content.Parts {
			if p == nil {
				continue
			}
			if p.Thought {
				thoughts = append(thoughts, p.Text)
				if !resp.Partial {
					t.Fatal("thought part on a non-partial response")
				}
				continue
			}
			if p.Text != "" && resp.Partial {
				texts = append(texts, p.Text)
			}
		}
		if resp.TurnComplete {
			final = resp
		}
	}
	if len(thoughts) != 1 || thoughts[0] != "Let me think." {
		t.Fatalf("thoughts = %v, want [\"Let me think.\"]", thoughts)
	}
	if len(texts) != 1 || texts[0] != "Hi" {
		t.Fatalf("streamed texts = %v, want [\"Hi\"]", texts)
	}
	if final == nil {
		t.Fatal("no TurnComplete response")
	}
	if final.Partial {
		t.Fatal("final response is partial")
	}
	if got := final.Content.Parts[0].Text; got != "Hi" {
		t.Fatalf("final text = %q, want Hi", got)
	}
	if final.UsageMetadata == nil || final.UsageMetadata.TotalTokenCount != 8 {
		t.Fatalf("final usage = %+v, want total 8", final.UsageMetadata)
	}

	// The recovered thought must lead the first streamed text, matching the
	// emission order of the wire stream.
	firstThought := -1
	firstText := -1
	for i, resp := range got {
		if resp.Content == nil {
			continue
		}
		for _, p := range resp.Content.Parts {
			if p == nil {
				continue
			}
			if p.Thought && firstThought == -1 {
				firstThought = i
			}
			if p.Text != "" && !p.Thought && resp.Partial && firstText == -1 {
				firstText = i
			}
		}
	}
	if firstThought == -1 || firstText == -1 || firstThought >= firstText {
		t.Fatalf("thought index %d, text index %d; want thought first", firstThought, firstText)
	}

	// Flag off: the plugin runs as it always has — no thought parts anywhere.
	for _, resp := range run(false) {
		if resp.Content == nil {
			continue
		}
		for _, p := range resp.Content.Parts {
			if p != nil && p.Thought {
				t.Fatalf("captureReasoning off yielded a thought part: %+v", p)
			}
		}
	}
}
