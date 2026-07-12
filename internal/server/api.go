package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/auth"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

type submitIssueRequest struct {
	Project      string            `json:"project"`
	Title        string            `json:"title"`
	Body         string            `json:"body"`         // optional description (trigger/API name)
	Description  string            `json:"description"`  // alias for body
	Source       string            `json:"source"`       // rejected if set
	SourcePath   string            `json:"source_path"`  // rejected if set
	DryRun       bool              `json:"dry_run"`
	AgentFlavors map[string]string `json:"agent_flavors"`
}

type decideRequest struct {
	Decision string `json:"decision"`
	Feedback string `json:"feedback"`
	Phase    string `json:"phase"`
	Force    bool   `json:"force"`

	// BudgetProvider + BudgetCeiling set an absolute session override for one provider.
	BudgetProvider string `json:"budget_provider"`
	BudgetCeiling  int    `json:"budget_ceiling"`
}

func (s *Server) handleSubmitIssue(w http.ResponseWriter, r *http.Request) {
	var req submitIssueRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "invalid json: "+err.Error())
		return
	}
	if req.Project == "" || req.Title == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, "project and title are required")
		return
	}
	if req.Source != "" || req.SourcePath != "" {
		writeJSONError(w, http.StatusUnprocessableEntity, "source/source_path is not accepted; configure projects.<name>.source_path in YAML")
		return
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = strings.TrimSpace(req.Body)
	}
	issue, err := s.eng.SubmitIssue(r.Context(), orchestrator.RunOptions{
		ProjectName:  req.Project,
		IssueTitle:   req.Title,
		Description:  desc,
		DryRun:       req.DryRun,
		AgentFlavors: req.AgentFlavors,
	})
	if err != nil {
		msg := err.Error()
		if isSubmitClientError(msg) {
			writeJSONError(w, http.StatusUnprocessableEntity, msg)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, msg)
		return
	}

	u := auth.UserFromContext(r.Context())
	var uid *int64
	if u != nil {
		uid = &u.ID
	}
	_ = s.eng.Audit().Record(uid, "submit_issue", "issue", orchestrator.IssueIDString(issue.ID), map[string]any{
		"project":       req.Project,
		"title":         req.Title,
		"has_description": desc != "",
		"dry_run":       req.DryRun,
		"agent_flavors": parseAgentFlavorsJSON(issue.AgentFlavorsJSON),
	})

	view, err := s.eng.GetIssue(r.Context(), issue.ID)
	if err != nil {
		writeJSON(w, http.StatusAccepted, issueToJSON(issue, req.Project, 0, 0, ""))
		return
	}
	writeJSON(w, http.StatusAccepted, viewToJSON(view))
}

func parseAgentFlavorsJSON(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" || raw == "{}" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (s *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	f := sqlite.IssueListFilter{}
	if p := r.URL.Query().Get("project"); p != "" {
		// resolve project name → id
		projects, err := s.eng.ListProjects(r.Context())
		if err == nil {
			for _, proj := range projects {
				if proj.Name == p {
					f.ProjectID = proj.ID
					break
				}
			}
		}
	}
	if st := r.URL.Query().Get("status"); st != "" {
		f.Status = st
	}
	if lim := r.URL.Query().Get("limit"); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil {
			f.Limit = n
		}
	}

	views, err := s.eng.ListIssues(r.Context(), f)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		out = append(out, viewToJSON(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": out})
}

func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid issue id")
		return
	}
	view, err := s.eng.GetIssue(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if view == nil {
		writeJSONError(w, http.StatusNotFound, "issue not found")
		return
	}
	writeJSON(w, http.StatusOK, viewToJSON(view))
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid issue id")
		return
	}
	rel := r.PathValue("path")
	rel = strings.TrimPrefix(rel, "/")
	data, key, err := s.eng.ReadArtifact(r.Context(), id, rel)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such") {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		if strings.Contains(err.Error(), "traversal") || strings.Contains(err.Error(), "absolute") || strings.Contains(err.Error(), "escapes") {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	ct := contentTypeFor(key)
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid issue id")
		return
	}
	if err := s.eng.DeleteIssue(r.Context(), id); err != nil {
		if errors.Is(err, orchestrator.ErrIssueNotFound) {
			writeJSONError(w, http.StatusNotFound, "issue not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := auth.UserFromContext(r.Context())
	var uid *int64
	if u != nil {
		uid = &u.ID
	}
	_ = s.eng.Audit().Record(uid, "delete_issue", "issue", orchestrator.IssueIDString(id), nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid issue id")
		return
	}
	var req decideRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "invalid json: "+err.Error())
		return
	}
	req.Decision = strings.ToLower(strings.TrimSpace(req.Decision))
	if req.Decision != "pass" && req.Decision != "fail" && req.Decision != "retry" {
		writeJSONError(w, http.StatusUnprocessableEntity, "decision must be pass|fail|retry")
		return
	}

	warning := ""
	if (req.Decision == "fail" || req.Decision == "retry") && strings.TrimSpace(req.Feedback) == "" {
		warning = "feedback is recommended for fail/retry"
	}

	u := auth.UserFromContext(r.Context())
	decidedBy := "api"
	var uid *int64
	if u != nil {
		decidedBy = fmt.Sprintf("%d", u.ID)
		if u.Email != "" {
			decidedBy = u.Email
		}
		uid = &u.ID
	}

	var budgetOverrides map[string]int
	if req.BudgetProvider != "" && req.BudgetCeiling > 0 {
		budgetOverrides = map[string]int{req.BudgetProvider: req.BudgetCeiling}
	}
	if err := s.eng.Decide(r.Context(), orchestrator.DecideOptions{
		IssueID:         id,
		Decision:        req.Decision,
		Feedback:        req.Feedback,
		Phase:           req.Phase,
		DecidedBy:       decidedBy,
		UserID:          uid,
		Force:           req.Force,
		BudgetOverrides: budgetOverrides,
	}); err != nil {
		if strings.Contains(err.Error(), "not waiting") {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = s.eng.Audit().Record(uid, "decide", "issue", orchestrator.IssueIDString(id), map[string]any{
		"decision": req.Decision,
		"feedback": truncate(req.Feedback, 200),
		"phase":    req.Phase,
	})

	view, _ := s.eng.GetIssue(r.Context(), id)
	resp := map[string]any{"ok": true}
	if warning != "" {
		resp["warning"] = warning
	}
	if view != nil {
		resp["issue"] = viewToJSON(view)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	// Prefer YAML-registered projects with flavor catalogs for the submit UI.
	// Include any orphan DB-only rows (historical) without agents catalog.
	registered, err := s.eng.ListRegisteredProjects(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	seen := map[string]bool{}
	out := make([]map[string]any, 0, len(registered))
	for _, rp := range registered {
		seen[rp.Project.Name] = true
		agents := map[string]any{}
		for typ, info := range rp.Agents {
			agents[typ] = map[string]any{
				"default": info.Default,
				"flavors": info.Flavors,
			}
		}
		out = append(out, map[string]any{
			"id":         rp.Project.ID,
			"name":       rp.Project.Name,
			"created_at": rp.Project.CreatedAt,
			"registered": true,
			"agents":     agents,
		})
	}
	all, err := s.eng.ListProjects(r.Context())
	if err == nil {
		for _, p := range all {
			if seen[p.Name] {
				continue
			}
			out = append(out, map[string]any{
				"id":         p.ID,
				"name":       p.Name,
				"created_at": p.CreatedAt,
				"registered": false,
				"agents":     map[string]any{},
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	rows, err := s.eng.Notifications().ListRecent(50)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pending, _ := s.eng.Notifications().CountPendingHumanGates()
	out := make([]map[string]any, 0, len(rows))
	for _, n := range rows {
		m := map[string]any{
			"id":         n.ID,
			"kind":       n.Kind,
			"recipient":  n.Recipient,
			"subject":    n.Subject,
			"body":       n.Body,
			"status":     n.Status,
			"error":      n.Error,
			"created_at": n.CreatedAt,
		}
		if n.IssueID.Valid {
			m["issue_id"] = n.IssueID.Int64
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"notifications": out,
		"pending_gates": pending,
	})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	filter := orchestrator.EventFilter{}
	if idStr := r.URL.Query().Get("issue_id"); idStr != "" {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			filter.IssueID = id
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.eng.Subscribe(r.Context(), filter)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := jsonMarshal(ev)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}

func viewToJSON(v *orchestrator.IssueView) map[string]any {
	if v == nil || v.Issue == nil {
		return nil
	}
	m := issueToJSON(v.Issue, v.ProjectName, v.TokenTotal, v.Attempt, v.PhaseStatus)
	if v.HoldReason != "" {
		m["hold_reason"] = v.HoldReason
	}
	if len(v.Attachments) > 0 {
		m["attachments"] = v.Attachments
	} else {
		m["attachments"] = []string{}
	}
	return m
}

func issueToJSON(i *sqlite.Issue, project string, tokens, attempt int, phaseStatus string) map[string]any {
	return map[string]any{
		"id":             i.ID,
		"project_id":     i.ProjectID,
		"project":        project,
		"title":          i.Title,
		"description":    i.Description,
		"status":         i.Status,
		"current_phase":  i.CurrentPhase,
		"dry_run":        i.DryRun,
		"agent_flavors":  parseAgentFlavorsJSON(i.AgentFlavorsJSON),
		"created_at":     i.CreatedAt,
		"updated_at":     i.UpdatedAt,
		"token_total":    tokens,
		"attempt":        attempt,
		"phase_status":   phaseStatus,
	}
}

func contentTypeFor(key string) string {
	switch path.Ext(key) {
	case ".json":
		return "application/json; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".jsonl":
		return "application/x-ndjson; charset=utf-8"
	case ".go", ".py", ".ts", ".js", ".yaml", ".yml", ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func isSubmitClientError(msg string) bool {
	return strings.Contains(msg, "unknown project") ||
		strings.Contains(msg, "not declared") ||
		strings.Contains(msg, "no projects declared") ||
		strings.Contains(msg, "flavor") ||
		strings.Contains(msg, "agent_flavors") ||
		strings.Contains(msg, "has no ") ||
		strings.Contains(msg, "attachment") ||
		strings.Contains(msg, "description exceeds") ||
		strings.Contains(msg, "at most ")
}

func jsonMarshal(v any) ([]byte, error) {
	return jsonMarshalImpl(v)
}
