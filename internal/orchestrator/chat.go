package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/tuffrabit/gorchestrator/internal/agents"
	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/tools"
)

const (
	// chatMaxMessageLen caps a single user message; anything longer is
	// rejected rather than silently truncated.
	chatMaxMessageLen = 8000
	// chatTurnTimeout bounds one processing goroutine (which may cover
	// several queued turns) so a wedged model call cannot pin a thread
	// forever.
	chatTurnTimeout = 30 * time.Minute
)

// ErrChatThreadBusy reports that a turn is currently running for this thread;
// the caller should surface a retry rather than acting underneath the
// in-flight turn.
var ErrChatThreadBusy = fmt.Errorf("chat thread has a turn in progress")

// ChatService runs dashboard chat conversations against agent identities.
// Each SendMessage persists a user row plus a pending assistant placeholder,
// then processes the turn asynchronously: the placeholder's content is
// updated with stage text as the turn progresses and tool calls/responses
// land as tool rows. When the turn finishes with tool rows, the placeholder
// is replaced by a fresh assistant row so the final reply (or error) sorts
// after the tool rows of that turn; tool-less turns update it in place.
// Turns for one thread are serialized and every turn
// reseeds the ADK session from the DB, so processing is stateless and a
// reopened drawer always renders truth.
type ChatService struct {
	eng *Engine
	mu  sync.Mutex
	// threadLocks serializes processing per thread so back-to-back
	// messages run in order.
	threadLocks map[int64]*sync.Mutex
	// newModel builds the turn LLM; replaceable in tests.
	newModel func(ctx context.Context, cfg llm.Config) (adkmodel.LLM, error)
}

func newChatService(eng *Engine) *ChatService {
	return &ChatService{
		eng:         eng,
		threadLocks: map[int64]*sync.Mutex{},
		newModel:    llm.New,
	}
}

// SendMessage validates and persists the message pair (user row + pending
// assistant placeholder), notifies subscribers, and launches processing on a
// detached context so HTTP disconnects / drawer closes never cancel a
// response. It returns once the rows are durable; processing continues in
// the background.
func (s *ChatService) SendMessage(ctx context.Context, threadID int64, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("message text is required")
	}
	if len(text) > chatMaxMessageLen {
		return fmt.Errorf("message exceeds %d characters", chatMaxMessageLen)
	}
	thread, err := s.eng.chatRepo.GetThread(threadID)
	if err != nil {
		return fmt.Errorf("get chat thread: %w", err)
	}
	if thread == nil {
		return fmt.Errorf("chat thread %d not found", threadID)
	}
	if _, err := s.eng.chatRepo.AddMessage(threadID, "user", text, "", "done"); err != nil {
		return fmt.Errorf("save user message: %w", err)
	}
	if _, err := s.eng.chatRepo.AddMessage(threadID, "assistant", "", "", "pending"); err != nil {
		return fmt.Errorf("save assistant message: %w", err)
	}
	_ = s.eng.chatRepo.TouchThread(threadID)
	s.publish(threadID, thread.ProjectID)

	// Process on a detached context: the turn must survive the HTTP request
	// that spawned it.
	go s.processDetached(threadID)
	return nil
}

// ClearThread empties one thread's persisted conversation (and therefore the
// model context for the next turn) without deleting the thread itself.
// Retention of other threads is untouched. It is safe against the processing
// loop for two reasons: TryLock on the per-thread lock means no goroutine for
// this thread is inside its write phase (finalize/addToolMessage), and the
// id <= maxID watermark means a message pair a concurrent SendMessage inserts
// after the snapshot survives and is processed normally (the HTTP handler must
// not block behind a turn that can hold the lock for up to chatTurnTimeout).
// It publishes EventChatMessage so other open drawers re-render.
func (s *ChatService) ClearThread(ctx context.Context, threadID int64) (int, error) {
	thread, err := s.eng.chatRepo.GetThread(threadID)
	if err != nil {
		return 0, fmt.Errorf("get chat thread: %w", err)
	}
	if thread == nil {
		return 0, fmt.Errorf("chat thread %d not found", threadID)
	}

	lock := s.threadLock(threadID)
	if !lock.TryLock() {
		return 0, ErrChatThreadBusy
	}
	defer lock.Unlock()

	maxID, err := s.eng.chatRepo.MaxMessageID(threadID)
	if err != nil {
		return 0, err
	}
	n, err := s.eng.chatRepo.ClearMessagesUpTo(threadID, maxID)
	if err != nil {
		return 0, err
	}
	_ = s.eng.chatRepo.TouchThread(threadID)
	s.publish(threadID, thread.ProjectID)
	return n, nil
}

// processDetached runs the per-thread processing loop with its own timeout
// context and the per-thread serialization lock.
func (s *ChatService) processDetached(threadID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), chatTurnTimeout)
	defer cancel()
	lock := s.threadLock(threadID)
	lock.Lock()
	defer lock.Unlock()
	s.processThread(ctx, threadID)
}

// processThread drains pending assistant turns for the thread, oldest first,
// looping until a snapshot shows nothing pending — so messages sent while a
// turn was in flight are picked up by the same goroutine, in order.
func (s *ChatService) processThread(ctx context.Context, threadID int64) {
	eng := s.eng
	// current is the assistant row handed to processTurn; the recover defer
	// is the safety net that keeps it from dangling in 'pending' if a panic
	// ever escapes processTurn.
	var current *sqlite.ChatMessage
	defer func() {
		if r := recover(); r != nil {
			log.Printf("chat thread %d: processing panic: %v", threadID, r)
			if current != nil {
				_ = eng.chatRepo.SetMessageResult(current.ID, fmt.Sprintf("Error: internal panic: %v", r), "error")
				_ = eng.chatRepo.TouchThread(threadID)
				if thread, err := eng.chatRepo.GetThread(threadID); err == nil && thread != nil {
					s.publish(threadID, thread.ProjectID)
				}
			}
		}
	}()

	for {
		thread, err := eng.chatRepo.GetThread(threadID)
		if err != nil || thread == nil {
			return
		}
		msgs, err := eng.chatRepo.ListMessages(threadID)
		if err != nil {
			log.Printf("chat thread %d: list messages: %v", threadID, err)
			return
		}
		var assistant *sqlite.ChatMessage
		for _, m := range msgs {
			if m.Role == "assistant" && m.Status == "pending" {
				assistant = m
				break
			}
		}
		if assistant == nil {
			return
		}
		var user *sqlite.ChatMessage
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" && msgs[i].ID < assistant.ID {
				user = msgs[i]
				break
			}
		}
		if user == nil {
			_ = eng.chatRepo.SetMessageResult(assistant.ID, "Error: no user message found for this turn", "error")
			s.publish(threadID, thread.ProjectID)
			continue
		}
		current = assistant
		s.processTurn(ctx, thread, user, assistant)
		current = nil
	}
}

// processTurn runs one chat turn: resolve config, acquire the model, build
// the chat agent, reseed session history, run, and record the result. The
// assistant row is never left pending: every exit path (including panics via
// the deferred safety net) lands it on done or error.
func (s *ChatService) processTurn(ctx context.Context, thread *sqlite.ChatThread, userMsg, assistantMsg *sqlite.ChatMessage) {
	eng := s.eng

	// stage updates the pending assistant row with progress text and
	// notifies subscribers (an open drawer re-renders; a reopened one shows
	// the latest stage instead of a bare spinner).
	stage := func(text string) {
		if err := eng.chatRepo.SetMessageResult(assistantMsg.ID, text, "pending"); err != nil {
			log.Printf("chat thread %d: stage update: %v", thread.ID, err)
		}
		s.publish(thread.ID, thread.ProjectID)
	}
	finished := false
	var toolRows int
	// finalize lands the turn's final assistant row. When the turn produced
	// tool rows, a fresh assistant row is inserted rather than updating the
	// pending one in place, so the reply sorts after the tool calls/
	// responses of this turn (the pending row was created up front so stage
	// updates have somewhere to live). Without tool rows the pending row is
	// updated in place, keeping the plain user/reply interleaving stable even
	// when a later message is sent while this turn is still running.
	finalize := func(text, status string) {
		if toolRows == 0 {
			if err := eng.chatRepo.SetMessageResult(assistantMsg.ID, text, status); err != nil {
				log.Printf("chat thread %d: save assistant reply: %v", thread.ID, err)
			}
			return
		}
		if _, err := eng.chatRepo.AddMessage(thread.ID, "assistant", text, "", status); err != nil {
			log.Printf("chat thread %d: insert final message: %v", thread.ID, err)
			_ = eng.chatRepo.SetMessageResult(assistantMsg.ID, text, status)
		} else if err := eng.chatRepo.DeleteMessage(assistantMsg.ID); err != nil {
			log.Printf("chat thread %d: delete pending message: %v", thread.ID, err)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("chat thread %d: turn panic: %v", thread.ID, r)
		}
		if !finished {
			// Safety net: a turn must never exit with the row still pending.
			finalize("Error: turn failed unexpectedly", "error")
			_ = eng.chatRepo.TouchThread(thread.ID)
			s.publish(thread.ID, thread.ProjectID)
		}
	}()
	fail := func(cause error) {
		finished = true
		finalize("Error: "+cause.Error(), "error")
		_ = eng.chatRepo.TouchThread(thread.ID)
		s.publish(thread.ID, thread.ProjectID)
	}

	stage("Preparing…")

	// Resolve the project and the merged agent config (global identity +
	// project flavor overlay), mirroring agentConfigForIssue.
	project, err := eng.projects.Get(thread.ProjectID)
	if err != nil {
		fail(fmt.Errorf("resolve project %d: %w", thread.ProjectID, err))
		return
	}
	if project == nil {
		fail(fmt.Errorf("resolve project %d: not found", thread.ProjectID))
		return
	}
	pc, err := eng.typedProjectConfig(project)
	if err != nil {
		fail(err)
		return
	}
	cfg := eng.cfg.Agent(thread.AgentType)
	if overlay, ok, oerr := pc.FlavorOverlay(thread.AgentType, thread.Flavor); oerr != nil {
		fail(oerr)
		return
	} else if ok {
		cfg = config.MergeAgent(cfg, overlay)
	}

	release, err := s.acquireChatModel(ctx, thread, cfg, stage)
	if err != nil {
		fail(err)
		return
	}
	defer release()

	stage("Thinking…")
	llmModel, err := s.newModel(ctx, llm.Config{
		Provider:    cfg.Model.Provider,
		Model:       cfg.Model.Model,
		APIKeyEnv:   cfg.Model.APIKeyEnv,
		BaseURL:     cfg.Model.BaseURL,
		Timeout:     modelTimeout(cfg.Model),
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
	})
	if err != nil {
		fail(fmt.Errorf("build model: %w", err))
		return
	}

	// Tools: read-only host tools rooted at a real, project-level source
	// tree (snapshot directory for source_path projects, a per-project
	// read-only git worktree for git-mode projects), filtered by the agent
	// allowlist, plus any MCP server tools. chatSourceRoot materializes the
	// worktree first — NewHostReadOnlyRegistry requires an absolute, existing
	// root — and returns "" when the project has no source at all.
	var registry []tool.Tool
	root, refreshed, err := eng.chatSourceRoot(ctx, project, pc)
	if err != nil {
		fail(fmt.Errorf("prepare chat source: %w", err))
		return
	}
	if root != "" {
		hostTools, err := tools.NewHostReadOnlyRegistry(root, eng.cfg.Tools.ReadFile.MaxBytes, eng.cfg.Tools.ReadFile.MaxLines)
		if err != nil {
			fail(fmt.Errorf("build host tools: %w", err))
			return
		}
		registry = tools.FilterByNames(hostTools, cfg.Tools)
	}
	if eng.mcp != nil && len(cfg.MCPServers) > 0 {
		mcpTools, err := eng.mcp.ToolsForAgent(cfg.MCPServers)
		if err != nil {
			fail(fmt.Errorf("mcp tools: %w", err))
			return
		}
		registry = append(registry, mcpTools...)
	}

	// No silent tool-less turns: a chat agent told it has tools but given no
	// declarations imitates tool calls in prose. A missing source or an
	// allowlist that filters everything out is a config error — fail loudly.
	if len(registry) == 0 {
		fail(fmt.Errorf("no source tree or tools available for project %q (set projects.%s.source_path or git.repo_url)", project.Name, project.Name))
		return
	}
	log.Printf("chat thread %d: %d tools exposed (root=%s refreshed=%v)", thread.ID, len(registry), root, refreshed)

	agentInst, err := agents.NewChat(thread.AgentType, cfg.SystemPrompt).Build(llmModel, registry)
	if err != nil {
		fail(fmt.Errorf("build chat agent: %w", err))
		return
	}
	wrapper, err := agent.New(agent.Config{
		Name:        "chat-" + thread.AgentType + "-runner",
		Description: "wrapper to run a chat-mode agent through the runner",
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return llmagent.RunLLMAgentAsNode(agentInst, agent.NewContext(ctx), ctx.UserContent())
		},
	})
	if err != nil {
		fail(fmt.Errorf("create agent wrapper: %w", err))
		return
	}

	userID := fmt.Sprintf("user-%d", thread.UserID)
	sessionID := fmt.Sprintf("chat-%d", thread.ID)
	sessSvc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "gorchestrator",
		Agent:             wrapper,
		SessionService:    sessSvc,
		AutoCreateSession: true,
	})
	if err != nil {
		fail(fmt.Errorf("create runner: %w", err))
		return
	}

	// History seeding (stateless, every turn): a fresh in-memory session is
	// populated with one event per prior DB message — user rows as user
	// content, finished assistant rows as model content authored by the
	// chat agent (any other author would be rewritten as a foreign "For
	// context:" message by the ADK contents processor). Tool rows, pending
	// rows, error rows, and the current user message are skipped.
	sess, err := chatSession(ctx, sessSvc, userID, sessionID)
	if err != nil {
		fail(fmt.Errorf("create chat session: %w", err))
		return
	}
	msgs, err := eng.chatRepo.ListMessages(thread.ID)
	if err != nil {
		fail(fmt.Errorf("list chat history: %w", err))
		return
	}
	author := "chat-" + thread.AgentType
	for _, m := range msgs {
		if m.ID >= userMsg.ID {
			break
		}
		var ev *session.Event
		switch {
		case m.Role == "user":
			ev = session.NewEvent(ctx, "seed")
			ev.Author = "user"
			ev.Content = genai.NewContentFromText(m.Content, genai.RoleUser)
		case m.Role == "assistant" && m.Status == "done":
			content := stripFabricatedCalls(m.Content)
			if content == "" {
				// The stored reply contained only imitated tool-call markup;
				// reseeding it would few-shot prime this turn to fake calls.
				continue
			}
			ev = session.NewEvent(ctx, "seed")
			ev.Author = author
			ev.Content = genai.NewContentFromText(content, genai.RoleModel)
		default:
			continue
		}
		if err := sessSvc.AppendEvent(ctx, sess, ev); err != nil {
			fail(fmt.Errorf("seed chat history: %w", err))
			return
		}
	}

	var texts []string
	for ev, err := range r.Run(ctx, userID, sessionID, genai.NewContentFromText(userMsg.Content, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			fail(fmt.Errorf("chat turn: %w", err))
			return
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		if ev.Content.Role == genai.RoleModel {
			for _, p := range ev.Content.Parts {
				if p == nil {
					continue
				}
				if p.Text != "" {
					texts = append(texts, p.Text)
				}
				if p.FunctionCall != nil {
					s.addToolMessage(thread.ID, p.FunctionCall.Name, p.FunctionCall.Args)
					toolRows++
				}
			}
		} else if ev.Content.Role == genai.RoleUser {
			for _, p := range ev.Content.Parts {
				if p == nil || p.FunctionResponse == nil {
					continue
				}
				s.addToolMessage(thread.ID, p.FunctionResponse.Name, p.FunctionResponse.Response)
				toolRows++
			}
		}
	}

	finalText := strings.Join(texts, "\n")
	if finalText == "" {
		finalText = "(no response)"
	}
	finalize(finalText, "done")
	_ = eng.chatRepo.TouchThread(thread.ID)
	finished = true
	s.publish(thread.ID, thread.ProjectID)
}

// acquireChatModel mirrors Engine.acquirePhaseModel residency semantics for a
// chat turn — same sideload exemption, same exclusive-mode lock with priority
// requeue, same reconcile (reuse the resident model, evict foreign residents
// with the sideloaded keep list, then load) — but reports progress through
// the stage callback instead of events.jsonl, and deliberately never touches
// the inference breaker. The returned release frees the exclusive-mode lock
// while KEEPING the model resident: chat follow-ups stay fast, and the next
// pipeline phase reconciles/evicts for itself. Release is safe to call after
// a failed acquire (it is a no-op then).
func (s *ChatService) acquireChatModel(ctx context.Context, thread *sqlite.ChatThread, cfg config.AgentConfig, stage func(string)) (func(), error) {
	eng := s.eng
	inf := eng.cfg.Inference
	if inf.Type == "" || eng.controller == nil {
		return func() {}, nil
	}
	model := cfg.Model.Model
	// Sideloaded models live outside the swap lifecycle: no lock, no load,
	// llama-swap serves them on request.
	if cfg.Sideload != nil && *cfg.Sideload {
		return func() {}, nil
	}
	sideloaded := eng.sideloadedModels()
	keep := make([]string, 0, len(sideloaded))
	for m := range sideloaded {
		keep = append(keep, m)
	}
	locked := false
	release := func() {
		if locked {
			modelLocks.Unlock(inf.BaseURL)
			locked = false
		}
	}

	if inf.Mode == "exclusive" {
		key := inf.BaseURL
		if !modelLocks.TryLock(key) {
			stage("Waiting for model…")
			modelLocks.LockPriority(key)
		}
		locked = true

		// Reconcile residency before loading: reuse a resident match, evict
		// foreign non-sideloaded residents first so the turn never runs
		// against the wrong model.
		running, err := eng.controller.RunningModels(ctx)
		if err != nil {
			release()
			return nil, fmt.Errorf("check running models: %w", err)
		}
		resident := make([]string, 0, len(running))
		for _, m := range running {
			if !sideloaded[m] {
				resident = append(resident, m)
			}
		}
		reuse := len(resident) == 1 && resident[0] == model
		if !reuse && len(resident) > 0 {
			if err := eng.controller.UnloadAll(ctx, keep...); err != nil {
				release()
				return nil, fmt.Errorf("unload resident models: %w", err)
			}
		}
		if !reuse {
			stage(fmt.Sprintf("Loading model %s…", model))
			if err := eng.controller.EnsureLoaded(ctx, model, modelTimeout(cfg.Model)); err != nil {
				release()
				return nil, fmt.Errorf("load model %s: %w", model, err)
			}
		}
		return release, nil
	}

	stage(fmt.Sprintf("Loading model %s…", model))
	if err := eng.controller.EnsureLoaded(ctx, model, modelTimeout(cfg.Model)); err != nil {
		return nil, fmt.Errorf("load model %s: %w", model, err)
	}
	return release, nil
}

// addToolMessage persists one tool call/response row for the drawer.
func (s *ChatService) addToolMessage(threadID int64, name string, payload any) {
	data, err := json.Marshal(payload)
	content := fmt.Sprintf("%v", payload)
	if err == nil {
		content = cappedText(string(data))
	}
	if _, err := s.eng.chatRepo.AddMessage(threadID, "tool", content, name, "done"); err != nil {
		log.Printf("chat thread %d: save tool message: %v", threadID, err)
	}
}

func (s *ChatService) publish(threadID, projectID int64) {
	s.eng.Publish(Event{
		Type:      EventChatMessage,
		ProjectID: projectID,
		Data:      map[string]any{"thread_id": threadID},
	})
}

// threadLock returns the per-thread processing mutex, created lazily.
func (s *ChatService) threadLock(threadID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.threadLocks[threadID]
	if !ok {
		l = &sync.Mutex{}
		s.threadLocks[threadID] = l
	}
	return l
}

// fabricatedCallPrefixes open an imitated tool-call block (closing tags such
// as </tool_code> and <tool_response> close one).
var fabricatedCallPrefixes = []string{
	"<tool_code>",
	"antml:invoke",
	"<tool_call>",
	"<function=",
	"<parameter>",
	"#list_directory(",
	"#read_file(",
	"#grep_search(",
}

// fabricatedCallClosers close an imitated tool-call block.
var fabricatedCallClosers = []string{
	"</tool_code>",
	"antml:invoke>",
	"<tool_call>",
	"</function>",
	"</parameter>",
}

// stripFabricatedCalls removes imitated tool-call markup from a stored
// assistant reply before it is reseeded into a chat session. A line is
// dropped when it carries an imitated-call marker (opening tag, closing
// tag, or inline marker); leading blank lines around the drop are trimmed
// away. It returns "" when nothing useful remains, in which case the
// caller skips the row.
func stripFabricatedCalls(s string) string {
	if !containsFabricatedCall(s) {
		return s
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if containsFabricatedCall(strings.TrimSpace(line)) {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// containsFabricatedCall reports whether s carries any imitated tool-call
// markup (opening prefix, closing tag, or inline marker).
func containsFabricatedCall(s string) bool {
	for _, p := range fabricatedCallPrefixes {
		if strings.Contains(s, p) {
			return true
		}
	}
	for _, c := range fabricatedCallClosers {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

// chatSession returns the runner session, creating it when absent. A fresh
// InMemoryService is used per turn, so Create normally succeeds; the
// Get-first dance keeps this correct if that ever changes.
func chatSession(ctx context.Context, svc session.Service, userID, sessionID string) (session.Session, error) {
	if res, err := svc.Get(ctx, &session.GetRequest{AppName: "gorchestrator", UserID: userID, SessionID: sessionID}); err == nil && res != nil && res.Session != nil {
		return res.Session, nil
	}
	res, err := svc.Create(ctx, &session.CreateRequest{AppName: "gorchestrator", UserID: userID, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return res.Session, nil
}
