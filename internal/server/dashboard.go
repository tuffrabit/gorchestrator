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
	phase := normalizePhase(r.URL.Query().Get("phase"))
	if phase == "" {
		phase = normalizePhase(view.Issue.CurrentPhase)
	}
	if phase == "" {
		phase = phaseResearch
	}
	// Deep links that used ?drawer=diff historically should land on implementation.
	if r.URL.Query().Get("tab") == "diff" {
		phase = phaseImplementation
	}
	content, contentHTML, err := s.drawerContent(r, view, tab, phase)
	if err != nil {
		content = err.Error()
	}
	// Build phase strip metadata for in-drawer tabs.
	phaseTabs := make([]map[string]any, 0, len(knownPhases))
	for _, p := range knownPhases {
		st := "pending"
		for _, step := range view.Phases {
			if step.Name == p {
				st = step.State
				break
			}
		}
		phaseTabs = append(phaseTabs, map[string]any{
			"Name":    p,
			"Label":   phaseLabel(p),
			"Agent":   phaseAgent(p),
			"State":   st,
			"Current": p == phase,
		})
	}
	data := map[string]any{
		"Issue":       view,
		"Tab":         tab,
		"Phase":       phase,
		"PhaseLabel":  phaseLabel(phase),
		"PhaseTabs":   phaseTabs,
		"Content":     content,
		"ContentHTML": contentHTML,
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
	ws := storage.WorkspacePath(issue.ProjectID, issue.ID)
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
	data := s.submitFormData(r, r.URL.Query().Get("project"), "", false)
	if err := render(w, "partials/drawer_submit.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePartialSubmitFlavors(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		project = r.FormValue("project")
	}
	data := s.submitFormData(r, project, "", false)
	if err := render(w, "partials/submit_flavors.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	flavors := map[string]string{}
	for _, typ := range []string{"researcher", "planner", "implementer"} {
		if v := r.FormValue("agent_flavor_" + typ); v != "" {
			flavors[typ] = v
		}
	}
	attachments, err := collectFormAttachments(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	issue, err := s.eng.SubmitIssue(r.Context(), orchestrator.RunOptions{
		ProjectName:  project,
		IssueTitle:   title,
		Description:  description,
		Attachments:  attachments,
		DryRun:       dryRun,
		AgentFlavors: flavors,
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
		"agent_flavors": parseAgentFlavorsJSON(issue.AgentFlavorsJSON),
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

// submitFormData builds template data for the New-issue drawer.
func (s *Server) submitFormData(r *http.Request, selectedProject, title string, dryRun bool) map[string]any {
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
	data := map[string]any{
		"Projects":        projects,
		"SelectedProject": selectedProject,
		"Title":           title,
		"Description":     "",
		"DryRun":          dryRun,
		"CSRF":            auth.CSRFToken(r),
		"FlavorSelects":   s.flavorSelectsForProject(selectedProject),
	}
	return data
}

// flavorSelectOption is one option in a flavor <select>.
type flavorSelectOption struct {
	Name     string
	Selected bool
}

// flavorSelect is a multi-flavor stage shown in the submit form.
type flavorSelect struct {
	Type    string
	Label   string
	Options []flavorSelectOption
}

func (s *Server) flavorSelectsForProject(projectName string) []flavorSelect {
	if projectName == "" {
		return nil
	}
	pc, ok := s.eng.ProjectConfig(projectName)
	if !ok {
		return nil
	}
	catalog := pc.FlavorCatalog()
	labels := map[string]string{
		"researcher":  "Researcher flavor",
		"planner":     "Planner flavor",
		"implementer": "Implementer flavor",
	}
	var out []flavorSelect
	for _, typ := range []string{"researcher", "planner", "implementer"} {
		info := catalog[typ]
		if len(info.Flavors) <= 1 {
			continue
		}
		opts := make([]flavorSelectOption, 0, len(info.Flavors))
		for _, name := range info.Flavors {
			opts = append(opts, flavorSelectOption{
				Name:     name,
				Selected: name == info.Default,
			})
		}
		out = append(out, flavorSelect{
			Type:    typ,
			Label:   labels[typ],
			Options: opts,
		})
	}
	return out
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
	// agent type → server allowlist from global defaults + project flavors
	agentServers := map[string]map[string]struct{}{
		"researcher":  {},
		"planner":     {},
		"implementer": {},
	}
	for _, typ := range []string{"researcher", "planner", "implementer"} {
		ac := cfg.Agent(typ)
		for _, name := range ac.MCPServers {
			agentServers[typ][name] = struct{}{}
		}
	}
	for _, pc := range cfg.Projects {
		for _, typ := range []string{"researcher", "planner", "implementer"} {
			pac, ok := pc.Agents[typ]
			if !ok {
				continue
			}
			for _, fl := range pac.Flavors {
				for _, name := range fl.MCPServers {
					agentServers[typ][name] = struct{}{}
				}
			}
		}
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
