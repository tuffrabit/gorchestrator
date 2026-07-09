package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
