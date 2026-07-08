# Phase 3 Implementation Plan — Daemon, Dashboard & Auth

> **Status:** Draft — coarse-grained; refine into session-sized parts before implementation begins.  
> **Prerequisite:** Phase 2 complete (full pipeline, unified adjudication, crash recovery, revised artifact contract).  
> **Scope:** Convert the one-shot CLI into a long-running daemon, then give humans a real-time window and intervention controls over it.

---

## Goal

```bash
gorchestrator serve
```

A team member logs in via OIDC, submits an issue from the dashboard (or CLI), watches the pipeline execute live, clicks **retry with a reason** at a human gate, and the agent re-runs with that feedback in context. Killing the daemon mid-run and restarting it recovers every in-flight issue (spec §9.4).

---

## Why daemonization is Workstream A, not a side effect

Everything in this phase — webhook-style submission, real-time views, human gates that respawn goroutines in-process, parallel issues — presumes a persistent process with a work queue. The spec (§11.0) names this explicitly so it is scheduled, not discovered. The Phase 2 engine was built as an embeddable library precisely so this phase changes the front-end, not the core.

---

## Workstream A — Daemonization

1. **Engine API surface.** Extract/confirm a clean library boundary on the orchestrator: `SubmitIssue`, `Decide(issue, phase, decision, feedback)`, `GetIssue`, `ListIssues`, `SubscribeEvents`. The CLI `run`/`resume` become thin callers.
2. **`serve` subcommand.** Long-running process hosting the engine, HTTP server, and worker pool.
3. **Issue queue + worker pool.** SQLite-backed queue states; configurable concurrency (`max_concurrent_issues`); one pipeline per worker goroutine. WAL/busy_timeout from cleanup are prerequisites.
4. **Startup recovery scan.** On boot, run the spec §9.4 state machine across all non-terminal issues: re-run crashed phases, surface `waiting_human` items in the decision queue, reconcile the SQLite index against filesystem truth.
5. **Human gates in-process.** In daemon mode a decision respawns the worker goroutine directly — the CLI `resume` path remains for one-shot/headless use.
6. **Graceful shutdown.** Drain workers on SIGTERM; in-flight phases finish or are marked for recovery; no `failed` statuses caused by shutdown (they are `cancelled` or recoverable `in_progress`).

## Workstream B — HTTP API

- `POST /api/issues` (submit), `GET /api/issues`, `GET /api/issues/{id}` (status + artifact index), `GET /api/issues/{id}/artifacts/{path}` (read-only artifact fetch), `POST /api/issues/{id}/decisions` (pass/fail/retry + feedback), `GET /api/events` (SSE stream).
- SSE (soft decision §17 Q4): per-issue event streams tail `events.jsonl` + status transitions; polling fallback for degraded clients.
- JSON errors, no sessions required for the API beyond the auth middleware below.

## Workstream C — Dashboard (HTMX, server-rendered — soft decision §17 Q1)

- **Issue list:** phase, status, attempt count, token burn, live via SSE.
- **Issue detail:** artifact viewer (rendered Markdown, file tree, syntax highlighting), workspace **diff view** (source snapshot vs. workspace), activity stream rendered from `events.jsonl`, per-run and cumulative token display.
- **Adjudication UI:** pass/fail/retry at any boundary regardless of configuration (spec §9.3), with a **first-class feedback field** — feedback strongly encouraged on retry/fail (warn when empty).
- **Notification center:** pending human gates, admin alerts on bad output.
- No build pipeline: templates + HTMX attributes served from the binary (embed static assets).

## Workstream D — Users, Auth, Audit

- Migrations: `users`, `sessions`, `audit_log` tables; roles `admin` / `member` / `viewer`.
- Authorization mapping: viewer = read-only; member = adjudicate + submit; admin = configuration + escalation targets.
- OIDC (built-in, spec §3.3): standard authorization-code flow, session cookie; SSO scope question (§17 Q6: SAML or not) decided at the start of this phase.
- Audit log entries for every decision, submission, and configuration change (who, what, when, feedback).

## Workstream E — Notifications (wired here, moved from Phase 2)

- `notifications` table + dispatch loop in the daemon.
- Built-in sink: console/log. External process adapters: Slack webhook and/or SMTP email (§17 Q5 decided at the start of this phase) — these become the first *real* JSON-RPC adapters, exercising the hardened client from cleanup.
- Admin notifications on bad output (spec §11.4): errors, timeouts, empty outputs, budget warnings (budgets themselves are Phase 5).

---

## Tests

- Daemon lifecycle: submit → pipeline completes → artifacts + SQLite consistent.
- Kill -9 mid-phase → restart → recovery scan re-runs the phase (extends the Phase 2 test to daemon mode).
- Human gate via API: decision with feedback → agent rerun includes feedback.
- Concurrency: N parallel issues with a worker pool smaller than N; no SQLite lock errors.
- AuthZ: viewer cannot decide; unauthenticated API rejected.
- SSE: events observed during a dry-run pipeline.

---

## Success Criteria

- Team can log in via OIDC, watch a live run, retry with a reason, and get a Slack/email alert on a human gate.
- Daemon survives kill/restart with zero lost or corrupted issues.
- CLI one-shot mode still works unchanged for local/headless use.

## Out of Scope

- MCP, external triggers (GitHub/Jira), git workspace, S3 → Phase 4.
- Token budgets, effort gates → Phase 5.

---

*End of Phase 3 Plan*
