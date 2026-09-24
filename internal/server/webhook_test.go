package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

// Webhook submits carry no flow picker, so they resolve to the project's
// default_flow — and a project without one must fail loudly rather than
// inventing a pipeline.

func webhookServer(t *testing.T, withDefaultFlow bool) *Server {
	t.Helper()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Triggers.Webhook.Enabled = true
	cfg.Triggers.Webhook.TokenEnv = "GORCH_TEST_WEBHOOK_TOKEN"
	if withDefaultFlow {
		cfg.Projects["acme"] = config.ProjectConfig{DefaultFlow: []string{"researcher", "implementer"}}
	} else {
		cfg.Projects["acme"] = config.ProjectConfig{} // no default_flow
	}
	t.Setenv("GORCH_TEST_WEBHOOK_TOKEN", "s3cret")

	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return srv
}

func postWebhook(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/issues", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gorch-Token", os.Getenv("GORCH_TEST_WEBHOOK_TOKEN"))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestWebhook_Issue_UsesProjectDefaultFlow(t *testing.T) {
	srv := webhookServer(t, true)
	rec := postWebhook(t, srv, `{"project":"acme","title":"from webhook","body":"do it"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID   int64 `json:"id"`
		Flow []struct {
			Index int    `json:"index"`
			Agent string `json:"agent"`
			Key   string `json:"key"`
		} `json:"flow"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse body: %v (%s)", err, rec.Body.String())
	}
	if len(out.Flow) != 2 {
		t.Fatalf("flow = %#v, want the project default_flow", out.Flow)
	}
	if out.Flow[0].Agent != "researcher" || out.Flow[0].Key != "step-1" {
		t.Fatalf("flow[0] = %#v", out.Flow[0])
	}
}

func TestWebhook_Issue_NoDefaultFlowIs422(t *testing.T) {
	srv := webhookServer(t, false)
	rec := postWebhook(t, srv, `{"project":"acme","title":"from webhook","body":"do it"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "default_flow") {
		t.Fatalf("error should name default_flow: %s", rec.Body.String())
	}
}
