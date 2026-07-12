# Phase 5 Implementation Plan — Guardrails

> **Status:** Implementation landed (Parts A–E in tree; suite green) — 2026-07-12.  
> **Approved plan frozen from session 2026-07-12.**  
> **Prerequisite:** Phase 4 complete (real workspaces, sandboxed tests, MCP, external triggers) **and** Phase 4 project refactor (YAML project registry + agent flavors) **and** the post–Phase 4 polish landings listed under **Landed foundations** below.  
> **Scope:** Cheap human gates and hard mid-session stops so unattended volume (external triggers) cannot runaway. **Not** a spend/wallet system — casting chooses cost; provider budgets guard **context-window scale per agent session**.  
> **Spec anchors:** §13 (guardrails — refined below for budgets), §5.5 (MCP per-tool), §8.2 / §8.3 (cast + context), §6.0 (projects), §9 (human gates), §11.5 (dashboard — do not regress).  
> **Markdown is canonical** — companion `phase_5.html` is presentation only.

---

## Landed foundations — do not regress

These are already in tree and recorded in `spec.md` (2026-07-10). Phase 5 work **must not** reintroduce free-text projects, title-only issues, current-phase-only drawers, or drop trigger/webhook bodies.

| Area | Contract |
|------|----------|
| **Projects** | YAML registry only; submit/API/CLI/webhook hard-fail on unknown names. Escalation rules and effort thresholds attach to **registered project names**, not inventable strings. |
| **Agent cast** | Named flavors of core types; frozen on the issue at submit; retries reuse cast. Human casting is the cost-control surface — not an orchestrator wallet. |
| **Issue input** | Required **title**; optional **description** (`body` on API/triggers); optional **text-like attachments** (extension gate at upload only). Dual-write: SQLite `description` + `issue.md` + `attachments/` via one orchestrator path. |
| **Agent context** | Inline title + description; list attachment paths; previous phase `output.*`. Scope heuristics must consider description (and attachment names/content signals), not title alone. |
| **Artifact drawer** | Phase tabs (research / plan / implementation); Result · Output/Workspace · Activity. Implementation **Workspace** tree with per-file diffs; **no** full unified Diff tab. |
| **Workspace download** | `GET /api/issues/{id}/workspace.zip` only when implementation is `done`; full workspace tree. |
| **Submit UX** | Project select + title + description + attachments + dry-run + conditional flavors. No source path field. |

If a Phase 5 design conflicts with the above, **update the design** — do not “simplify” by undoing polish.

---

## Goal

```text
Submit (pathological scope) → waiting_human (scope) → human pass
  → Research → Plan (effort tag)
  → [effort ≥ project threshold] → waiting_human (effort) → human pass
  → Implementer
  → mid-phase: provider session token gate hard-stops runaway model loops
MCP tools: only allowlisted tools + simple constraints reach the agent
Escalation: YAML thresholds → notify once (no alert storm)
```

The system asks **"are you sure?"** before expensive work, **halts a single agent session** that would exceed a configured context-scale limit, and gives admins **configurable escalation** instead of silent failure.

---

## Decisions (close draft ambiguities)

| Topic | Decision | Rationale |
|-------|----------|-----------|
| Token budget product meaning | **Session / context-window gate per agent run**, not a wallet. One agent phase = one ADK session = one conversation with the provider. | Casting chooses model cost; orchestrator only bounds runaway *within* a session. |
| Budget config axis | **Provider only** via top-level `providers:` map. No project, flavor, or issue YAML budget. | Agents inherit the budget of `model.provider`. |
| Budget unit | **Per issue × per phase (agent run).** Each phase that uses provider `P` gets a **fresh** ceiling of `providers.P.token_budget`. Counters do **not** accumulate across research → plan → implementer. | Matches new context window per agent. |
| Multi-provider on one issue | **Independent.** Tokens attributed to the provider of each model call; each provider has its own session ceiling when its phase runs. | Research on openai and implementer on anthropic do not share a pool. |
| Missing provider budget | **Unlimited** (no enforcement) for that provider. Local/dev and dry-run keep working without config. | Opt-in safety. |
| Old `AgentConfig.token_budget` | **Remove.** Config load **errors** if the key appears under agents / flavors. | Was stored-only Phase 4; contradicts provider-only model. |
| Enforcement hook | Wrap `model.LLM` so each `GenerateContent` checks spent vs ceiling **before** the call. Rehydrate spent from phase `events.jsonl` on crash/resume. | Loop-boundary checks alone miss multi-turn tool loops. |
| Usage events | Prefer **per-call** usage rows in `events.jsonl` (fix loop-aggregate-only). | Needed for true mid-run stop and rehydrate. |
| Breach | Phase → `failed` with error class / message `budget_exceeded`; admin notification via Phase 3 path. | Spec §13.2 hard stop. |
| Resume after breach | **Issue-level override on Decide** replaces the provider session ceiling for **this issue** (all remaining / retried phases using that provider). Not “add N tokens.” | Human raises the context gate without editing YAML mid-flight. |
| Warning | `providers.<name>.warn_pct` (default **80** when budget set). At most **one** warn notify per phase attempt. | No alert storm. |
| Optional pricing table | **Cosmetic only** on dashboard if configured; **tokens remain the only enforced unit.** May ship minimal or defer display polish. | Not a wallet. |
| Effort emit | **Planner-only** `finish_task` field `effort` enum: `high`, `medium`, or `low` (structured schema). Orchestrator never parses free-form `output.*`. | Spec §13.3. |
| Missing / invalid effort | Treat as **`high`** → gate. | Safe default. |
| Effort threshold | `projects.<name>.guardrails.effort_gate_min`: `high`, `medium`, or `low` (default **`high`**). Gate when `effort >= min` on ordered scale low < medium < high. | Cautious teams can set `medium`. |
| Effort gate placement | After plan phase is accepted, **before** implementation starts — regardless of plan/implementer adjudicator config. Uses `waiting_human` + Decide. | Reuse existing gate machinery. |
| Scope signals | Title + description + attachment **basenames** + **size-capped light reads** of small text attachments (not a content-sniff pipeline). | Spec §13.4 + landed issue input. |
| Scope hold | Issue created with source prepared; status **`waiting_human`**, phase **`research`**, reason scope. Research agent does **not** run until human pass. | Cheapest stop; reuse Decide. |
| Hold reason storage | Pending **decisions** row feedback + phase **`result.json`** error/reason (e.g. `scope: …` / `effort: high`). UI shows why held. | Survives reload; filterable later. |
| Escalation config | **YAML-only** rules this phase. | Matches projects-are-YAML; no second config authority. |
| Escalation UI | Minimal **admin read-only** page: active rules + recent escalations. No live CRUD editor. | Phase 5 need; full admin config GUI later. |
| MCP tool grants | Nested under **global** `mcp_servers` entries: tool allowlist + optional simple constraints. | Spec §5.5 Phase 5. |
| MCP agent bind | Agent still lists **server names** in `mcp_servers`. Tools/constraints come from the global server definition; agent does not re-declare tool lists. | Minimal agent schema change. |
| MCP constraints depth | Allow/deny tool names + **optional simple constraints** (e.g. require prefix on an arg, deny substrings, HTTP URL allowlist) enforced in `wrapMCPTool`. Not a full policy language. | Auditable; enough for SELECT-ish and endpoint-ish cases. |
| Implementation order | **A scope → B effort → C provider budgets → D MCP tools → E escalation** | Cheap stops first; mid-run session gate; then permissions; then alert rules. |

### Explicit non-goals for budgets

- Issue total token caps  
- Project spend caps / wallets  
- Cross-phase remaining budget  
- Enforcing currency / dollar limits  

Human **agent casting** (which flavor / model) is the cost-control process.

---

## Sequencing (five session-sized parts)

```text
A Scope detection
    ↓
B Effort estimation gate
    ↓
C Provider session token budgets
    ↓
D MCP per-tool granularity
    ↓
E Admin escalation rules (YAML + read-only page)
```

Parts are sequential by default. **D** depends only on existing MCP manager (Phase 4) and can start after **C** (or carefully parallel after **B** if budget work is isolated). **E** should land last so it can subscribe to failure / budget / sandbox signals introduced earlier.

| Part | Workstream | Depends on |
|------|------------|------------|
| **A** | Scope detection | Landed submit + `waiting_human` |
| **B** | Effort gate | A (shared hold-reason patterns helpful); plan phase |
| **C** | Provider session budgets | B optional; LLM factory + `runAgentLoop` |
| **D** | MCP per-tool | Phase 4 MCP manager |
| **E** | Escalation | C (+ failure paths from A–D) |

---

## Config sketches

```yaml
# Provider session gates (context-scale). Opt-in per provider name
# matching model.provider (openai, anthropic, gemini, …).
providers:
  openai:
    token_budget: 128000
    warn_pct: 80
  anthropic:
    token_budget: 200000
  # dryrun / unset → unlimited

# Global MCP servers — Phase 5 adds tools + constraints under each server.
mcp_servers:
  - name: internal-api
    command: ["./bin/my-mcp-server"]
    env: [API_TOKEN]
    # empty tools → all tools from server (Phase 4 compat)
    tools:
      - name: query_database
        # optional simple constraints (all must pass):
        constraints:
          - type: arg_prefix
            arg: sql
            prefix: "SELECT"
      - name: call_http
        constraints:
          - type: url_allowlist
            arg: url
            hosts: ["api.internal.example"]

projects:
  auth-service:
    guardrails:
      effort_gate_min: high   # high | medium | low — default high
      # scope: uses global heuristics unless overridden later
    agents:
      implementer:
        default: coder
        flavors:
          coder:
            mcp_servers: [internal-api]   # server allowlist only

# Escalation — YAML only; admin page is read-only.
escalation:
  rules:
    - name: consecutive_project_failures
      when: consecutive_failures
      project: "*"              # or registered project name
      threshold: 3
      notify: admin             # bootstrap admin emails / console
    - name: budget_breach
      when: budget_exceeded
      threshold: 1
      notify: admin
    - name: sandbox_refuse
      when: sandbox_refused
      threshold: 1
      notify: admin
  # Dedupe: fire once per (rule, issue|project window); no storm
```

**Removed:** `agents.*.token_budget` / flavor `token_budget` — load error if present.

---

## Part A — Scope detection

### Behavior

1. At **`SubmitIssue`** (all entry points: dashboard, API, CLI, webhook, external triggers), after project/cast/attachment validation and **`persistIssueContext` + `prepareIssueSource`**, run scope heuristics on:
   - title
   - description
   - attachment basenames
   - light read of small text attachments (size cap, e.g. first 8KiB each; skip binary/oversize)
2. Signals (initial set; tunable constants or YAML later):
   - excessive length (title+description)
   - forbidden phrases (e.g. "refactor the entire", "migrate everything", "rewrite the whole")
   - extreme vagueness heuristics (optional light pass)
3. If flagged:
   - Do **not** queue for research work as normal in-progress pipeline start.
   - Set issue `waiting_human`, `current_phase=research`.
   - Write research `result.json` with `status: waiting_human` and reason `scope: <summary>`.
   - Create pending **decision** with feedback describing why.
   - Notify human gate (existing path).
   - Audit / log the heuristic hit (advisory — false positives expected).
4. Human **pass** → re-queue; research runs. **fail** → terminal failed. **retry** with feedback optional (same as other gates).
5. Unflagged issues → existing `queued` behavior unchanged.

### Files

| Path | Change |
|------|--------|
| `internal/orchestrator/scope.go` | **New** — heuristic evaluation |
| `internal/orchestrator/service.go` | Hook after prepare source; hold path |
| `internal/orchestrator/engine.go` | Ensure process/resume respects scope hold |
| `internal/config/config.go` | Optional global phrase list / caps if needed |
| `internal/server/dashboard.go` / templates | Reason chip on expanded card for scope holds |
| Tests | Pathological description; title-only phrase; clean issue not held |

### Tests

- Pathological **description** → `waiting_human` before any research `in_progress` agent run.
- Title-only forbidden phrase (empty description) still held.
- Normal issue still `queued` and processes.
- Pass decision starts research.

---

## Part B — Effort estimation gate

### Behavior

1. Extend **planner-only** finish schema with required `effort` enum: `low` | `medium` | `high`.
2. Update planner system prompt to set effort honestly from plan complexity.
3. On plan phase **accepted** (`done`), before starting implementation:
   - Read planner effort from finish_task (persisted on plan `result.json` or side field).
   - If missing/invalid → treat as **`high`**.
   - Compare to `projects.<name>.guardrails.effort_gate_min` (default `high`).
   - If `effort >= min` → do **not** start implementer; set `waiting_human`, phase `implementation` (or pre-implement boundary), reason `effort: high|medium`, pending decision, notify.
4. Human pass → queue implementer. Fail / retry per existing Decide semantics.
5. Researcher / implementer finish schema **unchanged** (`done` + `rationale` only).
6. Dry-run model: emit a default effort (e.g. `low`) unless prompt requests otherwise so tests stay unblocked; dedicated tests force `high`.

### Files

| Path | Change |
|------|--------|
| `internal/agents/planner.go` / shared schema split | Planner `finishTaskSchema` + prompt |
| `internal/agents/researcher.go` | Keep shared base or split schemas cleanly |
| `internal/llm/dryrun.go` | Planner effort in finish_task args |
| `internal/orchestrator/engine.go` | Post-plan gate in `runPipeline` |
| `internal/config/config.go` | `ProjectGuardrails` / `effort_gate_min` |
| Dashboard | Effort reason on gate card |
| Tests | high → hold; low + min high → no hold; missing → high |

### Tests

- Planner tags `high` → pipeline pauses pre-implementation; pass proceeds.
- `effort_gate_min: medium` + effort `medium` → hold.
- Missing effort → hold as high.

---

## Part C — Provider session token budgets

### Behavior

1. **Config:** top-level `providers` map: `token_budget`, optional `warn_pct` (default 80).
2. **Remove** `AgentConfig.TokenBudget`; reject YAML that still sets `token_budget` on agents/flavors.
3. **Resolve** at phase start:  
   `ceiling = issue_provider_override[provider] ?? providers[provider].token_budget`  
   Missing / zero → unlimited.
4. **Wrap** `model.LLM` returned from `llm.New` (or factory helper) with a budget guard that:
   - Tracks spent for **this phase run** (in memory).
   - On construction / resume: rehydrate spent from phase `events.jsonl` usage records.
   - **Before** each `GenerateContent`: if `spent >= ceiling` → error `budget_exceeded` (no call).
   - **After** each call: add `UsageMetadata.TotalTokenCount` (or prompt+completion); append **per-call** usage event; if crossed `warn_pct`, notify once per attempt.
5. Phase fails with `budget_exceeded`; `NotifyBadOutput` / escalation hook; issue not auto-deleted — human can Decide.
6. **Decide override:** member/admin may supply absolute token ceiling for a provider on this issue (replace). Stored on issue (e.g. JSON column or config blob). Subsequent phases/retries using that provider use the override. Re-queue / retry same phase after override.
7. Dashboard: show session spent / ceiling on expanded card or result summary when configured (minimal). Optional pricing table is cosmetic only if present.

### Files

| Path | Change |
|------|--------|
| `internal/config/config.go` | `Providers` map; remove `TokenBudget`; validation |
| `configs/config.example.yaml` | Document `providers:`; remove agent token_budget |
| `internal/llm/budget.go` | **New** — wrapping LLM + rehydrate helper |
| `internal/orchestrator/engine.go` | Wire wrapper; fail path; per-call usage events |
| `internal/orchestrator/events.go` / event schema | Per-call usage clarity |
| `internal/sqlite/issues.go` | Issue provider budget override storage |
| `internal/orchestrator/service.go` | Decide accepts override |
| `internal/server/*` | Decide form/API field; display |
| Tests | Scripted usage → mid-phase stop; rehydrate; override resume |

### Tests

- Scripted model with known per-call usage → hard stop mid-phase before exceeding call; status failed `budget_exceeded`.
- Resume/rehydrate: partial events.jsonl counts toward ceiling.
- Decide with higher absolute ceiling → retry proceeds past prior stop point.
- Next phase gets a **fresh** counter (same provider budget value, spent reset).
- No `providers` entry → unlimited.

---

## Part D — MCP per-tool granularity

### Behavior

1. Extend `MCPServerConfig` with optional `tools: [{name, constraints[]}]`.
2. **Empty `tools`:** Phase 4 behavior — all tools from that server (when agent lists the server).
3. **Non-empty `tools`:** only listed tool names are wrapped into the agent toolset; others never advertised.
4. **Constraints** evaluated in `wrapMCPTool` before `CallTool`:
   - `arg_prefix` — named arg string must start with prefix (case rules documented).
   - `arg_deny_substring` — optional denylist.
   - `url_allowlist` — parse URL host against allowlist.
   - Unknown constraint type → fail closed for that tool call (or config load error — prefer **load error**).
5. Agent `mcp_servers: [name]` unchanged — server allowlist only.
6. Denial: tool absent from schema **and** if somehow called, reject at wrapper.
7. Dashboard: small **permissions** view (admin or project detail) listing server → tools → agents that can use them — **without** replacing multi-phase artifact drawer. Prefer a separate page/section under admin or project API, not drawer tabs.

### Files

| Path | Change |
|------|--------|
| `internal/config/config.go` | Nested tools + constraints types; validation |
| `internal/mcp/manager.go` | Filter + constraint enforcement |
| `internal/mcp/manager_test.go` | Allow/deny + constraint cases |
| `configs/config.example.yaml` | Examples |
| `internal/server/*` + templates | Read-only permissions surface |
| Tests | Tool absent from agent toolset; constraint violation at call |

### Tests

- Per-tool denial: not in advertised set; call-time deny if forced.
- `arg_prefix` SELECT-only happy path + reject `DROP`.
- Agent without server still sees no tools.

---

## Part E — Admin escalation rules

### Behavior

1. YAML `escalation.rules` as sketched; load/validate at process start.
2. Evaluate on signals: consecutive failures per project, budget_exceeded, sandbox refused, adapter restart loops (if supervisor exposes counters), retry count thresholds.
3. Deliver via existing `notify.Dispatcher` (console / Slack / email).
4. **Dedupe:** one fire per (rule identity, subject key, window) — no alert storm (test: N failures → one notify).
5. Admin **read-only** HTML page: list rules from config + recent matching notification rows. No YAML write-back UI.
6. Role: admin-only page; members keep existing notifications list.

### Files

| Path | Change |
|------|--------|
| `internal/config/config.go` | Escalation config structs |
| `internal/notify/escalation.go` | **New** — rule eval + dedupe |
| `internal/orchestrator/*` / `daemon/*` | Emit signals into evaluator |
| `internal/server/*` + templates | `/admin/escalation` read-only |
| Tests | Threshold fire once; no double storm |

### Tests

- N consecutive failures → single escalation notification.
- Budget breach triggers configured rule.
- Non-admin cannot open admin page.

---

## Cross-cutting tests

- Regression smoke: submit with description + `.md` attachment; open drawer on research after pipeline advanced; `workspace.zip` only when implementation done.
- Hold reasons visible after reload (decision + result.json).
- No guardrail bypassable by free-form agent `output.*` content.

---

## Success criteria

1. A deliberately runaway task is stopped **three ways**: at submission (**scope**), before implementation (**effort**), and mid-agent-session (**provider token gate**).
2. Provider budget is a **session/context gate**, not a wallet; cross-phase totals are intentionally unbounded by this mechanism.
3. No guardrail can be bypassed by agent output content — enforcement is orchestrator-side.
4. Guardrails compose with landed issue input and project registry (no free-text projects; scope sees description + attachments).
5. MCP agents only see/call allowlisted tools; simple constraints enforced at call time.
6. Escalation rules fire without alert storms; admin can read rules and recent escalations.

---

## Out of scope

- Metrics/reporting polish, deployment packaging → Phase 6.
- Changing the multi-phase drawer / workspace tree / zip model (already landed).
- Project membership / invites (still deferred — §17 Q17).
- Issue/project token **wallets** or currency enforcement.
- Full policy language for MCP; admin YAML editors.
- SAML, Azure Blob, WASM (unchanged deferrals).

---

## Spec touch-ups (when implementing / completing)

Update living `spec.md` to match:

- §13.2 — provider session budgets; remove multi-layer wallet reading; drop agent `token_budget` store-only story.
- §5.5 Phase 5 — nested tools + constraints under `mcp_servers`.
- §8.2 — remove `token_budget` from configurable flavor fields; add project `guardrails.effort_gate_min`.
- §17 / decisions log — record closed Phase 5 budget semantics.
- `configs/config.example.yaml` — `providers:`, guardrails, escalation, MCP tools examples.

---

## Implementation checklist (session order)

- [x] **A** Scope detection + hold reason UX  
- [x] **B** Planner effort + pre-implement gate  
- [x] **C** Provider session budgets + Decide override  
- [x] **D** MCP per-tool + constraints + permissions view  
- [x] **E** Escalation YAML + dedupe + admin read-only page  
- [x] Spec + example config + regression smoke  

---

*End of Phase 5 Plan*
