package llm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

// This file recovers chain-of-thought that the OpenAI provider path otherwise
// loses. Reasoning-parser servers (llama-swap, llama.cpp --reasoning-format,
// DeepSeek, ...) stream the model's reasoning in chat.completion.chunk deltas
// as "reasoning_content", a field openai-go's generated types do not carry and
// the ADK openaimodel plugin therefore never translates (its stream and
// blocking converters map only content/refusal/tool_calls). The recovery works
// at the HTTP layer: reasoningTransport tees the SSE response body and
// extracts reasoning_content itself, and openAIModel.GenerateContent forwards
// the collected text as thought parts on synthesized partial responses — the
// same shape Gemini/Anthropic thought parts already have downstream, so no
// orchestrator code needs to know about it.
//
// The collector for one model call travels in the call's context: the wrapper
// attaches it before invoking the plugin, and the plugin and openai-go keep
// context values on the request (http.NewRequestWithContext), so the
// transport sees exactly the collector belonging to the in-flight call. This
// stays correct under concurrent calls sharing one model instance.
//
// Delete this file when upstream (openai-go or the ADK plugin) learns to map
// reasoning_content onto genai thought parts; the capture_reasoning flag then
// becomes a no-op.

// reasoningCollectorContextKey is the context key under which one model call's
// reasoningCollector travels from openAIModel.GenerateContent to the
// transport.
type reasoningCollectorContextKey struct{}

// reasoningCollector accumulates the reasoning_content deltas of ONE model
// call. The transport appends as chunks arrive; the model wrapper drains.
type reasoningCollector struct {
	mu  sync.Mutex
	buf []string
}

// add buffers one delta. Empty deltas are dropped: providers emit them as
// structural noise (role-only chunks, keepalives).
func (c *reasoningCollector) add(delta string) {
	if delta == "" {
		return
	}
	c.mu.Lock()
	c.buf = append(c.buf, delta)
	c.mu.Unlock()
}

// drain returns everything buffered since the last drain and clears it.
func (c *reasoningCollector) drain() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buf) == 0 {
		return nil
	}
	out := c.buf
	c.buf = nil
	return out
}

// reasoningTransport wraps an http.RoundTripper. For SSE responses whose
// request context carries a reasoningCollector, it scans the streamed chunks
// for reasoning_content and feeds the collector; the body is forwarded
// untouched. Requests without a collector, and non-SSE responses, pass
// through unmodified.
type reasoningTransport struct {
	next http.RoundTripper
}

func (t *reasoningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	col, _ := req.Context().Value(reasoningCollectorContextKey{}).(*reasoningCollector)
	if col == nil {
		return resp, nil
	}
	if ct := resp.Header.Get("Content-Type"); !bytes.HasPrefix([]byte(ct), []byte("text/event-stream")) {
		return resp, nil
	}
	resp.Body = &reasoningSniffingBody{body: resp.Body, col: col}
	return resp, nil
}

var (
	sseDataPrefix     = []byte("data:")
	sseDone           = []byte("[DONE]")
	sseLineFeed       = []byte("\n")
	sseCarriageReturn = []byte("\r")
)

// reasoningSniffingBody forwards the stream body to the SDK while parsing it
// as server-sent events on the way through. Parsing rides on Read calls — no
// extra goroutine, so backpressure and http.Client.Timeout semantics are
// unchanged. Events are assembled per the SSE spec (data lines joined until a
// blank line); OpenAI-compatible servers emit one JSON payload per event.
type reasoningSniffingBody struct {
	body    io.ReadCloser
	col     *reasoningCollector
	line    []byte // partial line not yet terminated by \n
	event   []byte // data lines of the event not yet dispatched
	flushed bool
}

func (b *reasoningSniffingBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.feed(p[:n])
	}
	if err != nil {
		// EOF or a mid-stream error: process whatever the final read carried
		// so reasoning just before a truncated end is not lost.
		b.flush()
	}
	return n, err
}

func (b *reasoningSniffingBody) Close() error {
	b.flush()
	return b.body.Close()
}

func (b *reasoningSniffingBody) feed(chunk []byte) {
	b.line = append(b.line, chunk...)
	for {
		i := bytes.IndexByte(b.line, '\n')
		if i < 0 {
			return
		}
		b.handleLine(b.line[:i])
		b.line = b.line[i+1:]
	}
}

// flush processes the bytes left without a trailing newline (final event with
// no blank-line terminator, or a truncated stream).
func (b *reasoningSniffingBody) flush() {
	if b.flushed {
		return
	}
	b.flushed = true
	if len(b.line) > 0 {
		b.handleLine(b.line)
		b.line = nil
	}
	b.dispatch()
}

func (b *reasoningSniffingBody) handleLine(line []byte) {
	line = bytes.TrimSuffix(line, sseCarriageReturn)
	if len(line) == 0 {
		b.dispatch()
		return
	}
	if !bytes.HasPrefix(line, sseDataPrefix) {
		// Comments and event:/id:/retry: fields carry no payload.
		return
	}
	payload := line[len(sseDataPrefix):]
	if len(payload) > 0 && payload[0] == ' ' {
		payload = payload[1:]
	}
	b.event = append(b.event, payload...)
	b.event = append(b.event, sseLineFeed...)
}

// dispatch parses one assembled event. Anything that is not a JSON chunk with
// a non-empty reasoning_content delta — content deltas, role-only chunks,
// usage, [DONE], keepalive comments, malformed lines — is ignored; the SDK
// parses the same bytes for its own purposes and owns error handling there.
func (b *reasoningSniffingBody) dispatch() {
	data := bytes.TrimSuffix(b.event, sseLineFeed)
	b.event = b.event[:0]
	if len(data) == 0 || bytes.Equal(data, sseDone) {
		return
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.ReasoningContent != "" {
			b.col.add(choice.Delta.ReasoningContent)
		}
	}
}
