# Local Multi-Model Orchestration: gorchestrator + llama-swap

Design + implementation scoping document. Updated 2026-09-17; supersedes the
2026-08-24/25 working notes (history preserved in git).

## Current state (2026-09-17)

- **Inference server: built and running.** llama-swap is deployed, configured
  with the inference engine and multiple models, and verified to swap and serve.
  Pre-flight (old step 0) is DONE. The remaining work is all in gorchestrator.
- **gorchestrator code: old plan steps 1-4 landed.** Config schema + validation
  (`single_shot`, `single_shot_context_bytes`, `context_files`; timeout
  validation), repo digest (`internal/orchestrator/digest.go`), single-shot
  execution (`internal/orchestrator/single_shot.go`), effort-gate feedback fix.
- **Old step 5 only half-landed**: `configs/config.local.example.yaml` still has
  no llama-swap multi-flavor example. Redone as step 4 below.
- **Landed 2026-09-17**: modification 2 (human gate default + adjudicator name
  validation) and modification 3 (issue dependency chain: `depends_on` column +
  migration v11, submit validation, dep-aware `ClaimQueued`, `blocked_by`
  surfacing in API/dashboard, `--depends-on` on `gorchestrator run`).
- **Not built**: model load/unload control + exclusive-mode lock (modification
  1 — the remaining core work), example config redo (modification 4).

## Agent arrangement (CHANGED 2026-09-17)

Three stages, three model classes. This **inverts** the old doc's mapping (which
put the big MoE on research): the researcher is now the fast tool-user, and the
big slow model only plans.

| Stage | Model class | Execution mode | Output artifact |
|---|---|---|---|
| **researcher** | Small + fastest | ADK tool-call loop (`runAgentLoop`) — needs `read_file`/`grep_search`/`list` to explore the workspace | Context digest: accumulated, contextually significant detail for the planner (RAG-by-agent) |
| **planner** | Biggest + smartest, speed be damned | **Single-shot** (`runSingleShot`) — research artifact + repo digest pre-stuffed, zero or near-zero tool calls | Self-contained implementation plan |
| **implementer** | Mid: code + tool-calling tuned | ADK tool-call loop | The actual code changes |

Why this shape:

- The researcher's value is throughput, not depth. A fast small model doing many
  cheap tool rounds beats a slow genius pulling files one token at a time.
- The planner gets everything pre-stuffed (research artifact via
  `buildBaseInput`, repo digest via `buildRepoDigest`), so its slowness is paid
  exactly once, on the maximum-intelligence task.
- Every stage boundary is a human gate (modification 2), so a weak researcher
  output is caught before the expensive planner run, and a weak plan is caught
  before implementation.

The existing machinery supports this unmodified: `CoreAgentTypes =
{researcher, planner, implementer}` (`internal/config/config.go:70`), per-phase
flavors with full `model` blocks (`base_url`, `timeout`), `MergeAgent`
overlay (`config.go:746`), `phaseAgentType` mapping
(`internal/orchestrator/engine.go:1689`), and the hard-coded
`research → plan → implementation` phase list (`engine.go:526`). Single-shot is
per-flavor, so the planner alone can be single-shot while researcher and
implementer run tool loops.

## Models

Deployed model names live in the llama-swap config, not here. Role mapping:

- **researcher**: fastest available small dense model.
- **planner**: the big MoE (previously DeepSeek V4 Flash 0731 UD-Q4_K_XL,
  measured ~0.47 tok/s prompt / ~0.02 tok/s gen — one-shot phases only,
  `max_tokens` mandatory, timeout in hours).
- **implementer**: mid dense coder model (previously Qwen3.8-27B class) —
  strong tool calling on the available GPU.

## Architecture conclusions (unchanged unless noted)

- **Engine manager = llama-swap.** Arbitrary per-model launch commands,
  OpenAI-compatible routing by request `model` field, mutually-exclusive swap
  groups, `/unload`, `/running`, request-holding during loads. Verified in
  production now, not just against upstream docs.
- **MCP rejected for flow control.** Phase transitions (load → work → persist →
  unload) are orchestrator-driven, not model-driven. MCP fine *inside* phases.
- **Delegated headless harnesses still deferred.** Built-in implementer loop is
  good enough; revisit only if it proves underpowered.
- **Swap-cost discipline** (unchanged, now sharper): with the planner as the
  only big-model phase, each issue pays at most one slow-model load + one
  one-shot generation + one unload. The researcher absorbs the exploratory
  chattiness at fast-model prices.

## gorchestrator: what works today, unmodified

- Per-phase models against llama-swap via flavors (`model.base_url` +
  `model.timeout` per flavor; llama-swap swaps on the request's `model` field).
- Single-shot planner: `single_shot: true` + digest stuffing, landed and tested.
- Phase results, artifacts, resume, dashboard, human decision plumbing
  (`Decide`, pending-decision rows) — all backend-agnostic.
- Multi-provider/multi-endpoint configs: flavors already resolve per-phase
  `provider`+`base_url`, so a hybrid setup (local llama-swap for two stages +
  hosted API for a third) works today. The new serialization machinery
  (modification 1) must key on the inference endpoint, not assume one global
  server — but the initial optimization target is a single resource-constrained
  local server that can hold exactly one model and serve exactly one request at
  a time.

## Required modifications

### 1. Model lifecycle control + exclusive-mode serialization (the rock-solid requirement)

**Requirement**: on a single-model-at-a-time server, the harness must
*explicitly and reliably* drive model residency per agent stage — not rely on
llama-swap's implicit swap-on-request. For every phase: ensure the phase's
model is loaded before work, unload after the phase's artifact is persisted.
Failure to unload must fail safe (block the next phase, surface the error), never
silently thrash.

**Shape**:

- **Config**: new optional server-level block, e.g.
  ```yaml
  inference:
    type: llama-swap            # enables lifecycle control
    base_url: http://host:port  # management API (chat API is <base>/v1)
    mode: exclusive             # serialize ALL phases against this server
  ```
  `mode: exclusive` is the single-resource optimization. Without the block,
  behavior is exactly as today (llama-swap swaps implicitly) — this keeps
  multi-provider and hosted-API configs untouched.
- **Controller**: new `internal/orchestrator/modelctl.go`,
  `type Controller interface { EnsureLoaded(ctx, model string) error; UnloadAll(ctx) error }`.
  llama-swap implementation:
  - `EnsureLoaded`: warmup request (`POST {base}/v1/chat/completions`,
    `max_tokens: 1`) — llama-swap holds the request while loading, so a
    completed warmup == model resident. Verify via `GET /running`.
  - `UnloadAll`: `POST {base}/unload`, then poll `/running` until empty or
    timeout. On timeout: return error — the next phase must not start against
    an unknown-residency server.
- **Hook points** (both in `runPipeline`, per phase, NOT per attempt — retries
  keep the model resident):
  - Load: after `agentConfigForIssue` resolves the phase config
    (`engine.go:468`, called in the phase loop), before `runPhase`
    (`engine.go:598`).
  - Unload: after the phase's result.json is durably written — i.e. after
    `runPhase` returns and before status mapping/`EventPhaseFinished`
    (`engine.go:613`). Unload on ALL outcomes (done, waiting_human, failed):
    a phase waiting on a human must not pin the model.
- **Exclusive-mode lock**: a process-wide keyed mutex on the inference endpoint
  (new `internal/orchestrator/model_lock.go` — the old deferred modification 3,
  now required). With `mode: exclusive`, every phase (any issue) acquires the
  server key before load and holds it through unload. This makes
  `max_concurrent_issues > 1` safe even on one server: issues queue at the
  phase level instead of thrashing. Publish a "waiting for inference server"
  event before blocking so the dashboard shows the queue.
  - Keep `max_concurrent_issues: 1` as the recommended initial config; the lock
    is the correctness floor, not a license to raise concurrency on day one.
- **Observability**: record `model_load` / `model_unload` / `model_wait` events
  via `recordEvent` (`engine.go:1780`) so the dashboard/SSE shows residency
  transitions; note `recordEvent` is unlocked read-modify-write — the exclusive
  lock serializes the contended path, but keep events per-(issue, phase) as
  today.

**Landmines**:

- Warmup tokens cost prompt-processing time on the slow model — keep warmup to
  `max_tokens: 1` and an empty/short prompt; it forces the load, nothing more.
- `http.Client.Timeout` covers the whole body read including llama-swap
  swap-wait (`internal/llm/openai.go:54`) — the warmup client needs the same
  hours-scale timeout as the flavor.
- llama-swap `/unload` semantics: confirm the deployed version unloads the
  whole group, not just one model (smoke-test in step 3 verification).
- SIGTERM mid-phase still kills in-flight generation and re-runs from scratch
  on recovery (`internal/cli/serve.go:106-116`); on recovery the model may be
  left loaded — `EnsureLoaded` is idempotent by construction, so no special
  recovery path is needed. Operational rule unchanged: don't restart mid-phase.
- Multi-endpoint future: the lock is keyed by endpoint, so adding a second
  llama-swap server or a hosted API later needs no redesign — each key
  serializes independently.

### 2. Human gate at every stage boundary (removes self-approval) — LANDED 2026-09-17

**Decision**: remove the self/auto-approval mechanism from the active flow.
Every agent stage ends in a human decision. Rationale: with a queued batch of
issues, the operator reviews each stage artifact (research digest, plan,
implementation) before the harness spends the next stage's compute — and each
gate is also the natural model-swap point.

**Implementation choice: flip the default, keep the code.** Cheaper than
deletion, and "self" remains as an explicit config opt-in if wanted back:

- `defaultAgentConfig` (`internal/config/config.go:812`): change hard-coded
  `Adjudicator: "self"` (:821) → `"human"`.
- `adjudication.New` (`internal/adjudication/adjudicator.go:105`): keep the
  `"self"` case but treat unknown names as a **validation error at config load**
  instead of silently mapping to Null (:111-112) — today a typo like `selff`
  silently produces *less* gating. Add known-name validation in
  `validateAgentOverrides` (`config.go:488`).
- No plumbing changes needed: `HumanAdjudicator` → `WaitingHuman` → pending
  decision row → `Decide` (`service.go:115`) → re-queue → next phase. The
  forced-human override for untrusted external issues (`engine.go:486-492`) is
  already compatible.
- Effort gate (`maybeHoldForEffort`, `service.go:724`) becomes redundant at the
  plan→implementation boundary (human adjudication already holds there) but is
  harmless; leave it.
- Stale-contract cleanup: `single_shot.go` comment says `done=true` exists so
  self-adjudication passes — update to reflect human-gate-everywhere.
- Configs that explicitly set `adjudicator: self` keep working (opt-in), so
  this is a default change, not a breaking removal.
- **Tests**: many suites pin the self default — update
  `internal/adjudication/adjudicator_test.go`, `internal/daemon/daemon_test.go`,
  `internal/server/server_test.go`, `internal/orchestrator/run_test.go`,
  `projects_registry_test.go`, `service_test.go` for the human default and the
  new name validation.

### 3. Issue dependency chain — LANDED 2026-09-17

**Requirement**: an issue must not be picked up while an issue it depends on is
not fully `done`. With a queued batch, the harness walks issues one at a time,
pausing at human gates — dependencies make that walk order-aware.

**Shape**:

- **Schema**: `depends_on` JSON array of issue IDs on the issues table
  (`internal/sqlite/issues.go:20-34`), mirroring `AgentFlavorsJSON` style
  (`DependsOnJSON string`) + migration. A JSON column beats a join table at
  this scale; deps are write-once at submit.
- **Submit**: CLI + API accept `--depends-on 12,14` / `depends_on: [12,14]`.
  Validate: referenced issues must exist and belong to a project the caller can
  see. Cycles are impossible by construction if deps must reference *existing*
  issues (no forward references) — enforce that, and no cycle-check code is
  needed.
- **Claiming**: `ClaimQueued` (`issues.go:145-186`) currently selects the
  oldest `queued` row. Change to skip rows with unsatisfied deps:
  exclude any candidate whose `depends_on` contains an ID whose current status
  != `done`. Simplest correct form: fetch the oldest N queued candidates in the
  transaction, filter in Go (deps resolved against the same tx), claim the
  first eligible. Keep FIFO within the eligible set. Failed/cancelled deps do
  NOT unblock — dependent stays queued until a human retries/completes the
  blocker or edits the dep.
- **Surface**: `GET /api/issues` + dashboard show `blocked_by: [...]` for
  queued issues with unsatisfied deps (derive at read time; don't store a
  status — "blocked" is a view of queued, not a new state, so recovery and
  requeue logic stay untouched).
- **Tests**: claim skips blocked, claims after dep completes, FIFO preserved
  among eligible, missing-dep validation, dep on failed issue stays blocked.

### 4. Example config + docs (redo of old step 5's missing half)

`configs/config.local.example.yaml`: three-flavor llama-swap setup for the new
arrangement:

```yaml
inference:
  type: llama-swap
  base_url: http://192.168.1.152:8080   # management base; chat at /v1
  mode: exclusive

server:
  max_concurrent_issues: 1              # start here even with the lock

agents:
  researcher:
    model: { provider: openai, base_url: http://192.168.1.152:8080/v1,
             model: <fast-small-model>, timeout: 10m }
    # tool loop; adjudicator now defaults to human
  planner:
    model: { provider: openai, base_url: http://192.168.1.152:8080/v1,
             model: <big-moe>, timeout: 24h }
    single_shot: true
    single_shot_context_bytes: 32768    # ~8k tokens ≈ hours of prompt time — show the math
    max_tokens: 2048                    # uncapped gen at 0.02 tok/s is unbounded wall clock
    max_attempts: 1                     # human retry re-runs the whole generation
  implementer:
    model: { provider: openai, base_url: http://192.168.1.152:8080/v1,
             model: <mid-coder>, timeout: 2h }
```

Comments carry the token/time math (digest bytes ↔ prompt hours, max_tokens ↔
gen hours) from the measured MoE numbers.

## Suggested order of work

1. ~~Modification 2 (human gates)~~ — DONE 2026-09-17.
2. ~~Modification 3 (dependencies)~~ — DONE 2026-09-17.
3. **Modification 1 (lifecycle + exclusive lock)** — the remaining core reliability work;
   do it against the live server with `dryrun` flavors first (dryrun exercises
   the phase machinery without burning GPU), then one real issue end-to-end:
   research (fast) → human gate → plan (slow, single-shot) → human gate →
   implementation (mid), verifying load/unload events, swap-wait timeout
   headroom, and digest budget.
4. **Modification 4 (example config/docs)** — land alongside 3's verification.

Deferred (unchanged, only if real need shows): big-model plan-vs-diff review
loop, headless-harness adapter phase type, `max_tool_rounds` cap
(`BudgetLLM.callCount` is tracked but unused — the hook exists),
effort-gate removal (now redundant), multi-endpoint scheduling beyond the
keyed lock.

## Verification

- `go build ./... && go test ./...` after each step; existing suite stays green.
- Modification 1: unit-test the controller against a stub HTTP server (warmup
  called, unload polled, unload-timeout error blocks next phase); lock test
  with two concurrent phases on one key.
- Modification 2: config-load tests for adjudicator names; suite-wide default
  flip.
- Modification 3: claim-query tests as listed above.
- Final e2e: one real issue through all three stages on the live llama-swap
  server with human gates, plus one queued dependent issue proving it waits for
  its blocker.
