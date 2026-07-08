# Phase 6 Implementation Plan — Polish & Ship

> **Status:** Draft — coarse-grained; refine into session-sized parts before implementation begins.  
> **Prerequisite:** Phase 5 complete (guardrails).  
> **Scope:** Shippable product: audit completeness, metrics, docs, deployment, retention, and hardening.

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

- Configurable retention policy per project: workspaces (largest artifacts) auto-deleted N days after issue reaches terminal state; issue artifacts (`task.json`, `result.json`, `events.jsonl`, attempts) retained or archived — they are the audit substance.
- Suggested default: delete workspaces after 30 days, archive (tar) issue artifacts after 180, never silently delete audit rows.
- Dashboard storage-usage view; manual purge with confirmation + audit entry.

## Workstream D — Documentation

- Architecture doc (generated from spec.md's living content, not a fork of it).
- Admin guide: deployment, OIDC setup, project/git config, adapters, budgets, escalation.
- User guide: submitting issues, reading runs, adjudicating well (feedback quality matters — say so).
- API reference for the Phase 3 HTTP API.
- README kept current (started in Phase 2 Cleanup).

## Workstream E — Deployment & Operations

- Single static binary release (goreleaser or equivalent), embedded dashboard assets.
- Dockerfile + Docker Compose: one service, volumes for SQLite + storage root; container runtime socket access documented for `run_test`.
- Backup/restore guidance: SQLite snapshot + storage-root sync, restore drill documented and tested (filesystem-authoritative reconciliation §9.4 makes partial restores survivable — verify that claim with a test).
- Health/readiness endpoints; structured logging with levels; optional OTel export (dependency already transitively present via ADK).

## Workstream F — Hardening

- CI: build, vet, race-detector test run, lint.
- Fuzz tests on path resolution/containment and JSON-RPC framing (the two parsing surfaces exposed to untrusted-ish input).
- Load smoke test at target scale: sustained parallel issues at the configured worker cap; SQLite contention, SSE fan-out, storage growth observed.
- Dependency and release versioning policy; CHANGELOG.

---

## Success Criteria

- Fresh machine → deployed, configured, first issue through the pipeline using only the docs.
- Every decision in a month of simulated use is reconstructible from audit + artifacts.
- Load smoke test passes at target scale with no lock errors or lost events.
- Backup → destroy → restore drill succeeds.

---

*End of Phase 6 Plan*
