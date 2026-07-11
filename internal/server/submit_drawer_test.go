package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

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
