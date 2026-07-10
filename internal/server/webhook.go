package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

// handleWebhookIssue accepts externally submitted issues without a session cookie.
// Auth: X-Gorch-Token or Authorization: Bearer <token> matching triggers.webhook.token_env.
func (s *Server) handleWebhookIssue(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Triggers.Webhook.Enabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook disabled"})
		return
	}
	tokenEnv := s.cfg.Triggers.Webhook.TokenEnv
	if tokenEnv == "" {
		tokenEnv = "GORCH_WEBHOOK_TOKEN"
	}
	want := os.Getenv(tokenEnv)
	if want == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook token not configured"})
		return
	}
	got := r.Header.Get("X-Gorch-Token")
	if got == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			got = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}

	var body struct {
		Project string `json:"project"`
		Title   string `json:"title"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.Project == "" || body.Title == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "project and title required"})
		return
	}

	issue, err := s.eng.SubmitIssue(r.Context(), orchestrator.RunOptions{
		ProjectName: body.Project,
		IssueTitle:  body.Title,
		Source:      "webhook",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":     issue.ID,
		"status": issue.Status,
		"source": issue.Source,
	})
}
