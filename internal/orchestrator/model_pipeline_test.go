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

	// The exclusive-mode lock must be released even on unload failure.
	if !modelLocks.TryLock(srv.URL) {
		t.Fatal("exclusive lock still held after unload failure")
	}
	modelLocks.Unlock(srv.URL)

	// The failure is recorded in the phase's events.jsonl.
	eventsData, err := eng.store.Read(ctx, storage.EventsPath(project.ID, issue.ID, "research"))
	if err != nil {
		t.Fatalf("read research events.jsonl: %v", err)
	}
	var sawUnloadErr bool
	scanner := bufio.NewScanner(strings.NewReader(string(eventsData)))
	for scanner.Scan() {
		var ev eventRecord
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type == "model_unload" && ev.Error != "" {
			sawUnloadErr = true
		}
	}
	if !sawUnloadErr {
		t.Fatal("research events.jsonl missing model_unload error event")
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
