# Phase 6 Implementation Plan — Polish & Ship

> **Status:** Draft — coarse-grained; refine into session-sized parts before implementation begins.  
> **Prerequisite:** Phase 5 complete (guardrails).  
> **Scope:** Shippable product: audit completeness, metrics, docs, deployment, retention, and hardening.  
> **Spec anchors:** §11.5 (dashboard), §7 (artifact tree including `issue.md` / `attachments/`), §8.2.5 / §8.3 (issue input + context).

---

## Landed foundations — do not regress

Same contracts as Phase 5’s **Landed foundations** table (project registry, flavors, description + attachments, phase-scoped drawer, workspace.zip). Phase 6 packaging and retention **must** treat these as first-class artifacts, not optional leftovers.

| Area | Implication for Phase 6 |
|------|-------------------------|
| **Retention** | Policies must cover `issue.md`, `attachments/`, and `implementation/workspace/` (zip is derived — no need to retain zip blobs). |
| **User docs** | Submitting issues documents title + description + text attachments; reading runs documents phase tabs and Workspace tree. |
| **Audit** | Submission audit already notes description/attachment presence; keep that for completeness sweeps. |
| **API reference** | Document `body`/`description`, multipart submit, `workspace.zip`, drawer `phase` query param. |

---

## Goal

A team can deploy gorchestrator to production, operate it from the docs alone, audit every decision after the fact, and trust it under sustained load at the target scale (hundreds of users).

---

## Workstream A — Audit Completeness

- Sweep: every state-changing action (submission, decision, config change, budget change, adapter registration, run lifecycle) writes an audit entry — verified by test, not by convention.
- Audit views on the dashboard: per-issue timeline, per-user activity, exportable (JSONL/CSV).

## Workstream B — Metrics Dashboard

- Token burn per project/agent/model over time; cycle time (submission → done) per phase; human intervention rate (gates hit, retries per issue); failure/escalation rates.
- Sourced from data already persisted (`events.jsonl`, runs, decisions, audit) — this phase adds aggregation and display, not new collection.

## Workstream C — Retention & Cleanup (closes §17 Q14)

- Configurable retention policy per project: workspaces (largest artifacts) auto-deleted N days after issue reaches terminal state; issue artifacts (`issue.md`, `attachments/`, `task.json`, `result.json`, `events.jsonl`, attempts) retained or archived — they are the audit substance **and** the human-submitted context of record.
- Suggested default: delete workspaces after 30 days, archive (tar) issue artifacts after 180, never silently delete audit rows.
- Dashboard storage-usage view; manual purge with confirmation + audit entry.
- **Do not** define “issue artifacts” as phase dirs only and orphan `issue.md` / `attachments/`.

## Workstream D — Documentation

- Architecture doc (generated from spec.md's living content, not a fork of it).
- Admin guide: deployment, OIDC setup, project/git config, adapters, budgets, escalation.
- User guide: submitting issues (**title, description, text attachments**), reading multi-phase runs (drawer phase tabs, Workspace tree, zip download when done), adjudicating well (feedback quality matters — say so).
- API reference for the HTTP API (including description/body, attachments on multipart submit, workspace.zip).
- README kept current (started in Phase 2 Cleanup).

## Workstream E — Deployment & Operations

- Single static binary release (goreleaser or equivalent), embedded dashboard assets.
- Dockerfile + Docker Compose: one service, volumes for SQLite + storage root; container runtime socket access documented for `run_test`.
- Backup/restore guidance: SQLite snapshot + storage-root sync, restore drill documented and tested (filesystem-authoritative reconciliation §9.4 makes partial restores survivable — verify that claim with a test).
- Health/readiness endpoints; structured logging with levels; optional OTel export (dependency already transitively present via ADK).

## Workstream F — Hardening

- CI: build, vet, race-detector test run, lint.
- Fuzz tests on path resolution/containment and JSON-RPC framing (the two parsing surfaces exposed to untrusted-ish input). Attachment path sanitization remains in scope for fuzz/regression.
- Load smoke test at target scale: sustained parallel issues at the configured worker cap; SQLite contention, SSE fan-out, storage growth observed.
- Dependency and release versioning policy; CHANGELOG.

---

## Success Criteria

- Fresh machine → deployed, configured, first issue through the pipeline using only the docs (including description/attachment submit path).
- Every decision in a month of simulated use is reconstructible from audit + artifacts (including original issue.md context).
- Load smoke test passes at target scale with no lock errors or lost events.
- Backup → destroy → restore drill succeeds.

---

*End of Phase 6 Plan*
