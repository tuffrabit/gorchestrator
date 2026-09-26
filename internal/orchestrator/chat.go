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
	// chatStreamPublishEvery throttles the chat_message republishes that
	// streamed reply text triggers. Tool rows publish immediately (they are
	// discrete and few); text parts can arrive many times a second, and the
	// drawer re-reads the whole thread anyway.
	chatStreamPublishEvery = 300 * time.Millisecond
)

// ErrChatThreadBusy reports that a turn is currently running for this thread;
// the caller should surface a retry rather than acting underneath the
// in-flight turn.
var ErrChatThreadBusy = fmt.Errorf("chat thread has a turn in progress")

// ChatService runs dashboard chat conversations against agent identities.
// Each SendMessage persists a user row plus a pending assistant placeholder,
// then processes the turn asynchronously: the placeholder's content is
// updated with stage text as the turn progresses, tool calls/responses land
// as tool rows, and model text is streamed into the open assistant row as it
// is produced — each of which republishes, so an open drawer renders the turn
// in real time instead of dumping it at the end. A text segment that starts
// after this turn's tool rows get a fresh assistant row of their own (so it
// sorts after them); a placeholder that only ever carried stage text is
// dropped at finalize. Turns for one thread are serialized and every turn
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
// this thread is inside its write phase (finalize/addToolCallMessage), and the
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
	// A turn that died mid-flight (crash, killed process) can leave a tool row
	// stuck in "running": no result event ever reached it. Nothing else owns
	// those rows, and this goroutine holds the only per-thread write lock, so
	// closing them here keeps the drawer from rendering a permanently pending
	// tool box.
	if n, err := s.eng.chatRepo.CloseRunningToolMessages(threadID, "(no result: the turn ended before this call returned)"); err != nil {
		log.Printf("chat thread %d: close stale tool calls: %v", threadID, err)
	} else if n > 0 {
		if thread, err := s.eng.chatRepo.GetThread(threadID); err == nil && thread != nil {
			s.publish(threadID, thread.ProjectID)
		}
	}
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
	// A turn's assistant rows are segments: the placeholder created up front
	// (so stage updates have somewhere to live) plus one more row per text
	// segment that begins after this turn's tool rows. Each segment is written
	// as its text arrives and republishes, so an open drawer renders the reply
	// and the tool calls in real time and in the order the model produced them.
	type chatSegment struct {
		id   int64
		text []string
	}
	segments := []*chatSegment{{id: assistantMsg.ID}}
	cur := segments[0]
	var lastToolID int64

	// openSegment returns the segment the next model text part belongs to: the
	// current one while nothing has been inserted after it, otherwise a fresh
	// pending assistant row placed after this turn's tool rows.
	openSegment := func() *chatSegment {
		if cur.id > lastToolID {
			return cur
		}
		id, err := eng.chatRepo.AddMessage(thread.ID, "assistant", "", "", "pending")
		if err != nil {
			log.Printf("chat thread %d: open reply segment: %v", thread.ID, err)
			return cur
		}
		cur = &chatSegment{id: id}
		segments = append(segments, cur)
		return cur
	}

	// streamText appends one model text part to the open segment, persists it
	// on the pending row, and republishes (throttled) so the reply shows up
	// while the turn is still running instead of at its end.
	var lastPublish time.Time
	streamText := func(text string) {
		if text == "" {
			return
		}
		seg := openSegment()
		seg.text = append(seg.text, text)
		if err := eng.chatRepo.SetMessageResult(seg.id, strings.Join(seg.text, "\n"), "pending"); err != nil {
			log.Printf("chat thread %d: stream reply text: %v", thread.ID, err)
			return
		}
		if time.Since(lastPublish) >= chatStreamPublishEvery {
			lastPublish = time.Now()
			s.publish(thread.ID, thread.ProjectID)
		}
	}

	// recordTool notes the newest agent-event row (tool call, tool response,
	// or thought) so a text segment that follows it opens a row that sorts
	// after this turn's event rows.
	recordTool := func(id int64) {
		if id > lastToolID {
			lastToolID = id
		}
	}

	// pendingToolCalls holds this turn's tool call rows that have not been
	// answered yet, keyed by tool name in call order. ADK emits the call and
	// the response as two separate events; matching them here writes the
	// result back onto the call's own row, so the drawer shows ONE box per
	// tool call (arguments while running, result when it lands) instead of a
	// call box followed by a second result box.
	pendingToolCalls := map[string][]int64{}
	takePendingCall := func(name string) int64 {
		queue := pendingToolCalls[name]
		if len(queue) == 0 {
			return 0
		}
		id := queue[0]
		if len(queue) == 1 {
			delete(pendingToolCalls, name)
		} else {
			pendingToolCalls[name] = queue[1:]
		}
		return id
	}

	// thoughtBuf accumulates the model's chain-of-thought parts since the
	// last flush. They are shown as their own chronological "thought" rows in
	// the drawer (response → call → thought → call → response), but kept out
	// of the model context: they are never streamed into assistant rows.
	var thoughtBuf []string

	// flushThoughts persists the buffered thoughts as one thought row and
	// republishes. It runs before every tool call row of the turn and at
	// event end, so the drawer shows the reasoning in the order it happened.
	flushThoughts := func() {
		if len(thoughtBuf) == 0 {
			return
		}
		text := strings.Join(thoughtBuf, "\n")
		thoughtBuf = nil
		id, err := eng.chatRepo.AddMessage(thread.ID, "thought", text, "", "done")
		if err != nil {
			log.Printf("chat thread %d: save thought: %v", thread.ID, err)
			return
		}
		s.publish(thread.ID, thread.ProjectID)
		recordTool(id)
	}

	// finalize lands the turn's final text on the open segment and closes the
	// earlier ones: segments that carry streamed text are completed in place
	// (they are real replies in the interleaving), and a segment that only ever
	// held stage text is deleted, so a tool turn leaves user → call → reply
	// rather than a stray placeholder plus a lumped reply. Any thoughts still
	// buffered are flushed first so no reasoning is dropped on an early-exit
	// path (fail, panic, or a clean end), and any tool call row still waiting
	// for a result is closed out instead of hanging in "running".
	finalize := func(text, status string) {
		flushThoughts()
		for name, ids := range pendingToolCalls {
			for _, id := range ids {
				if id == 0 {
					continue
				}
				if err := eng.chatRepo.SetMessageResult(id, "(no result: the turn ended before this call returned)", "error"); err != nil {
					log.Printf("chat thread %d: close unanswered tool call %d (%s): %v", thread.ID, id, name, err)
				}
			}
			delete(pendingToolCalls, name)
		}
		for _, seg := range segments {
			if seg == cur {
				if err := eng.chatRepo.SetMessageResult(seg.id, text, status); err != nil {
					log.Printf("chat thread %d: save assistant reply: %v", thread.ID, err)
				}
				continue
			}
			content := strings.Join(seg.text, "\n")
			if content == "" {
				if err := eng.chatRepo.DeleteMessage(seg.id); err != nil {
					log.Printf("chat thread %d: delete placeholder message: %v", thread.ID, err)
				}
				continue
			}
			if err := eng.chatRepo.SetMessageResult(seg.id, content, "done"); err != nil {
				log.Printf("chat thread %d: save reply segment: %v", thread.ID, err)
			}
		}
		_ = eng.chatRepo.TouchThread(thread.ID)
		s.publish(thread.ID, thread.ProjectID)
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("chat thread %d: turn panic: %v", thread.ID, r)
		}
		if !finished {
			// Safety net: a turn must never exit with a row still pending.
			finalize("Error: turn failed unexpectedly", "error")
		}
	}()
	fail := func(cause error) {
		finished = true
		finalize("Error: "+cause.Error(), "error")
	}

	stage("Preparing…")

	// Resolve the project and the merged agent config (global agents: entry),
	// mirroring stepAgentConfig.
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
	cfg, aerr := eng.cfg.AgentMust(thread.AgentType)
	if aerr != nil {
		fail(aerr)
		return
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
				// Parts land in emission order: narration that comes with a call
				// stays above the call it explains.
				if p.Text != "" && p.Thought {
					// Chain of thought: buffered for its own thought row, never
					// mixed into the assistant reply text.
					thoughtBuf = append(thoughtBuf, p.Text)
					continue
				}
				if p.Text != "" {
					streamText(p.Text)
				}
				if p.FunctionCall != nil {
					flushThoughts()
					id := s.addToolCallMessage(thread, p.FunctionCall.Name, p.FunctionCall.Args)
					pendingToolCalls[p.FunctionCall.Name] = append(pendingToolCalls[p.FunctionCall.Name], id)
					recordTool(id)
				}
			}
			// Event end: thoughts that were not followed by a call in this
			// event still get their row before the turn moves on.
			flushThoughts()
		} else if ev.Content.Role == genai.RoleUser {
			for _, p := range ev.Content.Parts {
				if p == nil || p.FunctionResponse == nil {
					continue
				}
				recordTool(s.completeToolMessage(thread, takePendingCall(p.FunctionResponse.Name), p.FunctionResponse.Name, p.FunctionResponse.Response))
			}
		}
	}

	// The open segment holds this turn's closing text; earlier segments were
	// already persisted as they streamed.
	finalText := strings.Join(cur.text, "\n")
	if finalText == "" {
		finalText = "(no response)"
	}
	finished = true
	finalize(finalText, "done")
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

// addToolCallMessage persists a tool call row (arguments only, status
// "running") and republishes immediately: the call is what a user waits on
// during a turn, so it renders the moment the model makes it instead of
// arriving in a batch at the end. It returns the row id (0 on failure) so the
// turn can fill in the result on the same row and keep a following text
// segment sorted after this turn's tool rows.
func (s *ChatService) addToolCallMessage(thread *sqlite.ChatThread, name string, args any) int64 {
	id, err := s.eng.chatRepo.AddToolCall(thread.ID, name, toolPayloadText(args))
	if err != nil {
		log.Printf("chat thread %d: save tool call %s: %v", thread.ID, name, err)
		return 0
	}
	s.publish(thread.ID, thread.ProjectID)
	return id
}

// completeToolMessage writes a tool result onto its own call's row: one row,
// one box in the drawer. callID is the row addToolCallMessage created for this
// call; when the response arrives with no matching call row (a call the model
// never surfaced, or a call row whose insert failed) the result gets its own
// row so nothing is lost. It returns the row id so the turn keeps its event
// ordering.
func (s *ChatService) completeToolMessage(thread *sqlite.ChatThread, callID int64, name string, result any) int64 {
	content := toolPayloadText(result)
	if callID != 0 {
		if err := s.eng.chatRepo.SetMessageResult(callID, content, "done"); err != nil {
			log.Printf("chat thread %d: save tool result %s (row %d): %v", thread.ID, name, callID, err)
			return callID
		}
		s.publish(thread.ID, thread.ProjectID)
		return callID
	}
	id, err := s.eng.chatRepo.AddMessage(thread.ID, "tool", content, name, "done")
	if err != nil {
		log.Printf("chat thread %d: save orphan tool result %s: %v", thread.ID, name, err)
		return 0
	}
	s.publish(thread.ID, thread.ProjectID)
	return id
}

// toolPayloadText renders one tool payload (call arguments or tool result) for
// storage: JSON when the payload marshals, the Go rendering otherwise, always
// capped so a huge tool output cannot bloat the thread.
func toolPayloadText(payload any) string {
	if data, err := json.Marshal(payload); err == nil {
		return cappedText(string(data))
	}
	return cappedText(fmt.Sprintf("%v", payload))
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
