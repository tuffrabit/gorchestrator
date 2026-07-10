# Phase 5 Implementation Plan — Guardrails

> **Status:** Draft — coarse-grained; refine into session-sized parts before implementation begins.  
> **Prerequisite:** Phase 4 complete (real workspaces, sandboxed tests, MCP, external triggers) **and** Phase 4 project refactor (YAML project registry + agent flavors) **and** the post–Phase 4 polish landings listed under **Landed foundations** below.  
> **Scope:** The system protects itself and the user's wallet. With external triggers live, unattended volume is now possible — this phase makes it safe.  
> **Spec anchors:** §13 (budgets/effort/scope), §8.3 (issue context), §6.0 / §8.2 (projects + flavors), §11.5 (dashboard — do not regress).

---

## Landed foundations — do not regress

These are already in tree and recorded in `spec.md` (2026-07-10). Phase 5 work **must not** reintroduce free-text projects, title-only issues, current-phase-only drawers, or drop trigger/webhook bodies.

| Area | Contract |
|------|----------|
| **Projects** | YAML registry only; submit/API/CLI/webhook hard-fail on unknown names. Budgets and escalation attach to **registered project names / IDs**, not inventable strings. |
| **Agent cast** | Named flavors of core types; frozen on the issue at submit; retries reuse cast. |
| **Issue input** | Required **title**; optional **description** (`body` on API/triggers); optional **text-like attachments** (extension gate at upload only). Dual-write: SQLite `description` + `issue.md` + `attachments/` via one orchestrator path. |
| **Agent context** | Inline title + description; list attachment paths; previous phase `output.*`. Scope heuristics must consider description (and attachment names/content signals), not title alone. |
| **Artifact drawer** | Phase tabs (research / plan / implementation); Result · Output/Workspace · Activity. Implementation **Workspace** tree with per-file diffs; **no** full unified Diff tab. |
| **Workspace download** | `GET /api/issues/{id}/workspace.zip` only when implementation is `done`; full workspace tree. |
| **Submit UX** | Project select + title + description + attachments + dry-run + conditional flavors. No source path field. |

If a Phase 5 design conflicts with the above, **update the design** — do not “simplify” by undoing polish.

---

## Goal

The system asks "are you sure?" before burning budget, halts runaway work mid-flight, and gives admins configurable escalation instead of silent failure.

---

## Workstream A — Token Budget Enforcement

- Budgets configurable per agent, per issue, per project, and per provider (spec §13.2). **Project** means a YAML-registered project.
- **Enforcement point:** checked before each model call against accumulated per-call usage from `events.jsonl` — a budget is a hard mid-run stop, not a post-run report.
- Behavior on breach: phase → `failed` with `budget_exceeded` error class; admin notification; issue paused rather than silently dead (resumable after a human raises the budget).
- Warning threshold (e.g., 80%) → admin notification via the Phase 3 notification path.
- Optional cost mapping: a maintainer-supplied per-model pricing table converts tokens to currency on the dashboard; tokens remain the enforced unit.

## Workstream B — Effort Estimation Gate (spec §13.3)

- The Planner emits an effort tag (`high` / `medium` / `low`) as structured data via its `finish_task` result — not parsed out of free-form `output.*` (which the orchestrator never parses).
- `high` effort inserts a human confirmation gate before the implementer phase, regardless of configured adjudicator.
- Threshold behavior configurable per project (e.g., "medium also gates" for cautious teams).

## Workstream C — Scope Detection (spec §13.4)

- Cheap heuristics at submission time over **issue text** = title + optional description + optional attachment basenames (and optional light reads of small text attachments — not a second content-sniff pipeline). Signals: length/vagueness, forbidden-phrase list ("refactor the entire", "migrate everything"), later file-count estimates from the planner.
- Flagged issues require human confirmation before research begins — the cheapest possible point to stop a runaway task.
- Keep it honest: heuristics are advisory and logged; false-positive rate reviewed via audit data.
- **Do not** reintroduce title-only scope checks that ignore description/attachments.

## Workstream D — Admin Escalation Rules

- Configurable thresholds → notifications/escalations: consecutive failures per project, retry count per issue, budget warnings, adapter restart loops, sandbox refusals.
- Escalation targets reuse Phase 3 notification adapters; rules stored in config with an admin dashboard page.

## Workstream E — MCP Per-Tool Granularity (tightening Phase 4's per-server grants)

- Per-agent, per-tool allowlists (spec §5.5).
- Tool-level constraints where the tool supports them (e.g., `query_database` restricted to `SELECT`).
- Endpoint restrictions for HTTP-ish tools.
- Dashboard surface showing exactly which agent can touch what — the permission model must be auditable at a glance.
- Present this **without** replacing the existing multi-phase artifact drawer chrome.

---

## Tests

- Budget: scripted model with known usage → hard stop mid-phase at the boundary call; resumable after budget raise.
- Effort gate: planner tags high → pipeline pauses pre-implementation; human confirm proceeds.
- Scope flag: pathological **description** (and title) → held at submission; empty-description title-only pathological phrases still covered.
- Escalation: N consecutive failures → notification fired once (no alert storm).
- MCP: per-tool denial enforced at call time and absent from the agent's advertised toolset.
- Regression smoke: submit with description + `.md` attachment; open drawer on research after pipeline advanced; workspace.zip only when implementation done.

---

## Success Criteria

- A deliberately runaway task (huge scope, low budget) is stopped three separate ways: at submission (scope), before implementation (effort), and mid-run (budget).
- No guardrail can be bypassed by agent output content — all enforcement is orchestrator-side.
- Guardrails compose with landed issue input and project registry (no free-text projects; scope sees description).

## Out of Scope

- Metrics/reporting polish, deployment packaging → Phase 6.
- Changing the multi-phase drawer / workspace tree / zip model (already landed).
- Project membership / invites (still deferred — §17 Q17).

---

*End of Phase 5 Plan*
