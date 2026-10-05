# Plan C — Replace ADK with a native agent loop (openai-go for OpenAI providers)

Goal: remove `google.golang.org/adk/v2` entirely and replace the three things it
provides — the LLM-agent tool loop (`llmagent`), the runner/session plumbing
(`runner`, `session`), and tool declaration/dispatch (`functiontool`) — with a
native `internal/agentloop` package plus small shims. OpenAI-compatible
providers go through `github.com/openai/openai-go/v3`; Anthropic and Gemini keep
existing adapter code re-pointed at the new local interfaces.

Why consider this at all: it is the only option that eliminates (rather than
mitigates) the genai↔OpenAI turn-structure impedance mismatch — ADK packing tool
results into `user`-role contents, history-author rewriting, the reseeding
workarounds in `internal/orchestrator/chat.go:530-574` — because gorchestrator
then owns the message list end to end.

Estimated effort: **~1.5–2.5 weeks** broken down in §6.

---

## 1. Verified facts this plan relies on

- The ADK surface actually consumed is shallow (full inventory in the eval that
  preceded this plan): one `llmagent` per run (task or chat mode), the
  runner + wrapper-agent pattern at `internal/orchestrator/engine.go:1330-1350`
  and `internal/orchestrator/chat.go:504-528`, fresh per-run
  `session.InMemoryService()` sessions that are manually reseeded from SQLite,
  `functiontool.New` at 11 sites whose handlers are plain functions over
  `context.Context`, and `genai.Content/Part` as the data model. No callbacks,
  no sub-agents, no artifacts, no plugins, no stop sequences, no `ev.Actions`.
- `model.LLM` is a two-method interface
  (`adk/v2@v2.0.0/model/llm.go:26-29`): `Name()` and
  `GenerateContent(ctx, *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error]`.
  `LLMRequest`/`LLMResponse` are plain structs. **Copying those struct
  definitions into `internal/llm` makes every existing adapter — OpenAI,
  Anthropic, DryRun, and the `BudgetLLM` wrapper — compile unchanged against a
  local interface.** This is the central design decision; it converts most of
  the migration from "rewrite" to "repoint an import".
- `google.golang.org/genai` is a standalone module (not ADK). Keeping it as the
  internal data model costs nothing, keeps the Gemini adapter trivial, and lets
  the existing per-provider converters in `internal/llm/openai.go` /
  `anthropic.go` serve as reference implementations.
- ADK's `llmagent` behavior that must be replicated (from
  `adk/v2@v2.0.0/internal/llminternal/base_flow.go`):
  - tool results are packed into `user`-role `Content`s with `FunctionResponse`
    parts (`base_flow.go:1174-1188`); parallel calls in one turn are executed
    concurrently and merged into one content (`base_flow.go:1216`,
    `mergeParallelFunctionResponseEvents`);
  - task mode loops until the model stops calling tools, with the `finish_task`
    declaration derived from `OutputSchema` injected into the request;
  - usage metadata flows through `LLMResponse.UsageMetadata` (already what
    `BudgetLLM` and the engine's `usage` events consume).
- openai-go v3 (`v3.64.0`, the line ADK itself pins): streaming via
  `client.Chat.Completions.NewStreaming(ctx, params)` returning an
  `*ssestream.Stream[ChatCompletionChunk]`; built-in retries; `RawJSON()` is
  retained on every generated type (`chatcompletion.go:1073`, `:1140`) — the
  hook for recovering `reasoning_content`, which the typed structs otherwise
  drop entirely (zero matches for `reasoning_content` in the module).

## 2. Target architecture

```
internal/agentloop          NEW — the loop ADK used to own
  loop.go       Loop(ctx, Model, []Tool, History, Config) iter.Seq2[*Event, error]
  events.go     Event{Kind: TextDelta|Text|Thought|ToolCall|ToolResult|Usage|TurnEnd, ...}
  task.go       task-mode driver (finish_task detection, output schema injection)

internal/tools              MODIFIED — drop functiontool
  tool.go       Tool interface (below); registry + FilterByNames unchanged
  each tool     functiontool.New(cfg, fn) → NewTool(cfg, fn); handler bodies untouched

internal/llm                MODIFIED
  types.go      LLMRequest/LLMResponse copies (replace adk/v2/model import)
  openai.go     REWRITTEN on openai-go/v3 (streaming, tool-call accumulation)
  anthropic.go  untouched logic, repointed imports
  gemini.go     ADK gemini plugin → genai SDK Models.GenerateContentStream
  dryrun.go, budget.go, schema.go  untouched, repointed imports

internal/orchestrator       MODIFIED
  engine.go     runAgentLoop: swap runner/wrapper/session block for agentloop
  chat.go       processTurn: same swap; history built directly from SQLite
  single_shot.go, budget.go   untouched except imports/types

internal/agents             MODIFIED — Build returns agentloop.Config, not agent.Agent
internal/mcp                MODIFIED — wrapMCPTool targets the new Tool interface

go.mod: remove google.golang.org/adk/v2; add github.com/openai/openai-go/v3;
promote github.com/google/jsonschema-go to a direct dependency.
```

Key interfaces:

```go
// internal/llm/types.go — verbatim copies of the ADK structs (LLMRequest,
// LLMResponse and their genai fields) so the Model signature survives:
type Model interface {
    Name() string
    GenerateContent(ctx context.Context, req *LLMRequest, stream bool) iter.Seq2[*LLMResponse, error]
}

// internal/tools/tool.go:
type Tool interface {
    Name() string
    Description() string
    Declaration() *genai.FunctionDeclaration   // same shape task.json + requests use today
    Run(ctx context.Context, args map[string]any) (map[string]any, error)
}
```

The loop's `Event` kinds map 1:1 onto what the two consumers already handle, so
`engine.go`'s `events.jsonl` writes and `chat.go`'s segment/streamText machinery
survive with mechanical re-keying rather than redesign:

| agentloop event | engine.go today | chat.go today |
|---|---|---|
| `Text` (delta) | skipped (audit) | `streamText` (`:289`) |
| `Thought` | — | `thoughtBuf` (`:339`) |
| `ToolCall` | `tool_call` event (`:1402`) | `addToolCallMessage` (`:600`) |
| `ToolResult` | `tool_result` event (`:1439`) | `completeToolMessage` (`:615`) |
| `Usage` | `usage` event (`:1377`) | — |
| `TurnEnd` | `model_turn` event (`:1426`) | `finalize` (`:627`) |

## 3. Milestones (each ends with the build and full test suite green)

### M0 — Local model types (½ day)

- Add `internal/llm/types.go` with `LLMRequest`, `LLMResponse` copied from
  `adk/v2/model/llm.go` (all fields the project touches: `Model`, `Contents`,
  `Config`, `Tools`; `Content`, `UsageMetadata`, `Partial`, `TurnComplete`).
- Change `internal/llm` to use the local types; delete the
  `google.golang.org/adk/v2/model` import. `openai.go`, `anthropic.go`,
  `dryrun.go`, `budget.go` compile with import/identifier edits only.
- `internal/orchestrator/budget.go` (`wrapModelWithBudget`) and
  `single_shot.go` repoint likewise.
- Tests: `budget_test.go`, `single_shot_test.go` stubs swap `model.LLM` →
  `llm.Model` (mechanical).

### M1 — Tool layer without functiontool (1–2 days)

- `internal/tools/tool.go`: the `Tool` interface above + a generic constructor
  replacing `functiontool.New`:
  ```go
  func NewTool[A any, R any](cfg ToolConfig, fn func(ctx context.Context, args A) (R, error)) Tool
  ```
  Schema inference keeps the exact mechanism functiontool used — struct-tag
  reflection via `github.com/google/jsonschema-go` (promote to direct dep in
  go.mod) stored in `Declaration().ParametersJsonSchema`. This keeps
  `declaration_test.go` (`internal/tools/declaration_test.go:13-81`) passing
  with only constructor renames.
- Rewrite the 11 `functiontool.New` call sites (`internal/tools/*.go`,
  `internal/mcp/manager.go:248`): identical config and handlers; only the
  constructor name changes. `agent.Context` parameters become
  `context.Context` (they were only ever used as contexts — confirmed across
  all handlers, e.g. `read_file.go:53`, `runtest.go:114`, `hostfs.go:71`).
- Keep error mapping semantics: handler `error` → `{"error": err.Error()}`,
  except `runtest.go`'s intentional in-band soft failures (`runtest.go:64-83`)
  which return `(result, nil)` today and must stay in-band. Panic recovery that
  functiontool provided (`functiontool/function.go:187-191`) is reimplemented
  once, in `NewTool`.
- `engine.go:2157-2174` (`schemasFromTools`) keeps asserting the same
  `Declaration()` shape.

### M2 — The agent loop (3–5 days, the core)

New package `internal/agentloop`:

- `loop.go`:
  - Inputs: `Model`, `[]tools.Tool`, history `[]*genai.Content`, `Config{
    Mode (Task|Chat), SystemInstruction, Temperature, MaxTokens }`.
  - Builds `LLMRequest` per iteration: contents = history + system instruction
    on `Config`, tools from `Declaration()` (plus `finish_task` in task mode,
    derived from `agents.finishTaskSchema` — same `genai.Schema` object, so the
    declaration path is unchanged).
  - Calls `GenerateContent(ctx, req, true)` and consumes the stream
    (`Partial` text/thought deltas → `TextDelta`/`Thought` events; the final
    response → `Text`, `ToolCall`s, `Usage`, `TurnEnd`).
  - Task mode: stop when a `finish_task` call is observed (the loop executes it
    like any tool; the engine reads `done`/`rationale` from the call args,
    exactly as at `engine.go:1414-1423`). Chat mode: stop when the final
    response has no tool calls.
  - Tool execution: run the turn's calls concurrently (matching ADK's behavior
    at `base_flow.go:1085-1215`), emit one `ToolResult` event per call, append
    results to history as one `user`-role content with `FunctionResponse` parts
    **in call order** (ADK merges them the same way;
    `mergeParallelFunctionResponseEvents`), then iterate.
  - Cancellation: honor `ctx.Done()` between iterations and during tool runs
    (today: context timeout only — no regression).
  - Budget: `llm.IsBudgetExceeded` errors propagate out of the loop unchanged
    so `engine.go:1368` keeps working.
- `events.go`: the `Event` struct and kinds from the table in §2. Emit
  incrementally through an `iter.Seq2` so both consumers keep their
  `for ev, err := range ...` shape.
- Streaming detail the old code never had to handle: openai-go delivers tool
  calls as per-index deltas; accumulate `ID`/`Name`/`Arguments` fragments by
  `ToolCalls[i].Index` across chunks and only emit complete calls. The ADK
  plugin's accumulator (`adk/v2@v2.5.0/model/openaimodel/internal/completions/
  stream.go`) is the reference implementation.
- Decision — internal turn packing: keep the genai `Content`/`Part` shapes
  (`FunctionResponse` in `user` contents) rather than inventing OpenAI-shaped
  internals. Rationale: the two consumers, the SQLite reseeding, `task.json`
  schemas, and both surviving converters already speak it, and the loop now
  controls it directly. OpenAI-message-shape assembly moves into the OpenAI
  adapter where it belongs.

### M3 — OpenAI adapter on openai-go (2–3 days)

Rewrite `internal/llm/openai.go` (~300 lines) on `openai-go/v3`:

- `openai.NewClient(option.WithAPIKey(...), option.WithBaseURL(...),
  option.WithHTTPClient(&http.Client{Timeout: timeout}))`. SDK provides retries;
  delete the hand-rolled retry/backoff block (`openai.go:87-163`) but keep a
  test that 429s are retried to pin the behavior.
- Non-streaming path: `client.Chat.Completions.New`. Streaming path:
  `NewStreaming`, `stream.Next()/Current()`, `defer stream.Close()`, with
  `StreamOptions.IncludeUsage` so usage lands on the final chunk.
- Port the battle-tested conversions from the current file, keeping the
  comments as behavior specs:
  - `convertContents` (`openai.go:198-298`): system message, `thought`-part
    suppression on echo, tool responses as separate `tool` messages, `content`
    key always present for non-assistant and empty-string content for
    tool-calls-only assistant messages (llama.cpp strictness).
  - Synthesized `tool_call_id`s (`call_<name>`) for servers that emit empty IDs
    (`openai.go:239-243`) — keep; the ADK plugin instead errors on these, which
    our fleet may produce.
  - `reasoning_content`: recover via `chunk.RawJSON()` side-parse into
    `genai.Part{Thought: true}` — the typed SDK drops the field; RawJSON is
    retained (`chatcompletion.go:1073`, `:1140`). ~20 lines, one test.
- Schema emission: reuse `llm.DeclarationParameters` (`schema.go:24`) — output
  is exactly what openai-go's function-tool params accept (`any`).

### M4 — Rewire consumers (2–3 days)

`internal/orchestrator/engine.go` (`runAgentLoop`, `:1261-1460`):
- Delete the wrapper-agent + `runner.New` + `session.InMemoryService` block
  (`:1330-1350`) and the `r.Run` loop header (`:1357`); replace with
  `agentloop.Loop(...)` over a history assembled directly from
  `buildLoopInput` (`:1643`) — task-mode pipelines seed history per loop from
  the phase input only, so the in-memory session was already adding nothing.
- Keep the event-mapping body (`usage`/`tool_call`/`tool_result`/`model_turn`/
  `finish_task` extraction) keyed on the new event kinds.

`internal/orchestrator/chat.go` (`processTurn`, `:440-628`):
- Delete the wrapper agent (`:504-514`), runner (`:519-528`), and the
  `chatSession` + `AppendEvent` reseeding (`:536-574`, helper at `:843-852`).
- Build `[]*genai.Content` directly from `chatRepo.ListMessages`: user rows →
  `RoleUser` contents, done assistant rows → `RoleModel` contents. Two ADK
  workarounds become unnecessary and are deleted: the author-matching constraint
  ("any other author would be rewritten as a foreign 'For context:' message",
  `:530-535`) and `session.Get/CreateRequest` plumbing. Keep
  `stripFabricatedCalls` (`:783-838`) — that guards against model-prose tool
  imitation on reseed, which is model behavior, not ADK behavior.
- Map loop events onto the existing machinery: `Text`/`TextDelta` → `streamText`
  (deltas flow straight through the segment logic at `:260-303`), `Thought` →
  `thoughtBuf`, `ToolCall`/`ToolResult` → the pending-row matching at
  `:314-333`/`:600-617`, `TurnEnd` → `finalize`. `ev.Partial` disappears — the
  loop normalizes streaming; consumers see one event kind per thing.

`internal/agents/agent.go` / `chat.go`: `Build` stops returning `agent.Agent`;
return an `agentloop.Config` (mode, instruction, model, tools, output schema).
`agents/chat.go`'s dynamic instruction composition (`:32-66`) is unchanged.

`internal/orchestrator/single_shot.go`: unchanged (already bypasses the runner);
it now calls the openai-go-backed adapter directly.

### M5 — Gemini and Anthropic repointing (1 day)

- `internal/llm/gemini.go`: replace ADK's `model/gemini` plugin with the genai
  SDK directly: `genai.NewClient(ctx, cfg)` + `Models.GenerateContentStream`,
  translating each `GenerateContentResponse` chunk into local `LLMResponse`
  (mark text parts per chunk as `Partial` except the last; copy usage metadata).
  ~60 lines; ADK's own gemini adapter (`adk/v2@v2.0.0/model/gemini/gemini.go`)
  is the reference.
- `internal/llm/anthropic.go`: logic unchanged; swap imports to local types.
  Anthropic stays non-streaming (as today).
- `go.mod`: drop `google.golang.org/adk/v2`, run `go mod tidy`, confirm
  `google.golang.org/genai`, `github.com/google/jsonschema-go`, and
  `github.com/openai/openai-go/v3` are direct.

### M6 — Tests and cleanup (2-3 days)

- Rewrite the ADK-coupled stubs: `chat_test.go:29-165` (fakeChatModel/stubLLM),
  `agent_test.go`, `budget_test.go:13-19` — all become implementations of the
  local `llm.Model` interface yielding local `LLMResponse`s. The scenarios
  (tool round trip, budget ceiling, drawer interleave) are preserved.
- New loop tests in `internal/agentloop`: task-mode termination on
  `finish_task`; chat-mode termination on text-only response; multi-call turn
  with out-of-order tool completion (results emitted in call order); budget
  error propagation; cancellation mid-loop; stream with interleaved text and
  tool calls.
- Keep `llm/dryrun.go` — it is now the primary loop test double and still
  drives dry-run CLI mode.
- Sweep: delete the ADK section from docs (`spec.md`, `AGENTS.md` references if
  any), update `README.md` provider docs, and regenerate the code map per the
  AGENTS.md instructions.

## 4. What deliberately stays

- `google.golang.org/genai` as the internal content/schema data model and the
  Gemini transport.
- The hand-rolled Anthropic adapter (no SDK dependency added; its reasoning
  mapping via `thinking` blocks already works).
- llama.cpp compatibility workarounds, now consolidated in one adapter.
- SQLite-first state: no session service of any kind replaces the deleted ADK
  one; history is rebuilt per turn from the DB as it already is.

## 5. Test/verification plan

- `go build ./... && go vet ./... && go test ./...` green at every milestone
  boundary (M0–M5 are each mergeable).
- Dry-run end to end: `gorchestrator` dry-run pipeline phase (exercises the
  loop, tools, budget, and dryrun model with zero network).
- Manual fleet smoke (same checklist as Plan A §3 Step 6): streaming text,
  tool-row interleave, no duplication, thought rows via `reasoning_content`,
  empty-ID tool calls from llama.cpp.

## 6. Effort breakdown

| Milestone | Estimate |
|---|---|
| M0 local model types | ½ day |
| M1 tool layer | 1–2 days |
| M2 agent loop | 3–5 days |
| M3 openai-go adapter | 2–3 days |
| M4 consumer rewiring | 2–3 days |
| M5 gemini/anthropic repoint + go.mod | 1 day |
| M6 tests + docs | 2–3 days |
| **Total** | **~1.5–2.5 weeks** |

## 7. Risks

| Risk | Mitigation |
|---|---|
| Subtle behavior drift vs ADK's loop (parallel-tool merge order, output-schema edge cases, empty-stream final chunks) | M2 keeps genai-shaped packing identical to ADK's; ADK module source remains in the cache as the spec; loop tests cover merge order and empty finals. |
| Milestone M2 balloons (the one genuinely new component) | M0/M1 land first and are independently useful; if M2 stalls, Plan A remains the fallback for the streaming problem. |
| openai-go v3 API churn (v3 line is young; ADK pins v3.64.0) | One adapter file isolates the dependency; SDK upgrades touch one place. |
| Anthropic reasoning/usage mapping gaps surface later | Adapter unchanged from today's proven code — risk carried over, not introduced. |
| Losing ADK upstream fixes (e.g. they later map `reasoning_content`) | Acceptable: Plan C implements that mapping itself in M3; upstream's gemini/openai adapters remain readable references. |
