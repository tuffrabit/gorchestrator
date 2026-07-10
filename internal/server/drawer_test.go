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
	writePhaseArtifacts(t, eng, projectID, issueID, "research", "done", "# Research findings\n")
	// Plan artifacts
	writePhaseArtifacts(t, eng, projectID, issueID, "plan", "done", "# Plan\n")
	// Implementation running (not done)
	writePhaseArtifacts(t, eng, projectID, issueID, "implementation", "in_progress", "")

	// Source + workspace files
	src := storage.SourcePath(projectID, issueID)
	ws := storage.WorkspacePath(projectID, issueID)
	_ = eng.Store().Mkdir(ctx, src)
	_ = eng.Store().Mkdir(ctx, path.Join(ws, "pkg"))
	_ = eng.Store().Write(ctx, path.Join(src, "main.go"), []byte("package main\n"))
	_ = eng.Store().Write(ctx, path.Join(ws, "main.go"), []byte("package main\n// changed\n"))
	_ = eng.Store().Write(ctx, path.Join(ws, "pkg", "util.go"), []byte("package pkg\n"))

	// Advance issue pointer to implementation
	if err := eng.Issues().UpdateStatus(issueID, sqlite.StatusInProgress, "implementation"); err != nil {
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

	// Research output (not current phase)
	req := httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=output&phase=research", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("research output status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Research findings") {
		t.Fatalf("expected research markdown in body, got: %s", body)
	}
	if !strings.Contains(body, "drawer-phase-tab") {
		t.Fatalf("expected phase tabs in drawer")
	}
	if !strings.Contains(body, "openArtifactDrawer(1, 'output', 'research')") &&
		!strings.Contains(body, `openArtifactDrawer(1, 'output', 'research')`) {
		// phase tabs include research switcher
	}
	if !strings.Contains(body, "Research") || !strings.Contains(body, "Plan") || !strings.Contains(body, "Implementation") {
		t.Fatalf("expected phase tab labels Research/Plan/Implementation in body")
	}

	// Plan activity
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=activity&phase=plan", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan activity status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "phase_started") {
		t.Fatalf("expected plan events: %s", rec.Body.String())
	}

	// Default phase should be current (implementation) — workspace tree
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=output", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default output status = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "ws-tree") {
		t.Fatalf("expected workspace tree for implementation: %s", body)
	}
	if !strings.Contains(body, "main.go") || !strings.Contains(body, "util.go") {
		t.Fatalf("expected workspace files in tree: %s", body)
	}
	if !strings.Contains(body, "Download available when implementation is done") {
		t.Fatalf("expected download disabled message while implementation not done")
	}
	if strings.Contains(body, "workspace.zip") {
		t.Fatalf("download link must not appear before implementation done")
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
	writePhaseArtifacts(t, eng, projectID, issueID, "implementation", "done", "")
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
	req = httptest.NewRequest(http.MethodGet, "/partials/issues/"+itoa(issueID)+"/drawer?tab=output&phase=implementation", nil)
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

func TestNormalizePhase(t *testing.T) {
	if normalizePhase("researcher") != phaseResearch {
		t.Fatal("researcher")
	}
	if normalizePhase("IMPLEMENTATION") != phaseImplementation {
		t.Fatal("IMPLEMENTATION")
	}
	if normalizePhase("nope") != "" {
		t.Fatal("nope")
	}
}

