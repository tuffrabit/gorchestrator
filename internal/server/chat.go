package server

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/auth"
	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// chatMessageMaxLen mirrors orchestrator.chatMaxMessageLen: anything longer
// is rejected up front so the drawer can show a validation error instead of
// a failed assistant turn.
const chatMessageMaxLen = 8000

// chatProjectOption is one entry in the chat project <select>.
type chatProjectOption struct {
	Name     string
	Selected bool
}

// chatIdentityOption is one entry in the chat agent-identity <select>.
// Value is "<agentType>" for the base identity or "<agentType>:<flavor>".
type chatIdentityOption struct {
	Value    string
	Label    string
	Selected bool
}

// chatMessageView is one rendered chat message row.
type chatMessageView struct {
	Role        string
	Status      string
	ToolName    string
	Content     string        // raw text (template-escaped)
	ContentHTML template.HTML // assistant replies pre-rendered from markdown
	Pending     bool          // assistant placeholder showing a stage string
	Error       bool          // assistant row that finished with an error
}

// handlePartialChat renders the chat drawer shell: project + identity selects,
// the thread for the default identity (inline, no lazy load), and the composer.
func (s *Server) handlePartialChat(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	registered, err := s.eng.ListRegisteredProjects(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projects := make([]chatProjectOption, 0, len(registered))
	var selected *orchestrator.RegisteredProject
	want := r.URL.Query().Get("project")
	for i := range registered {
		if registered[i].Project == nil {
			continue
		}
		projects = append(projects, chatProjectOption{Name: registered[i].Project.Name})
		if want != "" && registered[i].Project.Name == want {
			selected = &registered[i]
		}
	}
	if selected == nil && len(projects) > 0 {
		for i := range registered {
			if registered[i].Project != nil && registered[i].Project.Name == projects[0].Name {
				selected = &registered[i]
				break
			}
		}
	}
	for i := range projects {
		projects[i].Selected = selected != nil && projects[i].Name == selected.Project.Name
	}

	data := map[string]any{
		"Projects":        projects,
		"SelectedProject": "",
		"CSRF":            auth.CSRFToken(r),
		"CanWrite":        u != nil && roleAtLeast(u.Role, auth.RoleMember),
	}
	if selected != nil {
		agentType, flavor := s.defaultChatIdentity(selected.Project.Name)
		data["SelectedProject"] = selected.Project.Name
		data["IdentityOptions"] = s.chatIdentityOptions(selected.Project.Name, "")
		data["Thread"] = s.chatThreadData(r, selected, agentType, flavor, u)
	}
	if err := render(w, "partials/drawer_chat.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePartialChatOptions renders the <option> list for the identity select
// for one project.
func (s *Server) handlePartialChatOptions(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		project = r.FormValue("project")
	}
	if _, err := s.chatProject(r.Context(), project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := map[string]any{
		"Options": s.chatIdentityOptions(project, ""),
	}
	if err := render(w, "partials/chat_options.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePartialChatThread renders the message list + composer for one
// (project, agent, flavor) selection. Read-only: never creates a thread.
func (s *Server) handlePartialChatThread(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	q := r.URL.Query()
	project := q.Get("project")
	agentType, flavor := chatSelectionFromQuery(q.Get("agent"), q.Get("flavor"), q.Get("identity"))
	if err := validateChatSelection(agentType, flavor); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rp, err := s.chatProject(r.Context(), project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if agentType == "" && flavor == "" {
		// Project-select refresh path: fall back to the same default identity
		// the options list marks selected, so thread and select agree.
		agentType, flavor = s.defaultChatIdentity(project)
	}
	if !validChatAgentType(agentType) {
		http.Error(w, fmt.Sprintf("unknown agent type %q", agentType), http.StatusBadRequest)
		return
	}
	if err := s.validateChatFlavor(project, agentType, flavor); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	data := s.chatThreadData(r, rp, agentType, flavor, u)
	if err := render(w, "partials/chat_thread.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePartialChatSend validates the composer form, persists the message
// pair (user + pending assistant), kicks off async processing, and re-renders
// the thread. Deliberately sets no HX-Trigger: the drawer must stay open
// (unlike the submit drawer, which closes on success).
func (s *Server) handlePartialChatSend(w http.ResponseWriter, r *http.Request) {
	if err := parseRequestForm(r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	u := auth.UserFromContext(r.Context())
	project := strings.TrimSpace(r.FormValue("project"))
	agentType, flavor := chatSelectionFromQuery(r.FormValue("agent"), r.FormValue("flavor"), r.FormValue("identity"))
	message := strings.TrimSpace(r.FormValue("message"))

	if project == "" {
		http.Error(w, "project is required", http.StatusUnprocessableEntity)
		return
	}
	if message == "" {
		http.Error(w, "message is required", http.StatusUnprocessableEntity)
		return
	}
	if len(message) > chatMessageMaxLen {
		http.Error(w, fmt.Sprintf("message exceeds %d characters", chatMessageMaxLen), http.StatusUnprocessableEntity)
		return
	}
	rp, err := s.chatProject(r.Context(), project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if agentType == "" && flavor == "" {
		agentType, flavor = s.defaultChatIdentity(project)
	}
	if !validChatAgentType(agentType) {
		http.Error(w, fmt.Sprintf("unknown agent type %q", agentType), http.StatusUnprocessableEntity)
		return
	}
	if err := s.validateChatFlavor(project, agentType, flavor); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	uid, err := s.chatUserID(u, true)
	if err != nil {
		http.Error(w, "could not resolve user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	thread, err := s.eng.ChatRepo().GetOrCreateThread(uid, rp.Project.ID, agentType, flavor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.eng.ChatService().SendMessage(r.Context(), thread.ID, message); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "required") || strings.Contains(msg, "exceeds") {
			http.Error(w, msg, http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	data := s.chatThreadData(r, rp, agentType, flavor, u)
	if err := render(w, "partials/chat_thread.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// chatProject resolves a registered project by name for chat handlers.
func (s *Server) chatProject(ctx context.Context, name string) (*orchestrator.RegisteredProject, error) {
	if name == "" {
		return nil, fmt.Errorf("project is required")
	}
	registered, err := s.eng.ListRegisteredProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range registered {
		if registered[i].Project != nil && registered[i].Project.Name == name {
			return &registered[i], nil
		}
	}
	return nil, fmt.Errorf("unknown project %q", name)
}

// chatSelectionFromQuery resolves agent/flavor from the explicit params
// (agent + flavor, used by SSE refresh and tests) or the combined identity
// select value ("<agentType>" or "<agentType>:<flavor>").
func chatSelectionFromQuery(agent, flavor, identity string) (string, string) {
	if identity != "" {
		a, f, err := splitChatIdentity(identity)
		if err == nil {
			if agent == "" {
				agent = a
			}
			if flavor == "" {
				flavor = f
			}
		}
	}
	return strings.TrimSpace(agent), strings.TrimSpace(flavor)
}

func validateChatSelection(agent, flavor string) error {
	if agent == "" && flavor == "" {
		return nil // blank selection → default identity
	}
	if agent == "" {
		return fmt.Errorf("agent is required when flavor is set")
	}
	return nil
}

func splitChatIdentity(v string) (agent, flavor string, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "", fmt.Errorf("empty identity")
	}
	if i := strings.Index(v, ":"); i >= 0 {
		agent, flavor = v[:i], v[i+1:]
	} else {
		agent = v
	}
	if agent == "" {
		return "", "", fmt.Errorf("missing agent type in identity %q", v)
	}
	return agent, flavor, nil
}

func validChatAgentType(agent string) bool {
	for _, typ := range config.CoreAgentTypes {
		if typ == agent {
			return true
		}
	}
	return false
}

// validateChatFlavor accepts an empty flavor (base identity) and rejects
// named flavors the project does not define.
func (s *Server) validateChatFlavor(project, agent, flavor string) error {
	if flavor == "" {
		return nil
	}
	pc, ok := s.eng.ProjectConfig(project)
	if !ok {
		return fmt.Errorf("unknown project %q", project)
	}
	if _, _, err := pc.FlavorOverlay(agent, flavor); err != nil {
		return err
	}
	return nil
}

// defaultChatIdentity is the selection shown when nothing is preselected:
// the first core agent type, using its project-default flavor when present.
func (s *Server) defaultChatIdentity(project string) (agent, flavor string) {
	agent = config.CoreAgentTypes[0]
	pc, ok := s.eng.ProjectConfig(project)
	if !ok {
		return agent, ""
	}
	if info, ok := pc.FlavorCatalog()[agent]; ok {
		return agent, info.Default
	}
	return agent, ""
}

// chatIdentityOptions lists base identity + one option per flavor for every
// core agent type. When selected is empty the default identity (the first
// type's project-default flavor, else base) is marked selected — a <select>
// can only show one selection, so per-type defaults for later types are not
// marked (that would leave the select showing the last type's default).
func (s *Server) chatIdentityOptions(project, selected string) []chatIdentityOption {
	if selected == "" {
		agent, flavor := s.defaultChatIdentity(project)
		selected = agent
		if flavor != "" {
			selected += ":" + flavor
		}
	}
	pc, ok := s.eng.ProjectConfig(project)
	var catalog map[string]config.AgentFlavorInfo
	if ok {
		catalog = pc.FlavorCatalog()
	}
	out := make([]chatIdentityOption, 0, len(config.CoreAgentTypes)*3)
	for _, typ := range config.CoreAgentTypes {
		out = append(out, chatIdentityOption{
			Value:    typ,
			Label:    typ + " (base)",
			Selected: selected == typ,
		})
		for _, f := range catalog[typ].Flavors {
			val := typ + ":" + f
			out = append(out, chatIdentityOption{
				Value:    val,
				Label:    typ + ": " + f,
				Selected: selected == val,
			})
		}
	}
	return out
}

// chatUserID resolves the users-table row for the request user. With
// auth.mode=disabled the middleware injects a synthetic admin whose ID (0)
// references no row; chat_threads.user_id has a foreign key to users, so we
// reconcile by email and create the row on first write (mirroring
// ensureLocalUser). With create=false a missing row maps to ID 0, which
// matches no thread and renders the empty state.
func (s *Server) chatUserID(u *auth.User, create bool) (int64, error) {
	if u == nil {
		return 0, fmt.Errorf("no authenticated user")
	}
	existing, err := s.eng.Users().GetByEmail(u.Email)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}
	if u.ID > 0 {
		if dbu, err := s.eng.Users().Get(u.ID); err == nil && dbu != nil {
			return dbu.ID, nil
		}
	}
	if !create {
		return 0, nil
	}
	created, err := s.eng.Users().Create(u.Email, u.DisplayName, u.Role, nil, nil)
	if err != nil {
		// Concurrent first-writes can race the unique email; re-read.
		if existing, e2 := s.eng.Users().GetByEmail(u.Email); e2 == nil && existing != nil {
			return existing.ID, nil
		}
		return 0, err
	}
	return created.ID, nil
}

// chatThreadData builds the view model for chat_thread.html: the current
// selection, the message rows (assistant replies pre-rendered as markdown),
// and composer fields. Never creates a thread.
func (s *Server) chatThreadData(r *http.Request, rp *orchestrator.RegisteredProject, agentType, flavor string, u *auth.User) map[string]any {
	data := map[string]any{
		"Project":  rp.Project.Name,
		"Agent":    agentType,
		"Flavor":   flavor,
		"CSRF":     auth.CSRFToken(r),
		"CanWrite": u != nil && roleAtLeast(u.Role, auth.RoleMember),
	}
	uid, err := s.chatUserID(u, false)
	if err != nil || uid == 0 {
		return data // no user row yet → no threads yet → empty state
	}
	thread, err := s.eng.ChatRepo().FindThread(uid, rp.Project.ID, agentType, flavor)
	if err != nil || thread == nil {
		return data
	}
	data["ThreadID"] = thread.ID
	msgs, err := s.eng.ChatRepo().ListMessages(thread.ID)
	if err != nil {
		data["Error"] = "Could not load messages."
		return data
	}
	views := make([]*chatMessageView, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, newChatMessageView(m))
	}
	data["Messages"] = views
	return data
}

func newChatMessageView(m *sqlite.ChatMessage) *chatMessageView {
	v := &chatMessageView{
		Role:     m.Role,
		Status:   m.Status,
		ToolName: m.ToolName,
		Content:  m.Content,
	}
	switch {
	case m.Role == "assistant" && m.Status == "pending":
		v.Pending = true
	case m.Role == "assistant" && m.Status == "error":
		v.Error = true
	case m.Role == "assistant":
		v.ContentHTML = renderChatMarkdown(m.Content)
	}
	return v
}

// renderChatMarkdown renders an assistant reply for the drawer. Like the
// artifact drawer, model output is treated as trusted content (drawerMarkdown
// keeps intentional raw HTML).
func renderChatMarkdown(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := drawerMarkdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}
