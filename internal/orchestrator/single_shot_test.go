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

	outputPath := storage.AttemptOutputPath(1, 2, "plan", 1)
	eventsPath := storage.EventsPath(1, 2, "plan")
	stub := &stubLLM{text: "  THE PLAN  ", total: 42}

	out, done, rationale, effort, tokens, err := e.runSingleShot(ctx, stub, "plan", config.AgentConfig{},
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
	if rationale != "" || effort != "" {
		t.Fatalf("rationale/effort should be empty, got %q/%q", rationale, effort)
	}
	if tokens != 42 {
		t.Fatalf("tokens = %d, want 42", tokens)
	}

	// The request carries no tools and the single-shot default instruction.
	if got := systemInstructionText(stub.gotReq); got != config.DefaultSingleShotPrompt("planner") {
		t.Fatalf("system instruction = %q", got)
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

	outputPath := storage.AttemptOutputPath(1, 2, "research", 1)
	_, _, _, _, _, err := e.runSingleShot(ctx, &stubLLM{text: "  "}, "research", config.AgentConfig{},
		genai.NewContentFromText("input", genai.RoleUser), outputPath, storage.EventsPath(1, 2, "research"), 1, 1)
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

	_, _, _, _, _, err := e.runSingleShot(ctx, &stubLLM{err: llm.ErrBudgetExceeded}, "research", config.AgentConfig{},
		genai.NewContentFromText("input", genai.RoleUser), storage.AttemptOutputPath(1, 2, "research", 1), storage.EventsPath(1, 2, "research"), 1, 1)
	if !llm.IsBudgetExceeded(err) {
		t.Fatalf("budget error must pass through unwrapped, got %v", err)
	}
}

func TestRunSingleShotUnsupportedPhase(t *testing.T) {
	ctx := context.Background()
	e, _ := newDigestEngine(t)

	_, _, _, _, _, err := e.runSingleShot(ctx, &stubLLM{text: "x"}, "implementation", config.AgentConfig{},
		genai.NewContentFromText("input", genai.RoleUser), storage.AttemptOutputPath(1, 2, "implementation", 1), storage.EventsPath(1, 2, "implementation"), 1, 1)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("want unsupported-phase error, got %v", err)
	}
}

func TestResolveSingleShotPrompt(t *testing.T) {
	def := config.DefaultSystemPrompt("planner")
	base := config.DefaultSingleShotPrompt("planner")

	// No user override (merged config still holds the built-in default).
	if got := resolveSingleShotPrompt("plan", config.AgentConfig{SystemPrompt: def}); got != base {
		t.Fatal("default prompt should resolve to the single-shot default")
	}
	// Only system_prompt_append was set (MergeAgent bakes it with "\n\n").
	got := resolveSingleShotPrompt("plan", config.AgentConfig{SystemPrompt: def + "\n\nEXTRA"})
	if got != base+"\n\nEXTRA" {
		t.Fatalf("append-only override = %q", got)
	}
	// Full user override honored as-is.
	if got := resolveSingleShotPrompt("plan", config.AgentConfig{SystemPrompt: "custom"}); got != "custom" {
		t.Fatalf("full override = %q", got)
	}
	// Empty (unmerged config, e.g. direct unit calls) also gets the default.
	if got := resolveSingleShotPrompt("research", config.AgentConfig{}); got != config.DefaultSingleShotPrompt("researcher") {
		t.Fatal("empty prompt should resolve to the single-shot default")
	}
	// No single-shot default for implementation.
	if got := resolveSingleShotPrompt("implementation", config.AgentConfig{}); got != "" {
		t.Fatalf("implementation should have no single-shot prompt, got %q", got)
	}
}

// TestRunSingleShotDryRun exercises the full pipeline with single-shot
// research+plan against the dryrun provider: both phases complete in one
// no-tools call, and the planner's missing effort tag defaults to high, holding
// the pipeline at the human gate before implementation.
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

	// Research and plan outputs come from the dryrun single-shot text path.
	for _, phase := range []string{"research", "plan"} {
		data, err := store.Read(ctx, storage.AttemptOutputPath(projectID, issueID, phase, 1))
		if err != nil {
			t.Fatalf("read %s output: %v", phase, err)
		}
		if !strings.Contains(string(data), "Dry-run single-shot output") {
			t.Fatalf("%s output not from single-shot path: %q", phase, data)
		}
	}

	// Plan effort defaults to high → human gate before implementation.
	res, err := readResult(ctx, store, storage.ResultPath(projectID, issueID, "implementation"))
	if err != nil {
		t.Fatalf("read implementation result: %v", err)
	}
	if res.Status != "waiting_human" {
		t.Fatalf("implementation status = %q, want waiting_human (effort gate)", res.Status)
	}

	issue, err := sqlite.NewIssueRepo(db).Get(issueID)
	if err != nil || issue == nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "waiting_human" {
		t.Fatalf("issue status = %q, want waiting_human", issue.Status)
	}
}
