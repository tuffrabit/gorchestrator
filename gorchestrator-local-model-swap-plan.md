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
  validation), modification 3 (issue dependency chain: `depends_on` column +
  migration v11, submit validation, dep-aware `ClaimQueued`, `blocked_by`
  surfacing in API/dashboard, `--depends-on` on `gorchestrator run`), and
  modification 1 (`inference:` config block, llama-swap `Controller` with
  warmup-load/unload-poll in `internal/orchestrator/modelctl.go`, keyed
  exclusive lock in `model_lock.go`, per-phase load/unload hooks in
  `runPipeline`, `model_wait`/`model_load`/`model_unload` events, fail-safe on
  unload error; hardened after a production 405 exposed a surfacing gap:
  inference-side failures now fail the issue visibly (`phase_result:
  inference_error` event + notification) and, in `mode: exclusive`, trip a
  process-wide circuit breaker (`internal/orchestrator/breaker.go`) that stops
  workers claiming new issues until a human decides on the tripped issue;
  llama-swap unload endpoint corrected to `/api/models/unload` with legacy
  `/unload` fallback), and modification 4 (`configs/config.local.example.yaml` now
  carries the three-flavor llama-swap setup: `inference:` block, fast
  researcher / single-shot big planner / mid implementer, sizing-math comments,
  `max_concurrent_issues: 1`; placeholders `<LLAMA_SWAP_HOST>:<PORT>`,
  `<FAST_SMALL_MODEL>`, `<BIG_MOE_MODEL>`, `<MID_CODER_MODEL>` to fill in;
  verified to parse via a substituted load test).
- **Not built**: nothing remains in code. Next: live e2e against the real
  server (fill in the example-config placeholders first).

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

Deployed model names live in the llama-swap config, not here. Role mapping with
**measured throughput on the production server (2026-09, post hardware build +
llama.cpp tuning)** — these obsolete the old disk-streamed estimates
(~0.47 tok/s prompt) that drove the original "hours per phase" sizing:

- **researcher**: fastest small dense model — ~300 tok/s prefill, ~50 tok/s decode.
- **planner**: the big smart model — ~40 tok/s prefill, ~10 tok/s decode.
- **implementer**: mid dense coder — ~100 tok/s prefill, ~20 tok/s decode.
- Every model loads with a **131072-token context window**.

Sizing math at these numbers (≈4 bytes/token): a 32 KiB digest ≈ 8k tokens ≈
**~3.5 min** of planner prefill; 2048 max_tokens ≈ **~3.5 min** decode. Even a
128 KiB digest (≈32k tokens) is only ~13 min of prefill. The constraint is now
the 131k context window (digest + research artifact + output must fit), not
wall clock — though `max_tokens` and a real timeout remain mandatory as
backstops against a runaway generation.

## Architecture conclusions (unchanged unless noted)

- **Engine manager = llama-swap.** Arbitrary per-model launch commands,
  OpenAI-compatible routing by request `model` field, mutually-exclusive swap
  groups, `/api/models/unload`, `/running`, request-holding during loads. Verified in
  production now, not just against upstream docs.
- **MCP rejected for flow control.** Phase transitions (load → work → persist →
  unload) are orchestrator-driven, not model-driven. MCP fine *inside* phases.
- **Delegated headless harnesses still deferred.** Built-in implementer loop is
  good enough; revisit only if it proves underpowered.
- **Swap-cost discipline** (much relaxed by the measured speeds, but the shape
  still holds): with the planner as the only big-model phase, each issue pays
  one model load + one one-shot generation + one unload. Prefill/decode are now
  fast enough (40/10 tok/s) that the planner phase is minutes, not hours; model
  load time likely dominates. The researcher absorbs the exploratory chattiness
  at fast-model prices. Plans handed to the implementer must still be
  self-contained — no KV survives a swap.

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

### 1. Model lifecycle control + exclusive-mode serialization (the rock-solid requirement) — LANDED 2026-09-17

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
  - `UnloadAll`: `POST {base}/api/models/unload` (current llama-swap path;
    falls back to legacy `POST /unload` on 404/405 — the legacy path 405s on
    current builds, confirmed in production 2026-09-17), then poll `/running`
    until empty or timeout. On timeout: return error — the next phase must not
    start against an unknown-residency server.
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
  swap-wait (`internal/llm/openai.go:54`) — the warmup client uses the flavor's
  timeout, which must still cover a cold model load (load time now likely
  exceeds inference time).
- llama-swap `/unload` semantics: RESOLVED 2026-09-17 — current builds moved
  unload to `POST /api/models/unload` (bare `/unload` returns 405); the
  controller uses the new path with legacy fallback.
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

### 4. Example config + docs — LANDED 2026-09-17

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
             model: <big-moe>, timeout: 1h }
    single_shot: true
    single_shot_context_bytes: 65536    # ~16k tokens ≈ ~7 min prefill at 40 tok/s; window is 131k
    max_tokens: 4096                    # backstop: ~7 min decode at 10 tok/s
    max_attempts: 1                     # human retry re-runs the whole generation
  implementer:
    model: { provider: openai, base_url: http://192.168.1.152:8080/v1,
             model: <mid-coder>, timeout: 2h }
```

Comments carry the token/time math from the measured production numbers above.

## Suggested order of work

1. ~~Modification 2 (human gates)~~ — DONE 2026-09-17.
2. ~~Modification 3 (dependencies)~~ — DONE 2026-09-17.
3. ~~Modification 1 (lifecycle + exclusive lock)~~ — DONE 2026-09-17.
4. ~~Modification 4 (example config/docs)~~ — DONE 2026-09-17. Remaining: the
   live e2e run —
   research (fast) → human gate → plan (slow, single-shot) → human gate →
   implementation (mid), verifying load/unload events, swap-wait timeout
   headroom, and digest budget.

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
