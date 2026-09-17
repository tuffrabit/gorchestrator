package orchestrator

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestRun_Inference_LoadUnloadOrder(t *testing.T) {
	for _, mode := range []string{"exclusive", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			ctx := context.Background()
			tmp := t.TempDir()
			cfg := testConfig(tmp)

			stub := newLlamaSwapStub()
			srv := newStubServer(t, stub)
			cfg.Inference = config.InferenceConfig{
				Type:    "llama-swap",
				BaseURL: srv.URL,
				Mode:    mode,
			}

			opts := RunOptions{ProjectName: "foo", IssueTitle: "add auth", DryRun: true}
			if err := Run(ctx, cfg, opts); err != nil {
				t.Fatalf("Run failed: %v", err)
			}

			// Each phase must warm up, verify, unload, and poll-empty before
			// the next phase's warmup starts.
			want := []string{}
			for i := 0; i < 3; i++ {
				want = append(want, "chat", "running", "unload", "running")
			}
			got := stub.requestLog()
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("request log = %v, want %v", got, want)
			}
			for _, w := range stub.warmups {
				if w["model"] != "dryrun-model" {
					t.Fatalf("warmup model = %v, want dryrun-model", w["model"])
				}
			}

			// Residency events bracket the phase work in events.jsonl.
			store, err := storage.NewFS(tmp)
			if err != nil {
				t.Fatalf("init storage: %v", err)
			}
			pid, iid := firstIssueIDs(t, cfg.DBPath)
			eventsData, err := store.Read(ctx, storage.EventsPath(pid, iid, "research"))
			if err != nil {
				t.Fatalf("read research events.jsonl: %v", err)
			}
			var types []string
			scanner := bufio.NewScanner(strings.NewReader(string(eventsData)))
			for scanner.Scan() {
				var ev eventRecord
				if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
					continue
				}
				types = append(types, ev.Type)
			}
			joined := strings.Join(types, ",")
			loadIdx := strings.Index(joined, "model_load")
			unloadIdx := strings.LastIndex(joined, "model_unload")
			turnIdx := strings.Index(joined, "model_turn")
			if loadIdx < 0 || unloadIdx < 0 || turnIdx < 0 {
				t.Fatalf("research events = %v, want model_load/model_turn/model_unload", types)
			}
			if !(loadIdx < turnIdx && turnIdx < unloadIdx) {
				t.Fatalf("research events out of order: %v", types)
			}
			if strings.Contains(joined, "model_wait") {
				t.Fatalf("model_wait recorded without contention: %v", types)
			}
		})
	}
}

func TestRun_Inference_UnloadOnWaitingHuman(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	// Human gate after research: the phase ends waiting_human and must still
	// unload the model rather than pin it.
	cfg.Agents["researcher"] = config.AgentConfig{
		Adjudicator: "human",
		MaxAttempts: 1,
		Loops:       1,
	}

	stub := newLlamaSwapStub()
	srv := newStubServer(t, stub)
	cfg.Inference = config.InferenceConfig{
		Type:    "llama-swap",
		BaseURL: srv.URL,
		Mode:    "exclusive",
	}

	opts := RunOptions{ProjectName: "foo", IssueTitle: "add auth", DryRun: true}
	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_, iid := firstIssueIDs(t, cfg.DBPath)
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	issue, err := sqlite.NewIssueRepo(db).Get(iid)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "waiting_human" {
		t.Fatalf("issue status = %q, want waiting_human", issue.Status)
	}

	// Research loaded and unloaded; no further phase touched the server.
	want := []string{"chat", "running", "unload", "running"}
	if got := stub.requestLog(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("request log = %v, want %v", got, want)
	}
}

// spyController records controller calls to prove the pipeline makes none
// when the inference block is absent.
type spyController struct {
	calls int
}

func (s *spyController) EnsureLoaded(ctx context.Context, model string, timeout time.Duration) error {
	s.calls++
	return nil
}

func (s *spyController) UnloadAll(ctx context.Context) error {
	s.calls++
	return nil
}

func newManualEngine(t *testing.T, cfg *config.Config) (*Engine, *sqlite.Project, *sqlite.Issue) {
	t.Helper()
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	project, err := eng.projects.GetByName("foo")
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	issue, err := eng.issues.Create(project.ID, "add auth")
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sourceDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := eng.snapshotSource(context.Background(), project.ID, issue.ID, sourceDir); err != nil {
		t.Fatalf("snapshot source: %v", err)
	}
	return eng, project, issue
}

func TestRun_Inference_UnloadFailureBlocksNextPhase(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	stub := newLlamaSwapStub()
	stub.unloadClearsIn = -1 // /running never empties
	srv := newStubServer(t, stub)
	cfg.Inference = config.InferenceConfig{
		Type:    "llama-swap",
		BaseURL: srv.URL,
		Mode:    "exclusive",
	}

	eng, project, issue := newManualEngine(t, cfg)
	// Shorten the unload poll deadline for the test.
	ctl := newLlamaSwapController(srv.URL)
	ctl.pollInterval = time.Millisecond
	ctl.unloadDeadline = 50 * time.Millisecond
	eng.controller = ctl

	err := eng.runPipeline(ctx, project, issue, true)
	if err == nil || !strings.Contains(err.Error(), "unload model after research") {
		t.Fatalf("runPipeline = %v, want unload-failure error", err)
	}

	// The research result is persisted (resume will pick up correctly) and
	// the plan phase never ran against unknown residency.
	result, rerr := readResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, "research"))
	if rerr != nil {
		t.Fatalf("read research result: %v", rerr)
	}
	if result.Status != "done" {
		t.Fatalf("research result status = %q, want done", result.Status)
	}
	if exists, _ := eng.store.Exists(ctx, storage.ResultPath(project.ID, issue.ID, "plan")); exists {
		t.Fatal("plan result.json exists; next phase ran despite unload failure")
	}

	// The issue must land in a visible failed state, not stay green/stale.
	got, err := eng.issues.Get(issue.ID)
	if err != nil || got == nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.Status != sqlite.StatusFailed {
		t.Fatalf("issue status = %q, want failed", got.Status)
	}
	if got.CurrentPhase != "research" {
		t.Fatalf("issue current_phase = %q, want research", got.CurrentPhase)
	}

	// The exclusive-mode lock must be released even on unload failure.
	if !modelLocks.TryLock(srv.URL) {
		t.Fatal("exclusive lock still held after unload failure")
	}
	modelLocks.Unlock(srv.URL)

	// The failure is recorded in the phase's events.jsonl, alongside the
	// inference_error entry from the failure path.
	eventsData, err := eng.store.Read(ctx, storage.EventsPath(project.ID, issue.ID, "research"))
	if err != nil {
		t.Fatalf("read research events.jsonl: %v", err)
	}
	var sawUnloadErr, sawInferenceErr bool
	scanner := bufio.NewScanner(strings.NewReader(string(eventsData)))
	for scanner.Scan() {
		var ev eventRecord
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type == "model_unload" && ev.Error != "" {
			sawUnloadErr = true
		}
		if ev.Type == "inference_error" {
			sawInferenceErr = true
		}
	}
	if !sawUnloadErr {
		t.Fatal("research events.jsonl missing model_unload error event")
	}
	if !sawInferenceErr {
		t.Fatal("research events.jsonl missing inference_error event")
	}

	// Exclusive mode: the breaker must trip on the inference-side failure.
	if tripped, tripIssue := eng.InferenceBreakerTripped(); !tripped || tripIssue != issue.ID {
		t.Fatalf("breaker = (%v, %d), want tripped on issue %d", tripped, tripIssue, issue.ID)
	}
}

// drainEvents collects all buffered events without blocking.
func drainEvents(ch <-chan Event) []Event {
	var out []Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestRun_Inference_LoadFailureFailsIssue(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	stub := newLlamaSwapStub()
	stub.noLoadOnWarmup = true // warmup completes but the model never loads
	srv := newStubServer(t, stub)
	cfg.Inference = config.InferenceConfig{
		Type:    "llama-swap",
		BaseURL: srv.URL,
		Mode:    "exclusive",
	}

	eng, project, issue := newManualEngine(t, cfg)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	eventCh := eng.Subscribe(subCtx, EventFilter{IssueID: issue.ID})

	err := eng.runPipeline(ctx, project, issue, true)
	if err == nil || !strings.Contains(err.Error(), "load model for research") {
		t.Fatalf("runPipeline = %v, want load-failure error", err)
	}

	// Issue row: visible failed state on the phase that failed to load.
	got, err := eng.issues.Get(issue.ID)
	if err != nil || got == nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.Status != sqlite.StatusFailed {
		t.Fatalf("issue status = %q, want failed", got.Status)
	}
	if got.CurrentPhase != "research" {
		t.Fatalf("issue current_phase = %q, want research", got.CurrentPhase)
	}

	// SSE: a phase_finished event carrying phase_result=inference_error,
	// plus the breaker-trip issue_status event.
	var sawPhaseFinished, sawBreakerTrip bool
	for _, ev := range drainEvents(eventCh) {
		if ev.Type == EventPhaseFinished && ev.Status == sqlite.StatusFailed && ev.Data["phase_result"] == "inference_error" {
			sawPhaseFinished = true
		}
		if ev.Type == EventIssueStatus && strings.Contains(ev.Message, "inference breaker tripped") {
			sawBreakerTrip = true
		}
	}
	if !sawPhaseFinished {
		t.Fatal("missing phase_finished event with phase_result=inference_error")
	}
	if !sawBreakerTrip {
		t.Fatal("missing issue_status breaker-trip event")
	}

	// events.jsonl: inference_error recorded for the phase.
	eventsData, err := eng.store.Read(ctx, storage.EventsPath(project.ID, issue.ID, "research"))
	if err != nil {
		t.Fatalf("read research events.jsonl: %v", err)
	}
	var sawInferenceErr bool
	scanner := bufio.NewScanner(strings.NewReader(string(eventsData)))
	for scanner.Scan() {
		var ev eventRecord
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type == "inference_error" {
			sawInferenceErr = true
		}
	}
	if !sawInferenceErr {
		t.Fatal("research events.jsonl missing inference_error event")
	}

	// Exclusive mode: breaker tripped, claiming halted.
	if tripped, tripIssue := eng.InferenceBreakerTripped(); !tripped || tripIssue != issue.ID {
		t.Fatalf("breaker = (%v, %d), want tripped on issue %d", tripped, tripIssue, issue.ID)
	}
	if claimed, err := eng.ClaimIssue(); err != nil || claimed != nil {
		t.Fatalf("ClaimIssue while tripped = (%v, %v), want (nil, nil)", claimed, err)
	}
}

func TestRun_Inference_LoadFailureNonExclusive(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	stub := newLlamaSwapStub()
	stub.noLoadOnWarmup = true
	srv := newStubServer(t, stub)
	cfg.Inference = config.InferenceConfig{
		Type:    "llama-swap",
		BaseURL: srv.URL,
		// No mode: exclusive: failure fails the issue but must not halt claiming.
	}

	eng, project, issue := newManualEngine(t, cfg)

	err := eng.runPipeline(ctx, project, issue, true)
	if err == nil || !strings.Contains(err.Error(), "load model for research") {
		t.Fatalf("runPipeline = %v, want load-failure error", err)
	}

	got, err := eng.issues.Get(issue.ID)
	if err != nil || got == nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.Status != sqlite.StatusFailed {
		t.Fatalf("issue status = %q, want failed", got.Status)
	}

	if tripped, _ := eng.InferenceBreakerTripped(); tripped {
		t.Fatal("breaker tripped in non-exclusive mode")
	}
	// Claiming is unaffected: a queued issue is still claimable.
	other, err := eng.issues.CreateQueued(project.ID, "unrelated queued issue", true)
	if err != nil {
		t.Fatalf("create queued issue: %v", err)
	}
	claimed, err := eng.ClaimIssue()
	if err != nil || claimed == nil {
		t.Fatalf("ClaimIssue = (%v, %v), want queued issue", claimed, err)
	}
	if claimed.ID != other.ID {
		t.Fatalf("ClaimIssue claimed %d, want %d", claimed.ID, other.ID)
	}
}

func TestRun_NoInferenceBlock_NoControllerCalls(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp) // no inference block

	eng, project, issue := newManualEngine(t, cfg)
	spy := &spyController{}
	eng.controller = spy

	if err := eng.runPipeline(ctx, project, issue, true); err != nil {
		t.Fatalf("runPipeline failed: %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("controller calls = %d, want 0 without inference config", spy.calls)
	}

	result, err := readResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, "implementation"))
	if err != nil {
		t.Fatalf("read implementation result: %v", err)
	}
	if result.Status != "done" {
		t.Fatalf("implementation result status = %q, want done", result.Status)
	}
}
