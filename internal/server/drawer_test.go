package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func seedIssueWithPhases(t *testing.T, eng *orchestrator.Engine) (issueID, projectID int64) {
	t.Helper()
	ctx := context.Background()
	issue, err := eng.SubmitIssue(ctx, orchestrator.RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Drawer phase test",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	projectID = issue.ProjectID
	issueID = issue.ID

	// Research artifacts
	writePhaseArtifacts(t, eng, projectID, issueID, "step-1", "done", "# Research findings\n")
	// Plan artifacts
	writePhaseArtifacts(t, eng, projectID, issueID, "step-2", "done", "# Plan\n")
	// Implementation running (not done)
	writePhaseArtifacts(t, eng, projectID, issueID, "step-3", "in_progress", "")

	// Source + workspace files
	src := storage.SourcePath(projectID, issueID)
	ws := storage.WorkspacePath(projectID, issueID)
	_ = eng.Store().Mkdir(ctx, src)
	_ = eng.Store().Mkdir(ctx, path.Join(ws, "pkg"))
	_ = eng.Store().Write(ctx, path.Join(src, "main.go"), []byte("package main\n"))
	_ = eng.Store().Write(ctx, path.Join(ws, "main.go"), []byte("package main\n// changed\n"))
	_ = eng.Store().Write(ctx, path.Join(ws, "pkg", "util.go"), []byte("package pkg\n"))

	// Advance issue pointer to implementation
	if err := eng.Issues().UpdateStatus(issueID, sqlite.StatusInProgress, "step-3"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	return issueID, projectID
}

func writePhaseArtifacts(t *testing.T, eng *orchestrator.Engine, projectID, issueID int64, phase, status, output string) {
	t.Helper()
	ctx := context.Background()
	outPath := storage.AttemptOutputPath(projectID, issueID, phase, 1)
	if output != "" {
		if err := eng.Store().Write(ctx, outPath, []byte(output)); err != nil {
			t.Fatalf("write output %s: %v", phase, err)
		}
	}
	res := map[string]any{
		"status":        status,
		"attempt":       1,
		"latest_output": outPath,
	}
	data, _ := json.Marshal(res)
	if err := eng.Store().Write(ctx, storage.ResultPath(projectID, issueID, phase), data); err != nil {
		t.Fatalf("write result %s: %v", phase, err)
	}
	if err := eng.Store().Write(ctx, storage.EventsPath(projectID, issueID, phase), []byte(`{"type":"phase_started"}`+"\n")); err != nil {
		t.Fatalf("write events %s: %v", phase, err)
	}
}

func TestDrawer_PhaseScopedArtifacts(t *testing.T) {
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

	issueID, _ := seedIssueWithPhases(t, eng)

	// Step-1 (researcher) output, not the current step
	req := httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=output&phase=step-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("step-1 output status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Research findings") {
		t.Fatalf("expected research markdown in body, got: %s", body)
	}
	if !strings.Contains(body, "drawer-phase-tab") {
		t.Fatalf("expected step tabs in drawer")
	}
	if !strings.Contains(body, `openArtifactDrawer(1, 'output', 'step-1')`) {
		t.Fatalf("expected step-1 tab switcher: %s", body)
	}
	for _, want := range []string{"1 · researcher", "2 · planner", "3 · implementer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected step tab label %q in body", want)
		}
	}

	// Step-2 (planner) activity
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=activity&phase=step-2", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("step-2 activity status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "phase_started") {
		t.Fatalf("expected step-2 events: %s", rec.Body.String())
	}

	// Default output without an explicit step lands on the final step, which
	// is still running: no workspace tree yet.
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=output", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default output status = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "(no output yet)") {
		t.Fatalf("expected no-output placeholder for running final step: %s", body)
	}

	// The dedicated workspace tab always shows the tree, but no download
	// link while the final step is not done.
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=workspace", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace tab status = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "ws-tree") {
		t.Fatalf("expected workspace tree: %s", body)
	}
	if !strings.Contains(body, "main.go") || !strings.Contains(body, "util.go") {
		t.Fatalf("expected workspace files in tree: %s", body)
	}
	if !strings.Contains(body, "Download available when the workspace step is done.") {
		t.Fatalf("expected download disabled message while final step not done")
	}
	if strings.Contains(body, "workspace.zip\">") {
		t.Fatalf("download link must not appear before final step done")
	}
}

// seedLegacyIssue creates a pre-flow issue (empty pipeline_json, agent_flavors
// set) and returns its ids. Projects are registered by the engine on submit,
// so a normal submit is used to make sure acme exists.
func seedLegacyIssue(t *testing.T, eng *orchestrator.Engine) (issueID, projectID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := eng.SubmitIssue(ctx, orchestrator.RunOptions{
		ProjectName: "acme",
		IssueTitle:  "registration seed",
		DryRun:      true,
	}); err != nil {
		t.Fatalf("register project: %v", err)
	}
	registered, err := eng.ListRegisteredProjects(ctx)
	if err != nil || len(registered) == 0 {
		t.Fatalf("registered projects: %v (%d)", err, len(registered))
	}
	projectID = registered[0].Project.ID

	issue, err := eng.Issues().CreateQueuedFrom(projectID, "legacy issue", "research", "",
		`{"researcher":"base","planner":"base","implementer":"base"}`, "[]", true, "cli", "")
	if err != nil {
		t.Fatalf("create legacy issue: %v", err)
	}
	if !sqlite.IsLegacyIssue(issue) {
		t.Fatal("seed issue should be legacy")
	}
	return issue.ID, projectID
}

// TestDrawer_LegacyIssueTabs proves the compatibility rule: an issue created
// before the flow change keeps its research/plan/implementation tabs and
// reads its original artifact directories.
func TestDrawer_LegacyIssueTabs(t *testing.T) {
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

	issueID, projectID := seedLegacyIssue(t, eng)
	writePhaseArtifacts(t, eng, projectID, issueID, "research", "done", "# Research findings\n")
	writePhaseArtifacts(t, eng, projectID, issueID, "plan", "done", "# Plan\n")
	writePhaseArtifacts(t, eng, projectID, issueID, "implementation", "done", "")
	ws := storage.LegacyWorkspacePath(projectID, issueID)
	if err := eng.Store().Mkdir(context.Background(), ws); err != nil {
		t.Fatalf("mkdir legacy workspace: %v", err)
	}
	if err := eng.Store().Write(context.Background(), path.Join(ws, "main.go"), []byte("package main\n")); err != nil {
		t.Fatalf("write legacy workspace file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=output&phase=research", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy research output status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Research findings") {
		t.Fatalf("expected legacy research artifact: %s", body)
	}
	for _, want := range []string{"research", "plan", "implementation", "1 · researcher", "3 · implementer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected legacy tab %q: %s", want, body)
		}
	}
	if strings.Contains(body, "step-1") {
		t.Fatalf("legacy issue must not be re-keyed to step-N: %s", body)
	}

	// Legacy workspace is still readable through the Workspace tab.
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=workspace", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy workspace status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "main.go") {
		t.Fatalf("expected legacy workspace tree: %s", rec.Body.String())
	}
}

// TestIssueCard_FlowBadge checks the collapsed card meta: a flow issue shows
// its frozen flow as "1 · agent → 2 · agent", a legacy issue keeps its old
// current-phase chip.
func TestIssueCard_FlowBadge(t *testing.T) {
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

	issueID, _ := seedIssueWithPhases(t, eng)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("flow card status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="flow-chip"`) {
		t.Fatalf("expected flow badge: %s", body)
	}
	for _, want := range []string{"1 · researcher", "2 · planner", "3 · implementer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected flow step %q: %s", want, body)
		}
	}

	legacyID, _ := seedLegacyIssue(t, eng)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(legacyID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy card status = %d body=%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if strings.Contains(body, `class="flow-chip"`) {
		t.Fatalf("legacy card must keep the old phase chip: %s", body)
	}
	if !strings.Contains(body, `class="phase-chip"`) || !strings.Contains(body, "research") {
		t.Fatalf("expected legacy phase chip: %s", body)
	}
}

func TestDrawer_RawHTMLOutputPreserved(t *testing.T) {
	// Agents may write free-form HTML into output.md (e.g. a single-page demo).
	// goldmark defaults strip raw HTML to "<!-- raw HTML omitted -->", which
	// leaves the Output tab blank. Drawer rendering must preserve it.
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

	issue, err := eng.SubmitIssue(context.Background(), orchestrator.RunOptions{
		ProjectName: "acme",
		IssueTitle:  "html output",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	htmlOut := "<!DOCTYPE html>\n<html><body><h1>ASCII Bunny</h1><pre>/\\_/\\</pre></body></html>\n"
	writePhaseArtifacts(t, eng, issue.ProjectID, issue.ID, "step-1", "done", htmlOut)

	req := httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issue.ID)+"/drawer?tab=output&phase=step-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw HTML omitted") {
		t.Fatalf("raw HTML was stripped from drawer output: %s", body)
	}
	if !strings.Contains(body, "ASCII Bunny") {
		t.Fatalf("expected HTML body content in drawer: %s", body)
	}
	if !strings.Contains(body, "<h1>") && !strings.Contains(body, "<h1") {
		// WithUnsafe should keep the tag; template.HTML must not re-escape it.
		t.Fatalf("expected unescaped HTML heading in drawer: %s", body)
	}
}

func TestDrawer_WorkspaceFileDiff(t *testing.T) {
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
	issueID, _ := seedIssueWithPhases(t, eng)

	req := httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/workspace-file?path=main.go", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("file diff status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "changed") && !strings.Contains(rec.Body.String(), "+") {
		t.Fatalf("expected diff content: %s", rec.Body.String())
	}

	// Path traversal rejected
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/workspace-file?path=../secret", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected rejection for path traversal, got 200")
	}
}

func TestWorkspaceZip_OnlyWhenDone(t *testing.T) {
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
	issueID, projectID := seedIssueWithPhases(t, eng)

	// Not done yet → 409
	req := httptest.NewRequest(http.MethodGet, "/api/issues/"+itoa(issueID)+"/workspace.zip", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("zip before done status = %d want 409 body=%s", rec.Code, rec.Body.String())
	}

	// Mark implementation done
	writePhaseArtifacts(t, eng, projectID, issueID, "step-3", "done", "")
	// Re-write workspace files (writePhaseArtifacts may not touch them)
	ctx := context.Background()
	ws := storage.WorkspacePath(projectID, issueID)
	_ = eng.Store().Write(ctx, path.Join(ws, "main.go"), []byte("package main\n// final\n"))
	_ = eng.Store().Write(ctx, path.Join(ws, "pkg", "util.go"), []byte("package pkg\n"))

	req = httptest.NewRequest(http.MethodGet, "/api/issues/"+itoa(issueID)+"/workspace.zip", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("zip when done status = %d body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "zip") {
		t.Fatalf("content-type = %q", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		_, _ = io.ReadAll(rc)
		rc.Close()
	}
	if !names["main.go"] || !names["pkg/util.go"] {
		t.Fatalf("zip entries = %v", names)
	}

	// Drawer should now show download link
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=output&phase=step-3", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("drawer status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "workspace.zip") {
		t.Fatalf("expected download link after done: %s", rec.Body.String())
	}
}

func TestBuildWorkspaceTree(t *testing.T) {
	files := []string{
		"projects/1/issues/1/implementation/workspace/a.go",
		"projects/1/issues/1/implementation/workspace/pkg/b.go",
		"projects/1/issues/1/implementation/workspace/pkg/nested/c.go",
	}
	ws := "projects/1/issues/1/implementation/workspace"
	changed := map[string]bool{"a.go": true, "pkg/b.go": false, "pkg/nested/c.go": true}
	tree := buildWorkspaceTree(files, ws, changed)
	if len(tree) != 2 { // pkg/ dir + a.go
		t.Fatalf("root nodes = %d, want 2: %+v", len(tree), tree)
	}
	// dirs first
	if !tree[0].IsDir || tree[0].Name != "pkg" {
		t.Fatalf("first node = %+v, want pkg dir", tree[0])
	}
	if tree[1].IsDir || tree[1].Name != "a.go" || !tree[1].Changed {
		t.Fatalf("second node = %+v, want changed a.go", tree[1])
	}
}

func TestDrawer_ActivityJsonTree(t *testing.T) {
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
	issueID, projectID := seedIssueWithPhases(t, eng)

	ctx := context.Background()
	eventsKey := storage.EventsPath(projectID, issueID, "step-1")
	valid := `{"type":"model_turn","role":"model","content":"hello"}` + "\n" +
		`{"type":"usage","tokens":42}` + "\n"
	if err := eng.Store().Write(ctx, eventsKey, []byte(valid)); err != nil {
		t.Fatalf("write events: %v", err)
	}

	// Valid JSONL → tree view with embedded JSON array payload.
	req := httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=activity&phase=step-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("activity status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "js-events-data") || !strings.Contains(body, "json-tree") {
		t.Fatalf("expected json tree markers in body: %s", body)
	}
	if !strings.Contains(body, "model_turn") || !strings.Contains(body, "usage") {
		t.Fatalf("expected event payloads in body: %s", body)
	}
	if !strings.Contains(body, "jsonTreeAll") {
		t.Fatalf("expected tree toolbar buttons in body")
	}

	// Invalid JSONL → raw text fallback, no tree.
	if err := eng.Store().Write(ctx, eventsKey, []byte("not json at all\n")); err != nil {
		t.Fatalf("rewrite events: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=activity&phase=step-1", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("activity fallback status = %d", rec.Code)
	}
	body = rec.Body.String()
	if strings.Contains(body, "js-events-data") {
		t.Fatalf("expected raw fallback for invalid JSONL: %s", body)
	}
	if !strings.Contains(body, "not json at all") {
		t.Fatalf("expected raw events text in fallback: %s", body)
	}
}

func TestDrawer_ActivityJsonTreeTruncated(t *testing.T) {
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
	issueID, projectID := seedIssueWithPhases(t, eng)

	// An events.jsonl larger than the payload cap: truncation must cut at a
	// line boundary so the tree view still renders (implementation logs are
	// the ones that grow this large).
	line := `{"type":"model_turn","role":"model","content":"` + strings.Repeat("x", 1024) + `"}` + "\n"
	var buf strings.Builder
	for buf.Len() < drawerPayloadCap+4096 {
		buf.WriteString(line)
	}
	ctx := context.Background()
	if err := eng.Store().Write(ctx, storage.EventsPath(projectID, issueID, "step-3"), []byte(buf.String())); err != nil {
		t.Fatalf("write events: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=activity&phase=step-3", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("activity status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "js-events-data") || !strings.Contains(body, "json-tree") {
		t.Fatalf("expected json tree for truncated events, got fallback: %.500s", body)
	}
	if !strings.Contains(body, "truncated") {
		t.Fatalf("expected truncation note in body")
	}
}

func TestStepKeyIn(t *testing.T) {
	steps := []sqlite.Step{
		{Key: "step-1", AgentID: "researcher", Index: 1},
		{Key: "step-2", AgentID: "implementer", Index: 2},
	}
	if !stepKeyIn(steps, "step-1") || !stepKeyIn(steps, "step-2") {
		t.Fatalf("known keys must match")
	}
	if stepKeyIn(steps, "research") || stepKeyIn(steps, "nope") || stepKeyIn(steps, "") {
		t.Fatalf("unknown keys must not match")
	}
	if stepIndexOf(steps, "step-2") != 1 || stepIndexOf(steps, "nope") != -1 {
		t.Fatalf("stepIndexOf wrong")
	}
}
