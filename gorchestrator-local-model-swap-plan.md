# Local Multi-Model Orchestration: gorchestrator + llama-swap

Working notes from a design/investigation session (2026-08-24). Goal: plan for adapting
[gorchestrator](https://github.com/tuffrabit/gorchestrator) into a two-model local
inference orchestrator.

## Models

- **DeepSeek V4 Flash 0731** (unsloth UD-Q4_K_XL) — frontier-size MoE, disk-streamed
  experts via mmap. Measured: ~0.47 tok/s prompt, ~0.02 tok/s gen (~55 s/token).
  Disk-bound at the SATA ceiling (verified math: ~15-25 GB expert reads/token at
  ~0.3-0.5 GB/s effective). Role: research, planning, review — one-shot, big-context
  phases only.
- **Qwen3.8-27B** (dense) — strong at tool calling and code, runs on 16 GB GPU at
  decent speed. Role: implementation.

## Architecture conclusions

- **Engine manager = llama-swap** (https://github.com/mostlygeek/llama-swap).
  Actively maintained; already does: arbitrary per-model launch commands (works for
  colibri too, not just llama.cpp), OpenAI-compatible passthrough routed by the
  request's `model` field, mutually-exclusive swappable groups (= "one model fits
  at a time" semantics), `/unload` + `/running`, holds requests while a model loads.
  Do NOT build a custom engine manager; only revisit if concrete gaps appear
  (async load semantics, colibri lifecycle quirks).
- **Orchestrator = gorchestrator** (modified, see below). Its research → plan →
  implementation pipeline + per-stage model flavors + artifact storage + resume +
  human adjudication gates already match the desired flow.
- **MCP rejected for flow control**: MCP is a tool transport; invocation timing is
  model-driven. Phase transitions (load → work → persist → unload → …) must be
  orchestrator-driven, not model-driven. MCP remains fine *inside* phases.
- **Delegated headless harnesses (kimi-cli -p, claude -p, …) deferred**: gorchestrator's
  built-in implementer agent loop is good enough to start; no subprocess adapters to
  maintain. Only add a "harness adapter" phase type later if the built-in loop
  proves underpowered vs dedicated coding harnesses.
- **Swap-cost discipline**: every model swap costs minutes; every round trip on the
  MoE costs tens of minutes at these speeds. Big-model phases must be one-shot with
  pre-stuffed context. Plans handed to the fast model must be self-contained
  (no KV survives the swap). Cheap re-invocation of the big model for
  plan-vs-diff review is fine (small context); full re-planning is not.

## gorchestrator: what works today, unmodified

- Per-phase models against llama-swap via flavors: flavors carry a full `model`
  block incl. `base_url` + `timeout` (`internal/config/config.go`; merge in
  `MergeAgent` config.go:699). Point both flavors at `http://<host>:<llama-swap>/v1`
  with different `model` names; llama-swap swaps on demand.
- Per-phase timeouts: `timeout:` per flavor, parsed in `modelTimeout`
  (`internal/orchestrator/engine.go:1700`).
- Adjudication/effort gates between phases are natural model-swap points.
- Phase results, artifacts, resume, dashboard all work regardless of model backend.

## Required modifications (the shape)

### 1. Single-shot mode for research/plan (the critical change)

**Why**: every phase is an ADK `ModeTask` tool-call loop (`runAgentLoop`,
engine.go:886) with NO round-trip cap — runs until the model calls `finish_task`.
At 0.5 tok/s prompt processing, a chatty research phase is days. ADK has no
iteration cap knob.

**Shape**:

- Config: add `SingleShot bool` (use `*bool` if tri-state needed, like
  `Temperature`) + `SingleShotContextBytes int` to `AgentConfig`
  (config.go:46-58); merge in `MergeAgent` (config.go:699+). Flows through flavors
  automatically.
- Execution: in `runAgentLoop`, branch right after model construction/budget-wrap
  (engine.go:952-966, before `buildAgent` at :968) to a new
  `Engine.runSingleShot` (~60 lines, new file `internal/orchestrator/single_shot.go`):
  1. Build `model.LLMRequest` directly (SystemInstruction + loopInput);
     `OpenAIModel.convertContents` (internal/llm/openai.go:202-210) already maps it.
  2. Prompt: `cfg.SystemPrompt` + hardcoded suffix ("You have no tools. Reply with
     the complete document as plain text.") — default researcher/planner prompts
     reference `write_output`/`finish_task`, which won't exist.
  3. One `GenerateContent(ctx, req, false)` call; take the single response.
  4. Write text to outputPath via `e.store.Write`; record `usage` + `model_turn`
     events via `recordEvent` (engine.go:1731) so budgets/dashboard keep working.
  5. Return `done=true` unconditionally — else `SelfAdjudicator` returns Retry
     (internal/adjudication/adjudicator.go:88-94) and re-runs the slow model.
- Bypassing the ADK runner is safe: sessions are in-memory and per-loop; all
  cross-phase state flows through storage (`result.json`).

**Landmines**:
- Planner effort gate: no `finish_task` → no `effort` → defaults to "high"
  (effort.go:41-47) → forced human gate before implementation (service.go:724).
  Arguably ideal for this use case (human gate = swap point); otherwise parse
  `effort:` from plan text.
- Set `max_tokens` in the flavor — uncapped generation at 0.02 tok/s is unbounded
  wall clock.
- Retries: human `retry` decisions re-run the whole slow generation. Attempts only
  re-fire on adjudicator Retry/Fail (won't happen with done=true).

### 2. Repo-context pre-stuffing (required companion to single-shot)

Nothing exists today — the model pulls everything via `read_file`/`grep_search`.
Add `Engine.buildRepoDigest(ctx, projectID, issueID, maxBytes)`:
- File list via `listRecursive` (engine.go:1610) over the storage snapshot
  (source already copied per issue; `copyDirToStorage` skips `.git`).
- Read via `e.store.Read`; skip binaries with existing `looksMostlyText`
  (scope.go:142); `.gitignore` matcher exists but is unexported
  (internal/tools/gitignore.go) — export or accept `.git`-only filtering.
- Emit `## <relpath>` + fenced content until byte budget; truncation marker.
- **The byte budget knob is the whole ballgame**: at 0.5 tok/s prompt, ~4
  bytes/token, 32 KiB ≈ 8k tokens ≈ 4.5 hours of prompt processing. Default small
  (16-32 KiB); consider a `context_files: [...]` explicit-list alternative in the
  flavor for targeted stuffing.

### 3. Model-residency lock (prevents swap thrash)

No model-residency awareness exists; `max_concurrent_issues: 1` is the only current
lever (works, but crude). Proper fix:
- `keyedMutex` type (new `internal/orchestrator/model_lock.go`), field on `Engine`
  (engine.go:83-99, init in `NewEngine`).
- In `runPipeline`, after `agentConfigForIssue` resolves phaseCfg (engine.go:547)
  and before `runPhase` (:585): lock on `provider|base_url|model`, hold across ALL
  attempts/loops of the phase, defer unlock.
- Phases ending `waiting_human` release the lock (model not pinned while waiting
  on humans — good; the plan→implementation effort gate frees the slow model before
  the fast model's phase starts).
- Enables pipelining with `max_concurrent_issues > 1`: issue A implementation
  (fast model) concurrent with issue B research (slow model), no thrash.
- Optional: publish a "waiting for model X" event before blocking (dashboard SSE
  already renders phase events).

### 4. Timeout ergonomics (small)

- `modelTimeout` (engine.go:1700) silently falls back to 60s on bad parse, and
  flavor-level timeouts get no load-time validation (unlike `default_model`,
  config.go:399-402). A typo'd `timeout: 6hours` (must be `6h`) silently neuters
  the slow-model config. Fix: validate in `normalizeProjects` (config.go:434) +
  log on fallback.
- `OpenAIModel` reads the whole body under one `http.Client.Timeout` (openai.go:54,
  :104) — timeout = total wall clock incl. llama-swap swap-wait. Set hours, e.g. `4h`.
- Retry loop (openai.go:91-135) retries 429/5xx up to 3x — a crashed backend costs
  3 full prompt re-processings before failing. llama-swap holds during load, so
  shouldn't trigger normally.
- SIGTERM kills in-flight generations instantly (daemon.go:155-157); recovery
  re-runs the phase from scratch. Operational rule: don't restart mid-phase.

## Suggested order of work

1. **Config-only validation**: two flavors (MoE + Qwen) against llama-swap,
   `max_concurrent_issues: 1`, big timeout on the MoE flavor. Use `dryrun` provider
   to validate flavor routing end-to-end before burning GPU hours.
2. **Single-shot mode + repo digest** (modifications 1+2 — do together; single-shot
   without context stuffing is useless).
3. **Model-residency lock** (modification 3) when raising concurrency.
4. **Timeout validation** (modification 4) — cheap, do opportunistically.
5. Later, only if needed: explicit llama-swap warmup/unload hooks at
   `EventPhaseStarted`/`EventPhaseFinished` (engine.go:575, :600); headless-harness
   adapter phase type; capped-tool-rounds middle ground (`max_tool_rounds` via a
   BudgetLLM-style decorator, internal/llm/budget.go:29 — but single-shot is the
   better fit at these speeds).

## Open questions — RESOLVED 2026-08-25

- **colibri vs llama.cpp mmap streaming for the MoE**: still empirical. Resolve in
  pre-flight (step 0 below) with a measurement run before tuning timeouts; llama-swap
  makes the backend swappable afterward, so this does not block any code work.
- **llama-swap feature set**: verified against upstream
  (https://github.com/mostlygeek/llama-swap) — actively maintained; groups
  (mutually-exclusive swap sets), `/unload`, `/running`, and request-holding during
  model loads all exist. Still smoke-test the exact version deployed (step 0).
- **Review-loop budget**: DEFERRED. There is no plan↔implementation feedback loop in
  the code today — adjudicator Retry only re-runs the *same* phase, and adjudicators
  are `null|self|human` only (internal/adjudication/adjudicator.go). A big-model
  plan-vs-diff review is net-new machinery; rely on the human effort gate +
  implementation self-adjudication until real output quality says otherwise.
- **Effort gate for single-shot planner**: RESOLVED — always gate. No `finish_task`
  → `finishEffort=""` → `EffectiveEffort("")="high"` (effort.go:40-46) → with the
  default `effort_gate_min: high`, `maybeHoldForEffort` (service.go:724) always holds
  for a human. This is the desired swap point. No effort parsing.

Additional decisions made 2026-08-25:

- **Model-residency lock: DEFERRED.** `max_concurrent_issues: 1` is the thrash
  guard for now. Build the keyed mutex (original mod 3) only when raising
  concurrency. Note for then: `recordEvent` (engine.go:1731) is read-modify-write
  with no locking — safe per (issue, phase) path, but revisit alongside the lock.
- **Repo digest: byte budget + explicit list.** Both knobs: auto-digest fills
  `single_shot_context_bytes` (default 16-32 KiB) in sorted path order,
  gitignore-filtered, with a truncation marker; a flavor may instead pin
  `context_files: [...]` for targeted stuffing.

## Verification corrections (code read 2026-08-25)

All line refs in this document checked out exactly. Sharpenings:

- `SelfAdjudicator` never returns Fail and trusts `done=true` blindly — a
  single-shot runner returning `done=true` always passes. There is NO quality check
  on single-shot output other than the human gate. Mitigation: empty-output check
  in runSingleShot (mirrors engine.go:1113).
- Never calling `finish_task` fails the phase directly (engine.go:1117-1119),
  bypassing adjudication — irrelevant once the loop is bypassed.
- SIGTERM handling lives in internal/cli/serve.go:106-116 (not daemon.go); the
  behavior described (in-flight generation killed, phase re-run from scratch on
  recovery) is accurate.
- BudgetLLM (internal/llm/budget.go) caps tokens only; `callCount` is tracked but
  unused. Fine — single-shot needs no round cap.
- `MergeAgent` is non-zero-wins, so `SingleShot` MUST be `*bool` (tri-state like
  `Temperature`), else a flavor can never turn off a globally-enabled single-shot.
- `defaultAgentConfig` (config.go:755-778) ALWAYS sets `SystemPrompt` to the
  tool-referencing default, so single-shot cannot tell "user override" from
  "built-in default" by emptiness. Resolution: export the defaults from config and
  compare (see step 3).
- PRE-EXISTING BUG (fix in passing, step 4): human feedback given at the
  effort-gate retry never reaches the implementer. The hold result has `attempt: 0`;
  a human retry keeps attempt 0, so `runPhase` starts at attempt 1 and
  `buildRetryContext` (engine.go:676, gated on `attempt > 1`) never injects
  `feedback.md`. With always-gate single-shot planning this is the main feedback
  path into implementation, so it matters now.

## Implementation plan (2026-08-25)

### Step 0 — Pre-flight, no code

- llama-swap: config with both models, smoke-test model-field routing, swap-wait
  request holding, `/running`, `/unload`.
- Measure colibri vs llama.cpp mmap streaming for the MoE; pick backend.
- gorchestrator config-only run: project with two flavors per agent type
  (`deepseek-moe` for researcher/planner, `qwen` for implementer), both
  `provider: openai`, same `base_url` (llama-swap), different `model` names;
  `max_concurrent_issues: 1`. Run an issue with `dryrun` and inspect the written
  `task.json` (`Model` block, engine.go:1156-1161) to confirm flavor resolution
  before burning GPU time.
- Sizing math for the MoE flavor (from measured 0.47 tok/s prompt, ~55 s/token gen):
  32 KiB digest ≈ 8k tokens ≈ 4.5 h prompt time; 1024 gen tokens ≈ 15 h. Set
  `max_tokens` explicitly (start ~1024-2048) and `timeout` in hours (e.g. `24h`);
  `http.Client.Timeout` covers the whole body read including llama-swap swap-wait.

### Step 1 — Config schema + validation

- `internal/config/config.go` AgentConfig (:46-58): add
  `SingleShot *bool` (`single_shot`), `SingleShotContextBytes int`
  (`single_shot_context_bytes`), `ContextFiles []string` (`context_files`),
  with yaml+json tags (project configs round-trip through SQLite `config_json`).
- `MergeAgent` (:699): `SingleShot` override when non-nil (deep-copy, like
  `Temperature`); `SingleShotContextBytes` when `> 0`; `ContextFiles` replace when
  non-empty (like `Tools`).
- Validation: reject unparseable `model.timeout` at load for global `agents.<type>`
  and every project flavor (walk in `normalizeProjects`, :434), matching the
  existing `default_model.timeout` check (:399-402); reject negative
  `single_shot_context_bytes`.
- `modelTimeout` (engine.go:1700): `log.Printf` on parse-failure fallback.
- Tests: merge tri-state semantics; validation accept/reject.

### Step 2 — Repo digest (lands together with step 3)

- Export gitignore matching from `internal/tools/gitignore.go`: add
  `ParseGitignore(io.Reader) (*GitignoreMatcher, error)` + exported `Match`;
  keep `loadGitignore` as a thin wrapper (digest reads `.gitignore` from the
  storage snapshot via `store.Read`, not the host FS).
- New `internal/orchestrator/digest.go`:
  `Engine.buildRepoDigest(ctx, projectID, issueID int64, cfg config.AgentConfig) string`.
  - Enumerate with `listRecursive` (engine.go:1610) under
    `storage.SourcePath(pid, iid)`; strip prefix for rel paths.
  - Skip: `.gitignore`-matched, non-text (`looksMostlyText`, scope.go:142 — same
    package, no export needed), and files that don't fit the remaining budget
    (skip, don't stop — small later files still get in).
  - `ContextFiles` non-empty → exactly those repo-relative paths, listed order;
    missing file → inline marker, not an error. Budget still applies (default
    64 KiB when `single_shot_context_bytes` is 0).
  - Emit: full path listing (cheap, tells the model what exists), then
    `## <relpath>` + fenced content sections, then a truncation marker naming the
    omitted-file count.
- Injection: in `runPipeline` after `buildBaseInput` (engine.go:580) — digest is
  constant per issue (source snapshot is frozen at issue creation), append once to
  `baseInput` when `SingleShot` is on and a budget/list is configured. Retries and
  the plan phase's inlined research output are unaffected.
- Tests with a temp-dir FS store: gitignore filtering, binary skip, budget
  truncation marker, explicit list, missing-file marker.

### Step 3 — Single-shot execution

- Prompts: export the built-in defaults from config
  (`DefaultSystemPrompt(agentType)` accessor over the existing private funcs) and
  add single-shot defaults (`singleShotResearcherPrompt` /
  `singleShotPlannerPrompt`: no tool references, "reply with the complete document
  as plain text", planner keeps its self-containedness requirements). Effective
  single-shot instruction:
  - `cfg.SystemPrompt` == built-in default → use the single-shot default;
  - has prefix `default + "\n\n"` (i.e. only `system_prompt_append` was set) →
    single-shot default + the appended suffix (MergeAgent bakes appends with
    exactly `"\n\n"`, config.go:722-726, so this split is exact);
  - anything else → full user override, used as-is.
- New `internal/orchestrator/single_shot.go`,
  `Engine.runSingleShot(...) ([]byte, bool, string, string, int, error)` — SAME
  signature as `runAgentLoop` so `runPhase` (attempts, result.json, adjudication)
  is untouched:
  1. `adkmodel.LLMRequest{Contents: []*genai.Content{loopInput}, Config:
     &genai.GenerateContentConfig{SystemInstruction: <resolved above>}}` — no tools.
     `OpenAIModel.convertContents` (openai.go:198) maps this unchanged.
  2. One `GenerateContent(ctx, req, false)`; concatenate text parts; empty → error
     (phase fails, same semantics as engine.go:1113).
  3. Write text to `outputPath` via `e.store.Write` (lands at the same
     `attempts/<n>/output.md` the tool-loop path uses, so `LatestOutput` /
     `buildBaseInput` for the next phase work unchanged).
  4. Record `usage` (from `UsageMetadata`) and `model_turn` (via `cappedText`,
     4096) events with the same `eventRecord` shape as engine.go:1021/:1065 so
     budget rehydration (`sumUsageFromEvents`) and the dashboard keep working.
  5. Return `(output, true, "", "", tokens, nil)` — done=true (self-adjudicator
     passes), effort="" (→ always human gate after plan, per decision).
- Branch in `runAgentLoop`: extract model construction + budget wrap
  (engine.go:939-966) into a small helper; if `cfg.SingleShot != nil &&
  *cfg.SingleShot`, call it and delegate to `runSingleShot` BEFORE the tool
  registry/MCP block (:919-937) — single-shot must not build tools or connect MCP.
- `internal/llm/dryrun.go`: when the request carries no tool declarations, return
  a plain-text canned response (keeps `dryrun` e2e validation working for
  single-shot flavors).
- Config guidance for slow flavors: `max_attempts: 1` (adjudicator retry can't
  fire with done=true anyway); human `retry` re-runs the whole generation —
  documented behavior, feedback DOES reach the next attempt via the step-4 fix.
- Tests: stub `model.LLM` (fixed text + usage) → output file written, events
  recorded, done=true; empty text → error; prompt resolution matrix
  (default / append-only / full override); dryrun no-tools path; e2e run_test.go-
  style: single-shot research+plan → waiting_human after plan (effort gate).

### Step 4 — Effort-gate feedback fix

- In `runPhase`, inject human feedback even when `attempt == 1`: if
  `storage.FeedbackPath(pid, iid, phase, attempt)` exists (written by `Decide`,
  service.go:287-317), append it to the attempt input. Keep the existing
  `attempt > 1` retry-context behavior untouched.
- Test: effort-hold → human retry with feedback → feedback present in the next
  implementer input.

### Step 5 — Examples + docs

- `configs/config.local.example.yaml`: two-flavor llama-swap setup with the
  token/time math in comments (digest bytes ↔ prompt hours, max_tokens ↔ gen
  hours), `max_attempts: 1` on single-shot flavors, `max_concurrent_issues: 1`.
- Update this document's status; note the deferred items (model-residency lock,
  big-model review loop, llama-swap warmup/unload hooks at EventPhaseStarted/
  Finished, headless-harness adapter, `max_tool_rounds`) as future work.

### Verification

- `go build ./... && go test ./...` after each step; existing suite
  (run_test.go, budget_test.go, phase_status_test.go, service_test.go, …) must
  stay green — non-single-shot behavior is untouched.
- Final: one real issue end-to-end against llama-swap (research → gate-free?
  no — research done, plan done, human gate, implementation on Qwen), confirming
  swap-wait timeout headroom and that the digest fits the measured prompt budget.
