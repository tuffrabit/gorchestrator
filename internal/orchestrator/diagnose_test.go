package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func newDiagStore(t *testing.T) storage.Port {
	t.Helper()
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("new fs store: %v", err)
	}
	return store
}

func writeEvents(t *testing.T, store storage.Port, key string, lines ...string) {
	t.Helper()
	data := strings.Join(lines, "\n") + "\n"
	if err := store.Write(context.Background(), key, []byte(data)); err != nil {
		t.Fatalf("write events: %v", err)
	}
}

func TestDiagnoseFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("budget exceeded", func(t *testing.T) {
		store := newDiagStore(t)
		cause := fmt.Errorf("loop 1: %w: spent 100 >= ceiling 100", llm.ErrBudgetExceeded)
		got := diagnoseFailure(ctx, store, "x/events.jsonl", cause)
		if got != "token budget exceeded (session ceiling hit)" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("context window", func(t *testing.T) {
		store := newDiagStore(t)
		cause := errors.New("openai: this model's maximum context length is 8192 tokens")
		got := diagnoseFailure(ctx, store, "x/events.jsonl", cause)
		if got != "token limit reached or context window exhausted" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("empty output error", func(t *testing.T) {
		store := newDiagStore(t)
		cause := errors.New("loop 2 produced empty output")
		got := diagnoseFailure(ctx, store, "x/events.jsonl", cause)
		if got != "empty response from model" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		store := newDiagStore(t)
		cause := errors.New(`Post "http://localhost:8080/v1/chat/completions": context deadline exceeded`)
		got := diagnoseFailure(ctx, store, "x/events.jsonl", cause)
		if got != "model request timed out" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("reasoning loop from events tail", func(t *testing.T) {
		store := newDiagStore(t)
		call := `{"type":"tool_call","tool_call":{"id":"c1","name":"read_file","args":{"path":"main.go"}}}`
		writeEvents(t, store, "x/events.jsonl",
			`{"type":"model_turn","role":"model","content":""}`,
			call,
			`{"type":"tool_result","tool_result":{"id":"c1","name":"read_file"}}`,
			call,
			`{"type":"tool_result","tool_result":{"id":"c1","name":"read_file"}}`,
			call,
		)
		cause := errors.New("loop 1 did not call finish_task")
		got := diagnoseFailure(ctx, store, "x/events.jsonl", cause)
		if got != "model reasoning loop (repeated tool call: read_file)" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("empty final model turn from events tail", func(t *testing.T) {
		store := newDiagStore(t)
		writeEvents(t, store, "x/events.jsonl",
			`{"type":"model_turn","role":"model","content":"some text"}`,
			`{"type":"usage","tokens":100}`,
			`{"type":"model_turn","role":"model","content":""}`,
		)
		cause := errors.New("loop 1 did not call finish_task")
		got := diagnoseFailure(ctx, store, "x/events.jsonl", cause)
		if got != "empty response from model" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("finish_task fallback", func(t *testing.T) {
		store := newDiagStore(t)
		writeEvents(t, store, "x/events.jsonl",
			`{"type":"model_turn","role":"model","content":"done-ish"}`,
		)
		cause := errors.New("loop 1 did not call finish_task")
		got := diagnoseFailure(ctx, store, "x/events.jsonl", cause)
		if got != "no finish_task call (incomplete turn)" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("unknown error, no events", func(t *testing.T) {
		store := newDiagStore(t)
		got := diagnoseFailure(ctx, store, "x/events.jsonl", errors.New("boom"))
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("nil cause with clean tail", func(t *testing.T) {
		store := newDiagStore(t)
		writeEvents(t, store, "x/events.jsonl",
			`{"type":"model_turn","role":"model","content":"all good"}`,
		)
		got := diagnoseFailure(ctx, store, "x/events.jsonl", nil)
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestIssueView_FailureReason(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, project, issue := newManualEngine(t, cfg)
	ctx := context.Background()

	if err := eng.issues.UpdateStatus(issue.ID, sqlite.StatusFailed, "research"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Hard failure (no result.json): fall back to the last phase_error event.
	writeEvents(t, eng.store, storage.EventsPath(project.ID, issue.ID, "research"),
		`{"type":"phase_error","error":"failed: model request timed out: phase research error: boom"}`,
	)
	view, err := eng.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if view.FailureReason != "failed: model request timed out: phase research error: boom" {
		t.Fatalf("FailureReason = %q", view.FailureReason)
	}

	// A failed result.json error wins over the events fallback.
	if err := writeResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, "research"), PhaseResult{
		Status:  "failed",
		Error:   "failed: empty response from model: loop 1 produced empty output",
		Attempt: 1,
	}); err != nil {
		t.Fatalf("write result: %v", err)
	}
	view, err = eng.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if view.FailureReason != "failed: empty response from model: loop 1 produced empty output" {
		t.Fatalf("FailureReason = %q", view.FailureReason)
	}

	// Non-failed issues carry no failure reason.
	if err := eng.issues.UpdateStatus(issue.ID, sqlite.StatusInProgress, "research"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	view, err = eng.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if view.FailureReason != "" {
		t.Fatalf("FailureReason = %q, want empty", view.FailureReason)
	}
}

func TestLastPhaseError(t *testing.T) {
	store := newDiagStore(t)
	writeEvents(t, store, "x/events.jsonl",
		`{"type":"phase_error","error":"first"}`,
		`{"type":"model_turn","role":"model","content":"x"}`,
		`{"type":"phase_error","error":"failed: empty response from model: second"}`,
	)
	got := lastPhaseError(context.Background(), store, "x/events.jsonl")
	if got != "failed: empty response from model: second" {
		t.Fatalf("got %q", got)
	}
	if got := lastPhaseError(context.Background(), store, "missing/events.jsonl"); got != "" {
		t.Fatalf("missing file: got %q", got)
	}
}
