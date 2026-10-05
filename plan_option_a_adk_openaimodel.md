# Plan A — Adopt ADK v2.5.0 `model/openaimodel` plugin (openai-go based)

Goal: replace the hand-rolled OpenAI HTTP client in `internal/llm/openai.go` with
Google's official ADK OpenAI model plugin (built on `github.com/openai/openai-go/v3`),
enable SSE streaming end to end, and fix the "all text arrives at the end" symptom
documented in `chat_tool_group_issue.md`. ADK remains the agent framework.

Estimated effort: **1–3 days** including tests.

---

## 1. Verified facts this plan relies on

All confirmed against `google.golang.org/adk/v2@v2.5.0` and
`github.com/openai/openai-go/v3@v3.64.0` in the module cache.

- `model/openaimodel` does not exist in our current v2.0.0 (which ships only
  `model/gemini` and `model/apigee`); it was introduced in v2.1.0 and this plan
  targets the current v2.5.0. It is a thin wrapper over openai-go v3 with two API modes:
  `APIResponses` (default) and `APIChatCompletions` — the latter is the mode
  OpenAI-compatible third-party servers (llama.cpp, llama-swap, OpenRouter)
  implement. Constructor:
  `openaimodel.NewModel(ctx, modelName, *openaimodel.ClientConfig)` returns
  `model.LLM` — the same interface our `internal/llm` factory already returns.
  `ClientConfig{APIKey, BaseURL, HTTPClient, Options, API}` — `HTTPClient` is
  accepted, so we keep our timeout plumbing.
- **Streaming is gated twice today**: (a) `agent.RunConfig{}` at
  `internal/orchestrator/engine.go:1357` and `internal/orchestrator/chat.go:576`
  leaves `StreamingMode` empty, and ADK only requests streaming when
  `StreamingMode == agent.StreamingModeSSE`
  (`adk/v2@v2.0.0/internal/llminternal/base_flow.go:781`); (b) our custom
  `OpenAIModel.GenerateContent` ignores its `stream` argument
  (`internal/llm/openai.go:66-75`). Both must be fixed; this plan fixes both.
- With SSE on, `session.Event` (which embeds `model.LLMResponse`,
  `adk/v2.5.0/session/session.go:100`) exposes `ev.Partial`, `ev.TurnComplete`,
  `ev.Content`, `ev.UsageMetadata` on every event.
- Stream shape from the plugin: text arrives as **partial delta events**; the
  final non-partial event carries the **aggregated full text**, the tool calls
  (tool calls are never emitted as partials — they only appear on the final
  response, `model/openaimodel/internal/completions/stream.go:34-36`), and the
  usage metadata (`stream_options.include_usage`, `.../completions/model.go:102-106`).
  The streaming aggregator (`internal/llminternal/stream_aggregator.go`)
  accumulates deltas across chunks.
- The plugin reads generation parameters from `genai.GenerateContentConfig`, not
  from model fields: `Temperature` (`.../completions/request.go:281`),
  `MaxOutputTokens` → `max_completion_tokens` (`request.go:292`),
  `SystemInstruction` (`request.go:323`). Our `llm.Config.Temperature` /
  `MaxTokens` currently live on the model struct, so a small per-call injection
  wrapper is required.
- Schema handling is llama.cpp-safe: the plugin lowercases genai's uppercase
  schema type names at every depth (`.../shared/schema.go:160-183`), reads both
  `Parameters` and `ParametersJsonSchema` (`.../completions/tools.go:61-70`),
  errors on non-function tools (`EnsureFunctionToolOnly`) and on nameless tool
  calls (`.../completions/response.go:95-101`). This replaces the behavior our
  `declaration_test.go` and `schema.go` guard.
- **Known regression — chain-of-thought**: the plugin's response translation
  (`.../completions/response.go:64-93`) maps only `content`, `refusal`, and
  `tool_calls`. openai-go v3 has **no `reasoning_content` field anywhere** in
  its generated types (verified: zero matches in the module), so CoT from
  llama-swap reasoning parsers / DeepSeek / OpenRouter is silently dropped and
  the chat drawer's "thought" rows (`internal/orchestrator/chat.go:591-596`,
  `flushThoughts`) stop appearing for OpenAI-provider models. Mitigation options
  in §4, Step 5.
- API compatibility for every other ADK symbol we use was checked against
  v2.5.0 sources and is unchanged: `llmagent.New(llmagent.Config{... Mode,
  OutputSchema, GenerateContentConfig})`, `tool/functiontool.New`, `runner.Config{
  AppName, Agent, SessionService, AutoCreateSession}`, `session.InMemoryService()`,
  `agent.New`, `llmagent.RunLLMAgentAsNode`, `agent.RunConfig.StreamingMode`.

## 2. Non-goals

- No change to Anthropic (`internal/llm/anthropic.go`) or Gemini
  (`internal/llm/gemini.go`) paths.
- No change to the tool layer (`internal/tools/*`, `internal/mcp/manager.go`),
  agent definitions (`internal/agents/*`), or session/history strategy (fresh
  in-memory session reseeded from SQLite per turn stays).
- No change to `events.jsonl` event types or the drawer schema.

## 3. Step-by-step

### Step 1 — Upgrade ADK (mechanical, ~30 min)

- `go.mod`: `google.golang.org/adk/v2 v2.0.0` → `v2.5.0`. This pulls
  `github.com/openai/openai-go/v3 v3.64.0` indirectly. Run `go mod tidy`.
- Full `go build ./... && go test ./...`. The v2 module line promises
  compatibility, and the symbols we use are unchanged (verified above), but this
  is a 5-minor-version jump — let the test suite (notably
  `internal/orchestrator`, `internal/agents`, `internal/tools`,
  `internal/server`) arbitrate. Fix any drift before proceeding.

### Step 2 — Replace `internal/llm/openai.go` with a plugin adapter (~half day)

- New file `internal/llm/openai_plugin.go`:
  - `newOpenAIPluginModel(modelName, apiKey, baseURL string, timeout time.Duration) (model.LLM, error)`
    calling `openaimodel.NewModel(ctx, modelName, &openaimodel.ClientConfig{
    APIKey: apiKey, BaseURL: baseURL, HTTPClient: &http.Client{Timeout: timeout},
    API: openaimodel.APIChatCompletions})`.
  - Keep the existing env-var resolution and default-base-URL logic from
    `NewOpenAIModelWithOptions` (`internal/llm/openai.go:39-58`).
  - `configInjector` wrapper (the one piece of new logic):
    ```go
    // GenerateContent clones req, ensures req.Config, copies Temperature and
    // MaxTokens into genai.GenerateContentConfig.Temperature / MaxOutputTokens
    // (the fields the plugin reads), then delegates.
    ```
    This preserves today's contract — `llm.Config.Temperature/MaxTokens` apply
    per model — without touching `internal/agents` or `llmagent.Config`.
- `internal/llm/factory.go:36`: the `"openai"` branch returns the wrapped plugin
  model. Keep the exported `NewOpenAIModel*` constructors only if referenced
  elsewhere; otherwise delete them with the old file.
- Delete `internal/llm/openai.go` (the hand-rolled HTTP client, retry loop, and
  `openAIChatResponse` structs) and `internal/llm/openai_test.go` (rewritten in
  Step 6).
- `internal/llm/schema.go` stays: still used by the Anthropic adapter and
  `schemasFromTools` (`internal/orchestrator/engine.go:2157-2174`).

### Step 3 — Enable SSE at both runner call sites (minutes)

- `internal/orchestrator/engine.go:1357` and `internal/orchestrator/chat.go:576`:
  `agent.RunConfig{StreamingMode: agent.StreamingModeSSE}`.
- Behavior note: with SSE on, every model provider receives `stream=true`.
  `DryRunModel` yields a single non-partial, `TurnComplete` response — already
  correct. The Anthropic and Gemini adapters ignore the flag (same as our old
  OpenAI adapter did), so they keep working unchanged but non-streaming.

### Step 4 — Handle partial events in the two consumers (~half day)

This is where the actual UX fix lands. Both loops currently assume one event
per model call carrying the full text.

`internal/orchestrator/chat.go` (`processTurn`, loop at `:576-618`):
- Skip nothing; branch on `ev.Partial`:
  - **Partial events**: for `RoleModel` contents, feed each text part through
    the existing `streamText` (`chat.go:289-303`) — deltas append to the open
    segment and the 300 ms throttled republish already does the right thing.
    Thought parts on partials: buffer via `thoughtBuf` only if the plugin emits
    them (it does not today — see Step 5). Tool calls never arrive on partials.
  - **Final event**: do **not** re-append its aggregated text parts (they are
    the sum of the deltas already streamed — appending would duplicate). Use it
    for: `FunctionCall` parts → existing `addToolCallMessage` path
    (`chat.go:600-605`), `flushThoughts`, and turn bookkeeping.
- `finalize` (`chat.go:367-400`) is unchanged — `seg.text` accumulation already
  produces the final text.
- Add a guard: if a turn produced **no** partials (non-streaming providers,
  dry-run), fall back to treating the final event's text as today (append via
  `streamText`). Simplest form: only skip final-event text when at least one
  partial was seen for that model call.

`internal/orchestrator/engine.go` (`runAgentLoop`, loop at `:1357-1452`):
- Skip partial events for `finalText` accumulation and for the `model_turn` /
  `tool_call` / `tool_result` event writes (they are display/audit rows; writing
  one per delta would flood `events.jsonl`). Tool calls still arrive only on the
  final event, so `tool_call` handling stays where it is.
- Usage accounting (`engine.go:1377-1392`): count only the final event's
  `UsageMetadata` (usage arrives once, on the final chunk, via
  `include_usage`). Partial chunks carry none from this plugin, but gate on
  `!ev.Partial` anyway to stay provider-correct.
- Keep `llm.IsBudgetExceeded(err)` short-circuit (`engine.go:1368`) unchanged.

### Step 5 — Decide on chain-of-thought recovery (decision point)

Pick one; default is (a):

a. **Accept the loss for now** (recommended for the first pass). Thought rows
   keep working for Gemini/Anthropic providers; they disappear for OpenAI-provider
   reasoning models. Document in the commit message and `README`/operator notes.

b. **Tee-transport collector** (~1 day): pass a custom `http.RoundTripper` via
   `openaimodel.ClientConfig.HTTPClient` that buffers the SSE response body,
   extracts `reasoning_content` deltas from the raw chunks, and forwards them to
   a per-turn collector that `chat.go` persists as thought rows. Workable because
   the config plumbs the HTTP client, but it is parsing a stream the SDK already
   parsed — accept the duplication, isolate it in one file, and gate it behind a
   config flag so it can be deleted when upstream maps reasoning.

c. **Upstream patch**: file an ADK feature request to map `reasoning_content` →
   `genai.Part{Thought: true}` (the Python ADK's LiteLLM path surfaces
   reasoning). Zero local code, unknown timeline; revisit after (a) ships.

Note: openai-go v3 retains raw JSON on every generated type
(`ChatCompletionChunk.RawJSON()`, `chatcompletion.go:1073`), so if we later call
the SDK directly (Plan C) the recovery is a ~20-line side-parse instead of a
transport tee.

### Step 6 — Tests (~half day)

- Delete `internal/llm/openai_test.go` with the old implementation; add
  `internal/llm/openai_plugin_test.go`:
  - the plugin adapter builds and names correctly;
  - `configInjector` copies `Temperature`/`MaxTokens` onto `req.Config` without
    mutating the caller's request.
- `internal/orchestrator/chat_test.go`: the existing stubs
  (`fakeChatModel`/`stubLLM`, `chat_test.go:29-165) yield single non-partial
  responses — they must keep passing unchanged (fallback path). Add one test
  with a stub yielding `Partial` text deltas followed by a final aggregated
  event, asserting: reply text appears once (no duplication), tool rows still
  interleave, and `finalize` lands the full text.
- `internal/orchestrator/single_shot_test.go`, `budget_test.go`: unchanged
  (budget wrapper sees identical usage metadata; single-shot calls with
  `stream=false` — non-streaming call path in the plugin).
- End-to-end smoke against the real fleet (manual): one chat turn against
  llama-swap with a reasoning-parser model and one against an OpenAI-compatible
  cloud endpoint, watching the drawer for (1) streaming text, (2) tool row
  interleave, (3) no duplicated text, (4) empty-`tool_call_id` behavior (the old
  client synthesized `call_<name>` IDs for llama.cpp; the plugin passes IDs
  through and errors only on *nameless* calls — verify the fleet emits IDs).

## 4. Rollout and rollback

- No config-file or schema changes; rollback is `git revert` (the old adapter
  lives in history). Ship behind normal CI.
- Optional safety valve: introduce provider value `openai-adk` alongside
  `openai` for one release so a fleet problem can be routed back to the legacy
  adapter by config alone. Recommended only if the manual llama-swap smoke test
  can't be done before merge.

## 5. Risks

| Risk | Mitigation |
|---|---|
| llama.cpp strictness quirks our old adapter worked around (empty `content` keys, synthesized tool-call IDs, `user`-role echo rules) resurface via the plugin | Manual smoke against llama-swap before merge; the plugin targets OpenAI-compatible providers and handles lowercase schemas, but our fleet is the final judge. Safety-valve provider string if needed. |
| 5-minor ADK upgrade breaks an untested interaction | Step 1 is a standalone commit with the full suite green before any other change. |
| Thought rows vanish for OpenAI-provider reasoning models | Step 5 decision; default accepts the loss and documents it. |
| `events.jsonl` volume explosion if partials leak into engine audit rows | Gated on `!ev.Partial` in Step 4; covered by new test. |
