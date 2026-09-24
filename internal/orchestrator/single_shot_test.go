package orchestrator

import (
	"context"
	"database/sql"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// stubLLM is a model.LLM returning fixed text (and usage) for single-shot tests.
type stubLLM struct {
	text   string
	total  int32
	err    error
	gotReq *adkmodel.LLMRequest
}

func (s *stubLLM) Name() string { return "stub" }

func (s *stubLLM) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		s.gotReq = req
		if s.err != nil {
			yield(nil, s.err)
			return
		}
		resp := &adkmodel.LLMResponse{
			Content:      genai.NewContentFromText(s.text, genai.RoleModel),
			TurnComplete: true,
		}
		if s.total > 0 {
			resp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{TotalTokenCount: s.total}
		}
		yield(resp, nil)
	}
}

func systemInstructionText(req *adkmodel.LLMRequest) string {
	var sb strings.Builder
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, p := range req.Config.SystemInstruction.Parts {
			if p != nil {
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String()
}

func TestRunSingleShot(t *testing.T) {
	ctx := context.Background()
	e, store := newDigestEngine(t)

	outputPath := storage.AttemptOutputPath(1, 2, "step-2", 1)
	eventsPath := storage.EventsPath(1, 2, "step-2")
	stub := &stubLLM{text: "  THE PLAN  ", total: 42}
	cfg := config.AgentConfig{ID: "planner", SystemPrompt: "You are a planner."}

	out, done, rationale, tokens, err := e.runSingleShot(ctx, stub, cfg,
		genai.NewContentFromText("issue input", genai.RoleUser), outputPath, eventsPath, 1, 1)
	if err != nil {
		t.Fatalf("runSingleShot: %v", err)
	}
	if !done {
		t.Fatal("single-shot must always return done=true")
	}
	if string(out) != "THE PLAN" {
		t.Fatalf("output = %q, want trimmed %q", out, "THE PLAN")
	}
	if rationale != "" {
		t.Fatalf("rationale should be empty, got %q", rationale)
	}
	if tokens != 42 {
		t.Fatalf("tokens = %d, want 42", tokens)
	}

	// The request carries no tools and the agent's configured system prompt.
	if got := systemInstructionText(stub.gotReq); got != cfg.SystemPrompt {
		t.Fatalf("system instruction = %q, want %q", got, cfg.SystemPrompt)
	}

	// Output lands at the same path the tool-loop path uses.
	data, err := store.Read(ctx, outputPath)
	if err != nil || string(data) != "THE PLAN" {
		t.Fatalf("output.md = %q, err %v", data, err)
	}

	// usage + model_turn events recorded for budgets/dashboard.
	events, err := store.Read(ctx, eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), `"type":"usage"`) || !strings.Contains(string(events), `"type":"model_turn"`) {
		t.Fatalf("events missing usage/model_turn:\n%s", events)
	}
}

func TestRunSingleShotEmptyOutput(t *testing.T) {
	ctx := context.Background()
	e, store := newDigestEngine(t)

	outputPath := storage.AttemptOutputPath(1, 2, "step-1", 1)
	cfg := config.AgentConfig{ID: "researcher", SystemPrompt: "You are a researcher."}
	_, _, _, _, err := e.runSingleShot(ctx, &stubLLM{text: "  "}, cfg,
		genai.NewContentFromText("input", genai.RoleUser), outputPath, storage.EventsPath(1, 2, "step-1"), 1, 1)
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("want empty-output error, got %v", err)
	}
	if exists, _ := store.Exists(ctx, outputPath); exists {
		t.Fatal("no output file should be written on empty response")
	}
}

func TestRunSingleShotBudgetExceeded(t *testing.T) {
	ctx := context.Background()
	e, _ := newDigestEngine(t)

	cfg := config.AgentConfig{ID: "researcher", SystemPrompt: "You are a researcher."}
	_, _, _, _, err := e.runSingleShot(ctx, &stubLLM{err: llm.ErrBudgetExceeded}, cfg,
		genai.NewContentFromText("input", genai.RoleUser), storage.AttemptOutputPath(1, 2, "step-1", 1), storage.EventsPath(1, 2, "step-1"), 1, 1)
	if !llm.IsBudgetExceeded(err) {
		t.Fatalf("budget error must pass through unwrapped, got %v", err)
	}
}

// TestRunSingleShotDryRun exercises the full pipeline with single-shot
// researcher+planner against the dryrun provider: both steps complete in one
// no-tools call and the implementer step runs normally to completion.
func TestRunSingleShotDryRun(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := cfg.Projects["foo"]
	proj.SourcePath = srcDir
	cfg.Projects["foo"] = proj

	on := true
	researcher := cfg.Agents["researcher"]
	researcher.SingleShot = &on
	researcher.SingleShotContextBytes = 4096
	cfg.Agents["researcher"] = researcher
	planner := cfg.Agents["planner"]
	planner.SingleShot = &on
	cfg.Agents["planner"] = planner

	if err := Run(ctx, cfg, RunOptions{ProjectName: "foo", IssueTitle: "add auth", DryRun: true}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	projectID, issueID := firstIssueIDs(t, cfg.DBPath)

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	// Step-1 and step-2 outputs come from the dryrun single-shot text path.
	for _, key := range []string{"step-1", "step-2"} {
		data, err := store.Read(ctx, storage.AttemptOutputPath(projectID, issueID, key, 1))
		if err != nil {
			t.Fatalf("read %s output: %v", key, err)
		}
		if !strings.Contains(string(data), "Dry-run single-shot output") {
			t.Fatalf("%s output not from single-shot path: %q", key, data)
		}
	}

	// The implementer step ran normally and the whole flow completed.
	res, err := readResult(ctx, store, storage.ResultPath(projectID, issueID, "step-3"))
	if err != nil {
		t.Fatalf("read step-3 result: %v", err)
	}
	if res.Status != "done" {
		t.Fatalf("step-3 status = %q, want done", res.Status)
	}

	issue, err := sqlite.NewIssueRepo(db).Get(issueID)
	if err != nil || issue == nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("issue status = %q, want done", issue.Status)
	}
}
