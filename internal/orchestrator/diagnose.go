package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// diagnoseEventsTail bounds how many trailing events.jsonl records the
// failure heuristics inspect.
const diagnoseEventsTail = 50

// diagnoseFailure infers a short, human-readable cause for a phase failure
// from the error itself and, when that is inconclusive, from the tail of the
// phase's events.jsonl (the most recent model request/response turns). It
// returns "" when nothing can be inferred — callers then show the raw error.
// cause may be nil (e.g. adjudication-driven failures), in which case only
// the event tail is consulted.
func diagnoseFailure(ctx context.Context, store storage.Port, eventsPath string, cause error) string {
	if cause != nil {
		if llm.IsBudgetExceeded(cause) {
			return "token budget exceeded (session ceiling hit)"
		}
		errText := strings.ToLower(cause.Error())
		for _, sig := range []string{
			"context length", "context window", "maximum context",
			"prompt is too long", "too many tokens", "max_tokens",
			"token limit", "reduce the length",
		} {
			if strings.Contains(errText, sig) {
				return "token limit reached or context window exhausted"
			}
		}
		if strings.Contains(errText, "produced empty output") || strings.Contains(errText, "empty response") {
			return "empty response from model"
		}
		if strings.Contains(errText, "context deadline exceeded") || strings.Contains(errText, "client.timeout") {
			return "model request timed out"
		}
	}

	// Error text alone is inconclusive: inspect the tail of the phase's
	// event log (the most recent model turns and tool calls).
	if diag := diagnoseFromEvents(ctx, store, eventsPath); diag != "" {
		return diag
	}
	if cause != nil && strings.Contains(cause.Error(), "did not call finish_task") {
		return "no finish_task call (incomplete turn)"
	}
	return ""
}

// withDiagnosis prefixes a failure detail with an inferred cause label, e.g.
// "failed: empty response from model: <detail>". detail is returned unchanged
// when diag is empty.
func withDiagnosis(diag, detail string) string {
	if diag == "" {
		return detail
	}
	return "failed: " + diag + ": " + detail
}

// diagnoseFromEvents applies event-tail heuristics: an empty final model
// turn, or the same tool call repeated over and over (reasoning loop).
func diagnoseFromEvents(ctx context.Context, store storage.Port, eventsPath string) string {
	events := readEventsTail(ctx, store, eventsPath, diagnoseEventsTail)
	if len(events) == 0 {
		return ""
	}
	// events are in reverse chronological order (most recent first).
	for i, ev := range events {
		if ev.Type != "model_turn" {
			continue
		}
		if strings.TrimSpace(ev.Content) != "" {
			break
		}
		// Empty model turn: an empty final response unless a tool call
		// followed it (smaller index = more recent).
		toolAfter := false
		for _, later := range events[:i] {
			if later.Type == "tool_call" {
				toolAfter = true
				break
			}
		}
		if !toolAfter {
			return "empty response from model"
		}
		break
	}
	if name, ok := repeatedToolCall(events); ok {
		return fmt.Sprintf("model reasoning loop (repeated tool call: %s)", name)
	}
	return ""
}

// repeatedToolCall reports whether the most recent run of consecutive
// tool_call events (ignoring other event types) repeats the same name+args
// at least 3 times — the signature of a model stuck in a reasoning loop.
func repeatedToolCall(events []eventRecord) (string, bool) {
	const minRepeats = 3
	var calls []eventRecord
	for _, ev := range events {
		if ev.Type == "tool_call" {
			calls = append(calls, ev)
		}
	}
	if len(calls) < minRepeats {
		return "", false
	}
	key := toolCallKey(calls[0])
	if key == "" {
		return "", false
	}
	n := 1
	for _, c := range calls[1:] {
		if toolCallKey(c) != key {
			break
		}
		n++
	}
	if n < minRepeats {
		return "", false
	}
	name, _ := calls[0].ToolCall["name"].(string)
	return name, true
}

// toolCallKey identifies a tool call by name+args (the per-call id varies
// even when the model repeats the exact same call).
func toolCallKey(ev eventRecord) string {
	if ev.ToolCall == nil {
		return ""
	}
	b, err := json.Marshal(map[string]any{
		"name": ev.ToolCall["name"],
		"args": ev.ToolCall["args"],
	})
	if err != nil {
		return ""
	}
	return string(b)
}

// lastPhaseError returns the Error of the most recent phase_error event in
// the phase's events.jsonl, or "" — used to surface hard phase failures
// (which never wrote a result.json) on the dashboard.
func lastPhaseError(ctx context.Context, store storage.Port, eventsPath string) string {
	data, err := store.Read(ctx, eventsPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var ev eventRecord
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Type == "phase_error" && ev.Error != "" {
			return ev.Error
		}
	}
	return ""
}

// readEventsTail parses up to n trailing event records from events.jsonl,
// most recent first. Best-effort: unreadable files and unparseable lines
// are skipped.
func readEventsTail(ctx context.Context, store storage.Port, eventsPath string, n int) []eventRecord {
	if store == nil || eventsPath == "" {
		return nil
	}
	data, err := store.Read(ctx, eventsPath)
	if err != nil || len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	events := make([]eventRecord, 0, n)
	for i := len(lines) - 1; i >= 0 && len(events) < n; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var ev eventRecord
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events
}
