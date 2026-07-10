# Phase 4 Project Refactor — YAML Registry & Agent Flavors

> **Status:** Implemented (2026-07-10).  
> **Approved:** 2026-07-10  
> **Prerequisite:** Phase 4 Parts A–G in tree (git, `run_test`, MCP, triggers, S3, agent personality fields, JSON-RPC supervisor). Full suite green before starting this refactor is preferred but not a hard gate if residual Phase 4 polish is isolated.  
> **Scope:** Make projects and agent casting first-class YAML configuration. Remove create-on-submit / CLI `GetOrCreate`. Named flavors of the three core agent types; freeze cast on the issue; submit drawer selects project + optional flavors.  
> **Spec anchors:** §6.0, §8.2, §10.1, §10.3, §11.5, §17 Q9 / Q16.  
> **Out of scope:** Project membership / invites, admin config GUI, SSO polish, Phase 5 budgets.

---

## Context

Phase 4 delivered extensibility (git workspaces, container `run_test`, MCP allowlists, triggers, S3, global agent personality fields). Two foundational gaps remain:

1. **Projects are accidental.** `SubmitIssue` / CLI `Run` call `GetOrCreate` on a free-text name. The submit drawer is a text input + datalist; members can invent projects and mutate `source_path` at submit time. Phase 5 budgets and escalation cannot attach cleanly to garbage project names.
2. **Casting is incomplete.** Spec casting thesis and §17 Q9 call for project + per-issue layers. Code merges only built-in → global `default_model` → global `agents.<type>`. There is no project flavor catalog and no submit-time selection UI.

This refactor closes both **before** Phase 5. It does not add multi-tenant membership yet (deferred — local testing / global roles only).

---

## Goal

```text
Maintainer edits config.yaml projects: map
  → process start upserts SQLite registry
  → member opens New issue
  → selects a registered project
  → (if project has multiple flavors per stage) selects researcher / planner / implementer flavors
  → issue stores frozen cast
  → pipeline resolves agent config via merge layers including that cast
```

Unknown project names fail hard on every entry path (dashboard, API, CLI, webhook, external triggers). No project is ever created by issue traffic.

---

## Decisions (frozen 2026-07-10)

| Topic | Decision | Rationale |
|-------|----------|-----------|
| Config authority | **YAML only** for project definition. No admin config GUI this cut. | Matches product philosophy; GUI later edits the same model. |
| Project create | **No GetOrCreate** on submit/CLI/API/triggers. Registry upsert from `projects:` at process start. | Projects are administrative entities. |
| Membership / invites | **Deferred.** Global roles only; all authenticated users see all registered projects. | Local testing first; avoid building ACL twice. |
| Agent model | **Named flavors** of core types only (`researcher` / `planner` / `implementer`). No free-form agent types. | Casting without diluting Go identities. |
| Flavor UI | Show per-stage select **only if** `len(flavors) > 1` for that type on the selected project. Zero or one flavor → no picker. | Keeps the drawer simple. |
| No project agents block | Use global + built-in only; no flavor pickers. | Correct default. |
| Source path | **Removed from submit GUI and submit payload semantics.** Lives under project YAML. | Config ≠ issue input. |
| Cast persistence | Freeze flavor names on the issue at submit (`agent_flavors_json`). Retries/crash recovery reuse them. | YAML edits must not retarget in-flight work. |
| CLI | `run --project=foo` requires `foo` in YAML registry. Fail if missing. | Same rule as daemon. |

---

## Target config schema

```yaml
# Global (existing)
default_model: { ... }
agents:
  researcher: { ... }   # optional global defaults per type
  planner: { ... }
  implementer: { ... }

# NEW: project registry (source of truth)
projects:
  acme:
    source_path: /path/to/acme     # local snapshot mode; omit when using git
    git:
      repo_url: "git@github.com:myorg/acme.git"
      base_branch: main
      push: false
      create_pr: false
      auth:
        type: ssh_key
    test:
      command: "go test ./..."
      timeout: 60s
      image: "golang:1.22"
      secrets_env: []
    trust_external: false
    agents:
      researcher:
        default: thorough          # flavor name when submit omits choice
        flavors:
          thorough:
            model: { provider: openai, model: o3-mini }
            system_prompt_append: "Prefer root-cause depth..."
          cheap:
            model: { provider: openai, model: gpt-4o-mini }
      planner:
        default: standard
        flavors:
          standard: {}             # inherits global + built-in
      implementer:
        default: coder
        flavors:
          coder:
            model: { provider: anthropic, model: claude-sonnet-4 }
```

### Merge order (per phase)

1. Built-in type defaults (`defaultAgentConfig`)
2. Global `default_model` + global `agents.<type>`
3. Selected **project flavor** for that type (if any)
4. **Frozen issue cast** (flavor name → re-resolve overlay from project catalog at run time; name is frozen, not a deep-copied config blob — see note below)
5. Orchestrator policy (e.g. external → force implementer `adjudicator: human` unless `trust_external`)

**Freeze semantics:** Persist **flavor names** on the issue, not a full resolved config snapshot. At run time, re-resolve name → overlay from the current YAML/synced project config. If a flavor name is **missing** after a YAML edit (deleted flavor), fail the phase clearly rather than silently falling back — the cast is intentional. (Optional later: snapshot full resolved config at submit for air-gapped audit; not required for this cut.)

**Default resolution at submit:**

| Project type config | Behavior |
|---------------------|----------|
| No `agents` / no flavors for type | Cast field empty or omit; resolve global+built-in only |
| One flavor | Always that flavor (or `default`); no UI control |
| Multiple flavors | UI select; default = `default` key; persist choice |

---

## Sequencing (four session-sized parts)

```text
A Config types + registry sync + kill GetOrCreate
    ↓
B Agent flavor merge + issue cast column
    ↓
C Submit API + drawer UX (project select, conditional flavors, no source)
    ↓
D Docs, example config, tests green, residual call-site cleanup
```

| Part | Workstream | Depends on |
|------|------------|------------|
| **A** | YAML `projects` map, sync, hard-fail unknown project | — |
| **B** | Flavor schema, merge in `agentConfigForIssue`, persist cast | A |
| **C** | Dashboard + API submit surface | A, B |
| **D** | `config.example.yaml`, README note, full test pass | A–C |

Parts **B** and early **C** can overlap once A lands (API contract for cast can be stubbed).

---

## Part A — Project registry from YAML

### Behavior

1. Add `config.ProjectConfig` (and nested git/test/agents types as needed) and `Config.Projects map[string]ProjectConfig`.
2. On `NewEngine` / daemon start / CLI engine init: **`SyncProjects(cfg)`**
   - For each YAML name: `GetByName` → if missing `Create` with marshaled config; if present `UpdateConfigJSON`.
   - Do **not** delete SQLite projects absent from YAML in v1 (orphan rows may hold historical issues). Document: rename = new project + leave old issues on old id; optional later prune command.
3. Replace `GetOrCreate` on all user paths with **`GetByName` + error if nil**:
   - `Engine.SubmitIssue`, `Engine.Run`
   - Webhook / trigger adapters that pass project name
   - Any test helpers that assumed create-on-miss should declare projects in fixture config or call a test-only upsert helper.
4. Stop mutating project config from submit/CLI **source path**:
   - Remove `SourcePath` from issue-submit semantics (CLI flag may remain temporarily as **error with message** "configure projects.<name>.source_path in YAML" or be deleted outright — prefer delete).
   - Keep internal `setProjectSourcePath` / `setProjectGitConfig` only if still needed for tests; prefer writing via sync from fixture YAML.

### Files (expected)

| Path | Change |
|------|--------|
| `internal/config/config.go` | `Projects` map + nested types; validation |
| `internal/sqlite/projects.go` | `UpdateConfigJSON`; deprecate/remove `GetOrCreate` or restrict to sync |
| `internal/orchestrator/engine.go` / `service.go` | Sync on init; hard-fail unknown project |
| `internal/cli/run.go` | Drop `--source` create/update behavior; require registered project |
| `internal/server/webhook.go`, `internal/daemon/daemon.go` | Same resolve rules |
| Tests across orchestrator/server/daemon | Fixture projects in YAML or explicit create |

### Tests

- Sync creates missing project; second start updates `config_json`.
- `SubmitIssue` / `Run` with unknown project name → error; no new row.
- Known project → issue created; `source_path` from YAML used for snapshot when set.
- Webhook with unknown project → 4xx, not 500 inventing a project.

---

## Part B — Agent flavors + frozen cast

### Behavior

1. Schema under `projects.<name>.agents.<type>`:
   - `default string`
   - `flavors map[string]AgentConfig`
2. Migration: `issues.agent_flavors_json TEXT NOT NULL DEFAULT '{}'` (or NULL + treat empty as defaults).
3. `RunOptions` gains optional `AgentFlavors map[string]string` (keys: researcher/planner/implementer).
4. At submit:
   - Validate each provided flavor exists on the project (or project has no flavors for that type → reject non-empty).
   - Fill missing keys from project `default` when flavors exist.
   - Persist JSON on the issue row.
5. `agentConfigForIssue`:
   - Start from `e.cfg.Agent(type)` (global merge).
   - If issue cast names a flavor, merge that flavor's `AgentConfig` on top.
   - Then apply external-trust implementer human-gate policy (existing).
6. Expose a small read helper for the UI: given project name, return per-type flavor list + default (for Part C).

### Files (expected)

| Path | Change |
|------|--------|
| `internal/config/config.go` | Project agent flavor types; merge helper `MergeAgent(base, overlay)` |
| `internal/sqlite/db.go` | Migration N: `agent_flavors_json` |
| `internal/sqlite/issues.go` | Persist/load cast |
| `internal/orchestrator/engine.go` | `agentConfigForIssue` uses project + cast |
| `internal/orchestrator/service.go` | Validate cast on `SubmitIssue` |

### Tests

- Global-only project: pipeline uses global agent model/prompt.
- Project single flavor: always applied; no need for submit map.
- Project multi flavor: submit chooses `cheap`; researcher phase uses that model config (assert via `task.json` or dry-run / recorded config).
- Invalid flavor name → submit error.
- Retry after decide uses same cast (issue row unchanged).
- External issue still forces implementer human adjudicator unless `trust_external`.

---

## Part C — Submit API + drawer UX

### Behavior

1. **API** `POST /api/issues` body:

   ```json
   {
     "project": "acme",
     "title": "Add OIDC login",
     "dry_run": false,
     "agent_flavors": {
       "researcher": "thorough",
       "planner": "standard",
       "implementer": "coder"
     }
   }
   ```

   - `source` / `source_path` fields: **reject or ignore with no config mutation** (prefer reject 422 if present to fail loud during migration).
   - `agent_flavors` optional; defaults applied server-side.

2. **GET** helper for drawer (choose one):
   - Extend `GET /api/projects` to include flavor catalog per project, or
   - `GET /api/projects/{name}` with `{ name, agents: { researcher: { default, flavors: [names] }, ... } }`  
   Prefer expanding list payload modestly so the submit partial can render without N+1.

3. **Drawer** (`drawer_submit.html` + handlers):
   - Project: **required `<select>`** of registered projects (not free text / datalist create).
   - Remove source path field.
   - On project change: HTMX swap of flavor field partial (or full form) so only multi-flavor stages appear.
   - Dry-run + title unchanged.
   - POST includes selected flavors.

4. **AuthZ:** unchanged global roles (`member+` submit). No membership filter yet.

### Files (expected)

| Path | Change |
|------|--------|
| `internal/server/api.go` | Submit request shape; projects JSON includes flavors |
| `internal/server/dashboard.go` | Submit GET/POST partials |
| `internal/web/templates/partials/drawer_submit.html` | Select + conditional flavors |
| `internal/web/static/js/app.js` / CSS | Minimal if HTMX-only; fix dry-run alignment if touching the form |
| `internal/server/server_test.go` | Submit rejects unknown project / invalid flavor; no source mutation |

### Tests

- HTML partial: multi-flavor project renders three selects; single-flavor / none hides them.
- POST form creates issue with expected `agent_flavors_json`.
- Viewer cannot submit (existing).
- Manual checklist: change project in drawer → flavor fields update; submit prepends card.

---

## Part D — Docs, example config, cleanup

### Behavior

1. `configs/config.example.yaml` — document `projects:` map with commented multi-flavor example.
2. `configs/config.local.example.yaml` if present — same pattern for local dev.
3. README — short "Projects are declared in YAML" note; update any `run --project=foo` examples to imply pre-registration.
4. Grep for remaining `GetOrCreate`, submit `source`, free-text project assumptions; eliminate or confine to tests.
5. Full `go test ./...` green.

### Optional (nice-to-have, same PR if cheap)

- `gorchestrator projects list` CLI dumping synced registry (debug).
- Audit detail includes `agent_flavors` on `submit_issue`.

---

## Migration notes for existing local DBs

- Existing SQLite projects **not** in YAML remain in the DB (historical issues) but **cannot accept new issues** until listed in YAML (sync will not delete them; submit requires name ∈ YAML after sync — if name exists only in DB but not YAML, behavior: **sync does not remove**, but if we only allow submit for names in **current YAML map**, orphan DB-only names also fail. Prefer: **allow submit only for names present in YAML** after sync; YAML is the allowlist for new work.)
- Operators who relied on type-to-create must add those projects to config before continuing Phase 4 testing.
- `agent_flavors_json` defaults to `{}` for existing issues → resolve as global/built-in (and project default flavors if later YAML adds them — document that old issues with `{}` use **current** project defaults when flavors exist; acceptable for pre-prod).

---

## Success criteria

1. Fresh config with `projects.acme` only: submit/CLI to `acme` works; to `other` fails with a clear error; no new `projects` row for `other`.
2. Process restart re-syncs project `config_json` from YAML (e.g. change `source_path`, restart, new issue uses new path).
3. Multi-flavor project: drawer shows selects; chosen flavors appear on the issue; `task.json` / agent run reflects the flavor model/prompt.
4. Single-flavor / no-agents project: drawer has no agent UI; pipeline still runs.
5. Source path cannot be set via submit GUI or API.
6. Decide → retry reuses the same cast.
7. `go test ./...` green; no production path calls `GetOrCreate`.

---

## Out of scope

| Item | Where it goes |
|------|----------------|
| Per-project membership / email invites | Later; open question §17 Q17 |
| Admin config GUI | Future; still YAML-backed |
| Snapshotting full resolved AgentConfig on the issue | Optional later for audit immutability |
| Deleting orphan projects from SQLite when removed from YAML | Later ops command |
| Phase 5 budgets / escalation | Phase 5 (now attaches to real projects) |
| Changing core agent types or tool matrices | Unchanged §5.2 / §8.1 |

---

## Relationship to Phase 4 / 5

- **Phase 4 (`phase_4.md`):** Implementation of A–G treated as landed; this document is a **follow-on foundational refactor**, not a rewrite of git/MCP/triggers.
- **Phase 5:** Should not start until success criteria 1–2 and 5 at minimum are met (project identity is real). Flavor casting (3–4, 6) is strongly preferred so budgets can be per flavor later if desired.

---

*End of Phase 4 Project Refactor Plan*
