package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

func testConfig(tmp string) *config.Config {
	return &config.Config{
		StorageRoot: tmp,
		DBPath:      filepath.Join(tmp, "gorchestrator.db"),
		DefaultModel: config.ModelConfig{
			Provider:   "dryrun",
			Model:      "dryrun",
			Timeout:    "30s",
			TimeoutDur: 30 * time.Second,
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileConfig{MaxBytes: 64 * 1024, MaxLines: 2000},
		},
		Agents: map[string]config.AgentConfig{
			"researcher":  {Adjudicator: "self", MaxAttempts: 1, Loops: 1},
			"planner":     {Adjudicator: "self", MaxAttempts: 1, Loops: 1},
			"implementer": {Adjudicator: "self", MaxAttempts: 1, Loops: 1},
		},
		Projects: map[string]config.ProjectConfig{
			"acme": {DefaultFlow: []string{"researcher", "planner", "implementer"}},
		},
		Server: config.ServerConfig{
			Listen:              "127.0.0.1:0",
			MaxConcurrentIssues: 2,
			PublicBaseURL:       "http://127.0.0.1:8080",
			ShutdownTimeoutDur:  5 * time.Second,
		},
		Auth: config.AuthConfig{
			Mode:          "disabled",
			SessionTTLDur: 24 * time.Hour,
		},
	}
}

func TestAPI_SubmitListGet(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	h := srv.Handler()

	body := `{"project":"acme","title":"Add OIDC","dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/issues", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var list struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(list.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(list.Issues))
	}

	req = httptest.NewRequest(http.MethodGet, "/api/issues/1", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
}

func TestAPI_DeleteIssue(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	h := srv.Handler()

	body := `{"project":"acme","title":"delete via api","dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := int64(created["id"].(float64))

	req = httptest.NewRequest(http.MethodDelete, "/api/issues/"+strconv.FormatInt(id, 10), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/issues/"+strconv.FormatInt(id, 10), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/issues/"+strconv.FormatInt(id, 10), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
}

func TestAPI_ArtifactPathTraversalRejected(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(context.Background(), orchestrator.RunOptions{
		ProjectName: "acme", IssueTitle: "x", DryRun: true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/issues/"+itoa(issue.ID)+"/artifacts/../../etc/passwd", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected rejection, got 200")
	}
}

func TestAPI_DecideWaitingHuman(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Agents["researcher"] = config.AgentConfig{Adjudicator: "human", MaxAttempts: 2, Loops: 1}
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(context.Background(), orchestrator.RunOptions{
		ProjectName: "acme", IssueTitle: "human", DryRun: true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusInProgress, "research")
	if err := eng.ProcessIssue(context.Background(), issue.ID); err != nil {
		t.Fatalf("process: %v", err)
	}

	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	h := srv.Handler()

	body := `{"decision":"pass","feedback":"lgtm"}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues/"+itoa(issue.ID)+"/decisions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("decide status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPI_Submit_UnknownProject(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"project":"missing","title":"x","dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}
}

func TestAPI_Submit_RejectsSourcePath(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"project":"acme","title":"x","source":"/tmp/repo","dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}
}

func TestAPI_Submit_Flow(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit flow is frozen into the issue and echoed back as steps.
	body := `{"project":"acme","title":"custom flow","dry_run":true,"flow":["planner","researcher"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	flow, _ := created["flow"].([]any)
	if len(flow) != 2 {
		t.Fatalf("flow = %#v", created["flow"])
	}
	first, _ := flow[0].(map[string]any)
	second, _ := flow[1].(map[string]any)
	if first["agent"] != "planner" || first["index"] != float64(1) {
		t.Fatalf("flow[0] = %#v", first)
	}
	if second["agent"] != "researcher" || second["index"] != float64(2) {
		t.Fatalf("flow[1] = %#v", second)
	}

	// The removed agent_flavors field is rejected with a 422.
	body = `{"project":"acme","title":"cast","dry_run":true,"agent_flavors":{"researcher":"cheap"}}`
	req = httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("agent_flavors status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}

	// GET /api/agents lists the configured agent ids only.
	req = httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agents status = %d", rec.Code)
	}
	for _, want := range []string{`"id":"implementer"`, `"id":"planner"`, `"id":"researcher"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("agents payload missing %s: %s", want, rec.Body.String())
		}
	}

	// List projects includes the flow default + available agents.
	req = httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("projects status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "flow_default") || !strings.Contains(rec.Body.String(), "available") {
		t.Fatalf("projects payload missing flow info: %s", rec.Body.String())
	}
}

func TestPartialSubmit_RendersFlowBuilder(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/partials/submit?project=acme", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Source path") || strings.Contains(body, `name="source"`) {
		t.Fatalf("source field should be gone: %s", body)
	}
	if !strings.Contains(body, `<select id="project"`) {
		t.Fatalf("expected project select: %s", body)
	}
	if !strings.Contains(body, `id="flow-builder"`) || !strings.Contains(body, `name="flow_state"`) {
		t.Fatalf("expected flow builder with state input: %s", body)
	}
	if !strings.Contains(body, `name="flow_agent"`) {
		t.Fatalf("expected flow agent selects: %s", body)
	}
	// The project's default_flow prefills the builder: three steps in order.
	for _, want := range []string{`value="researcher" selected`, `value="planner" selected`, `value="implementer" selected`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected prefilled step %s: %s", want, body)
		}
	}
	// The project select refreshes the flow builder via the new endpoint.
	if !strings.Contains(body, `hx-get="/partials/submit/flow"`) {
		t.Fatalf("expected project select to hit /partials/submit/flow: %s", body)
	}
}

// TestAPI_Submit_FlowUnknownAgent: an agent id that is not under agents:
// is a 422 that names the offending id.
func TestAPI_Submit_FlowUnknownAgent(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"project":"acme","title":"bad pick","dry_run":true,"flow":["researcher","nope"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nope") {
		t.Fatalf("error should name the offending agent: %s", rec.Body.String())
	}
}

// TestAPI_Submit_FlowNoDefault: a project with no default_flow rejects a
// submit that carries no flow instead of silently inventing a pipeline.
func TestAPI_Submit_FlowNoDefault(t *testing.T) {
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

	body := `{"project":"acme","title":"no flow","dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "default_flow") {
		t.Fatalf("error should point at default_flow: %s", rec.Body.String())
	}
}

// TestPartialSubmitFlow_Endpoint covers the flow builder's HTMX refresh:
// the hidden flow_state is authoritative and each request applies one change.
func TestPartialSubmitFlow_Endpoint(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	get := func(query string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/partials/submit/flow?"+query, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Change event: the second select now picks implementer.
	body := get("project=acme&flow_state=researcher,planner&flow_agent=researcher&flow_agent=implementer")
	if !strings.Contains(body, `value="implementer" selected`) {
		t.Fatalf("changed pick not rendered: %s", body)
	}
	if !strings.Contains(body, `value="researcher,implementer"`) {
		t.Fatalf("flow_state not updated: %s", body)
	}

	// Add: a blank slot appears after the current flow.
	body = get("project=acme&flow_state=researcher&flow_agent=researcher&flow_add=1")
	if !strings.Contains(body, "— pick an agent —") {
		t.Fatalf("expected a blank step after add: %s", body)
	}

	// Remove: step 2 disappears, leaving one select.
	body = get("project=acme&flow_state=researcher,planner&flow_remove=2")
	if strings.Contains(body, "planner\" selected") {
		t.Fatalf("removed step still rendered: %s", body)
	}
	if n := strings.Count(body, `name="flow_agent"`); n != 1 {
		t.Fatalf("flow_agent selects = %d, want 1: %s", n, body)
	}
}

func TestAPI_Submit_DependsOn(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	// Missing dependency → 422.
	body := `{"project":"acme","title":"orphan","dry_run":true,"depends_on":[999]}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing dep status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}

	// Submit a blocker, then a dependent on it.
	body = `{"project":"acme","title":"blocker","dry_run":true}`
	req = httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("blocker submit status = %d body=%s", rec.Code, rec.Body.String())
	}
	var blocker map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &blocker); err != nil {
		t.Fatal(err)
	}
	blockerID := int64(blocker["id"].(float64))

	body = `{"project":"acme","title":"dependent","dry_run":true,"depends_on":[` + itoa(blockerID) + `]}`
	req = httptest.NewRequest(http.MethodPost, "/api/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("dependent submit status = %d body=%s", rec.Code, rec.Body.String())
	}
	var dependent map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dependent); err != nil {
		t.Fatal(err)
	}

	// Blocker is queued, not done → dependent lists blocked_by.
	blocked, ok := dependent["blocked_by"].([]any)
	if !ok || len(blocked) != 1 || int64(blocked[0].(float64)) != blockerID {
		t.Fatalf("blocked_by = %#v, want [%d]", dependent["blocked_by"], blockerID)
	}

	// List endpoint surfaces the same derived field.
	req = httptest.NewRequest(http.MethodGet, "/api/issues", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"blocked_by":[`) {
		t.Fatalf("list payload missing blocked_by: %s", rec.Body.String())
	}

	// Blocker done → dependent no longer blocked.
	if err := eng.Issues().UpdateStatus(blockerID, sqlite.StatusDone, "implementation"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/issues/"+itoa(int64(dependent["id"].(float64))), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "blocked_by") {
		t.Fatalf("blocked_by should be absent after dep done: %s", rec.Body.String())
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
