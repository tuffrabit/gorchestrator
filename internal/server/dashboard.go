package server

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/auth"
	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/web"
)

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	s.renderFeed(w, r, 0, "", "")
}

func (s *Server) handleFeedIssue(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	drawer := r.URL.Query().Get("drawer")
	phase := r.URL.Query().Get("phase")
	s.renderFeed(w, r, id, drawer, phase)
}

func (s *Server) renderFeed(w http.ResponseWriter, r *http.Request, expandID int64, drawer, drawerPhase string) {
	f := s.listFilterFromRequest(r)
	views, err := s.eng.ListIssues(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projects, _ := s.eng.ListProjects(r.Context())
	pending, _ := s.eng.Notifications().CountPendingHumanGates()
	u := auth.UserFromContext(r.Context())

	data := map[string]any{
		"User":          u,
		"CSRF":          auth.CSRFToken(r),
		"Issues":        views,
		"Projects":      projects,
		"PendingGates":  pending,
		"ExpandID":      expandID,
		"Drawer":        drawer,
		"DrawerPhase":   drawerPhase,
		"FilterStatus":  f.Status,
		"FilterProject": r.URL.Query().Get("project"),
		"CanWrite":      u != nil && roleAtLeast(u.Role, auth.RoleMember),
		"IsAdmin":       u != nil && roleAtLeast(u.Role, auth.RoleAdmin),
	}
	if err := render(w, "feed.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleNotificationsPage(w http.ResponseWriter, r *http.Request) {
	rows, err := s.eng.Notifications().ListRecent(50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Also list waiting_human issues as pending gates.
	gates, _ := s.eng.ListIssues(r.Context(), sqlite.IssueListFilter{Status: sqlite.StatusWaitingHuman, Limit: 50})
	pending, _ := s.eng.Notifications().CountPendingHumanGates()
	u := auth.UserFromContext(r.Context())
	data := map[string]any{
		"User":          u,
		"CSRF":          auth.CSRFToken(r),
		"Notifications": rows,
		"Gates":         gates,
		"PendingGates":  pending,
		"CanWrite":      u != nil && roleAtLeast(u.Role, auth.RoleMember),
		"IsAdmin":       u != nil && roleAtLeast(u.Role, auth.RoleAdmin),
	}
	if err := render(w, "notifications.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePartialsIssues(w http.ResponseWriter, r *http.Request) {
	f := s.listFilterFromRequest(r)
	views, err := s.eng.ListIssues(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u := auth.UserFromContext(r.Context())
	data := map[string]any{
		"Issues":   views,
		"ExpandID": int64(0),
		"CSRF":     auth.CSRFToken(r),
		"CanWrite": u != nil && roleAtLeast(u.Role, auth.RoleMember),
		"IsAdmin":  u != nil && roleAtLeast(u.Role, auth.RoleAdmin),
	}
	if err := render(w, "partials/issue_list.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePartialIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	view, err := s.eng.GetIssue(r.Context(), id)
	if err != nil || view == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	expanded := r.URL.Query().Get("expanded") == "1"
	u := auth.UserFromContext(r.Context())
	data := map[string]any{
		"Issue":    view,
		"Expanded": expanded,
		"CSRF":     auth.CSRFToken(r),
		"CanWrite": u != nil && roleAtLeast(u.Role, auth.RoleMember),
		"IsAdmin":  u != nil && roleAtLeast(u.Role, auth.RoleAdmin),
	}
	if err := render(w, "partials/issue_card.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePartialDrawer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "result"
	}
	// Full-diff tab removed; legacy ?tab=diff → implementation workspace tree.
	if tab == "diff" {
		tab = "output"
	}
	view, err := s.eng.GetIssue(r.Context(), id)
	if err != nil || view == nil || view.Issue == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Resolve the requested step key against the issue's frozen flow.
	steps, err := s.eng.StepsForIssue(view.Issue)
	if err != nil || len(steps) == 0 {
		http.Error(w, "issue has no agent flow", http.StatusNotFound)
		return
	}
	phase := r.URL.Query().Get("phase")
	if !stepKeyIn(steps, phase) {
		if stepKeyIn(steps, view.Issue.CurrentPhase) {
			phase = view.Issue.CurrentPhase
		} else if tab == "output" || tab == "workspace" {
			phase = steps[len(steps)-1].Key
		} else {
			phase = steps[0].Key
		}
	}
	payload, err := s.drawerContent(r, view, tab, phase)
	if err != nil {
		payload.Content = err.Error()
	}
	// Build step strip metadata for in-drawer tabs: one tab per flow step,
	// plus a dedicated Workspace tab whenever the issue has a workspace.
	phaseTabs := make([]map[string]any, 0, len(steps)+1)
	for _, p := range view.Phases {
		phaseTabs = append(phaseTabs, map[string]any{
			"Name":    p.Key,
			"Label":   fmt.Sprintf("%d · %s", p.Index, p.AgentID),
			"Agent":   p.AgentID,
			"State":   p.State,
			"Current": p.Key == phase && tab != "workspace",
		})
	}
	wsKey, werr := s.eng.WorkspaceKey(r.Context(), view.Issue)
	if werr == nil {
		if wsExists, _ := s.eng.Store().Exists(r.Context(), wsKey); wsExists {
			phaseTabs = append(phaseTabs, map[string]any{
				"Name":    "workspace",
				"Label":   "Workspace",
				"Agent":   "",
				"State":   "done",
				"Current": tab == "workspace",
			})
		}
	}
	data := map[string]any{
		"Issue":       view,
		"Tab":         tab,
		"Phase":       phase,
		"PhaseLabel":  phase,
		"PhaseTabs":   phaseTabs,
		"Content":     payload.Content,
		"ContentHTML": payload.ContentHTML,
		"EventsJSON":  payload.EventsJSON,
		"Truncated":   payload.Truncated,
		"CSRF":        auth.CSRFToken(r),
	}
	if err := render(w, "partials/drawer_artifact.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePartialWorkspaceFile returns a single-file source-vs-workspace diff partial.
func (s *Server) handlePartialWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if err := storage.ValidateRelativePath(rel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	view, err := s.eng.GetIssue(r.Context(), id)
	if err != nil || view == nil || view.Issue == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	issue := view.Issue
	ws, werr := s.eng.WorkspaceKey(r.Context(), issue)
	if werr != nil {
		http.Error(w, werr.Error(), http.StatusInternalServerError)
		return
	}
	src := storage.SourcePath(issue.ProjectID, issue.ID)
	diff, err := singleFileDiff(r.Context(), s.eng.Store(), src, ws, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Path": rel,
		"Diff": diff,
	}
	if err := render(w, "partials/drawer_workspace_file.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePartialSubmit(w http.ResponseWriter, r *http.Request) {
	data := s.submitFormData(r, r.URL.Query().Get("project"), nil, "", false)
	if err := render(w, "partials/drawer_submit.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePartialSubmitFlow re-renders the flow builder. The hidden flow_state
// input is the authoritative ordered flow (length-prefixed so blanks are
// preserved); the request adds exactly one change event (a changed select,
// carrying its row index as flow_pick), a flow_remove index, or a flow_add.
func (s *Server) handlePartialSubmitFlow(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := q.Get("project")
	if project == "" {
		project = r.FormValue("project")
	}
	flow := decodeFlowCSV(q.Get("flow_state"))
	switch {
	case q.Get("flow_add") != "":
		if len(flow) < maxFlowSteps {
			flow = append(flow, "")
		}
	case q.Get("flow_remove") != "":
		if i, err := strconv.Atoi(q.Get("flow_remove")); err == nil && i >= 1 && i <= len(flow) {
			flow = append(flow[:i-1], flow[i:]...)
		}
	default:
		// A select's change event. The select carries the 1-based index of the
		// row it belongs to (flow_pick), so its value is applied at exactly that
		// slot. We deliberately do NOT infer the changed row from the order of
		// the flow_agent values: htmx appends the triggering element's value
		// first and then the rest in DOM order, so positional diffing corrupts
		// the rows above the one that changed.
		pick, err := strconv.Atoi(q.Get("flow_pick"))
		if err != nil || pick < 1 {
			break
		}
		// htmx sends the triggering select's value first.
		picked := ""
		if vals := q["flow_agent"]; len(vals) > 0 {
			picked = strings.TrimSpace(vals[0])
		}
		for len(flow) < pick && len(flow) < maxFlowSteps {
			flow = append(flow, "")
		}
		if len(flow) < pick {
			break
		}
		flow[pick-1] = picked
	}
	data := s.submitFormData(r, project, flow, "", false)
	if err := render(w, "partials/submit_flow.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// maxFlowSteps mirrors orchestrator.maxFlowSteps for the submit UI cap.
const maxFlowSteps = 8

// encodeFlowCSV encodes the authoritative ordered flow for the hidden
// flow_state input. The leading count keeps the encoding length-preserving so
// the handler can tell "no steps" ("") apart from "one or more unpicked
// steps" ("1:", "2:,", …): a bare comma-join collapses those, since
// strings.Join([""], ",") == "" and round-trips to zero rows.
func encodeFlowCSV(flow []string) string {
	return fmt.Sprintf("%d:%s", len(flow), strings.Join(flow, ","))
}

// decodeFlowCSV parses a flow_state string back into the ordered flow, keeping
// blank slots ("1:a:" → ["a", ""). splitFlowCSV remains as a fallback for
// legacy/unprefixed values.
func decodeFlowCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	idx := strings.IndexByte(raw, ':')
	if idx < 0 {
		return splitFlowCSV(raw)
	}
	n, err := strconv.Atoi(raw[:idx])
	if err != nil || n < 0 {
		return splitFlowCSV(raw)
	}
	if n == 0 {
		return nil
	}
	rest := strings.Split(raw[idx+1:], ",")
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if i < len(rest) {
			out = append(out, strings.TrimSpace(rest[i]))
		} else {
			out = append(out, "")
		}
	}
	return out
}

// splitFlowCSV splits an authoritative flow-state CSV, keeping blank slots
// ("a,,b" → ["a", "", "b"]).
func splitFlowCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func (s *Server) handlePartialSubmitPost(w http.ResponseWriter, r *http.Request) {
	if err := parseRequestForm(r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	project := r.FormValue("project")
	title := r.FormValue("title")
	description := strings.TrimSpace(r.FormValue("description"))
	if description == "" {
		description = strings.TrimSpace(r.FormValue("body"))
	}
	if r.FormValue("source") != "" || r.FormValue("source_path") != "" {
		http.Error(w, "source/source_path is not accepted; configure projects.<name>.source_path in YAML", http.StatusUnprocessableEntity)
		return
	}
	dryRun := r.FormValue("dry_run") == "on" || r.FormValue("dry_run") == "1" || r.FormValue("dry_run") == "true"
	if project == "" || title == "" {
		http.Error(w, "project and title required", http.StatusUnprocessableEntity)
		return
	}
	// The <select name="flow_agent"> elements serialize in DOM order —
	// r.PostForm[key] preserves document order, exactly the ordered flow we
	// need. Blank slots (unpicked steps) are dropped; the engine's resolveFlow
	// rejects the rest (unknown ids, duplicates, too long).
	flow := make([]string, 0)
	for _, v := range r.PostForm["flow_agent"] {
		if v = strings.TrimSpace(v); v != "" {
			flow = append(flow, v)
		}
	}
	attachments, err := collectFormAttachments(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	dependsOn, err := parseDependsOnForm(r.FormValue("depends_on"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	issue, err := s.eng.SubmitIssue(r.Context(), orchestrator.RunOptions{
		ProjectName: project,
		IssueTitle:  title,
		Description: description,
		Attachments: attachments,
		DryRun:      dryRun,
		Flow:        flow,
		DependsOn:   dependsOn,
	})
	if err != nil {
		msg := err.Error()
		if isSubmitClientError(msg) {
			http.Error(w, msg, http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	u := auth.UserFromContext(r.Context())
	var uid *int64
	if u != nil {
		uid = &u.ID
	}
	_ = s.eng.Audit().Record(uid, "submit_issue", "issue", orchestrator.IssueIDString(issue.ID), map[string]any{
		"project": project, "title": title, "has_description": description != "",
		"attachment_count": len(attachments), "dry_run": dryRun,
		"flow": pipelineStepObjects(issue.PipelineJSON),
	})
	view, _ := s.eng.GetIssue(r.Context(), issue.ID)
	// Return the new card partial; HTMX can prepend it.
	// After-Swap is the reliable close signal (requesting form is still in the
	// drawer); plain HX-Trigger is kept as a belt-and-suspenders fallback.
	w.Header().Set("HX-Trigger", "close-drawer")
	w.Header().Set("HX-Trigger-After-Swap", "close-drawer")
	data := map[string]any{
		"Issue":    view,
		"Expanded": false,
		"CSRF":     auth.CSRFToken(r),
		"CanWrite": true,
	}
	if err := render(w, "partials/issue_card.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// parseDependsOnForm parses a comma-separated issue ID list ("12,14").
func parseDependsOnForm(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("depends_on: invalid issue id %q", part)
		}
		out = append(out, id)
	}
	return out, nil
}

// collectFormAttachments reads multipart files named "attachments".
func collectFormAttachments(r *http.Request) ([]orchestrator.AttachmentFile, error) {
	if r.MultipartForm == nil || r.MultipartForm.File == nil {
		return nil, nil
	}
	headers := r.MultipartForm.File["attachments"]
	if len(headers) == 0 {
		return nil, nil
	}
	var out []orchestrator.AttachmentFile
	for _, fh := range headers {
		if fh == nil || fh.Filename == "" {
			continue
		}
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("open attachment %q: %w", fh.Filename, err)
		}
		// Hard cap slightly above orchestrator max so we fail fast on huge uploads.
		data, err := io.ReadAll(io.LimitReader(f, 2*1024*1024+1))
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", fh.Filename, err)
		}
		if len(data) > 2*1024*1024 {
			return nil, fmt.Errorf("attachment %q is too large", fh.Filename)
		}
		out = append(out, orchestrator.AttachmentFile{Name: fh.Filename, Data: data})
	}
	return out, nil
}

// submitFormData builds template data for the New-issue drawer. flow is the
// in-progress flow for the selected project; when empty it is prefilled
// from projects.<name>.default_flow.
func (s *Server) submitFormData(r *http.Request, selectedProject string, flow []string, title string, dryRun bool) map[string]any {
	registered, _ := s.eng.ListRegisteredProjects(r.Context())
	type projOpt struct {
		Name string
	}
	projects := make([]projOpt, 0, len(registered))
	for _, rp := range registered {
		projects = append(projects, projOpt{Name: rp.Project.Name})
	}
	if selectedProject == "" && len(projects) == 1 {
		selectedProject = projects[0].Name
	}
	var defaultFlow []string
	if pc, ok := s.eng.ProjectConfig(selectedProject); ok {
		defaultFlow = pc.DefaultFlow
	}
	// Only seed the project default on the very first render, when no
	// flow_state has been submitted at all. Once the user has interacted with
	// the builder (even to remove every step, which encodes as "0:") the
	// submitted flow is authoritative and must not be overwritten by the
	// default.
	if len(flow) == 0 && len(defaultFlow) > 0 && r.URL.Query().Get("flow_state") == "" {
		flow = defaultFlow
	}
	data := map[string]any{
		"Projects":        projects,
		"SelectedProject": selectedProject,
		"Title":           title,
		"Description":     "",
		"DependsOn":       "",
		"DryRun":          dryRun,
		"CSRF":            auth.CSRFToken(r),
		"Flow":            flow,
		"FlowCSV":         encodeFlowCSV(flow),
		"FlowSteps":       s.flowStepsForProject(selectedProject, flow),
		"AgentIDs":        s.eng.Cfg().AgentIDs(),
		"DefaultFlow":     defaultFlow,
		"HasDefaultFlow":  len(defaultFlow) > 0,
		"CanAddFlowStep":  len(flow) < maxFlowSteps,
	}
	return data
}

// flowStepOption is one option in a step's agent <select>.
type flowStepOption struct {
	ID       string
	Selected bool
}

// flowStep is one row of the flow builder: a 1-based step index plus the
// agent options (every configured agent id is valid at every position).
type flowStep struct {
	Index   int
	Options []flowStepOption
}

func (s *Server) flowStepsForProject(project string, current []string) []flowStep {
	if project == "" {
		return nil
	}
	if _, ok := s.eng.ProjectConfig(project); !ok {
		return nil
	}
	ids := s.eng.Cfg().AgentIDs()
	steps := make([]flowStep, 0, len(current))
	for i, sel := range current {
		opts := make([]flowStepOption, 0, len(ids)+1)
		// Unpicked step: lead with an explicit blank so the select does not
		// silently default to the first agent.
		if sel == "" {
			opts = append(opts, flowStepOption{ID: "", Selected: true})
		}
		for _, id := range ids {
			opts = append(opts, flowStepOption{ID: id, Selected: id == sel && sel != ""})
		}
		steps = append(steps, flowStep{Index: i + 1, Options: opts})
	}
	return steps
}

func (s *Server) handlePartialDeleteIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.eng.DeleteIssue(r.Context(), id); err != nil {
		if errors.Is(err, orchestrator.ErrIssueNotFound) {
			http.Error(w, "issue not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, orchestrator.ErrIssueActive) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u := auth.UserFromContext(r.Context())
	var uid *int64
	if u != nil {
		uid = &u.ID
	}
	_ = s.eng.Audit().Record(uid, "delete_issue", "issue", orchestrator.IssueIDString(id), nil)
	// Empty body + outerHTML swap removes the card from the feed.
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePartialStopIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.eng.StopIssue(r.Context(), id); err != nil {
		if errors.Is(err, orchestrator.ErrIssueNotFound) {
			http.Error(w, "issue not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	u := auth.UserFromContext(r.Context())
	var uid *int64
	if u != nil {
		uid = &u.ID
	}
	_ = s.eng.Audit().Record(uid, "stop_issue", "issue", orchestrator.IssueIDString(id), nil)
	view, _ := s.eng.GetIssue(r.Context(), id)
	data := map[string]any{
		"Issue":    view,
		"Expanded": true,
		"CSRF":     auth.CSRFToken(r),
		"CanWrite": true,
	}
	if err := render(w, "partials/issue_card.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePartialDecide(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := parseRequestForm(r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	decision := strings.ToLower(strings.TrimSpace(r.FormValue("decision")))
	feedback := r.FormValue("feedback")
	if decision != "pass" && decision != "fail" && decision != "retry" {
		http.Error(w, "decision must be pass|fail|retry (got empty or invalid — check submit button)", http.StatusUnprocessableEntity)
		return
	}
	var budgetOverrides map[string]int
	if prov := strings.TrimSpace(r.FormValue("budget_provider")); prov != "" {
		if ceil, err := strconv.Atoi(strings.TrimSpace(r.FormValue("budget_ceiling"))); err == nil && ceil > 0 {
			budgetOverrides = map[string]int{prov: ceil}
		}
	}
	u := auth.UserFromContext(r.Context())
	decidedBy := "dashboard"
	var uid *int64
	if u != nil {
		decidedBy = u.Email
		uid = &u.ID
	}
	if err := s.eng.Decide(r.Context(), orchestrator.DecideOptions{
		IssueID:         id,
		Decision:        decision,
		Feedback:        feedback,
		DecidedBy:       decidedBy,
		UserID:          uid,
		BudgetOverrides: budgetOverrides,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = s.eng.Audit().Record(uid, "decide", "issue", orchestrator.IssueIDString(id), map[string]any{
		"decision": decision, "feedback": truncate(feedback, 200),
	})
	view, _ := s.eng.GetIssue(r.Context(), id)
	data := map[string]any{
		"Issue":    view,
		"Expanded": true,
		"CSRF":     auth.CSRFToken(r),
		"CanWrite": true,
	}
	if err := render(w, "partials/issue_card.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) listFilterFromRequest(r *http.Request) sqlite.IssueListFilter {
	f := sqlite.IssueListFilter{Limit: 100}
	if st := r.URL.Query().Get("status"); st != "" {
		if st == "needs_you" {
			f.Status = sqlite.StatusWaitingHuman
		} else {
			f.Status = st
		}
	}
	if name := r.URL.Query().Get("project"); name != "" {
		projects, err := s.eng.ListProjects(r.Context())
		if err == nil {
			for _, p := range projects {
				if p.Name == name {
					f.ProjectID = p.ID
					break
				}
			}
		}
	}
	return f
}

func roleAtLeast(have, need string) bool {
	rank := map[string]int{auth.RoleViewer: 1, auth.RoleMember: 2, auth.RoleAdmin: 3}
	return rank[have] >= rank[need]
}

func render(w http.ResponseWriter, name string, data any) error {
	tmpl, err := web.Templates()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// layout executes named template
	if strings.HasPrefix(name, "partials/") {
		return tmpl.ExecuteTemplate(w, name, data)
	}
	// pages use layout
	return tmpl.ExecuteTemplate(w, name, data)
}

func staticFS() http.FileSystem {
	return http.FS(web.Static())
}

// compile-time check: FileServer needs http.FileSystem
var _ http.FileSystem = staticFS()

// Ensure template.HTML is available for drawer content.
var _ = template.HTML("")

func fmtIssue(id int64) string {
	return fmt.Sprintf("#%d", id)
}

func (s *Server) handlePermissionsPage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	pending, _ := s.eng.Notifications().CountPendingHumanGates()
	data := map[string]any{
		"User":         u,
		"CSRF":         auth.CSRFToken(r),
		"PendingGates": pending,
		"CanWrite":     u != nil && roleAtLeast(u.Role, auth.RoleMember),
		"IsAdmin":      u != nil && roleAtLeast(u.Role, auth.RoleAdmin),
		"Matrix":       s.permissionMatrix(),
	}
	if err := render(w, "permissions.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleAPIPermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"servers": s.permissionMatrix()})
}

// permissionMatrix builds a read-only view of MCP server → tools → agents.
func (s *Server) permissionMatrix() []map[string]any {
	cfg := s.eng.Cfg()
	if cfg == nil {
		return nil
	}
	// agent id → server allowlist from the global agents: block
	agentServers := map[string]map[string]struct{}{}
	for id, ac := range cfg.Agents {
		set := map[string]struct{}{}
		for _, name := range ac.MCPServers {
			set[name] = struct{}{}
		}
		agentServers[id] = set
	}

	var mgr interface {
		DiscoveredTools(string) []string
	}
	if m := s.eng.MCP(); m != nil {
		mgr = m
	}

	out := make([]map[string]any, 0, len(cfg.MCPServers))
	for _, srv := range cfg.MCPServers {
		// which agents can use this server
		agents := []string{}
		for typ, set := range agentServers {
			if _, ok := set[srv.Name]; ok {
				agents = append(agents, typ)
			}
		}
		// tools: grant list or discovered
		type toolRow struct {
			Name        string
			Constraints []config.MCPToolConstraint
			Granted     bool
		}
		var tools []map[string]any
		if len(srv.Tools) == 0 {
			// all discovered tools
			var names []string
			if mgr != nil {
				names = mgr.DiscoveredTools(srv.Name)
			}
			if len(names) == 0 {
				tools = append(tools, map[string]any{"name": "(all tools from server)", "granted": true, "constraints": []any{}})
			}
			for _, n := range names {
				tools = append(tools, map[string]any{"name": n, "granted": true, "constraints": []any{}})
			}
		} else {
			for _, g := range srv.Tools {
				tools = append(tools, map[string]any{
					"name":        g.Name,
					"granted":     true,
					"constraints": g.Constraints,
				})
			}
		}
		out = append(out, map[string]any{
			"name":    srv.Name,
			"command": srv.Command,
			"agents":  agents,
			"tools":   tools,
			"mode":    map[bool]string{true: "all_tools", false: "allowlist"}[len(srv.Tools) == 0],
		})
	}
	return out
}

func (s *Server) handleEscalationPage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	pending, _ := s.eng.Notifications().CountPendingHumanGates()
	var rules any
	var recent any
	if esc := s.eng.Escalator(); esc != nil {
		rules = esc.Rules()
		recent = esc.Recent()
	}
	data := map[string]any{
		"User":         u,
		"CSRF":         auth.CSRFToken(r),
		"PendingGates": pending,
		"CanWrite":     u != nil && roleAtLeast(u.Role, auth.RoleMember),
		"IsAdmin":      u != nil && roleAtLeast(u.Role, auth.RoleAdmin),
		"Rules":        rules,
		"Recent":       recent,
	}
	if err := render(w, "escalation.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
