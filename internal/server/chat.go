package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
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
// Value is the configured agent id.
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
		identity := s.defaultChatIdentity(selected.Project.Name)
		data["SelectedProject"] = selected.Project.Name
		data["IdentityOptions"] = s.chatIdentityOptions(selected.Project.Name, "")
		data["Thread"] = s.chatThreadData(r, selected, identity, u)
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
// (project, agent) selection. Read-only: never creates a thread.
func (s *Server) handlePartialChatThread(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	q := r.URL.Query()
	project := q.Get("project")
	identity := chatSelectionFromQuery(q.Get("agent"), q.Get("identity"))
	rp, err := s.chatProject(r.Context(), project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if identity == "" {
		// Project-select refresh path: fall back to the same default identity
		// the options list marks selected, so thread and select agree.
		identity = s.defaultChatIdentity(project)
	}
	if !s.validChatAgent(identity) {
		http.Error(w, fmt.Sprintf("unknown agent %q", identity), http.StatusBadRequest)
		return
	}
	data := s.chatThreadData(r, rp, identity, u)
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
	identity := chatSelectionFromQuery(r.FormValue("agent"), r.FormValue("identity"))
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
	if identity == "" {
		identity = s.defaultChatIdentity(project)
	}
	if !s.validChatAgent(identity) {
		http.Error(w, fmt.Sprintf("unknown agent %q", identity), http.StatusUnprocessableEntity)
		return
	}

	uid, err := s.chatUserID(u, true)
	if err != nil {
		http.Error(w, "could not resolve user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	thread, err := s.eng.ChatRepo().GetOrCreateThread(uid, rp.Project.ID, identity, "")
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

	data := s.chatThreadData(r, rp, identity, u)
	if err := render(w, "partials/chat_thread.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePartialChatClear clears ONE chat thread (the one for the currently
// selected project + identity) so the user can start a fresh line of
// conversation. Retention is untouched for every other identity/thread. It
// re-renders the thread partial — the existing empty state — and, like
// handlePartialChatSend, sets no HX-Trigger so the drawer stays open. The
// thread id is always derived from the authenticated user's row + the form
// selection, never from a client-supplied id, so no Viewer can wipe someone
// else's thread by guessing ids.
func (s *Server) handlePartialChatClear(w http.ResponseWriter, r *http.Request) {
	if err := parseRequestForm(r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	u := auth.UserFromContext(r.Context())
	project := strings.TrimSpace(r.FormValue("project"))
	identity := chatSelectionFromQuery(r.FormValue("agent"), r.FormValue("identity"))

	rp, err := s.chatProject(r.Context(), project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if identity == "" {
		// Same default-identity fallback as the send/thread handlers so the
		// clear targets the thread the drawer is actually showing.
		identity = s.defaultChatIdentity(project)
	}
	if !s.validChatAgent(identity) {
		http.Error(w, fmt.Sprintf("unknown agent %q", identity), http.StatusUnprocessableEntity)
		return
	}

	// create=false read path: a user with no row owns no threads → nothing
	// to clear; render the empty state the drawer would show anyway.
	uid, err := s.chatUserID(u, false)
	if err != nil {
		http.Error(w, "could not resolve user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var thread *sqlite.ChatThread
	if uid != 0 {
		thread, err = s.eng.ChatRepo().FindThread(uid, rp.Project.ID, identity, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if thread == nil {
		// Nothing to clear: the empty-state partial is the success shape.
		if err := render(w, "partials/chat_thread.html", s.chatThreadData(r, rp, identity, u)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	n, err := s.eng.ChatService().ClearThread(r.Context(), thread.ID)
	if err != nil {
		if errors.Is(err, orchestrator.ErrChatThreadBusy) {
			// A turn is in flight: re-render the UNCHANGED thread with a notice
			// (chatThreadData's Error slot renders as a notice banner) so the
			// composer and drawer survive — a plain http.Error body would be
			// swapped into #chat-thread and blank the composer.
			data := s.chatThreadData(r, rp, identity, u)
			data["Error"] = "A reply is still being generated. Try again in a moment."
			if err := render(w, "partials/chat_thread.html", data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var auditUID *int64
	if u != nil {
		auditUID = &u.ID
	}
	_ = s.eng.Audit().Record(auditUID, "clear_chat", "chat_thread", strconv.FormatInt(thread.ID, 10), map[string]any{"messages_removed": n})

	if err := render(w, "partials/chat_thread.html", s.chatThreadData(r, rp, identity, u)); err != nil {
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

// chatSelectionFromQuery resolves the identity from the explicit agent param
// (used by SSE refresh and tests) or the combined identity select value.
func chatSelectionFromQuery(agent, identity string) string {
	if identity != "" {
		return strings.TrimSpace(identity)
	}
	return strings.TrimSpace(agent)
}

// validChatAgent reports whether the agent id exists in the global config.
func (s *Server) validChatAgent(agent string) bool {
	if s.eng.Cfg() == nil {
		return false
	}
	_, ok := s.eng.Cfg().Agent(agent)
	return ok
}

// defaultChatIdentity is the selection shown when nothing is preselected:
// the first configured agent id.
func (s *Server) defaultChatIdentity(project string) string {
	return firstAgentID(s.eng.Cfg())
}

// chatIdentityOptions lists every configured agent id as one option each. When
// selected is empty the default identity (the first configured agent) is
// marked selected.
func (s *Server) chatIdentityOptions(project, selected string) []chatIdentityOption {
	ids := s.eng.Cfg().AgentIDs()
	if selected == "" && len(ids) > 0 {
		selected = ids[0]
	}
	out := make([]chatIdentityOption, 0, len(ids))
	for _, id := range ids {
		out = append(out, chatIdentityOption{
			Value:    id,
			Label:    id,
			Selected: selected == id,
		})
	}
	return out
}

// firstAgentID returns the first configured agent id, or "" when none.
func firstAgentID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	ids := cfg.AgentIDs()
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
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
func (s *Server) chatThreadData(r *http.Request, rp *orchestrator.RegisteredProject, identity string, u *auth.User) map[string]any {
	data := map[string]any{
		"Project":  rp.Project.Name,
		"Agent":    identity,
		"CSRF":     auth.CSRFToken(r),
		"CanWrite": u != nil && roleAtLeast(u.Role, auth.RoleMember),
	}
	uid, err := s.chatUserID(u, false)
	if err != nil || uid == 0 {
		return data // no user row yet → no threads yet → empty state
	}
	thread, err := s.eng.ChatRepo().FindThread(uid, rp.Project.ID, identity, "")
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
