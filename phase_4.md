# Phase 4 Implementation Plan — Extensibility

> **Status:** Implementation landed (Parts A–G in tree; full suite green). Polish/edge cases may remain.  
>
> **Approved plan frozen from session 2026-07-09.**  
> **Prerequisite:** Phase 3 complete (daemon, dashboard, auth, notifications).  
> **Scope:** Real git workspaces, container-sandboxed `run_test`, MCP tools with per-agent server allowlists, external triggers, S3 storage, agent personality config, JSON-RPC client completion.  
>
> **Follow-on (before Phase 5):** YAML project registry + named agent flavors — see `phase_4_project_refactor.md` (approved 2026-07-10). That plan supersedes free-text project create and completes the casting layers described in §17 Q9 / Q16.  

---

## Context

Phases 1–3 delivered a full Research → Plan → Implement pipeline, crash recovery, and a daemon + dashboard. Source access is still the §6.6 **snapshot interim** (`source/` copy without `.git`; implementer workspace seeded by copy). There is no `run_test` tool, no MCP client, no TriggerPort, no cloud storage, and the JSON-RPC client cannot restart dead adapters or receive streaming notifications.

Phase 4 is when users plug in *their* world: a real repo, sandboxed tests, internal APIs via MCP, issues from Jira/GitHub, and optional S3 for artifacts.

---

## Goal

```text
External issue (webhook / GitHub / Jira)
  → daemon queue
  → pipeline on a real git branch checkout
  → implementer test-and-fix via containerized run_test
  → optional MCP tools under per-agent server allowlist
  → single structured commit (+ optional push / PR)
  → human review
```

Hands-free except configured human gates. Externally-triggered issues default to a human gate before implementation.

---

## Decisions (close open questions + resolve draft ambiguities)

| Topic | Decision | Rationale |
|-------|----------|-----------|
| §17 Q9 — YAML agent schema | **Per-agent map with merge layers:** global `default_model` → `agents.<type>` defaults → per-issue overrides (issue `config_json` / submit options). Fields: `model`, `temperature`, `max_tokens`, `system_prompt` (full override), `system_prompt_append`, `tools` (core subset), `mcp_servers`, `adjudicator` / `max_attempts` / `loops` / `rubric`, `token_budget` (**stored only**; enforcement Phase 5). | Spec §8.2; casting thesis. Existing `config.Agent` merge is the seed — extend, don't rewrite. |
| §17 Q12 — Adapter auth | **Adapters own backend credentials** via process env and/or adapter-local config files. Core never injects secrets into agent-visible artifacts (`task.json`, `events.jsonl` payloads). JSON-RPC params may carry non-secret operational data only. | Matches Slack/email (`SLACK_WEBHOOK_URL`, SMTP env). Same for S3 (`AWS_*` / endpoint env), Jira/GitHub tokens. |
| §17 Q13 — Git commits | **Single commit per implementer run** (soft → hard for this phase). Structured message template (below). | Soft decision; granular commits are Phase 6 polish. |
| §17 Q15 — Test secrets | **Maintainer-only** `test.secrets_env: [NAME,…]` — host env values injected into the container; never written into `task.json` / events (tool result may say `secrets: N injected` without values). | Soft decision; agent-readable artifacts must stay clean. |
| §17 Q3 — Search tool | **Out of scope.** Keep pure-Go `grep_search`. | Phase 4+ candidates remain deferred. |
| §17 Q14 — Retention | **Out of scope.** Phase 6. | Unchanged. |
| Git layout vs §6.6 | **Replace snapshot copies with git worktrees.** Per-project **bare clone cache**; per-issue read-only `source/` worktree on `base_branch`; per-implementer-run worktree at `implementation/workspace` on `ai-implementer/{issue_id}-{run_id}`. Drop `.git`-less rsync snapshots when git is configured. | Eliminates copy overhead; matches draft workstream A; still uses existing path helpers for agent allowlists. |
| Local-only source | If project has **no** `git.repo_url` but has `source_path`, keep Phase 2 snapshot behavior. Git path is opt-in per project. | CLI/dev fixtures and non-remote monorepos must not regress. |
| Branch name | `ai-implementer/{issue_id}-{run_id}` (run_id = SQLite `runs.id`). | Stable, unique, audit-friendly (prefer over timestamp in §6.4 example). |
| Push / PR defaults | `git.push: false` by default; `git.create_pr: false` by default. Enable per project. PR only when `auth.type: gh_cli` (or `gh` available + remote is GitHub). | Safe default; human owns merge. |
| Commit message | `ai-implementer: {title}\n\nIssue: #{id}\nRun: {run_id}\nAgent: implementer` | Single structured form for review. |
| `run_test` runtime | **Docker or Podman** (prefer `docker` if both on PATH). **No bare-subprocess fallback.** Refuse with clear error if neither present. | Spec §6.5 hard requirement. |
| Test container image | **Required** when `test.command` is set: `test.image` (e.g. `golang:1.22`). No magic default language image. | Avoids silent wrong runtime; projects own their toolchain. |
| Container isolation | `--network=none`, bind-mount workspace **read-write** at `/workspace`, cwd `/workspace`, CPU/memory/time limits from config, stdout/stderr size-capped (default 64KiB each). | Spec §6.5. |
| Dry-run + `run_test` | In dry-run, `run_test` returns a stub success without spawning a container. | Keeps pipeline tests offline. |
| MCP SDK | **Official** `github.com/modelcontextprotocol/go-sdk` for stdio client. Fixture test server may use the same SDK. | Prefer official over community; stdio is enough (streamable HTTP later). |
| MCP permission (Phase 4) | **Per-agent, per-server allowlists.** If server allowed, all tools from that server are exposed. Per-tool allowlists → Phase 5. Servers deny-by-default (not connected unless configured). | Spec §5.5 / §5.6 — must land *with* external triggers. |
| MCP registration | Servers declared in config; tools discovered at connect time for schema, but **only servers on the agent allowlist** enter the agent's tool registry. No global "all agents get all MCP tools." | Aligns §12.2 "explicit" with §5.5 Phase 4 model. |
| TriggerPort | Formal port: long-running sources emit `IssueSubmission` into the daemon queue via `SubmitIssue`. Built-in: existing CLI/API/dashboard + **HTTP webhook**. External: GitHub Issues + Jira (JSON-RPC stdio listeners). | Spec §3.3 / draft D. |
| Webhook auth | Shared secret: header `X-Gorch-Token: <token>` (or `Authorization: Bearer <token>`) compared with constant-time equality to `triggers.webhook.token_env`. | Minimal; no HMAC body signing required for MVP. |
| Untrusted input default | Issues whose `source` is `webhook` / `github` / `jira` / other external → **force `implementer` adjudicator to `human`** unless project sets `triggers.trust_external: true` (or per-source override). Research/plan keep project agent config. | Spec §5.6; gate at highest-blast boundary. |
| Trigger adapters ship | **Both** GitHub Issues and Jira as external adapters (thin, env-auth). Plus a **fixture** JSON-RPC trigger for tests. | Goal narrative needs both; fixture covers restart/backoff without network. |
| S3 vs Azure | **S3 only** this phase (JSON-RPC storage adapter). Azure Blob follow-on after pattern proven. | Draft out-of-scope; overrides broader Phase 4 bullet in §14. |
| WASM adapters | **Still deferred.** | Spec §4.6. |
| JSON-RPC restart | Supervised client: on process death, exponential backoff restart (cap e.g. 60s), re-`initialize`, fail in-flight calls; optional max-restart window then permanent error + notify. | Deferred from cleanup; needed for long-running triggers. |
| JSON-RPC notifications | Client demux: responses (with `id`) → pending map; notifications (`id` absent) → registered handler channel. | Required for trigger feed events. |
| Diff drawer | Prefer `git diff base...HEAD` in workspace when git-managed; fall back to source-vs-workspace file walk. | Phase 3 diff remains useful for non-git projects. |

---

## Sequencing (seven session-sized parts)

Dependency order (do not rearrange casually):

```text
A JSON-RPC client completion
    ↓
B Git workspace model ──────────────┐
    ↓                               │
C run_test (container)              │
    ↓                               │
D Agent personality config          │
    ↓                               │
E MCP client + per-agent allowlists │
    ↓                               │
F TriggerPort + webhook + GH/Jira + untrusted defaults
    ↓
G S3 storage adapter
```

| Part | Workstream | Depends on | Notes |
|------|------------|------------|-------|
| **A** | F (JSON-RPC) | — | Unblocks durable external adapters + trigger streams. |
| **B** | A (Git) | — | Independent of A; can parallelize with A in one long session. |
| **C** | B (`run_test`) | B | Needs real workspace path; isolation is acceptance criterion. |
| **D** | E (agent config) | — | Soft-depends on existing config; lands before MCP so allowlists have a home. |
| **E** | C (MCP) | A (process patterns optional), D | **Must complete before or with F** (§5.6). |
| **F** | D (Triggers) | A, E | External triggers without MCP allowlists are out of policy. |
| **G** | E (S3) | A | First real non-FS StoragePort consumer; slash-canonical keys already exist. |

Parts **A** and **B** may run in parallel. **C** after **B**. **D** can overlap **B/C**. **E** after **D**. **F** after **A+E**. **G** after **A**, optionally parallel with **C–F**.

---

## Part A — JSON-RPC client completion

### Behavior

1. **Supervised lifecycle** wrapping `adapters.Client`:
   - Spawn + `initialize` (existing).
   - On unexpected exit: exponential backoff restart (e.g. 1s → 2s → 4s … cap 60s).
   - In-flight `Call`s fail with a typed error; new calls wait for reconnect or fail if supervisor is stopped.
   - After prolonged failure (configurable window, default 10m), mark adapter dead, log, optional console notify — do not spin forever silently.
2. **Notification demux** in `readLoop`:
   - Message with `id` → response path (existing).
   - Message without `id` (or notification shape) → fan-out to `Subscribe(method) <-chan Notification` or a single `Notifications() <-chan`.
3. **Env passthrough** when spawning: optional `Cmd.Env` from adapter config so credentials stay in the child (Q12).
4. Wire notification adapters (Slack/email) through the supervisor so a crashed Slack adapter restarts instead of killing the daemon path.

### Files

| Path | Change |
|------|--------|
| `internal/adapters/jsonrpc.go` | Demux notifications; optional env; extract dial/spawn |
| `internal/adapters/supervisor.go` | **New** — restart/backoff supervisor |
| `internal/adapters/jsonrpc_test.go` | Kill child → restart; notification delivery |
| `internal/notify/notify.go` | Use supervisor for adapter sinks |

### Tests

- Fixture adapter exits → restarted; subsequent `Call` succeeds.
- Fixture sends JSON-RPC notification → received on channel within timeout.
- Backoff does not tight-loop (assert minimum delay under test with injectable clock if needed).

---

## Part B — Git workspace model

### Project config (in `projects.config_json` and/or global YAML project registry)

```yaml
# Illustrative project-level git block (stored in projects.config_json or future projects.yaml)
git:
  repo_url: "git@github.com:myorg/auth-service.git"
  base_branch: "main"
  push: false
  create_pr: false
  auth:
    type: ssh_key          # ssh_key | token | gh_cli
    ssh_key_path: ""       # for ssh_key; empty → agent default keys
    # token_env: GORCH_GIT_TOKEN
    # gh_profile: work
```

- Validate at project registration / first use: `git` on PATH; for `gh_cli`/`create_pr`, `gh` on PATH; `repo_url` + `base_branch` non-empty.
- **Do not** clone mid-agent-turn; clone/fetch is orchestrator-side only.

### Layout

```text
{storage_root}/
  repos/{project_id}.git          ← bare clone cache
  projects/{pid}/issues/{iid}/
    source/                       ← worktree @ base_branch (read-only for agents)
    implementation/
      workspace/                  ← worktree @ ai-implementer/{iid}-{run_id}
```

### Lifecycle (orchestrator-managed; agent never runs git)

1. **EnsureCache:** `git clone --bare` if missing; else `git fetch --all --prune`.
2. **Issue create (git projects):** create/update `source/` worktree at `base_branch` (replace `snapshotSource` copy). Non-git projects keep existing snapshot.
3. **Implementer attempt start:** remove prior workspace worktree if present; `git worktree add -b ai-implementer/{issue_id}-{run_id} …` from cache at `base_branch`.
4. **Post-implementer success:** `git add -A`; single commit with structured message (author: configurable `git.author_name` / `git.author_email` defaults `gorchestrator` / `gorchestrator@localhost`); if `push`, push branch; if `create_pr`, `gh pr create`.
5. **Retry / crash re-run:** new `run` row → new branch name → fresh worktree (do not reuse dirty failed workspace).
6. **SQLite:** `runs.workspace_id`, `runs.branch_name` (migration 6).

### Files

| Path | Change |
|------|--------|
| `internal/git/` | **New** package: cache, worktree, commit, push, pr helpers via `os/exec` |
| `internal/orchestrator/engine.go` | Replace/branch `snapshotSource` / `seedWorkspace`; post-phase commit |
| `internal/sqlite/db.go` | Migration 6: `workspace_id`, `branch_name` on `runs` |
| `internal/sqlite/runs.go` | Persist new columns |
| `internal/storage/paths.go` | Optional `RepoCachePath(projectID)`; keep `SourcePath` / `WorkspacePath` |
| `internal/server/drawer.go` | Git-aware diff when `.git` / worktree present |
| `internal/config/config.go` | Global git author defaults if useful; project git stays in `config_json` |

### Tests

- Fixture bare remote (local) → clone cache → issue → source worktree has files; implementer workspace is separate branch; commit exists after phase.
- Parallel-ish: two run IDs → two branch names, no worktree collision.
- Symlink escape still rejected by existing path tools (regression).
- Non-git `source_path` path unchanged.

---

## Part C — `run_test` tool (container sandbox)

### Config

```yaml
# Per-project (config_json) or documented in config.example
test:
  command: "go test ./..."
  timeout: 60s
  image: "golang:1.22"       # required if command set
  cpu: "1"
  memory: "512m"
  secrets_env: []            # host env names injected into container
  # runtime: auto | docker | podman
```

### Tool behavior

- Implementer-only; immutable command from project config (agent cannot change argv).
- Resolve runtime; if none → tool error string (not panic): `run_test unavailable: no container runtime (docker/podman)`.
- Run approximately:
  ```text
  docker run --rm --network=none \
    --cpus=… --memory=… \
    -v {absWorkspace}:/workspace:rw -w /workspace \
    -e SECRET=… (from secrets_env) \
    {image} sh -c '{test.command}'
  ```
- Enforce wall-clock timeout; kill container on expiry.
- Cap stdout/stderr; return combined summary to the model.
- Record tool call in `events.jsonl` **without** secret values.

### Files

| Path | Change |
|------|--------|
| `internal/tools/run_test.go` | **New** tool |
| `internal/tools/tools.go` | Add to `NewImplementerRegistry` |
| `internal/tools/bound.go` or `BoundTools` | Add test config + abs workspace host path |
| `internal/orchestrator/engine.go` | Pass test config + host workspace path into BoundTools |
| `internal/config/config.go` | Types for test block if global; project-level via config_json parsers |

### Tests

- No runtime → clear refusal.
- Fake/`docker` shim or integration tag: timeout kill; network isolation assertion (optional best-effort); secrets not in events/task.json.
- Scripted failing-then-passing test in workspace → implementer loop can observe both (integration / dry-run hybrid as practical).

---

## Part D — Agent personality config

### Schema extensions (`config.AgentConfig`)

| Field | Meaning |
|-------|---------|
| `temperature` | float; passed into model request where provider supports it |
| `max_tokens` | completion cap hook |
| `system_prompt` | full override (existing) |
| `system_prompt_append` | appended after base/default prompt |
| `tools` | optional allowlist of **core** tool names for this agent; empty = all default for type |
| `mcp_servers` | list of MCP server names (Part E) |
| `token_budget` | int; persisted in task.json for visibility; **not enforced** until Phase 5 |

### Merge order

1. Built-in defaults (`defaultAgentConfig`)
2. Global `default_model` / tools
3. `agents.<name>` in YAML
4. Per-issue overrides from `issues` metadata / submit payload / `projects.config_json` agent map (minimal surface: adjudicator + model + mcp_servers)

Wire temperature/max_tokens into LLM request builders (`internal/llm/*`) where the API allows; no-op with log if provider ignores.

### Files

| Path | Change |
|------|--------|
| `internal/config/config.go` | Fields + merge |
| `internal/orchestrator/engine.go` | `buildTask`, registry filtering by `tools` |
| `internal/llm/*.go` | Plumb temperature / max_tokens |
| `configs/config.example.yaml` | Document full agent block |

### Tests

- Override system prompt append; tool subset drops `grep_search` from registry; temperature present in outbound request (httptest fake).

---

## Part E — MCP client + per-agent server allowlists

### Config

```yaml
mcp_servers:
  - name: internal-api
    command: ["my-mcp-server"]
    args: []
    env: []                    # env var NAMES to pass through from host (values not logged)
    # transport: stdio         # only stdio this phase

agents:
  implementer:
    mcp_servers: [internal-api]
  researcher:
    mcp_servers: []            # deny
```

### Behavior

1. At engine/daemon start (or first use): for each configured server, spawn stdio MCP client via official Go SDK.
2. When building an agent registry: core tools ± subset, **plus** tools from servers listed in that agent's `mcp_servers`.
3. Tool names: prefix with server name to avoid collisions (`internal-api__tool_name`) in the ADK registry; strip/map on call.
4. Every MCP tool call → `events.jsonl` like core tools (size-capped result).
5. Supervisor-style restart for MCP processes (reuse Part A patterns where practical; MCP is not JSON-RPC-adapter but same process-care).

### Files

| Path | Change |
|------|--------|
| `internal/mcp/` | **New** — client manager, allowlist filter, ADK tool wrappers |
| `internal/config/config.go` | `mcp_servers` + agent field |
| `internal/orchestrator/engine.go` | Attach MCP tools per agent |
| `go.mod` | `github.com/modelcontextprotocol/go-sdk` |

### Tests

- Fixture MCP server with one tool; allowlisted agent sees it; non-allowlisted does not.
- Call recorded in `events.jsonl`.
- Deny-by-default: empty config → no MCP tools.

---

## Part F — TriggerPort + external triggers

### Port

```go
// internal/trigger/port.go
type Submission struct {
    Project   string
    Title     string
    Body      string // optional issue text artifact
    Source    string // "manual" | "webhook" | "github" | "jira" | ...
    ExternalID string
    Metadata  map[string]string
}

type Port interface {
    // Name of the trigger source.
    Name() string
    // Start emits submissions until ctx is cancelled.
    Start(ctx context.Context, out chan<- Submission) error
}
```

Daemon wires: for each submission → `SubmitIssue` with source tag; apply untrusted defaults (implementer adjudicator → human unless trusted).

### Built-in webhook

| Method | Path | Auth | Body |
|--------|------|------|------|
| `POST` | `/hooks/issues` | shared token | `{ "project", "title", "body?" }` → `202` + issue id |

- Registered **outside** session-cookie auth; uses trigger token.
- Rate-limit lightly (optional simple in-memory throttle) — nice-to-have, not blocker.

### External adapters

| Adapter | Port | Notes |
|---------|------|-------|
| `adapters/github/` + yaml | `trigger` | Listen via API polling or webhook relay over JSON-RPC notifications; auth via `GITHUB_TOKEN` |
| `adapters/jira/` + yaml | `trigger` | Same pattern; `JIRA_*` env |
| Fixture in tests | `trigger` | Emits one issue then heartbeats; kill → supervisor restarts |

JSON-RPC methods (illustrative):

- `initialize` → `{port: "trigger"}`
- Notifications: `trigger.issue` with submission payload
- Optional requests: `trigger.health`

### Untrusted posture

- `Submission.Source != "manual" && !project.TrustExternal` → issue flags `external=true`; engine forces implementer boundary `adjudicator=human`.
- Surface on dashboard card: chip "external" / "human gate required".

### Files

| Path | Change |
|------|--------|
| `internal/trigger/` | **New** port, webhook helper, adapter sink |
| `internal/daemon/daemon.go` | Start trigger listeners |
| `internal/server/server.go` | `/hooks/issues` |
| `internal/orchestrator/service.go` | `SubmitIssue` accepts source + trust rules |
| `adapters/github/`, `adapters/jira/`, manifests | **New** |
| `internal/sqlite/db.go` | Optional: `issues.source`, `issues.external_id` (migration 7) |

### Tests

- Webhook with bad token → 401; good token → queued issue.
- Fixture trigger notification → issue created; adapter kill → restart (Part A).
- External issue → implementer adjudicator is human even if YAML says `self`.

---

## Part G — S3 storage adapter

### Scope

- External JSON-RPC adapter implementing StoragePort methods: `storage.read`, `storage.write`, `storage.list`, `storage.exists`, `storage.mkdir`.
- Keys: **slash-canonical** relative paths as already defined (`internal/storage` port keys); adapter maps to `s3://bucket/{prefix}/{key}`.
- Core selects storage backend via config:

```yaml
storage:
  backend: fs              # fs | adapter
  # adapter: s3            # name from adapters: list with port: storage
```

Default remains FS. When adapter selected, `NewEngine` constructs a `storage.Port` that RPC-forwards (thin client wrapper), not only FS.

### Auth

- Adapter process env: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `S3_BUCKET`, `S3_PREFIX`, optional `S3_ENDPOINT` (MinIO).

### Files

| Path | Change |
|------|--------|
| `adapters/s3/main.go`, `adapters/s3.yaml` | **New** |
| `internal/storage/rpc.go` | **New** — Port over JSON-RPC client |
| `internal/config/config.go` | `storage.backend` |
| `internal/orchestrator/engine.go` | Construct Port from config |

### Tests

- Round-trip against MinIO or `httptest` fake S3; keys identical on Linux path assumptions (forward-slash keys only).
- Existing FS tests remain default path.

---

## Package layout (additions summary)

```text
adapters/
  github/     main.go + github.yaml
  jira/       main.go + jira.yaml
  s3/         main.go + s3.yaml
internal/
  adapters/   supervisor.go; jsonrpc notification demux
  git/        cache, worktree, commit, push
  mcp/        client manager + ADK wrappers
  trigger/    port, webhook, adapter bridge
  tools/      run_test.go
  storage/    rpc.go
  config/     agent/mcp/test/git/trigger/storage fields
  sqlite/     migrations 6–7; runs + issues columns
  orchestrator/  git lifecycle, submit source, MCP wiring
  server/     /hooks/issues; drawer git diff
  daemon/     start triggers + supervised adapters
```

---

## Schema migrations

**Migration 6 — run workspace tracking**

```sql
ALTER TABLE runs ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN branch_name TEXT NOT NULL DEFAULT '';
```

**Migration 7 — issue provenance**

```sql
ALTER TABLE issues ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE issues ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_issues_external ON issues(source, external_id);
```

---

## Config surface (canonical example additions)

```yaml
storage:
  backend: fs                # fs | adapter
  # adapter_name: s3

mcp_servers: []
# - name: internal-api
#   command: ["./bin/my-mcp"]
#   env: [API_TOKEN]

triggers:
  webhook:
    enabled: true
    token_env: GORCH_WEBHOOK_TOKEN
  adapters: []               # names from adapters: with port: trigger
  trust_external: false      # if true, skip forced human implementer gate

# agents: ... extended fields (temperature, tools, mcp_servers, …)
# adapters: ... existing registry; add github/jira/s3 manifests
```

Project-level (`projects.config_json`) holds `git`, `test`, optional per-project agent overrides and `trust_external`.

---

## End-to-end tests (phase success)

| Scenario | Expectation |
|----------|-------------|
| Webhook issue → pipeline on fixture git repo | Branch + single commit exists; push mocked or local bare remote |
| `run_test` | Refusal without runtime; timeout; no secrets in artifacts |
| MCP | Allowlist enforced; events recorded |
| Trigger adapter | Emit issue; kill → backoff restart |
| S3 | Round-trip MinIO/fake; slash keys stable |
| External default gate | Implementer waits on human |
| Non-git project | Snapshot path still works; no git required |

---

## Success criteria

1. Jira/GitHub (or webhook) issue → reviewed branch, hands-free except configured gates.
2. Implementer completes a test-and-fix loop inside the container sandbox.
3. An internal API is used by an agent via MCP under a per-agent server allowlist.
4. Externally-triggered issues stop at a human gate before implementation by default.
5. JSON-RPC adapters survive child death via supervised restart.
6. Optional S3 backend stores artifacts with portable keys.

## Out of scope

- Token budget **enforcement**, effort estimation, scope detection → Phase 5.
- MCP per-**tool** granularity → Phase 5.
- Azure Blob → follow-on.
- Workspace retention policies → Phase 6.
- Semantic/vector search (Q3) → later.
- WASM adapters → deferred.
- Multi-host / multi-daemon clone cache coordination.

---

## Spec updates (on implementation / plan freeze)

1. Close §17 Q9 and Q12 as soft/hard decisions in the Decisions Log.
2. Phase 4 bullet: storage adapters **S3** (Azure deferred).
3. §6.4: document worktree + bare clone cache; branch naming `ai-implementer/{issue_id}-{run_id}`; note §6.6 interim replaced when git configured.
4. §12.2: Phase 4 per-agent server allowlists (not MVP “all tools to all agents”).
5. Revision log entry for Phase 4 plan solidification date.

---

## Critical existing code to reuse

| Piece | Path |
|-------|------|
| Engine pipeline / allowlists / seedWorkspace | `internal/orchestrator/engine.go` |
| SubmitIssue / Decide / queue | `internal/orchestrator/service.go` |
| Path helpers (slash-canonical) | `internal/storage/paths.go`, `port.go`, `fs.go` |
| JSON-RPC client + manifests | `internal/adapters/jsonrpc.go`, `manifest.go` |
| Notification adapter pattern (env auth) | `internal/notify/notify.go`, `adapters/slack/`, `adapters/email/` |
| Agent config merge | `internal/config/config.go` (`Agent`, `AgentConfig`) |
| Implementer registry | `internal/tools/tools.go` |
| Path containment / symlinks | `internal/tools/paths.go`, write/read tools |
| Daemon worker pool | `internal/daemon/daemon.go` |
| Runs / projects repos | `internal/sqlite/runs.go`, `projects.go`, `db.go` migrations |
| Diff drawer | `internal/server/drawer.go` |

---

## Verification

```bash
go test ./...
# targeted as parts land:
go test ./internal/adapters/ ./internal/git/ ./internal/tools/ ./internal/mcp/ ./internal/trigger/ ./internal/storage/ ./internal/orchestrator/ ./internal/daemon/
```

Integration: local bare git remote + optional Docker + MinIO for full Part G. CI without Docker should still pass unit tests (container tests skip or use build tags `integration`).

---

## Implementation note

After approval, replace the draft body of repo `phase_4.md` with this plan (status → ready / in progress), and apply the listed `spec.md` decision closures when the phase starts or completes.
