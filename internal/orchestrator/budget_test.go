package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"

	adkmodel "google.golang.org/adk/v2/model"
)

func TestBudgetLLM_StopsBeforeCall(t *testing.T) {
	inner := llm.NewDryRunModel("dryrun")
	wrapped := llm.WrapBudget(inner, llm.BudgetConfig{Ceiling: 5, Spent: 5})
	req := &adkmodel.LLMRequest{}
	gotErr := false
	for _, err := range wrapped.GenerateContent(context.Background(), req, false) {
		if err == nil {
			t.Fatal("expected budget error")
		}
		if !llm.IsBudgetExceeded(err) {
			t.Fatalf("err = %v", err)
		}
		gotErr = true
	}
	if !gotErr {
		t.Fatal("no yield")
	}
}

func TestProcessIssue_BudgetExceededMidPhase(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	// dry-run: first tool call ~10 tokens; ceiling 10 blocks the finish_task call.
	cfg.Providers = map[string]config.ProviderBudgetConfig{
		"dryrun": {TokenBudget: 10, WarnPct: 80},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Budget me",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ProcessIssue(ctx, issue.ID); err == nil {
		// ProcessIssue may return error from runPipeline; either way phase must fail.
		t.Log("ProcessIssue returned nil (phase may still be failed)")
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusFailed {
		// Read research result
		res, rerr := readResult(ctx, eng.store, storage.ResultPath(issue.ProjectID, issue.ID, "research"))
		if rerr != nil {
			t.Fatalf("status=%s read result: %v", issue.Status, rerr)
		}
		if res.Status != "failed" || !strings.Contains(res.Error, "budget_exceeded") {
			t.Fatalf("status=%s result=%+v", issue.Status, res)
		}
	} else {
		res, _ := readResult(ctx, eng.store, storage.ResultPath(issue.ProjectID, issue.ID, issue.CurrentPhase))
		if !strings.Contains(res.Error, "budget_exceeded") {
			t.Fatalf("error = %q, want budget_exceeded", res.Error)
		}
	}
}

func TestProcessIssue_BudgetOverrideResume(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Providers = map[string]config.ProviderBudgetConfig{
		"dryrun": {TokenBudget: 10, WarnPct: 80},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Raise budget",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = eng.ProcessIssue(ctx, issue.ID)

	// Raise ceiling high enough for full dry-run multi-turn sessions.
	if err := eng.Decide(ctx, DecideOptions{
		IssueID:  issue.ID,
		Decision: "retry",
		Feedback: "raise budget",
		BudgetOverrides: map[string]int{
			"dryrun": 1_000_000,
		},
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status = %q after retry", issue.Status)
	}
	if !strings.Contains(issue.BudgetOverridesJSON, "dryrun") {
		t.Fatalf("overrides = %s", issue.BudgetOverridesJSON)
	}

	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue after raise: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("final status = %q, want done", issue.Status)
	}
}

func TestSumUsageFromEvents(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatal(err)
	}
	path := "events.jsonl"
	_ = store.Write(ctx, path, []byte(
		`{"type":"usage","tokens":10}`+"\n"+
			`{"type":"model_turn","content":"x"}`+"\n"+
			`{"type":"usage","tokens":15}`+"\n",
	))
	if got := sumUsageFromEvents(ctx, store, path); got != 25 {
		t.Fatalf("sum = %d", got)
	}
}

func TestResolveSessionBudget_OverrideWins(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Providers = map[string]config.ProviderBudgetConfig{
		"openai": {TokenBudget: 100, WarnPct: 50},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	issue := &sqlite.Issue{BudgetOverridesJSON: `{"openai":500}`}
	c, w := eng.resolveSessionBudget(issue, "openai")
	if c != 500 || w != 50 {
		t.Fatalf("ceiling=%d warn=%d", c, w)
	}
	c, _ = eng.resolveSessionBudget(&sqlite.Issue{BudgetOverridesJSON: "{}"}, "openai")
	if c != 100 {
		t.Fatalf("ceiling=%d", c)
	}
	c, _ = eng.resolveSessionBudget(nil, "missing")
	if c != 0 {
		t.Fatalf("unlimited expected, got %d", c)
	}
}
