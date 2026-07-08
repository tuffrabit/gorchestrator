# Phase 4 Implementation Plan — Extensibility

> **Status:** Draft — coarse-grained; refine into session-sized parts before implementation begins.  
> **Prerequisite:** Phase 3 complete (daemon, dashboard, auth, notifications).  
> **Scope:** Users plug in their own world: real git workspaces, sandboxed test execution, MCP tools, external triggers, and cloud storage.

---

## Goal

An issue filed in Jira flows into the daemon, the pipeline runs against a real git branch, the implementer iterates through container-sandboxed test-and-fix loops using an internal API exposed over MCP, and the resulting branch is pushed for human review.

---

## Workstream A — Git Workspace Model (spec §6; replaces the §6.6 snapshot interim)

1. Project git config (spec §6.3): `repo_url`, `base_branch`, auth mode (`ssh_key` / `token` / `gh_cli`). Environment prerequisites (`git`, optional `gh`) validated at project registration, not mid-run.
2. Clone cache per project; fetch before each run.
3. Branch per implementer run: `ai-implementer/{issue_id}-{run_id}`; workspace = checkout of that branch (replaces per-issue snapshot copies; researcher/planner read allowlists point at the checkout).
4. Post-run: stage, single structured commit (soft decision §17 Q13), optional push; the human decides whether to merge. Optional PR creation via `gh` where configured.
5. Parallel implementers: branch + workspace per run, `workspace_id`/`branch_name` tracked in SQLite (schema already reserved in spec §10.1).
6. Symlink and containment hardening from cleanup is assumed — real repos contain symlinks.

## Workstream B — `run_test` Tool (container sandbox is the acceptance criterion — spec §6.5)

- Executes the project's immutable `test_command` in a **container** (Docker/Podman): no network, workspace-only mount, CPU/memory/time limits, size-capped stdout/stderr returned to the agent.
- **No bare-subprocess fallback.** If no container runtime is available, `run_test` refuses to run with a clear error. Rationale: the command executes code the implementer just wrote — this is arbitrary code execution by proxy (spec §6.5).
- Secrets: injected from maintainer-only config into the container env; never rendered into `task.json` or any agent-readable artifact (soft decision §17 Q15).
- Wire into the implementer's registry; test-and-fix loop covered by an integration test with a scripted failing-then-passing test.

## Workstream C — MCP Client (spec §5.5, §12.2)

- MCP client over stdio (streamable HTTP later if needed); servers declared in project config; **deny by default**.
- **Per-agent, per-server allowlists ship in this phase** — required to land *with* external triggers, not after (spec §5.6). Per-tool granularity remains Phase 5.
- MCP tools registered explicitly in agent config (no dynamic discovery into prompts); tool calls recorded in `events.jsonl` like core tools.

## Workstream D — Trigger Port + External Triggers

- Formalize `TriggerPort` (deferred from Phase 1): a source of new issues feeding the daemon queue.
- Built-in: manual CLI/API/dashboard submission (already exists), HTTP webhook endpoint.
- External process adapters: GitHub Issues, Jira (JSON-RPC stdio, long-running listeners) — these exercise adapter restart/backoff (Workstream F).
- **Untrusted input posture (spec §5.6):** issues from external triggers default to `adjudicator: human` on the implementation boundary; overridable per project, but the default is the safe one.

## Workstream E — Storage & Agent Config

- S3 external storage adapter (JSON-RPC stdio) — first real consumer of slash-canonical port keys and the `events.jsonl` write path.
- Agent personality config (the casting thesis, realized): per-agent model/provider, temperature, token budget hooks (enforced Phase 5), system prompt override/extension, tool subsets — per-project defaults with per-issue overrides (settles §17 Q9).
- Adapter authentication pattern decided here (§17 Q12): adapters own their backend credentials via their own config/env, never through the core's agent-visible artifacts.

## Workstream F — JSON-RPC Client Completion

- Restart with exponential backoff on adapter death (deferred from cleanup).
- Streaming notifications (JSON-RPC notification messages) for long-running adapter events (trigger feeds).

---

## Tests

- End-to-end: webhook-submitted issue → pipeline on a real (fixture) git repo → branch with commit exists; push mocked or against a local bare repo.
- `run_test`: container enforced (refusal without runtime), timeout kill, no-network assertion, secrets absent from all artifacts.
- MCP: fixture MCP server; allowlisted agent sees tools, non-allowlisted agent does not; calls recorded in events.
- Trigger adapters: fixture JSON-RPC trigger emits an issue; adapter killed → restarted with backoff.
- S3 adapter: round-trip against a local S3 stand-in (e.g., MinIO) — keys identical across host platforms.

---

## Success Criteria

- Jira/GitHub issue → reviewed branch, hands-free except configured human gates.
- Implementer completes a test-and-fix loop inside the sandbox.
- An internal API is used by an agent via MCP under a per-agent server allowlist.
- Externally-triggered issues stop at a human gate before implementation by default.

## Out of Scope

- Token budgets, effort estimation, scope detection → Phase 5.
- Per-tool MCP granularity → Phase 5.
- Azure Blob (follow-on once S3 pattern is proven).

---

*End of Phase 4 Plan*
