package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/tuffrabit/gorchestrator/internal/config"
	gorchgit "github.com/tuffrabit/gorchestrator/internal/git"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// fakeChatModel is a model.LLM that replies with canned text and captures the
// contents of every request so tests can assert history seeding. When
// callTurns is greater than zero, that many turns first answer with a
// list_directory FunctionCall part (exercising the ADK function-call loop)
// before falling back to the canned text. The call is consumed when the
// response is actually yielded (not when GenerateContent is invoked), because
// ADK defers iterator evaluation and may re-wrap req in a second call for
// the same turn.
type fakeChatModel struct {
	mu           sync.Mutex
	text         string
	requests     [][]capturedContent
	declarations [][]string
	callTurns    int
	callDone     bool
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
	var decls []string
	if req.Config != nil {
		for _, tc := range req.Config.Tools {
			if tc == nil {
				continue
			}
			for _, d := range tc.FunctionDeclarations {
				decls = append(decls, d.Name)
			}
		}
	}
	f.requests = append(f.requests, contents)
	f.declarations = append(f.declarations, decls)
	f.mu.Unlock()

	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		f.mu.Lock()
		call := f.callTurns > 0 && !f.callDone
		if call {
			f.callDone = true
		} else {
			// A final text answer ends the turn: the next turn may call again.
			f.callDone = false
		}
		f.mu.Unlock()
		if call {
			yield(&adkmodel.LLMResponse{
				Content: &genai.Content{
					Role: genai.RoleModel,
					Parts: []*genai.Part{{
						FunctionCall: &genai.FunctionCall{
							ID:   "call_list_directory",
							Name: "list_directory",
							Args: map[string]any{"path": "."},
						},
					}},
				},
				TurnComplete: true,
			}, nil)
			return
		}
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

func (f *fakeChatModel) capturedDeclarations() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.declarations))
	copy(out, f.declarations)
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
	return chatTestEngineFor(t, "foo", mutate)
}

// chatTestEngineFor is chatTestEngine for an arbitrary fixture project.
func chatTestEngineFor(t *testing.T, projectName string, mutate func(*config.Config)) (*Engine, *sqlite.User, *sqlite.Project, *fakeChatModel) {
	t.Helper()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	// Give the fixture project a real, existing snapshot source tree so
	// chat turns have tools. Tests that need a source-less project clear
	// SourcePath in their mutate (see TestChat_NoSourceFailsLoudly).
	src := t.TempDir()
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir chat source: %v", err)
	}
	if pc := cfg.Projects[projectName]; pc.SourcePath == "" {
		pc.SourcePath = src
		cfg.Projects[projectName] = pc
	}
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
	project, err := eng.projects.GetByName(projectName)
	if err != nil || project == nil {
		t.Fatalf("get project %s: %v", projectName, err)
	}
	return eng, user, project, fake
}

// assertChatToolTurn checks the message layout of a turn that made one
// list_directory call: user, tool call, tool response, assistant.
func assertChatToolTurn(t *testing.T, msgs []*sqlite.ChatMessage) {
	t.Helper()
	if len(msgs) != 4 {
		t.Fatalf("messages = %+v, want 4 rows (user, tool, tool, assistant)", msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Status != "done" {
		t.Fatalf("row 0 = %+v, want done user message", msgs[0])
	}
	if msgs[1].Role != "tool" || msgs[1].ToolName != "list_directory" || msgs[1].Status != "done" {
		t.Fatalf("row 1 = %+v, want done list_directory tool call", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolName != "list_directory" || !strings.Contains(msgs[2].Content, "hello.go") {
		t.Fatalf("row 2 = %+v, want tool response listing hello.go", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Status != "done" || msgs[3].Content != "chat reply" {
		t.Fatalf("row 3 = %+v, want done assistant reply", msgs[3])
	}
}

// assertHostToolDeclarations asserts the first model request carried the
// host read-only tool declarations.
func assertHostToolDeclarations(t *testing.T, fake *fakeChatModel) {
	t.Helper()
	decls := fake.capturedDeclarations()
	if len(decls) == 0 || len(decls[0]) == 0 {
		t.Fatalf("first model request carried no tool declarations: %v", decls)
	}
	seen := map[string]bool{}
	for _, n := range decls[0] {
		seen[n] = true
	}
	for _, want := range []string{"read_file", "list_directory", "grep_search"} {
		if !seen[want] {
			t.Fatalf("declarations %v missing %q", decls[0], want)
		}
	}
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
		agent := cfg.Agents["researcher"]
		agent.Sideload = &sideload
		cfg.Agents["researcher"] = agent
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
	if ensureLoaded+runningModels+unloadAll != 0 {
		t.Fatalf("sideloaded chat touched the controller (ensure=%d running=%d unload=%d), want zero calls", ensureLoaded, runningModels, unloadAll)
	}

	// And it never waited on the exclusive lock either.
	if !modelLocks.TryLock(baseURL) {
		t.Fatal("exclusive model lock held after sideloaded chat turn")
	}
	modelLocks.Unlock(baseURL)
}

func TestChat_GitProjectGetsRealTools(t *testing.T) {
	remote := initLocalBareRemote(t)
	eng, user, project, fake := chatTestEngineFor(t, "gitproj", func(cfg *config.Config) {
		pc := cfg.Projects["gitproj"]
		pc.SourcePath = "" // exercise the git-mode path, not the snapshot root
		pc.Git = &config.ProjectGitConfig{
			RepoURL:    remote,
			BaseBranch: "main",
		}
		cfg.Projects["gitproj"] = pc
	})
	fake.callTurns = 1
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := eng.ChatService().SendMessage(context.Background(), thread.ID, "what files are here"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	msgs := waitForChatMessages(t, eng, thread.ID, 4)
	assertChatToolTurn(t, msgs)

	// The chat LLM request carried the host tool declarations — this is the
	// bug under test: a tool-less request makes the model imitate calls.
	assertHostToolDeclarations(t, fake)

	// The project-level chat worktree was materialized detached at
	// projects/<id>/chat/source.
	abs := storage.Abs(eng.cfg.StorageRoot, storage.ChatSourcePath(project.ID))
	if _, err := os.Stat(filepath.Join(abs, "hello.go")); err != nil {
		t.Fatalf("chat worktree missing hello.go: %v", err)
	}
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(out)) != "HEAD" {
		t.Fatalf("chat worktree not detached: %q err=%v", strings.TrimSpace(string(out)), err)
	}
}

func TestChat_SnapshotModeUnchanged(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.go"), []byte("package hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, user, project, fake := chatTestEngine(t, func(cfg *config.Config) {
		pc := cfg.Projects["foo"]
		pc.SourcePath = src
		cfg.Projects["foo"] = pc
	})
	fake.callTurns = 1
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := eng.ChatService().SendMessage(context.Background(), thread.ID, "what files are here"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	msgs := waitForChatMessages(t, eng, thread.ID, 4)
	assertChatToolTurn(t, msgs)
	assertHostToolDeclarations(t, fake)
}

func TestChat_NoSourceFailsLoudly(t *testing.T) {
	// "acme" is a fixture project with neither source_path nor git.
	eng, user, project, _ := chatTestEngineFor(t, "acme", func(cfg *config.Config) {
		pc := cfg.Projects["acme"]
		pc.SourcePath = ""
		cfg.Projects["acme"] = pc
	})
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := eng.ChatService().SendMessage(context.Background(), thread.ID, "hello agent"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// No silent tool-less turn: the assistant row is a readable error naming
	// the project and the two ways to give it a source tree.
	msgs := waitForChatMessages(t, eng, thread.ID, 2)
	if msgs[1].Role != "assistant" || msgs[1].Status != "error" {
		t.Fatalf("assistant row = %+v, want error status", msgs[1])
	}
	want := fmt.Sprintf("no source tree or tools available for project %q (set projects.%s.source_path or git.repo_url)", "acme", "acme")
	if !strings.Contains(msgs[1].Content, want) {
		t.Fatalf("assistant content = %q, want it to contain %q", msgs[1].Content, want)
	}
}

func TestChat_GitTwoTurnsReuseWorktree(t *testing.T) {
	remote := initLocalBareRemote(t)
	eng, user, project, fake := chatTestEngineFor(t, "gitproj", func(cfg *config.Config) {
		pc := cfg.Projects["gitproj"]
		pc.SourcePath = "" // exercise the git-mode path, not the snapshot root
		pc.Git = &config.ProjectGitConfig{
			RepoURL:    remote,
			BaseBranch: "main",
		}
		cfg.Projects["gitproj"] = pc
	})
	fake.callTurns = 2
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx := context.Background()
	if err := eng.ChatService().SendMessage(ctx, thread.ID, "first question"); err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	msgs := waitForChatMessages(t, eng, thread.ID, 4)
	assertChatToolTurn(t, msgs)

	abs := storage.Abs(eng.cfg.StorageRoot, storage.ChatSourcePath(project.ID))
	st1, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat chat worktree: %v", err)
	}

	if err := eng.ChatService().SendMessage(ctx, thread.ID, "second question"); err != nil {
		t.Fatalf("second SendMessage: %v", err)
	}
	msgs = waitForChatMessages(t, eng, thread.ID, 8)
	assertChatToolTurn(t, msgs[4:])

	// Fresh (within the TTL) worktree is reused, not re-created: the
	// directory mtime is unchanged and the tree is still intact.
	st2, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat chat worktree after second turn: %v", err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("worktree recreated on second turn (mtime %s -> %s), want reuse", st1.ModTime(), st2.ModTime())
	}
	if _, err := os.Stat(filepath.Join(abs, "hello.go")); err != nil {
		t.Fatalf("chat worktree missing hello.go after second turn: %v", err)
	}
}

func TestChat_GitConcurrentWithPipelineSourcePrep(t *testing.T) {
	remote := initLocalBareRemote(t)
	eng, user, project, _ := chatTestEngineFor(t, "gitproj", func(cfg *config.Config) {
		pc := cfg.Projects["gitproj"]
		pc.SourcePath = "" // exercise the git-mode path, not the snapshot root
		pc.Git = &config.ProjectGitConfig{
			RepoURL:    remote,
			BaseBranch: "main",
		}
		cfg.Projects["gitproj"] = pc
	})
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A chat turn and a pipeline source prep race against the same bare
	// cache; the per-project git workspace lock must keep both worktrees
	// intact.
	var wg sync.WaitGroup
	var prepErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		gitCfg := gorchgit.Config{RepoURL: remote, BaseBranch: "main"}
		prepErr = eng.prepareGitSource(ctx, project.ID, 1, gitCfg)
	}()
	if err := eng.ChatService().SendMessage(ctx, thread.ID, "hello agent"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	wg.Wait()
	if prepErr != nil {
		t.Fatalf("prepareGitSource raced against chat turn: %v", prepErr)
	}
	msgs := waitForChatMessages(t, eng, thread.ID, 2)
	if msgs[1].Role != "assistant" || msgs[1].Status != "done" {
		t.Fatalf("assistant row = %+v, want done", msgs[1])
	}

	// Both worktrees survived the race.
	srcKey := storage.SourcePath(project.ID, 1)
	if ok, _ := eng.store.Exists(ctx, srcKey+"/hello.go"); !ok {
		t.Fatalf("issue source worktree missing hello.go after race")
	}
	chatAbs := storage.Abs(eng.cfg.StorageRoot, storage.ChatSourcePath(project.ID))
	if _, err := os.Stat(filepath.Join(chatAbs, "hello.go")); err != nil {
		t.Fatalf("chat worktree missing hello.go after race: %v", err)
	}
	cache := filepath.Join(eng.cfg.StorageRoot, "repos", fmt.Sprintf("%d.git", project.ID))
	out, err := exec.Command("git", "-C", cache, "worktree", "list").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	for _, p := range []string{srcKey, storage.ChatSourcePath(project.ID)} {
		if !strings.Contains(string(out), p) {
			t.Fatalf("worktree list %q missing %q", string(out), p)
		}
	}
}

func TestChat_ClearThreadResetsHistory(t *testing.T) {
	eng, user, project, fake := chatTestEngine(t, nil)
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx := context.Background()

	if err := eng.ChatService().SendMessage(ctx, thread.ID, "first question"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitForChatMessages(t, eng, thread.ID, 2)

	n, err := eng.ChatService().ClearThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("ClearThread: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows cleared = %d, want 2", n)
	}
	msgs, err := eng.ChatRepo().ListMessages(thread.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("messages after clear = %d, want 0", len(msgs))
	}
	// The thread row itself survives: the same identity still maps to the
	// same thread id (and ADK session id).
	got, err := eng.ChatRepo().GetThread(thread.ID)
	if err != nil || got == nil {
		t.Fatalf("thread row missing after clear: %+v err=%v", got, err)
	}

	// The next turn must run with no prior conversation history.
	if err := eng.ChatService().SendMessage(ctx, thread.ID, "second question"); err != nil {
		t.Fatalf("second SendMessage: %v", err)
	}
	waitForChatMessages(t, eng, thread.ID, 2)
	reqs := fake.capturedRequests()
	if len(reqs) != 2 {
		t.Fatalf("model requests = %d, want 2", len(reqs))
	}
	last := reqs[1]
	if len(last) != 1 || last[0].role != "user" || last[0].text != "second question" {
		t.Fatalf("post-clear turn contents = %+v, want only the new user message (no seeded history)", last)
	}
}

func TestChat_ClearThreadBusy(t *testing.T) {
	eng, user, project, _ := chatTestEngine(t, nil)
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx := context.Background()
	if err := eng.ChatService().SendMessage(ctx, thread.ID, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitForChatMessages(t, eng, thread.ID, 2)

	// Holding the per-thread lock stands in for an in-flight turn: the clear
	// must refuse instead of deleting underneath the write phase.
	lock := eng.chatSvc.threadLock(thread.ID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := eng.ChatService().ClearThread(ctx, thread.ID); !errors.Is(err, ErrChatThreadBusy) {
		t.Fatalf("ClearThread = %v, want ErrChatThreadBusy", err)
	}
	msgs, err := eng.ChatRepo().ListMessages(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages after refused clear = %d, want 2", len(msgs))
	}
}

func TestChat_ClearThreadConcurrentSendSurvives(t *testing.T) {
	eng, user, project, _ := chatTestEngine(t, nil)
	thread, err := eng.ChatRepo().GetOrCreateThread(user.ID, project.ID, "researcher", "")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx := context.Background()

	// Simulate a turn in flight: with the per-thread lock held, a
	// concurrent SendMessage still persists its pair synchronously (its
	// processing goroutine blocks on the lock) and the clear is refused, so
	// nothing is silently dropped.
	lock := eng.chatSvc.threadLock(thread.ID)
	lock.Lock()
	if err := eng.ChatService().SendMessage(ctx, thread.ID, "queued question"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := eng.ChatService().ClearThread(ctx, thread.ID); !errors.Is(err, ErrChatThreadBusy) {
		t.Fatalf("ClearThread = %v, want ErrChatThreadBusy", err)
	}
	lock.Unlock()

	// The queued turn completes after the lock is released: no lost message,
	// and a reply was produced.
	msgs := waitForChatMessages(t, eng, thread.ID, 2)
	if msgs[0].Role != "user" || msgs[0].Content != "queued question" {
		t.Fatalf("user row = %+v, want queued question", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Status != "done" {
		t.Fatalf("assistant row = %+v, want done reply", msgs[1])
	}
}

func TestChat_ClearThreadMissingThread(t *testing.T) {
	eng, _, _, _ := chatTestEngine(t, nil)
	if _, err := eng.ChatService().ClearThread(context.Background(), 999999); err == nil {
		t.Fatal("ClearThread to missing thread = nil, want error")
	}
}

func TestStripFabricatedCalls(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain answer", "plain answer"},
		{"The tree has main.go.", "The tree has main.go."},
		{"Let me look.\n<tool_code>\n#list_directory(path='.')\n</tool_code>\nDone.", "Let me look.\nDone."},
		{"<tool_code>\n#read_file(path='a.go')\n</tool_code>", ""},
		{"before\n<parameter>path</parameter>\nafter", "before\nafter"},
	}
	for _, tc := range cases {
		if got := stripFabricatedCalls(tc.in); got != tc.want {
			t.Errorf("stripFabricatedCalls(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
