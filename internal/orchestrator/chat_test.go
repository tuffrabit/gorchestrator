package orchestrator

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// fakeChatModel is a model.LLM that replies with canned text and captures the
// contents of every request so tests can assert history seeding.
type fakeChatModel struct {
	mu       sync.Mutex
	text     string
	requests [][]capturedContent
}

type capturedContent struct {
	role string
	text string
}

func (f *fakeChatModel) Name() string { return "fake-chat-model" }

func (f *fakeChatModel) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	f.mu.Lock()
	var contents []capturedContent
	for _, c := range req.Contents {
		if c == nil {
			continue
		}
		var sb strings.Builder
		for _, p := range c.Parts {
			if p != nil && p.Text != "" {
				sb.WriteString(p.Text)
			}
		}
		contents = append(contents, capturedContent{role: string(c.Role), text: sb.String()})
	}
	f.requests = append(f.requests, contents)
	f.mu.Unlock()

	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		yield(&adkmodel.LLMResponse{
			Content:      genai.NewContentFromText(f.text, genai.RoleModel),
			TurnComplete: true,
		}, nil)
	}
}

func (f *fakeChatModel) capturedRequests() [][]capturedContent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]capturedContent, len(f.requests))
	copy(out, f.requests)
	return out
}

// fakeChatController records lifecycle calls without touching a server.
type fakeChatController struct {
	mu            sync.Mutex
	ensureLoadedN int
	runningN      int
	unloadN       int
}

func (f *fakeChatController) EnsureLoaded(ctx context.Context, model string, timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLoadedN++
	return nil
}

func (f *fakeChatController) RunningModels(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runningN++
	return nil, nil
}

func (f *fakeChatController) UnloadAll(ctx context.Context, keep ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unloadN++
	return nil
}

func (f *fakeChatController) counts() (ensureLoaded, runningModels, unloadAll int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensureLoadedN, f.runningN, f.unloadN
}

// chatTestEngine builds an engine with the fake chat model installed and a
// user + the fixture "foo" project ready for thread creation.
func chatTestEngine(t *testing.T, mutate func(*config.Config)) (*Engine, *sqlite.User, *sqlite.Project, *fakeChatModel) {
	t.Helper()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	if mutate != nil {
		mutate(cfg)
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	fake := &fakeChatModel{text: "chat reply"}
	eng.chatSvc.newModel = func(ctx context.Context, cfg llm.Config) (adkmodel.LLM, error) {
		return fake, nil
	}

	user, err := eng.users.Create("chat@example.com", "Chat User", sqlite.RoleMember, nil, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	project, err := eng.projects.GetByName("foo")
	if err != nil || project == nil {
		t.Fatalf("get project foo: %v", err)
	}
	return eng, user, project, fake
}

// waitForChatMessages polls until the thread has exactly want messages and no
// assistant row is still pending.
func waitForChatMessages(t *testing.T, eng *Engine, threadID int64, want int) []*sqlite.ChatMessage {
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
				return msgs
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d completed messages; have %v", want, msgs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChat_SendMessagePersistsAndCompletes(t *testing.T) {
	eng, user, project, _ := chatTestEngine(t, nil)
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := eng.Subscribe(subCtx, EventFilter{})

	if err := eng.ChatService().SendMessage(context.Background(), thread.ID, "hello agent"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	msgs := waitForChatMessages(t, eng, thread.ID, 2)
	if msgs[0].Role != "user" || msgs[0].Content != "hello agent" || msgs[0].Status != "done" {
		t.Fatalf("user row = %+v, want done user message", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "chat reply" || msgs[1].Status != "done" {
		t.Fatalf("assistant row = %+v, want done reply %q", msgs[1], "chat reply")
	}

	// Subscribers were notified with the thread reference on the event.
	saw := false
	for _, ev := range drainEvents(events) {
		if ev.Type != EventChatMessage || ev.ProjectID != project.ID {
			continue
		}
		if tid, ok := ev.Data["thread_id"].(int64); ok && tid == thread.ID {
			saw = true
		}
	}
	if !saw {
		t.Fatal("no chat_message event with thread_id published")
	}
}

func TestChat_SendMessageValidation(t *testing.T) {
	eng, user, project, _ := chatTestEngine(t, nil)
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx := context.Background()

	if err := eng.ChatService().SendMessage(ctx, thread.ID, "   \n\t "); err == nil {
		t.Fatal("SendMessage with blank text = nil, want error")
	}
	if err := eng.ChatService().SendMessage(ctx, thread.ID, strings.Repeat("x", chatMaxMessageLen+1)); err == nil {
		t.Fatal("SendMessage over the cap = nil, want error")
	}
	if err := eng.ChatService().SendMessage(ctx, 999999, "hi"); err == nil {
		t.Fatal("SendMessage to missing thread = nil, want error")
	}

	msgs, err := eng.ChatRepo().ListMessages(thread.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("validation failures persisted %d rows, want 0", len(msgs))
	}
}

func TestChat_MessagesProcessInOrder(t *testing.T) {
	eng, user, project, fake := chatTestEngine(t, nil)
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx := context.Background()

	if err := eng.ChatService().SendMessage(ctx, thread.ID, "first question"); err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	if err := eng.ChatService().SendMessage(ctx, thread.ID, "second question"); err != nil {
		t.Fatalf("second SendMessage: %v", err)
	}

	msgs := waitForChatMessages(t, eng, thread.ID, 4)
	want := []struct {
		role, content, status string
	}{
		{"user", "first question", "done"},
		{"assistant", "chat reply", "done"},
		{"user", "second question", "done"},
		{"assistant", "chat reply", "done"},
	}
	for i, w := range want {
		if msgs[i].Role != w.role || msgs[i].Content != w.content || msgs[i].Status != w.status {
			t.Fatalf("message %d = %+v, want role=%s content=%q status=%s", i, msgs[i], w.role, w.content, w.status)
		}
		if i > 0 && msgs[i].ID <= msgs[i-1].ID {
			t.Fatalf("message %d id %d not after previous id %d", i, msgs[i].ID, msgs[i-1].ID)
		}
	}

	// The model saw the turns in order: one request per turn, first question
	// before second question.
	reqs := fake.capturedRequests()
	if len(reqs) != 2 {
		t.Fatalf("model requests = %d, want 2", len(reqs))
	}
	if len(reqs[0]) != 1 || reqs[0][0].role != "user" || !strings.Contains(reqs[0][0].text, "first question") {
		t.Fatalf("turn 1 request contents = %+v, want only the first question", reqs[0])
	}
	if len(reqs[1]) == 0 || !strings.Contains(reqs[1][len(reqs[1])-1].text, "second question") {
		t.Fatalf("turn 2 request contents = %+v, want the second question last", reqs[1])
	}
}

func TestChat_HistorySeededEachTurn(t *testing.T) {
	eng, user, project, fake := chatTestEngine(t, nil)
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx := context.Background()

	if err := eng.ChatService().SendMessage(ctx, thread.ID, "first question"); err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	waitForChatMessages(t, eng, thread.ID, 2)
	if err := eng.ChatService().SendMessage(ctx, thread.ID, "second question"); err != nil {
		t.Fatalf("second SendMessage: %v", err)
	}
	waitForChatMessages(t, eng, thread.ID, 4)

	reqs := fake.capturedRequests()
	if len(reqs) != 2 {
		t.Fatalf("model requests = %d, want 2", len(reqs))
	}

	// Turn 1 is stateless: a fresh session with only the current question.
	if len(reqs[0]) != 1 || reqs[0][0].role != "user" || reqs[0][0].text != "first question" {
		t.Fatalf("turn 1 contents = %+v, want single user content %q", reqs[0], "first question")
	}

	// Turn 2 reseeds from the DB: first question, first reply, then the
	// runner-appended current question. The reply must arrive as a model
	// turn (not rewritten into a foreign "For context:" user message).
	want := []capturedContent{
		{role: "user", text: "first question"},
		{role: "model", text: "chat reply"},
		{role: "user", text: "second question"},
	}
	if len(reqs[1]) != len(want) {
		t.Fatalf("turn 2 contents = %+v, want %+v", reqs[1], want)
	}
	for i, w := range want {
		if reqs[1][i].role != w.role || reqs[1][i].text != w.text {
			t.Fatalf("turn 2 content %d = %+v, want %+v", i, reqs[1][i], w)
		}
	}
}

func TestChat_ExclusiveModeAcquiresModel(t *testing.T) {
	baseURL := "http://127.0.0.1:9/chat-exclusive"
	eng, user, project, _ := chatTestEngine(t, func(cfg *config.Config) {
		cfg.Inference = config.InferenceConfig{Type: "llama-swap", BaseURL: baseURL, Mode: "exclusive"}
	})
	ctl := &fakeChatController{}
	eng.controller = ctl

	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := eng.ChatService().SendMessage(context.Background(), thread.ID, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msgs := waitForChatMessages(t, eng, thread.ID, 2)
	if msgs[1].Content != "chat reply" || msgs[1].Status != "done" {
		t.Fatalf("assistant row = %+v, want done chat reply", msgs[1])
	}

	ensureLoaded, runningModels, unloadAll := ctl.counts()
	if ensureLoaded == 0 {
		t.Fatal("exclusive turn never called EnsureLoaded")
	}
	if runningModels == 0 {
		t.Fatal("exclusive turn never reconciled RunningModels")
	}
	if unloadAll != 0 {
		t.Fatalf("chat release unloaded %d times; release must keep the model resident", unloadAll)
	}

	// The exclusive lock was freed after the turn.
	if !modelLocks.TryLock(baseURL) {
		t.Fatal("exclusive model lock still held after chat turn")
	}
	modelLocks.Unlock(baseURL)
}

func TestChat_SideloadSkipsController(t *testing.T) {
	baseURL := "http://127.0.0.1:9/chat-sideload"
	eng, user, project, _ := chatTestEngine(t, func(cfg *config.Config) {
		sideload := true
		pc := cfg.Projects["foo"]
		pc.Agents = map[string]config.ProjectAgentConfig{
			"researcher": {
				Default: "cheap",
				Flavors: map[string]config.AgentConfig{
					"cheap": {Sideload: &sideload},
				},
			},
		}
		cfg.Projects["foo"] = pc
		cfg.Inference = config.InferenceConfig{Type: "llama-swap", BaseURL: baseURL, Mode: "exclusive"}
	})
	ctl := &fakeChatController{}
	eng.controller = ctl

	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "cheap")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := eng.ChatService().SendMessage(context.Background(), thread.ID, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msgs := waitForChatMessages(t, eng, thread.ID, 2)
	if msgs[1].Content != "chat reply" || msgs[1].Status != "done" {
		t.Fatalf("assistant row = %+v, want done chat reply", msgs[1])
	}

	ensureLoaded, runningModels, unloadAll := ctl.counts()
	if ensureLoaded+runningModels+unloadAll != 0 {
		t.Fatalf("sideloaded chat touched the controller (ensure=%d running=%d unload=%d), want zero calls", ensureLoaded, runningModels, unloadAll)
	}

	// And it never waited on the exclusive lock either.
	if !modelLocks.TryLock(baseURL) {
		t.Fatal("exclusive model lock held after sideloaded chat turn")
	}
	modelLocks.Unlock(baseURL)
}
