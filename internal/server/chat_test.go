package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// chatTestServer builds an engine + server with the shared test config
// (auth disabled, dry-run model). Chat processing therefore never touches a
// real LLM: the background turn completes with canned dry-run output.
func chatTestServer(t *testing.T, mutate func(*config.Config)) (*orchestrator.Engine, *Server) {
	t.Helper()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	if mutate != nil {
		mutate(cfg)
	}
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return eng, srv
}

func chatPostForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func chatProjectID(t *testing.T, eng *orchestrator.Engine, name string) int64 {
	t.Helper()
	registered, err := eng.ListRegisteredProjects(context.Background())
	if err != nil {
		t.Fatalf("list registered projects: %v", err)
	}
	for _, rp := range registered {
		if rp.Project != nil && rp.Project.Name == name {
			return rp.Project.ID
		}
	}
	t.Fatalf("project %q not registered", name)
	return 0
}

func TestPartialChat_RendersControls(t *testing.T) {
	eng, srv := chatTestServer(t, nil)
	_ = eng
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/partials/chat", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="chat-project"`, `id="chat-agent"`, "acme", "researcher (base)", "No messages yet", "chat-composer", "chat-thread-inner"} {
		if !strings.Contains(body, want) {
			t.Fatalf("drawer chat missing %q: %.600s", want, body)
		}
	}
}

func TestPartialChatOptions_ListsFlavors(t *testing.T) {
	_, srv := chatTestServer(t, func(cfg *config.Config) {
		cfg.Projects["acme"] = config.ProjectConfig{
			Agents: map[string]config.ProjectAgentConfig{
				"researcher": {
					Default: "thorough",
					Flavors: map[string]config.AgentConfig{
						"thorough": {},
						"cheap":    {},
					},
				},
			},
		}
	})
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/partials/chat/options?project=acme", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`value="researcher"`, `value="researcher:thorough"`, `value="researcher:cheap"`, "researcher (base)", "researcher: thorough", "researcher: cheap"} {
		if !strings.Contains(body, want) {
			t.Fatalf("options missing %q: %s", want, body)
		}
	}
	// Project default flavor is the preselected option.
	if !strings.Contains(body, `value="researcher:thorough" selected`) {
		t.Fatalf("expected project default flavor selected: %s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/partials/chat/options?project=nope", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown project status = %d, want 400", rec.Code)
	}
}

func TestPartialChatThread_EmptyStateAndValidation(t *testing.T) {
	eng, srv := chatTestServer(t, nil)
	h := srv.Handler()

	// Fresh selection: empty state, no thread created.
	req := httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=acme", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"No messages yet", `data-chat-project="acme"`, `data-chat-agent="researcher"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("thread missing %q: %.600s", want, body)
		}
	}
	// Reads must not create the user row (auth-disabled synthetic user) or a thread.
	if u, err := eng.Users().GetByEmail("disabled@localhost"); err != nil || u != nil {
		t.Fatalf("GET thread must not create user row: user=%v err=%v", u, err)
	}

	// Unknown project → 400.
	req = httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=nope", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown project status = %d, want 400", rec.Code)
	}

	// Unknown agent type → 400.
	req = httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=acme&agent=writer", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown agent status = %d, want 400", rec.Code)
	}

	// Unknown flavor → 422.
	req = httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=acme&agent=researcher&flavor=nope", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown flavor status = %d, want 422", rec.Code)
	}

	// Combined identity select value is accepted.
	req = httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=acme&identity=planner", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("identity param status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-chat-agent="planner"`) {
		t.Fatalf("identity param not applied: %s", rec.Body.String())
	}
}

func TestPartialChatSend_CreatesThreadAndRendersMessages(t *testing.T) {
	eng, srv := chatTestServer(t, nil)
	h := srv.Handler()

	form := url.Values{}
	form.Set("project", "acme")
	form.Set("agent", "researcher")
	form.Set("flavor", "")
	form.Set("message", "hello agent")
	rec := chatPostForm(t, h, "/partials/chat/send", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hello agent") {
		t.Fatalf("response missing user message: %.600s", body)
	}
	if !strings.Contains(body, "chat-msg-pending") && !strings.Contains(body, "chat-msg-assistant") {
		t.Fatalf("response missing assistant row: %.600s", body)
	}
	// Chat sends must NOT close the drawer (that behavior is submit-specific).
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("HX-Trigger = %q, want empty", got)
	}
	if got := rec.Header().Get("HX-Trigger-After-Swap"); got != "" {
		t.Fatalf("HX-Trigger-After-Swap = %q, want empty", got)
	}

	// The auth-disabled synthetic user gets a real users row on first write.
	user, err := eng.Users().GetByEmail("disabled@localhost")
	if err != nil || user == nil {
		t.Fatalf("expected synthetic user row created: user=%v err=%v", user, err)
	}
	projectID := chatProjectID(t, eng, "acme")
	thread, err := eng.ChatRepo().FindThread(user.ID, projectID, "researcher", "")
	if err != nil || thread == nil {
		t.Fatalf("expected thread created: thread=%v err=%v", thread, err)
	}
	msgs, err := eng.ChatRepo().ListMessages(thread.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2 (user + assistant): %v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello agent" {
		t.Fatalf("user message = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" {
		t.Fatalf("assistant message = %+v", msgs[1])
	}

	// A follow-up read renders the same thread (viewer route, no recreation).
	req := httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=acme&agent=researcher", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello agent") {
		t.Fatalf("thread reread status=%d body=%.600s", rec.Code, rec.Body.String())
	}
}

func TestPartialChatSend_ValidationErrors(t *testing.T) {
	eng, srv := chatTestServer(t, nil)
	h := srv.Handler()

	longMessage := strings.Repeat("x", chatMessageMaxLen+1)
	cases := []url.Values{
		{"project": {"acme"}, "agent": {"researcher"}, "message": {""}},
		{"project": {""}, "agent": {"researcher"}, "message": {"hi"}},
		{"project": {"acme"}, "agent": {"researcher"}, "message": {longMessage}},
		{"project": {"nope"}, "agent": {"researcher"}, "message": {"hi"}},
		{"project": {"acme"}, "agent": {"writer"}, "message": {"hi"}},
		{"project": {"acme"}, "agent": {"researcher"}, "flavor": {"nope"}, "message": {"hi"}},
	}
	for i, form := range cases {
		rec := chatPostForm(t, h, "/partials/chat/send", form)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("case %d status = %d body=%s, want 422", i, rec.Code, rec.Body.String())
		}
	}

	// No thread and no user row may be created by rejected sends.
	if u, err := eng.Users().GetByEmail("disabled@localhost"); err != nil || u != nil {
		t.Fatalf("rejected sends must not create user row: user=%v err=%v", u, err)
	}
}

func TestPartialChatSend_WithFlavor(t *testing.T) {
	eng, srv := chatTestServer(t, func(cfg *config.Config) {
		cfg.Projects["acme"] = config.ProjectConfig{
			Agents: map[string]config.ProjectAgentConfig{
				"researcher": {
					Default: "thorough",
					Flavors: map[string]config.AgentConfig{
						"thorough": {},
						"cheap":    {},
					},
				},
			},
		}
	})
	h := srv.Handler()

	form := url.Values{}
	form.Set("project", "acme")
	form.Set("agent", "researcher")
	form.Set("flavor", "cheap")
	form.Set("message", "quick question")
	rec := chatPostForm(t, h, "/partials/chat/send", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-chat-flavor="cheap"`) {
		t.Fatalf("flavor not echoed on thread root: %.600s", rec.Body.String())
	}

	user, err := eng.Users().GetByEmail("disabled@localhost")
	if err != nil || user == nil {
		t.Fatalf("expected synthetic user row: %v", err)
	}
	projectID := chatProjectID(t, eng, "acme")
	thread, err := eng.ChatRepo().FindThread(user.ID, projectID, "researcher", "cheap")
	if err != nil || thread == nil {
		t.Fatalf("expected flavor thread: thread=%v err=%v", thread, err)
	}
	if thread.Flavor != "cheap" {
		t.Fatalf("thread flavor = %q", thread.Flavor)
	}
}

// waitForChatTurnDone polls until the thread has exactly want messages and no
// assistant row is still pending, so tests can observe a completed turn
// before exercising the clear.
func waitForChatTurnDone(t *testing.T, eng *orchestrator.Engine, threadID int64, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		msgs, err := eng.ChatRepo().ListMessages(threadID)
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(msgs) == want {
			pending := false
			for _, m := range msgs {
				if m.Role == "assistant" && m.Status == "pending" {
					pending = true
				}
			}
			if !pending {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d completed messages; have %v", want, msgs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPartialChatClear_EmptiesThreadAndKeepsDrawer(t *testing.T) {
	eng, srv := chatTestServer(t, nil)
	h := srv.Handler()

	form := url.Values{}
	form.Set("project", "acme")
	form.Set("agent", "researcher")
	form.Set("message", "hello agent")
	rec := chatPostForm(t, h, "/partials/chat/send", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("send status = %d body=%s", rec.Code, rec.Body.String())
	}
	user, err := eng.Users().GetByEmail("disabled@localhost")
	if err != nil || user == nil {
		t.Fatalf("expected synthetic user row: user=%v err=%v", user, err)
	}
	projectID := chatProjectID(t, eng, "acme")
	thread, err := eng.ChatRepo().FindThread(user.ID, projectID, "researcher", "")
	if err != nil || thread == nil {
		t.Fatalf("expected thread: thread=%v err=%v", thread, err)
	}
	waitForChatTurnDone(t, eng, thread.ID, 2)

	clear := url.Values{}
	clear.Set("project", "acme")
	clear.Set("agent", "researcher")
	rec = chatPostForm(t, h, "/partials/chat/clear", clear)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No messages yet.") {
		t.Fatalf("cleared thread not empty: %.600s", body)
	}
	// The composer must survive the swap: no blanked drawer.
	if !strings.Contains(body, `name="message"`) {
		t.Fatalf("composer missing after clear: %.600s", body)
	}
	// HX-Trigger must stay empty so the drawer stays open.
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("HX-Trigger = %q, want empty", got)
	}

	// The delete is durable: a follow-up read also shows the empty state, and
	// the thread row itself still exists.
	req := httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=acme&agent=researcher", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No messages yet.") {
		t.Fatalf("thread reread status=%d body=%.600s", rec.Code, rec.Body.String())
	}
	if got, err := eng.ChatRepo().GetThread(thread.ID); err != nil || got == nil {
		t.Fatalf("thread row missing after clear: %+v err=%v", got, err)
	}
	msgs, err := eng.ChatRepo().ListMessages(thread.ID)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("messages after clear = %v err=%v, want 0", msgs, err)
	}
}

func TestPartialChatClear_OnlyClearsSelectedIdentity(t *testing.T) {
	eng, srv := chatTestServer(t, func(cfg *config.Config) {
		cfg.Projects["acme"] = config.ProjectConfig{
			Agents: map[string]config.ProjectAgentConfig{
				"researcher": {
					Flavors: map[string]config.AgentConfig{
						"thorough": {},
						"cheap":    {},
					},
				},
			},
		}
	})
	h := srv.Handler()

	for _, flavor := range []string{"", "cheap"} {
		form := url.Values{}
		form.Set("project", "acme")
		form.Set("agent", "researcher")
		form.Set("flavor", flavor)
		form.Set("message", "question about the "+flavor+" flavor")
		rec := chatPostForm(t, h, "/partials/chat/send", form)
		if rec.Code != http.StatusOK {
			t.Fatalf("send flavor=%q status = %d body=%s", flavor, rec.Code, rec.Body.String())
		}
	}
	user, err := eng.Users().GetByEmail("disabled@localhost")
	if err != nil || user == nil {
		t.Fatalf("expected synthetic user row: user=%v err=%v", user, err)
	}
	projectID := chatProjectID(t, eng, "acme")
	base, err := eng.ChatRepo().FindThread(user.ID, projectID, "researcher", "")
	if err != nil || base == nil {
		t.Fatalf("expected base thread: %+v err=%v", base, err)
	}
	cheap, err := eng.ChatRepo().FindThread(user.ID, projectID, "researcher", "cheap")
	if err != nil || cheap == nil {
		t.Fatalf("expected cheap thread: %+v err=%v", cheap, err)
	}
	waitForChatTurnDone(t, eng, base.ID, 2)
	waitForChatTurnDone(t, eng, cheap.ID, 2)

	clear := url.Values{}
	clear.Set("project", "acme")
	clear.Set("agent", "researcher")
	clear.Set("flavor", "cheap")
	rec := chatPostForm(t, h, "/partials/chat/clear", clear)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "No messages yet.") {
		t.Fatalf("cheap thread not cleared: %.600s", rec.Body.String())
	}

	// The other identity's thread is untouched: retention is per identity.
	req := httptest.NewRequest(http.MethodGet, "/partials/chat/thread?project=acme&agent=researcher", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "question about the  flavor") {
		t.Fatalf("base thread status=%d body=%.600s, want its message retained", rec.Code, rec.Body.String())
	}
	msgs, err := eng.ChatRepo().ListMessages(base.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("base thread messages = %v err=%v, want 2", msgs, err)
	}
}

func TestPartialChatClear_UnknownProjectOrFlavor(t *testing.T) {
	_, srv := chatTestServer(t, nil)
	h := srv.Handler()

	for _, form := range []url.Values{
		{"project": {"nope"}, "agent": {"researcher"}},
		{"project": {""}, "agent": {"researcher"}},
		{"project": {"acme"}, "agent": {"writer"}},
		{"project": {"acme"}, "agent": {"researcher"}, "flavor": {"nope"}},
	} {
		rec := chatPostForm(t, h, "/partials/chat/clear", form)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("form %v status = %d body=%s, want 422", form, rec.Code, rec.Body.String())
		}
	}
}

func TestPartialChatClear_NoThread(t *testing.T) {
	eng, srv := chatTestServer(t, nil)
	h := srv.Handler()

	// No message was ever sent: nothing to clear, and the response is the
	// empty-state partial (200), not a 404.
	form := url.Values{}
	form.Set("project", "acme")
	form.Set("agent", "researcher")
	rec := chatPostForm(t, h, "/partials/chat/clear", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200 empty state", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "No messages yet.") {
		t.Fatalf("missing empty state: %.600s", rec.Body.String())
	}
	// The clear must not have created a user row or a thread.
	if u, err := eng.Users().GetByEmail("disabled@localhost"); err != nil || u != nil {
		t.Fatalf("clear must not create user row: user=%v err=%v", u, err)
	}
}

func TestPartialChatClear_DoesNotAcceptClientThreadID(t *testing.T) {
	eng, srv := chatTestServer(t, nil)
	h := srv.Handler()

	// A second, unrelated user owns a thread with messages.
	other, err := eng.Users().Create("other@example.com", "Other", sqlite.RoleMember, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectID := chatProjectID(t, eng, "acme")
	otherThread, err := eng.ChatRepo().GetOrCreateThread(other.ID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ChatRepo().AddMessage(otherThread.ID, "user", "sensitive chat", "", "done"); err != nil {
		t.Fatal(err)
	}

	// The authenticated (synthetic) user POSTs the other user's thread id:
	// it must be ignored — ownership comes from the session, not the form.
	form := url.Values{}
	form.Set("project", "acme")
	form.Set("agent", "researcher")
	form.Set("thread_id", strconv.FormatInt(otherThread.ID, 10))
	rec := chatPostForm(t, h, "/partials/chat/clear", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	msgs, err := eng.ChatRepo().ListMessages(otherThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "sensitive chat" {
		t.Fatalf("other user's thread was cleared: %v", msgs)
	}
	// No user row was created for the synthetic user either.
	if u, err := eng.Users().GetByEmail("disabled@localhost"); err != nil || u != nil {
		t.Fatalf("clear must not create user row: user=%v err=%v", u, err)
	}
}

func TestChatSplitIdentity(t *testing.T) {
	agent, flavor, err := splitChatIdentity("researcher:fast")
	if err != nil || agent != "researcher" || flavor != "fast" {
		t.Fatalf("split named = %q %q %v", agent, flavor, err)
	}
	agent, flavor, err = splitChatIdentity("researcher")
	if err != nil || agent != "researcher" || flavor != "" {
		t.Fatalf("split base = %q %q %v", agent, flavor, err)
	}
	if _, _, err := splitChatIdentity(""); err == nil {
		t.Fatal("empty identity must fail")
	}
	if _, _, err := splitChatIdentity(":fast"); err == nil {
		t.Fatal("missing agent must fail")
	}
	a, f := chatSelectionFromQuery("", "", "planner:cheap")
	if a != "planner" || f != "cheap" {
		t.Fatalf("from query = %q %q", a, f)
	}
	a, f = chatSelectionFromQuery("researcher", "", "planner:cheap")
	if a != "researcher" || f != "cheap" {
		t.Fatalf("explicit agent wins = %q %q", a, f)
	}
}

// The view model must render a finished assistant reply as markdown HTML
// (via the shared drawerMarkdown converter) and a pending one as stage text.
func TestChatMessageView_Rendering(t *testing.T) {
	done := newChatMessageView(&sqlite.ChatMessage{Role: "assistant", Status: "done", Content: "## Hello\n\nworld"})
	if done.Pending || done.Error {
		t.Fatalf("done flags: %+v", done)
	}
	if !strings.Contains(string(done.ContentHTML), "<h2") || !strings.Contains(string(done.ContentHTML), "Hello") {
		t.Fatalf("markdown not rendered: %q", done.ContentHTML)
	}
	pending := newChatMessageView(&sqlite.ChatMessage{Role: "assistant", Status: "pending", Content: "Thinking…"})
	if !pending.Pending {
		t.Fatalf("pending flag: %+v", pending)
	}
	errView := newChatMessageView(&sqlite.ChatMessage{Role: "assistant", Status: "error", Content: "Error: boom"})
	if !errView.Error || errView.Content == "" {
		t.Fatalf("error view: %+v", errView)
	}
	tool := newChatMessageView(&sqlite.ChatMessage{Role: "tool", Status: "done", ToolName: "read_file", Content: "{}"})
	if tool.ToolName != "read_file" {
		t.Fatalf("tool view: %+v", tool)
	}
}
