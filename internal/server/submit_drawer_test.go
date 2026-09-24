package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// TestPartialSubmitPost_FreezesFlowInDocumentOrder proves the submit drawer's
// contract: the <select name="flow_agent"> list arrives in DOM order and is
// frozen into pipeline_json as-is (blank, unpicked slots are dropped).
func TestPartialSubmitPost_FreezesFlowInDocumentOrder(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects["acme"] = config.ProjectConfig{} // force an explicit pick
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{}
	form.Set("project", "acme")
	form.Set("title", "ordered flow")
	form.Set("dry_run", "1")
	form.Add("flow_agent", "planner")
	form.Add("flow_agent", "") // unselected step
	form.Add("flow_agent", "implementer")
	req := httptest.NewRequest(http.MethodPost, "/partials/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	issue, err := eng.Issues().Get(1)
	if err != nil || issue == nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.PipelineJSON != `["planner","implementer"]` {
		t.Fatalf("pipeline_json = %s", issue.PipelineJSON)
	}
	if issue.CurrentPhase != "step-1" {
		t.Fatalf("current_phase = %q, want step-1", issue.CurrentPhase)
	}
	steps, err := sqlite.StepsForIssue(issue)
	if err != nil || len(steps) != 2 || steps[0].AgentID != "planner" || steps[1].Key != "step-2" {
		t.Fatalf("steps = %#v (%v)", steps, err)
	}
}

func TestPartialSubmitPost_SetsCloseDrawerTrigger(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects["acme"] = config.ProjectConfig{}
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{}
	form.Set("project", "acme")
	form.Set("title", "drawer close test")
	form.Set("dry_run", "1")
	// Explicit ordered flow (the project has no default_flow).
	form.Add("flow_agent", "researcher")
	form.Add("flow_agent", "implementer")
	req := httptest.NewRequest(http.MethodPost, "/partials/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "close-drawer" {
		t.Fatalf("HX-Trigger = %q, want close-drawer; headers=%v", got, rec.Header())
	}
	if got := rec.Header().Get("HX-Trigger-After-Swap"); got != "close-drawer" {
		t.Fatalf("HX-Trigger-After-Swap = %q, want close-drawer; headers=%v", got, rec.Header())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "issue-card") && !strings.Contains(body, "card-title") {
		snip := body
		if len(snip) > 300 {
			snip = snip[:300]
		}
		t.Fatalf("expected issue card HTML, got: %s", snip)
	}
}
