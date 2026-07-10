# AI Agent Orchestration System — Product Specification

> **Session Date:** 2026-07-04 (original planning) · **Last Revised:** 2026-07-10 (project registry + agent flavors)  
> **Status:** Living document — single source of truth  
> **Purpose:** This document captures all architectural decisions, constraints, and phase definitions. Future implementation sessions should reference this document as the single source of truth.
>
> **Documentation convention:** This spec is the only *living* design document — it is updated whenever a decision changes. Phase plan documents (`phase_*.md`) are frozen once their phase completes and serve as historical changelogs. Each phase markdown has a companion `phase_*.html` rendering for human consumption, regenerated whenever its markdown changes materially. **Markdown is canonical** — on any conflict, the `.md` wins. `spec_summary.html` remains a frozen snapshot of the original planning session.

### Revision Log

| Date | Change |
|------|--------|
| 2026-07-04 | Initial spec from planning session. |
| 2026-07-08 | Post-review revision. Recorded the ADK-native LLM integration decision (supersedes LLMProviderPort); unified handoff modes and adjudicators into a single axis (§9); specified crash-recovery semantics (§9.4); added per-phase `events.jsonl` transcripts and attempt versioning with adjudication feedback (§7); moved read-only project source access into Phase 2 (§6.6); named daemonization as Phase 3's first workstream (§11.0); hardened `run_test` sandbox requirements (§6.5); added untrusted-input/prompt-injection section (§5.6); switched adapter discovery to an explicit registry (§4.3); inserted a "Phase 2 Project Cleanup" phase (§14); converted resolved open questions into soft decisions (§17). |
| 2026-07-09 | Phase 3 plan solidification. Closed §17 Q5 (notifications: console + Slack webhook + SMTP email) and Q6 (OIDC-only MVP; SAML deferred). Recorded local auth mode for dev/test, issue-row queue model, and HTMX/SSE promoted hard for Phase 3. |
| 2026-07-09 | Dashboard UX: vertical expandable status-tinted issue cards (not kanban); dark-only theme (greys/blues + neon pink); multi-expand; adjudication on expanded card; artifact slide-out drawer; submit from top-bar drawer. See §11.5. |
| 2026-07-09 | Phase 3 complete: `serve` daemon, HTMX dashboard, local+OIDC auth, SSE, notifications (console/Slack/email), human retry on failed/cancelled, OpenAI-compatible tool schema normalization for llama.cpp-class servers. |
| 2026-07-09 | Phase 4 plan solidification. Closed §17 Q9 (agent config merge layers) and Q12 (adapters own credentials via env). Git worktree model; S3-only storage this phase (Azure deferred); MCP per-agent server allowlists with external triggers; seven session-sized parts A–G. See `phase_4.md`. |
| 2026-07-10 | **Project registry + agent flavors.** Projects are YAML-declared only (no create-on-submit/CLI). `source_path` / git / test / agent flavors live under `projects.<name>` in config. Named agent **flavors** of the three core types; submit drawer selects a flavor per stage when a type has more than one. Per-issue cast frozen at submit. Project membership / invites deferred. See §6.0, §8.2, §11.5, §17 Q9/Q16; plan in `phase_4_project_refactor.md`. |

---

## 1. Product Vision

A **tight human + AI agent collaboration platform** for software engineering teams. The system manages and coordinates AI agents through a structured pipeline of requirements collection, research, implementation planning, and actual implementation.

This is **not** a generic "solve everything" agent orchestrator. It is specifically designed for business/team problems that can be solved by software. The product is extensible and plugin-able, but opinionated about its core workflow.

---

## 2. Core Philosophy

- **Human-in-the-loop is first-class.** Humans observe, configure, and intervene. The system is transparent, not autonomous.
- **Agent casting matters.** The human's job is to min/max which LLM plays which role (e.g., deep research model vs. fast planner vs. competent coder).
- **Filesystem is the agent's memory.** Agents share data through filesystem artifacts, not through shared memory or session state.
- **Go concurrency is a feature.** Goroutines, in-process execution, SQLite, and filesystem storage are sufficient for the target scale (hundreds of users, not thousands).
- **Crash resilience by design.** If the process crashes mid-execution, the orchestrator recovers from filesystem state on restart. Because agent reasoning state is ephemeral (in-memory ADK sessions), recovery means re-running the interrupted phase from its `task.json` — see §9.4 for the precise state machine.
- **Adapters are external processes.** The core binary is static; extensibility is achieved through JSON-RPC over stdio with external adapter binaries. Common adapters are built-in.
- **Secure by default.** Core tools are architecturally scoped by agent type. Agents do not get raw filesystem, shell, or git access.

---

## 3. Architecture Overview

### 3.1 High-Level Pipeline

```
Trigger → Researcher → [Adjudicate] → Planner → [Adjudicate] → Implementer → Done
```

Each handoff boundary is configurable. Adjudication is optional and pluggable.

### 3.2 Component Boundaries

| Layer | Responsibility | Technology |
|-------|---------------|------------|
| **Orchestrator** | Pipeline state machine, handoff logic, goroutine lifecycle, git workspace management. Built as an embeddable engine: the one-shot CLI (Phases 1–2) and the daemon (Phase 3+) are thin front-ends over the same core. | Custom Go |
| **Agent Runtime** | Single-agent reasoning, tool calling, LLM interaction | Google ADK Go |
| **Storage (App State)** | Projects, issues, users, run history, audit log | SQLite |
| **Storage (Agent Memory)** | Agent outputs, shared artifacts, free-form content | Filesystem (hexagonal port) |
| **LLM Provider** | Model-agnostic LLM API access | ADK `model.LLM` implementations, in-process (see §3.3 note — no longer a JSON-RPC port) |
| **Tools** | Core capabilities + custom extensions | Native Go tools + MCP adapter port |
| **Triggers** | How work enters the system | Hexagonal port with multiple adapters |
| **Adjudication** | Quality gates between phases | Hexagonal port (null / self / human) |
| **Dashboard** | Human visibility and intervention | Go HTTP server + lightweight frontend |

### 3.3 Hexagonal Ports (Defined)

1. **StoragePort** — Filesystem operations (read, write, list; `watch` is deferred and not in the current interface)
   - Port paths are canonical forward-slash relative keys; each adapter translates to its native form (OS separators, object keys). This keeps S3/Azure keys portable across host platforms.
   - Built-in adapters: OS filesystem (default)
   - External adapters: S3, Azure Blob (via JSON-RPC stdio)
2. **LLM Integration** — *superseded as a port (2026-07-08).* Phase 2 adopted tight integration with ADK's `model.LLM` interface and deleted the custom LLMProviderPort. LLM extensibility now means:
   - Built-in `model.LLM` implementations: OpenAI, Anthropic, Gemini
   - OpenAI-compatible HTTP endpoints (llama.cpp, Ollama, enterprise gateways) via the OpenAI implementation with a custom `base_url`
   - Exotic providers implement `model.LLM` in-process — **not** JSON-RPC stdio adapters
3. **TriggerPort** — How issues enter the system
   - Built-in adapters: Manual CLI, webhook
   - External adapters: GitHub Issues, Jira, email, Slack/Teams bot (via JSON-RPC stdio)
4. **AdjudicatorPort** — Quality gate between phases
   - Built-in adapters: NullAdjudicator, SelfAdjudicator, HumanAdjudicator
   - External adapters: Custom AI reviewer (via JSON-RPC stdio)
5. **MCPAdapterPort** — Custom tool integration via Model Context Protocol
   - Native MCP protocol over stdio (no custom wrapper needed)
6. **SSOAdapterPort** — Authentication
   - Built-in adapter: OIDC
   - External adapters: SAML (via JSON-RPC stdio)
7. **NotificationAdapterPort** — Human alerts
   - Built-in adapters: Console logging
   - External adapters: Email (SMTP), Slack webhook (via JSON-RPC stdio)

---

## 4. Adapter Implementation Model

### 4.1 Philosophy

Go is a compiled, statically-linked language. Runtime plugin loading via `plugin.Open()` is effectively dead (Linux-only, fragile version matching, poorly maintained). The core binary should remain a single static executable.

**Extensibility is achieved through external process adapters**, not compile-time plugins or dynamic linking. This is the Unix philosophy applied to Go: small, composable, purpose-built binaries that interoperate via a well-defined protocol.

### 4.2 Two Adapter Categories

| Category | Description | Examples |
|----------|-------------|----------|
| **Built-in** | Compiled directly into the core binary. Zero external dependencies. Covers the 80% use case. | OS filesystem storage, OpenAI/Anthropic LLM, webhook trigger, null/self adjudicator, OIDC auth, console logging |
| **External Process** | Standalone binary discovered at runtime. Communicates with core via JSON-RPC over stdio. | S3 storage, Jira trigger, email notification, SAML auth, custom LLM gateway |

### 4.3 External Process Contract: JSON-RPC over Stdio

**Protocol:** JSON-RPC 2.0 over stdin/stdout (line-delimited JSON, aka JSON Lines).

**Lifecycle:**
1. Adapters are **declared explicitly in configuration** (name + manifest path). The core does not blind-scan a directory and spawn whatever executables it finds — running unlisted binaries is a supply-chain risk. *(Revised 2026-07-08; supersedes directory scanning.)*
2. Core spawns the adapter as a child process, holding stdin/stdout pipes open
3. Core sends `initialize` method with the port's expected interface schema
4. All subsequent calls are JSON-RPC request/response pairs
5. If the adapter process dies, core restarts it with exponential backoff
6. For streaming (LLM tokens), adapter sends JSON-RPC `notification` messages

**Adapter Manifest:** Each adapter binary is accompanied by a YAML manifest:

```yaml
name: s3
version: "1.0.0"
protocol: jsonrpc-stdio
port: storage
binary: ./gorchestrator-adapter-s3   # explicit path, relative to the manifest; must be a regular executable file
capabilities: [read, write, list]
```

The `binary` field is explicit — the binary path is not inferred from `name`, and the core verifies it is a regular executable file (not a directory) before spawning.

**Example Request/Response:**

```json
// Core → Adapter
{"jsonrpc":"2.0","method":"storage.read","params":{"path":"research/output.md"},"id":1}

// Adapter → Core
{"jsonrpc":"2.0","result":{"content":"...","exists":true,"size":1234},"id":1}
```

### 4.4 Serialization

JSON serialization is handled by Go's standard library `encoding/json` or `github.com/goccy/go-json` for improved performance. The overhead of JSON-RPC stdio is acceptable for the target scale (hundreds of users, not thousands). If performance becomes a bottleneck in the future, WASM adapters may be introduced as an additional adapter category.

### 4.5 Port-Specific Adapter Strategy

| Port | Built-in Adapters | External Adapters | Rationale |
|------|-------------------|-------------------|-----------|
| **StoragePort** | OS filesystem | S3, Azure Blob | Storage is high-frequency; external process overhead is acceptable for MVP. Built-in filesystem covers 80% of use cases. |
| **LLM (`model.LLM`)** | OpenAI, Anthropic, Gemini; OpenAI-compatible endpoints for local/gateways | — (in-process `model.LLM` implementations only) | Superseded as a JSON-RPC port 2026-07-08; see §3.3. Common providers are built-in for zero-config setup. |
| **TriggerPort** | Manual CLI, HTTP webhook | GitHub Issues, Jira, email IMAP, Slack bot | Triggers are naturally long-running listeners. External process is a good fit. |
| **AdjudicatorPort** | Null, Self, Human | Custom AI reviewer | Core adjudication modes are built-in. Custom reviewers can be external. |
| **MCPAdapterPort** | — | Any MCP server | MCP is natively an external process protocol (stdio or HTTP). Core implements MCP client. |
| **NotificationAdapterPort** | Console logging | Email (SMTP), Slack webhook, Teams | Low-frequency, naturally external. |
| **SSOAdapterPort** | OIDC | SAML | OIDC is standard HTTP flow, built-in. SAML is complex enough to warrant external process. |

### 4.6 Future: WebAssembly (WASM) Adapters

WASM adapters (via `wazero` or similar) are a candidate for future phases if external process overhead becomes a bottleneck. This would introduce a third adapter category:

- **WASM adapters:** Sandboxed, near-native performance, single-binary deployment (core + `.wasm` files)
- **Use case:** High-frequency ports like StoragePort where process spawn overhead is measurable

**Deferred to:** Phase 4 or beyond. Not in MVP scope.

---

## 5. Security Model: Secure by Default

### 5.1 Philosophy

Security is not a permissions layer bolted on top — it is the architecture itself. Agents do not get raw filesystem, shell, or git access. They receive orchestrator-mediated tools that are scoped by design. Each agent type receives a **different tool binding**, not the same tools with different permissions.

### 5.2 Core Tool Matrix (Per Agent Type)

| Tool | Researcher | Planner | Implementer | Description |
|------|-----------|---------|-------------|-------------|
| `read_file` | ✅ | ✅ | ✅ | Two modes: whole-file (subject to a configurable cap) or surgical line-range read. Path resolved by orchestrator against allowlist. See §12.1. |
| `list_directory` | ✅ | ✅ | ✅ | List contents of a directory. |
| `grep_search` | ✅ | ✅ | ✅ | Search file contents via pattern matching. |
| `write_output` | ✅ | ✅ | ❌ | Write to the agent's designated `output.*` file in the issue directory. Orchestrator resolves path; agent does not know the filesystem layout. |
| `write_file` | ❌ | ❌ | ✅ | Write a file within the implementer's workspace. Path is workspace-relative; orchestrator resolves to absolute path. |
| `update_file` | ❌ | ❌ | ✅ | Update (patch/overwrite) a file within the implementer's workspace. |
| `run_test` | ❌ | ❌ | ✅ | Execute the project's pre-configured test command in a sandboxed subprocess with timeout. |
| `bash` / `shell_exec` | ❌ | ❌ | ❌ | **Not available to any core agent.** |

**Key principle:** Researcher and Planner never write files directly. They use `write_output`, which is an orchestrator-provided tool that **only** writes to the current agent's `output.*` path in the issue directory. The agent does not know the path; the orchestrator resolves it. This is not a "permission" — it is a completely different tool with a single hardcoded destination.

### 5.3 No Shell Access for Core Agents

Core agents do not have `bash` or `shell_exec` tools. All operations are mediated through typed tool calls. The only exception is `run_test`, which executes a **pre-configured, immutable command** defined in project configuration. The agent sees stdout/stderr output but cannot modify the command.

Note that command immutability alone is *not* a security boundary — the command executes code the implementer just wrote. See §6.5 for why `run_test` must be treated as arbitrary code execution and container-isolated accordingly.

### 5.4 Path Resolution & Allowlist

The orchestrator maintains an allowlist of readable paths for each agent run:
- **Researcher/Planner:** the issue's read-only source snapshot (§6.6), the issue's agent output directories (previous phases), and any paths explicitly provided in `task.json`.
- **Implementer:** its workspace directory, the source snapshot, and the issue's previous agent output directories.

Source access matters: without it, the researcher has nothing to research and produces hallucinated findings. It is part of Phase 2 (§6.6), not deferred to the Phase 4 git model.

All tool paths provided by the agent are resolved by the orchestrator against this allowlist. Attempts to access paths outside the allowlist are rejected by the orchestrator before reaching the StoragePort.

**Containment rules (revised 2026-07-08):**
- Prefix checks are separator-aware: an allowed root of `/data/gorch` must not admit `/data/gorch-evil`.
- Symlinks are resolved (`EvalSymlinks`) before the containment check; a symlink that escapes the allowed roots is rejected. This matters once workspaces are copies of real repositories, which routinely contain symlinks.

### 5.5 MCP Permission Model

MCP servers are the real permission surface because they expose arbitrary capabilities (database access, API calls, internal service invocations).

**MVP Model:**
- MCP servers are **project-level configuration**
- All tools from an enabled MCP server are available to all agents
- The human maintainer decides which MCP servers to connect
- **Default: deny** — no MCP servers are connected unless explicitly configured

**Phase 4 (must land *with* external triggers, not after):**
- Per-agent, per-**server** allowlists — an agent only sees tools from MCP servers explicitly granted to it

**Phase 5:**
- Per-agent, per-**tool** allowlists
- Tool-level permission granularity (e.g., `query_database` only allows `SELECT`)
- Endpoint restrictions for API tools

The MVP "all tools to all agents" posture is acceptable only while all pipeline input is human-authored. It must tighten before external triggers (GitHub/Jira) land — see §5.6.

### 5.6 Untrusted Input & Prompt Injection

The pipeline consumes untrusted text: issue titles/bodies arriving via external triggers (GitHub, Jira, email) and the contents of repository files the agents read. Any of it can contain instructions crafted to manipulate an agent. Combined with capable tools — especially MCP servers exposing database or API access — this is an exfiltration and abuse vector.

Mitigations, by architecture and by phase:
- Core tools are scoped by construction (§5.2) — a hijacked researcher can still only write to its own output file.
- Adjudication gates are the primary human backstop. Human review before implementation is the recommended posture for externally-triggered issues; Phase 4 makes this the default for external trigger sources.
- Per-agent MCP server allowlists ship in Phase 4 alongside external triggers — not after.
- `run_test` container isolation (§6.5) bounds what a hijacked implementer can execute.
- Secrets are never rendered into agent-visible artifacts (`task.json` is agent-readable; see §17 Q15).

This does not make the system injection-proof — no LLM pipeline is. The spec's position: scope every capability so the blast radius of a hijacked agent is the smallest the workflow allows.

---

## 6. Project Registry, Configuration & Git

### 6.0 Project Registry (YAML Source of Truth)

*(Added 2026-07-10. Closes the accidental "type a project name at issue create" model.)*

**YAML is the sole configuration surface for projects.** There is no admin config GUI in this phase; a future admin UI may edit the same YAML-backed model, but until then maintainers edit `config.yaml` (or an included projects file) and restart/reload the process.

#### 6.0.1 Lifecycle

1. Maintainer declares zero or more projects under the top-level `projects:` map in YAML.
2. On process start (daemon `serve` and CLI engine init), the orchestrator **upserts** the SQLite project registry from that map: create missing rows by name, refresh `config_json` from YAML for existing names. SQLite holds a **runtime index** (stable `project_id`, timestamps, synced config blob) — it is not an editor.
3. Issue submission (dashboard, API, CLI, webhook, external triggers) **resolves a project by name** against the registry. Unknown name → **hard error**. There is no `GetOrCreate` on user paths.
4. Projects are **never** created by free-text issue input, CLI flags alone, or trigger payloads. Registration is always YAML → sync.

#### 6.0.2 What belongs in project YAML

| Block | Purpose |
|-------|---------|
| `source_path` | Local source directory for snapshot-mode projects (§6.6) |
| `git` | Remote repo, base branch, auth mode, push/PR flags (§6.3) |
| `test` | Immutable `run_test` command, image, limits, secrets env names (§6.5) |
| `agents` | Named **flavors** of core agent types + defaults (§8.2) |
| `trust_external` | Optional override for untrusted-input implementer gate (§5.6) |

`source_path` is **not** collected on the submit form. When it returns to a UI, that UI is admin-only config — not member issue creation.

#### 6.0.3 Illustrative project map

```yaml
projects:
  auth-service:
    source_path: ""              # omit or empty when using git
    git:
      repo_url: "git@github.com:myorg/auth-service.git"
      base_branch: "develop"
      push: false
      create_pr: false
      auth:
        type: ssh_key            # ssh_key | token | gh_cli
        ssh_key_path: "/secrets/deploy_key"
    test:
      command: "go test ./..."
      timeout: 60s
      image: "golang:1.22"
      secrets_env: []
    agents:
      researcher:
        default: thorough
        flavors:
          thorough:
            model: { provider: openai, model: o3-mini }
            system_prompt_append: "Prefer root-cause depth over speed."
          cheap:
            model: { provider: openai, model: gpt-4o-mini }
      planner:
        default: standard
        flavors:
          standard: {}           # inherits global + built-in only
      implementer:
        default: coder
        flavors:
          coder:
            model: { provider: anthropic, model: claude-sonnet-4 }
    # trust_external: false
```

A project with **no** `agents` block uses global/built-in agent config only (§8.2) and shows **no** flavor pickers on submit.

#### 6.0.4 Authorization (current vs deferred)

- **Current:** Global roles only (`admin` / `member` / `viewer`). All authenticated users see all YAML-registered projects. Sufficient for local/dev testing.
- **Deferred:** Per-project membership and email invites. When added, membership is expected to live in YAML (or an admin surface that writes YAML-equivalent config) and to filter list/submit/decide by project. Do not invent a second parallel config authority.

### 6.1 Git Philosophy

The core implementer agent and toolset are geared around a **targeted, pre-existing git repository**. No agent can create a new git repository or GitHub/Bitbucket project via core tools. Repository management is a human responsibility. Project registration (including git settings) is maintainer configuration (§6.0), not an agent or member action.

### 6.2 Prerequisites

- `git` must be installed and configured in the environment
- `gh` CLI is optional but recommended for GitHub-specific workflows
- SSH keys, tokens, or `gh` profiles must be configured by the human maintainer outside the application
- The application does not manage auth setup; it assumes the environment is ready

### 6.3 Project-Level Git Configuration

Git settings live under `projects.<name>.git` in YAML (synced into `projects.config_json`). Example:

```yaml
projects:
  auth-service:
    git:
      repo_url: "git@github.com:myorg/auth-service.git"
      base_branch: "develop"
      auth:
        type: "ssh_key"  # or "token", "gh_cli"
        ssh_key_path: "/secrets/deploy_key"
      # or:
      # type: "gh_cli"
      # profile: "work"
```

### 6.4 Implementer Workspace Lifecycle (Orchestrator-Managed)

The agent never touches git. The orchestrator handles the entire git lifecycle:

1. **Bare clone cache:** Per project at `{storage_root}/repos/{project_id}.git`. Fetch before each run. Validated at project registration (`git` on PATH; optional `gh`).
2. **Source worktree:** At issue creation, orchestrator creates a read-only worktree at `projects/{pid}/issues/{iid}/source/` checked out to `base_branch` (replaces the §6.6 snapshot copy when git is configured).
3. **Branch + implementer worktree:** Per implementer run:
   ```bash
   git worktree add -b ai-implementer/{issue_id}-{run_id} implementation/workspace <base>
   ```
   Branch name uses SQLite `runs.id` as `{run_id}`. Workspace path remains `implementation/workspace` (path helper); parallel runs use distinct branches/worktrees.
4. **Agent Execution:** Implementer receives `read_file`, `write_file`, `update_file` (and `run_test`) tools scoped to that workspace path. The agent sees relative paths; the orchestrator resolves them. Agents never run git.
5. **Post-Execution:** Orchestrator stages changes, creates a **single structured commit** per run (§17 Q13), optionally pushes (`git.push`, default false), optionally opens a PR via `gh` (`git.create_pr`, default false). The human decides whether to merge.

**Local-only projects:** If `git.repo_url` is unset but `source_path` is set, the Phase 2 snapshot path is retained.

**Parallel Implementers:** Each implementer run gets its own branch and workspace. No collision. The orchestrator tracks `workspace_id` and `branch_name` in SQLite.

### 6.5 Test Execution (`run_test` Tool)

The implementer has access to a `run_test` core tool that executes the project's pre-configured test command in a sandboxed subprocess.

```yaml
projects:
  auth-service:
    test:
      command: "go test ./..."
      timeout: 60s
      image: "golang:1.22"       # required when command is set
      secrets_env: []
      # runtime: auto | docker | podman
```

**Properties:**
- Command is **immutable** — defined in project YAML, not modifiable by the agent or by issue submit
- Executed against the implementer's workspace
- Agent receives stdout/stderr (size-capped) but cannot modify the command
- Enables implementer test-and-fix loops without opening arbitrary shell access

**Security posture (revised 2026-07-08).** An immutable command is *not* a security boundary: the command executes code the implementer just wrote, so a hostile or hijacked agent can put anything in a test file — read env vars, hit the network, escape the workspace. `run_test` is arbitrary code execution by proxy and must be treated as such:

- Execution **must** be container-isolated (Docker/Podman): no network, workspace-only mount, CPU/memory/time limits. This is a Phase 4 acceptance criterion, not an optimization.
- A bare subprocess with a timeout is **not** an acceptable fallback on shared or credentialed hosts. If no container runtime is available, `run_test` refuses to run rather than degrading.
- Test-environment secrets are injected from maintainer-only configuration into the container environment and are never written to any agent-readable artifact (see §17 Q15).

### 6.6 Phase 2 Interim: Read-Only Source Snapshot

The full git workspace model above lands in Phase 4. From Phase 2 onward, the orchestrator provides read-only source access so research, planning, and implementation operate on the real codebase instead of a vacuum:

- Per project, YAML configuration points at a local `source_path` (or git settings — §6.0 / §6.3). Source is **not** supplied at issue submit time.
- On issue creation, the orchestrator copies `source_path` to `projects/{pid}/issues/{iid}/source/` (excluding `.git`) as an immutable snapshot when git is not configured.
- The snapshot is in every agent's read allowlist. The implementer's `workspace/` is seeded as a **copy of the snapshot**, so implementation edits real code.
- Phase 4 replaces snapshot copies with git-managed branch checkouts when `git.repo_url` is set (and eliminates the per-issue copy overhead).

---

## 7. Filesystem Artifact Contract

### 7.1 Directory Structure (Per Issue)

```
projects/{project_id}/issues/{issue_id}/
├── source/                  ← read-only project source snapshot (orchestrator-created; §6.6)
├── research/
│   ├── task.json            ← orchestrator writes: instructions, model config, loop/adjudication config
│   ├── result.json          ← orchestrator writes: status envelope (at phase START and completion; §9.4)
│   ├── events.jsonl         ← orchestrator writes: append-only run transcript (model turns, tool calls, usage)
│   └── attempts/
│       ├── 1/
│       │   ├── output.*     ← agent writes free-form content: .md, .json, .py, .xlsx, etc.
│       │   └── feedback.md  ← orchestrator writes adjudicator feedback if this attempt was rejected
│       └── 2/ ...           ← one directory per adjudication attempt; retries never overwrite
├── plan/
│   └── (same structure)
└── implementation/
    ├── (same structure)
    └── workspace/           ← implementer's mutable copy of source (Phase 2); git checkout (Phase 4)
```

*(Revised 2026-07-08: added `source/`, `events.jsonl`, and the `attempts/` layout; removed the separate `adjudication/` phase directory — adjudication is a boundary evaluation (§9), not a phase, and its record lives in `feedback.md` + SQLite decisions.)*

### 7.2 The Minimal Contract

- **task.json** — Orchestrator writes. Contains: agent type, system prompt reference, model config, tool list, adjudication/loop config, input context (paths to issue text and previous accepted outputs), readable path allowlist. **Never contains secrets** — it is agent-readable.
- **result.json** — Orchestrator writes **at phase start** (`status: in_progress`) and again on completion. Contains: status, error message (if any), attempt count, loop count, tokens consumed, duration, done_rationale (self adjudication only), pointer to the latest attempt, timestamp. Writing it at phase start is what makes crash detection possible (§9.4).
- **events.jsonl** — Orchestrator writes, append-only, one JSON object per line: model turns, tool calls and results (size-capped), and per-call token usage. This is the substrate for the Phase 3 activity stream, token accounting, debugging, and the audit trail. Without it, agent runs are black boxes.
- **output.*** — Agent writes, into the current attempt directory. **Completely free-form.** Content is determined by the agent's system prompt and the underlying LLM. The orchestrator never parses this file. The next agent receives the content of the accepted attempt's output as input context.
- **feedback.md** — Orchestrator writes into an attempt directory when an adjudicator rejects it: the decision and the feedback/rationale. The next attempt receives this content in its input context (§8.3).

**Critical rule:** No agent reads or parses `result.json` or `events.jsonl`. Only the orchestrator manages status. Agents only read their `task.json` inputs and write their `output.*`.

**Authority rule:** where filesystem and SQLite disagree, **the filesystem is authoritative**. SQLite is a queryable index over filesystem truth, reconciled at startup (§9.4, §10.3).

### 7.3 Status Values

| Status | Meaning |
|--------|---------|
| `in_progress` | Agent goroutine is running |
| `done` | Agent completed successfully, ready for handoff evaluation |
| `failed` | Agent encountered an error, timeout, or exception |
| `waiting_human` | HumanAdjudicator gate triggered, goroutine exited, awaiting human decision |
| `retry` | Adjudicator rejected the attempt; a new attempt is starting with the rejection feedback in context |
| `skipped` | Phase was skipped (e.g., adjudication configured as null) |
| `cancelled` | Run was cancelled by the user (Ctrl-C / shutdown) before completion — distinct from `failed` |

---

## 8. Agent Design

### 8.1 Base Agent Identities (Baked into Go Code)

Agent types are **structs in the Go codebase**, not generic configurations. Each has a default system prompt and toolset, but can be altered via YAML config.

| Agent | Role | Typical LLM Profile | Core Tools |
|-------|------|---------------------|------------|
| **Researcher** | Requirements analysis, investigation, root cause identification, solution discovery | Deep thinker, high context window, thorough | `read_file`, `list_directory`, `grep_search`, `write_output` |
| **Planner** | Implementation planning, dependency analysis, effort estimation, task decomposition | Fast, structured, good at planning | `read_file`, `list_directory`, `grep_search`, `write_output` |
| **Implementer** | Code generation, file writing, test scaffolding, spreadsheet creation | Coding-competent, instruction-following | `read_file`, `list_directory`, `grep_search`, `write_file`, `update_file`, `run_test` |

### 8.2 Agent Configuration (YAML) — Global Defaults, Project Flavors, Issue Cast

*(Revised 2026-07-10. Extends the Phase 4 casting schema with **named flavors** per project. Agent **types** remain the three Go structs in §8.1 — flavors are configuration of those types, not new agent types.)*

#### 8.2.1 Configurable fields (per flavor / override)

- LLM model/provider (and `base_url` / `api_key_env` where applicable)
- Temperature, max tokens, token budget (`token_budget` stored Phase 4; enforced Phase 5)
- System prompt override (`system_prompt`) or extension (`system_prompt_append`)
- Core tool subset (`tools` allowlist; empty = all default for type)
- MCP server allowlist (`mcp_servers`)
- Boundary configuration: `adjudicator`, `max_attempts`, `loops`, `rubric` (§9.1)

#### 8.2.2 Named flavors (per project, per core type)

Under `projects.<name>.agents.<type>`:

| Field | Meaning |
|-------|---------|
| `default` | Flavor name used when the submitter does not choose (or when there is only one flavor / no picker) |
| `flavors` | Map of flavor name → `AgentConfig` overlay for that core type |

Rules:

- Only core types may appear: `researcher`, `planner`, `implementer`.
- **No flavors / no `agents` block:** use global + built-in only; **no** submit pickers for that project.
- **Exactly one flavor** for a type: always use it (or the named `default`); **no** picker for that stage.
- **More than one flavor** for a type: submit UI shows a select for that stage; `default` is pre-selected.
- Flavor names are project-local strings (e.g. `thorough`, `cheap`, `coder`).

#### 8.2.3 Merge order

Resolved **per phase** when building `task.json` / the agent run:

1. Built-in type defaults (`defaultAgentConfig`)
2. Global `default_model` + global `agents.<type>`
3. Selected **project flavor** for that type (if any)
4. **Frozen issue cast** (flavor names chosen at submit — see §8.2.4)
5. Orchestrator policy overrides (e.g. external trigger → force implementer `adjudicator: human` unless `trust_external`)

Changing project YAML after an issue is submitted must **not** silently retarget in-flight issues: the issue's frozen cast remains authoritative for flavor selection until a human explicitly resubmits or a future "re-cast" action exists.

#### 8.2.4 Issue cast (frozen at submit)

At issue creation the submitter's choices (or defaults) are persisted on the issue, e.g. `agent_flavors_json`:

```json
{"researcher":"thorough","planner":"standard","implementer":"coder"}
```

- CLI/API/webhook may omit flavors; missing stages resolve to the project `default` (or pure global/built-in when no project flavors).
- Invalid flavor name for the project → hard error at submit.
- Retries, crash recovery, and resume use the same frozen cast.

#### 8.2.5 Submit UX (member)

See also §11.5. The New-issue drawer:

- **Project:** required select of YAML-registered projects only (no free text, no create-by-typing).
- **Title / dry-run:** as today.
- **Source path:** **not present** (project YAML only).
- **Per-stage agent flavor:** one control per core type **only when** that project type has `len(flavors) > 1`; otherwise hidden.

### 8.3 Context Flow

The default context recipe for each phase is: **the original issue title/body, plus the entire content of the immediately-previous phase's accepted `output.*`**. (The first phase receives only the issue.) Per-agent config may add further inputs — e.g., give the implementer the research output as well as the plan. No automatic summarization or distillation by the orchestrator — the human's job is to configure agents such that Agent N's output is useful as Agent N+1's input.

Two additional context rules (revised 2026-07-08):

- **Refinement loops feed forward.** When an agent runs multiple loops within an attempt, loop *i* receives loop *i−1*'s output as context. Fresh-context loops that overwrite each other are pure token burn; the loop mechanism exists for iterative refinement.
- **Retries see why they failed.** When an adjudicator rejects an attempt, the next attempt's context includes the rejected `output.*` and the adjudicator's `feedback.md`. A blind retry discards the most valuable signal a human gate produces.

---

## 9. Handoff & Adjudication (Unified Model)

*(Revised 2026-07-08: the previously separate "handoff modes" — `n_loops` / `self_done` / `human_gate` — and "adjudicators" — null / self / human — overlapped almost 1:1 and produced ambiguous configurations ("`loop_mode: self_done` with `adjudicator: human` — what happens?"). They are merged into a single axis.)*

### 9.1 The Boundary Model

Every phase boundary is configured with exactly three settings:

| Setting | Meaning | Default |
|---------|---------|---------|
| `adjudicator` | Who decides whether the phase output is accepted: `null`, `self`, `human` *(future: `agent`)* | `null` |
| `max_attempts` | Maximum adjudication attempts before the phase is marked `failed` | 1 |
| `loops` | Refinement iterations *within* one attempt; loop *i* receives loop *i−1*'s output (§8.3) | 1 |

The legacy modes map cleanly: `n_loops` ≡ `adjudicator: null` + `loops: N`; `self_done` ≡ `adjudicator: self`; `human_gate` ≡ `adjudicator: human`. One concept, one config axis, simpler phase machine — and the future AgentAdjudicator drops in without a new mode.

### 9.2 Adjudicators

Adjudication is a **handoff boundary evaluation**, not an agent type. The system does not care WHO adjudicates.

- **NullAdjudicator** — Auto-pass after the configured loops complete.
- **SelfAdjudicator** — The agent evaluates itself against an English rubric (defined in config) and reports done/not-done with a rationale via its `finish_task` call.
- **HumanAdjudicator** — Pauses the pipeline, notifies a human, waits for a decision (dashboard in Phase 3; `resume` CLI before that).
- *(Future)* **AgentAdjudicator** — Dedicated AI reviewer agent (external process adapter).

Every adjudicator returns a **decision** (`pass` / `fail` / `retry`) **and feedback text**. Feedback is stored twice — in SQLite (`decisions.feedback`) and in the rejected attempt's `feedback.md` — and injected into the retry context (§8.3). The human adjudication UI must make feedback entry first-class: pass/fail buttons without a "why" field throw away the point of the gate.

### 9.3 Goroutine Lifecycle

- Agents run as goroutines spawned by the orchestrator.
- On a human gate, the goroutine writes `waiting_human` status and **dies**. No long-running sleep/poll loops.
- On a human decision, a new goroutine is spawned (in-process in daemon mode; via `resume` in CLI mode).
- Humans can pass/fail/retry any agent output at any time via dashboard, regardless of the boundary's configured adjudicator.

### 9.4 Crash Recovery Semantics

Crash resilience is a headline claim; these are its exact semantics.

**What survives a crash:** everything on disk (`task.json`, `result.json`, `events.jsonl`, attempt outputs, workspaces) and everything in SQLite. **What does not:** in-flight agent reasoning — ADK sessions are in-memory and ephemeral.

**Therefore, recovery = re-running the interrupted phase from its `task.json`.** There is no mid-phase resume. Tokens already spent on the interrupted attempt are lost; this is accepted, and the loss should be visible in run history.

**Detection state machine** (evaluated at daemon startup, and before any command touches an issue):

| Observed state | Interpretation | Action |
|----------------|----------------|--------|
| `task.json` exists; `result.json` says `in_progress`; no live goroutine | Crashed mid-phase | Re-run the phase: new attempt, fresh workspace copy where applicable, partial output discarded |
| `result.json` says `waiting_human` | Awaiting decision | Leave in place; surface in the decision queue |
| `result.json` says `done` / `failed` / `cancelled` | Terminal | Reconcile the SQLite index if it disagrees |
| `task.json` missing but SQLite says in-progress | SQLite ahead of filesystem | Trust the filesystem: reset the issue to the last phase with a terminal `result.json` |

To make detection possible, the orchestrator **writes `result.json` with `status: in_progress` at phase start**, not only at completion.

Recovery behavior must be covered by a kill-mid-phase → restart → verify-recovery test (Phase 2 Part 2 test list).

---

## 10. SQLite vs. Filesystem Boundary

### 10.1 SQLite (Application State)

- Project registry (project_id, name, created_at, **config_json** synced from YAML `projects.<name>` — §6.0)
- Issue queue (issue_id, project_id, title, status, current_phase, created_at, updated_at, dry_run, source, external_id, **agent_flavors_json** frozen cast — §8.2.4)
- Agent run history (run_id, issue_id, agent_type, model, tokens_used, duration, result_status, timestamp, workspace_id, branch_name)
- User/team accounts, roles, SSO mappings (global roles today; project membership deferred — §6.0.4)
- Audit log (user_id, action, target_issue, timestamp, details)
- Notification queue (notification_id, issue_id, agent_type, status, recipient, sent_at)
- Human decision queue (decision_id, issue_id, phase, requested_at, decided_at, decision, **feedback**, decided_by)

### 10.2 Filesystem (Agent Memory & Artifacts)

- Agent output directories (`research/`, `plan/`, `implementation/`)
- Agent free-form outputs (`attempts/N/output.*` files) and adjudication feedback (`attempts/N/feedback.md`)
- Read-only source snapshots (`source/`, §6.6)
- Workspace directories (`workspaces/{issue_id}/implementer-{run_id}/`)
- Per-phase run transcripts (`events.jsonl`)
- Configuration files (YAML) — **source of truth for projects, agents, server, auth, adapters**
- Logs (optional, per-project)
- Dashboard static assets

### 10.3 Authority & Concurrency (added 2026-07-08)

- **Filesystem is authoritative** for phase/agent state. SQLite is a queryable index over filesystem truth, reconciled at startup (§9.4). When they disagree, the filesystem wins. (Both store status; a crash between the two writes can make them diverge — this rule resolves it.)
- **YAML is authoritative** for project definition and agent flavor catalogs (§6.0, §8.2). SQLite project rows are a synced index; issue rows hold the **frozen cast** chosen at submit so runs remain stable if YAML later changes.
- SQLite is opened with `PRAGMA journal_mode=WAL`, a `busy_timeout`, and `PRAGMA foreign_keys=ON`. Foreign keys are silently unenforced in SQLite without the pragma, and WAL + busy timeout are prerequisites for the multi-goroutine daemon.
- Schema changes use **versioned migrations**, not accreting `CREATE TABLE IF NOT EXISTS` statements — `IF NOT EXISTS` cannot evolve existing tables.

---

## 11. Human Interface (Dashboard)

### 11.0 Process Model (added 2026-07-08)

Phases 1–2 are one-shot CLI invocations. Everything below — webhook triggers, real-time views, human gates that respawn goroutines, parallel issues — presumes a **long-running daemon** (`gorchestrator serve`) with an issue queue and a worker pool. Daemonization is therefore the *first* workstream of Phase 3, named explicitly rather than left as an implied side effect. The orchestrator core is built as an embeddable engine from Phase 2 onward so the CLI and the daemon are thin front-ends over the same code.

### 11.1 Phase 1-2: CLI Only

Configuration and operation via YAML files and CLI commands. No web interface.

### 11.3 Phase 3: Web Dashboard (Observation + Adjudication)

Capabilities (what the human can do):

- **Real-time issue feed:** live list of issues with phase, status, attempts, token burn (Server-Sent Events; §17 Q4)
- **Artifact viewer:** rendered Markdown, syntax-highlighted code/JSON, activity from `events.jsonl`, workspace diff — primarily in a **slide-out drawer** (§11.5)
- **Adjudication UI:** pass/fail/retry **plus a first-class feedback text field** on the **expanded issue card**, available at any handoff boundary regardless of configuration (§9.2, §9.3)
- **Token burn display:** per-run and cumulative — from `runs` / `events.jsonl`
- **Notification center:** pending human gates, admin alerts on failures
- **Submit issue:** from the top bar (member+), not from a board column — project select from YAML registry; optional per-stage agent flavor selects (§8.2.5, §11.5)

Layout, visual language, and interaction details are normative in **§11.5**.

### 11.4 Admin Features

- Admin users always receive notifications on "bad" agent output (errors, timeouts, exceptions, empty outputs)
- Configurable admin escalation rules (Phase 5)
- **Project and agent configuration** remain YAML-only for now (§6.0). Future admin GUI may edit the same model; it does not invent a second store.

### 11.5 Dashboard UX (Phase 3) — Layout, Theme & Interaction

*(Added 2026-07-09. Canonical UX for the HTMX dashboard. Implementation details live in `phase_3.md` Part D; this section is the product contract.)*

#### 11.5.1 Information architecture

**Not a kanban / sprint board.** No columns for “To do / In progress / Done.”

The primary surface is a **single vertical feed** of **issue cards**, newest or most-recently-updated first (stable secondary sort by id). Optional filters (project, status) sit under the top bar as a compact strip — chips or selects, not a second navigation tree.

| Surface | Role |
|---------|------|
| **Top bar** | Brand, primary nav (Issues, Notifications with badge count), **New issue** (member+), user menu (role, logout) |
| **Issue feed** (`/`) | Vertical stack of expandable cards; default home after login |
| **Expanded card** | Inline summary of current phase + truncated `result.json` fields + adjudication |
| **Artifact drawer** | Right-hand slide-out for full `result.json`, `output.*`, `events.jsonl`, markdown render, workspace diff |
| **Submit drawer** | Right-hand slide-out form opened from **New issue**: **project select** (YAML-registered only), title, dry-run, and **per-stage agent flavor selects only when that stage has multiple flavors** (§8.2.5). No free-text project. No source path field. |
| **Notifications** (`/notifications`) | Pending human gates + recent notification rows (same dark shell) |
| **Login** (`/login`) | Minimal centered card; no marketing chrome |

Deep link: `/issues/{id}` renders the feed with that card **pre-expanded** (and optional `?drawer=result|output|events|diff` to open the artifact drawer on load). There is no separate full-page issue detail layout in Phase 3 — expansion + drawer *are* the detail experience.

#### 11.5.2 Issue card — collapsed (default)

Minimal chrome. One horizontal band per issue:

- **Left:** status color wash (see §11.5.5) + thin brighter status accent bar on the leading edge
- **Identity:** `#id` · project name · **title** (truncate with ellipsis)
- **Meta chips:** status label · current phase · attempt `N` · cumulative tokens · relative `updated_at`
- **Affordance:** chevron indicating expand; whole card header is the click/toggle target (keyboard: Enter/Space on focused card)

No action buttons on the collapsed face except the expand control (adjudication is expand-only so the feed stays scannable).

#### 11.5.3 Issue card — expanded

**Multi-expand:** any number of cards may be open at once (parallel runs, compare two gates). Expanding one does **not** collapse others. Expanded state is client-side (and restored from the deep-link URL for a single id); SSE may re-render a card without forcing collapse.

Expanded body (below the header, same status-tinted panel):

1. **Phase strip** — research → plan → implementation as three steps; completed = check, current = neon-pink pulse/dot, future = dim. Clicking a completed step can open the drawer focused on that phase’s artifacts when available.
2. **Result summary** — truncated fields from the current phase `result.json`: `status`, `attempt`, `loop_count`, `tokens_used`, `duration_ms`, `error` / `done_rationale` (one short paragraph max; overflow ellipsis).
3. **Artifact actions** — text buttons/links:
   - **Full result.json** → opens drawer on JSON tab
   - **Output** → opens drawer on rendered output (`output.md` / latest attempt)
   - **Activity** → opens drawer on `events.jsonl` stream (tail-friendly)
   - **Diff** → opens drawer on source vs workspace unified diff (when implementation workspace exists; otherwise hidden/disabled)
4. **Adjudication block** (member+; always shown for the current phase when a decision is meaningful per §9.3 — especially `waiting_human`, and for manual intervene):
   - Feedback **textarea** first-class (placeholder encourages “why”)
   - **Pass** / **Fail** / **Retry** buttons
   - Empty feedback on Fail/Retry: client-side warning, still submittable; server may echo a warning
   - Viewer role: block visible but controls disabled with short explanation

Live updates: when SSE reports a status/phase change for an expanded card, the header chips, tint, phase strip, and summary refresh in place (HTMX swap of the card partial).

#### 11.5.4 Slide-out drawer (artifacts & submit)

- **Position:** fixed to the **right**, full viewport height, width ~min(520px, 92vw) on desktop; near-full width on small viewports.
- **Behavior:** slides in over a dimmed scrim; **Esc** or scrim click or ✕ closes; body scroll lock while open; focus trapped while open.
- **Artifact drawer tabs:** `Result` | `Output` | `Activity` | `Diff` (tabs omitted when N/A). Content is server-rendered HTML partials (goldmark for Markdown; `<pre>` + highlight.js for JSON/code). Large payloads: stream or size-cap with “truncated” notice rather than melting the browser.
- **Submit drawer:** form fields only (no tabs); success closes drawer and inserts/refreshes the new card at the top of the feed via HTMX. Fields:
  - **Project** — `<select>` of registered projects (required). Changing project may HTMX-refresh the flavor fields for that project.
  - **Title** — required text.
  - **Agent flavors** — zero to three selects (`researcher` / `planner` / `implementer`), each shown only when the selected project defines more than one flavor for that type; values default to the project's `default` for the type.
  - **Dry run** — checkbox.
  - **Not included:** source path (project YAML), free-text project name, create-project affordances.
- **Stacking:** only one drawer at a time; opening submit while artifact is open replaces it (and vice versa).

#### 11.5.5 Theme — dark only

**No light theme and no theme switcher.** One deliberate dark palette: deep blue-greys, cool surfaces, **hot neon pink** as the interactive accent (CTAs, focus rings, current-phase marker, badge pulses, primary buttons).

**Design tokens** (CSS custom properties; names are normative for `internal/web` stylesheets):

| Token | Role | Value (hex) |
|-------|------|-------------|
| `--bg-app` | Page background | `#0a0e14` |
| `--bg-elevated` | Top bar, drawer, modals | `#121820` |
| `--bg-card` | Neutral card base (before status wash) | `#151c27` |
| `--border-subtle` | Dividers, card edge | `#243041` |
| `--text-primary` | Titles, body | `#e8eef7` |
| `--text-muted` | Meta, labels | `#8b9bb4` |
| `--accent` | Neon pink — primary actions, focus | `#ff2d95` |
| `--accent-dim` | Pink wash / glow | `#ff2d9533` |
| `--accent-hot` | Hover/active pink | `#ff4db8` |
| `--focus-ring` | Keyboard focus | `0 0 0 2px #0a0e14, 0 0 0 4px #ff2d95` |
| `--status-queued` | Waiting its turn | wash `#1a2a4a` · accent bar `#3d7eff` · text chip `#8eb6ff` |
| `--status-active` | In progress / running | wash `#0f2a22` · accent bar `#2ee59a` · text chip `#7df0c4` |
| `--status-human` | Waiting on human | wash `#2a2410` · accent bar `#f5c542` · text chip `#ffe08a` |
| `--status-error` | Failed | wash `#2a1218` · accent bar `#ff4d6a` · text chip `#ff8a9b` |
| `--status-done` | Completed pipeline | wash `#141c24` · accent bar `#5b8def` · text chip `#a8c0e8` |
| `--status-cancelled` | Cancelled (not failure) | wash `#1a1d24` · accent bar `#6b7280` · text chip `#9ca3af` |

Status → token mapping:

| Issue / phase status | Visual |
|----------------------|--------|
| `queued` | **Blue** — “waiting its turn” |
| `in_progress` | **Green** — active / running |
| `waiting_human` | **Yellow/amber** — human gate; optional subtle pink border pulse so gates still “pop” in a long feed |
| `failed` | **Red** |
| `done` | Cool slate + blue accent (calm terminal; not screaming green) |
| `cancelled` | Neutral grey (distinct from `failed`) |

Cards use a **status wash** (tinted background + 3–4px leading accent bar), not a solid full-saturation fill — keeps title text readable on dark UI. Neon pink is **not** a status color; it is reserved for interaction (New issue, Pass emphasis optional, links, focus, “you are here” on the phase strip).

Typography: system UI stack (`ui-sans-serif, system-ui, …`) for chrome; `ui-monospace` for ids, JSON, tokens, and code. Comfortable density — compact cards, not dashboard-wall sparse.

Motion: short (150–220ms) expand/collapse and drawer slide; respect `prefers-reduced-motion` (instant expand, no pulse).

#### 11.5.6 Real-time & empty states

- SSE drives card re-tints, chip updates, notification badge count, and expanded summary refresh.
- Degraded clients: HTMX polling on the feed partial every ~5s (already planned as SSE fallback).
- Empty feed: short copy + **New issue** affordance (member+) or “waiting for work” (viewer).
- `waiting_human` cards may sort toward the top of the default ordering when a “Needs you” filter/chip is active; default sort remains recency unless the user selects that filter.

#### 11.5.7 Accessibility (minimum bar)

- Expand/collapse and drawer close keyboard-operable; focus ring uses `--focus-ring`.
- Status is not color-only: every card also shows a text status chip.
- Drawer labels tabs with visible text; `aria-expanded` / `aria-modal` on the appropriate nodes.
- Contrast: primary text on washes must remain readable (washes stay dark and desaturated).

---

## 12. Tool Strategy

### 12.1 Core First-Class Tools

Small, native Go toolset available to agents based on agent type:

| Tool | Availability | Description |
|------|-------------|-------------|
| `read_file` | Researcher, Planner, Implementer | Two explicit modes. **(1) Whole-file:** no range args — returns the full file, subject to a **configurable cap** (default 64KB / ~2,000 lines, whichever first) with an explicit truncation marker and total line count so the agent knows to switch modes. **(2) Surgical:** line-number range (offset + limit) — the intended follow-up to `grep_search`, which returns file + line numbers precisely so agents can read just the relevant region instead of whole files. The tool description teaches this grep → targeted-read workflow to the model. Paths resolved against orchestrator allowlist. |
| `list_directory` | Researcher, Planner, Implementer | List directory contents. |
| `grep_search` | Researcher, Planner, Implementer | Pattern search across files. Respects `.gitignore`, skips binaries, caps result count. |
| `write_output` | Researcher, Planner | Write to the agent's designated `output.*` file. Orchestrator resolves path. |
| `write_file` | Implementer only | Write a file within the implementer's workspace. |
| `update_file` | Implementer only | Update/patch a file within the implementer's workspace. |
| `run_test` | Implementer only | Execute pre-configured test command in sandboxed subprocess. |

### 12.2 MCP Adapter Port

- Users can connect any MCP server for custom tools (stdio in Phase 4; streamable HTTP later if needed)
- MCP servers are declared in project/global config (**deny by default**)
- Core implements the MCP client (official Go SDK); tools from a server are discovered at connect time for schemas
- **Phase 4:** Per-agent, per-**server** allowlists — an agent only receives tools from servers listed in its `mcp_servers` config. If a server is allowed, all of its tools are exposed (name-prefixed to avoid collisions)
- **Phase 5:** Per-agent, per-**tool** permission granularity
- MCP tool calls are recorded in `events.jsonl` like core tools

---

## 13. Guardrails (Phase 5)

### 13.1 "Bad Output" Definition (MVP)

- LLM API errors or timeouts
- Agent exceptions during tool execution
- Configuration errors or missing required config
- Empty output: an attempt that produces neither a `write_output` call nor final model text is a **failed loop**, enforced at the orchestrator — not a silent success flagged later by heuristic

### 13.2 Token Budgets

- Configurable per agent and per provider
- Hard stop when budget exceeded
- Notification to admin when approaching threshold

### 13.3 Effort Estimation (Phase 5)

- Planner tags issues as high / medium / low effort
- High effort requires human confirmation before proceeding
- Prevents runaway tasks (e.g., "refactor the entire monolith")

### 13.4 Scope Detection (Phase 5)

- Basic heuristics to flag overly broad issues
- Human confirmation required for flagged issues

---

## 14. Implementation Phases

*(Revised 2026-07-08: Phase 2 is restructured into three sub-phases — Part 1 (ADK migration, complete), Project Cleanup (bug fixes + spec alignment), and Part 2 (pipeline). Read-only source access moved into Phase 2. Daemonization named as Phase 3's first workstream. Webhook/email example adapters moved out of Phase 2 to the phases where they are actually wired. Each phase has a detailed plan document: `phase_1.md`, `phase_2.md`, `phase_2_cleanup.md`, `phase_3.md`, `phase_4.md`, `phase_4_project_refactor.md`, `phase_5.md`, `phase_6.md`.)*

### Phase 1: The Engine (CLI-Only) — ✅ Complete
**Goal:** A running system that can accept a trigger, run a no-op agent, and write artifacts to storage.

- StoragePort interface + OS filesystem adapter (built-in)
- Minimal artifact contract: `task.json` + `result.json` + free-form `output.*`
- Trigger: manual CLI (TriggerPort formalization deferred to Phase 4)
- LLM: OpenAI + OpenAI-compatible local endpoints, dry-run stub
- Orchestrator: goroutine-based agent runner with context cancellation
- One agent type: Researcher (basic)
- Handoff: `n_loops` mode only
- SQLite: project and issue registry, run history
- Adapter manifest parsing + JSON-RPC stdio client foundation
- **Deliverable (met):** `run --issue="add auth" --project=foo` creates issue, runs researcher, writes to filesystem, marks done in SQLite

### Phase 2 Part 1: ADK Migration — ✅ Complete
**Goal:** Replace the custom agent loop and LLM port with tight ADK Go v2 integration.

- Tools rebuilt as ADK function tools; Researcher rebuilt as ADK LLMAgent
- `model.LLM` implementations: OpenAI (custom), Gemini (ADK built-in), dry-run
- Orchestrator drives the ADK runner; filesystem remains source of truth
- **Deliverable (met):** same Phase 1 artifacts, produced through the ADK runtime

### Phase 2 Project Cleanup — ✅ Complete (see `phase_2_cleanup.md`)
**Goal:** Pay down the defects and spec drift found in the post-Part-1 review before building the pipeline on top of them. No new pipeline features.

- Security fixes: separator-aware path containment, symlink resolution, manifest binary validation
- Correctness fixes: per-call token accounting, accurate loop counts, `cancelled` status, empty-output = failed loop, `read_file` size cap
- Robustness: JSON-RPC client hardening (scanner buffer, initialize handshake, close semantics, stderr capture), OpenAI retry/backoff, SQLite pragmas (WAL, busy_timeout, foreign_keys), versioned migrations
- Contract alignment: `result.json` written at phase start, `events.jsonl` transcripts, `attempts/` layout, feed-forward loops, slash-canonical storage keys, `.gitignore`-aware grep, explicit adapter registry
- Hygiene: dead code removal, shared schema helpers, README
- **Deliverable:** same single-researcher behavior, on the revised artifact contract, with the known defects closed

### Phase 2 Part 2: The Pipeline (Still CLI) — ✅ Complete
**Goal:** Full Research → Plan → Implement pipeline with unified adjudication, operating on real project source.

- Read-only source snapshot per issue (§6.6); implementer workspace seeded from it
- Agent types: Researcher, Planner, Implementer (Go structs), running in ADK task mode (`finish_task`)
- Core toolset: `read_file`, `list_directory`, `grep_search`, `write_output`, `write_file`, `update_file`
- Unified adjudication boundaries (§9.1): adjudicator + max_attempts + loops; Null and Self adjudicators built-in; human gate via `waiting_human` + `resume` command
- Adjudicator feedback stored and injected into retries; attempt versioning
- Context chaining per §8.3 recipe (issue + previous accepted output; feed-forward loops)
- Anthropic `model.LLM` implementation (casting needs the strongest coding models available)
- Crash recovery per §9.4, with a kill-mid-phase test
- Token tracking per run, SQLite logging
- **Deliverable:** Full pipeline from issue to code changes against a real codebase, configurable per-agent via YAML, recoverable after a crash

### Phase 3: Human Interface (Daemon + Dashboard + Auth) — ✅ Complete
**Goal:** Humans can see what's happening and intervene. (See `phase_3.md`.)

- **Daemonization first:** `gorchestrator serve` — embeddable engine, issue-row queue + worker pool, graceful shutdown, startup recovery scan (§9.4). This is a named workstream, not an implied side effect of the dashboard.
- Web dashboard: Go HTTP server + HTMX; **vertical expandable status-tinted issue cards** (not kanban); **dark-only** blue-grey + neon pink theme (§11.5, §17 Q1)
- Real-time feed via SSE: card re-tint/chips; full artifacts in a **right slide-out drawer** (`result.json`, output, `events.jsonl`, diff) (§17 Q4)
- Adjudication UI on the **expanded card**: pass/fail/retry **with feedback field** at waiting/failed/cancelled boundaries
- HumanAdjudicator: pauses, notifies, worker exits; decision re-queues and a new worker runs (in-process; CLI `resume` remains for headless)
- User/team model: SQLite-backed roles (admin, member, viewer); OIDC built-in; **local auth mode for dev/test** (not production)
- Notifications wired: console (built-in) + Slack webhook + SMTP email (external process adapters — §17 Q5); SAML out of scope (§17 Q6)
- **Deliverable (met):** Team can log in, watch agents work live, click retry with a reason, get Slack/email/console alerts

### Phase 4: Extensibility
**Goal:** Users can plug in their own world. (See `phase_4.md`.)

- Git workspace model: bare clone cache, worktrees, branch per run, single commit/optional push (replaces §6.6 snapshot copies when git configured)
- `run_test` core tool — **container-isolated (acceptance criterion, §6.5)**, secrets injection per §17 Q15
- MCP client: custom tools via MCP servers, **with per-agent server allowlists (§5.6)**
- Trigger port formalized + adapters: HTTP webhook (built-in), GitHub Issues, Jira (external process); externally-triggered issues default to human adjudication before implementation
- Storage adapter: **S3** (external process); Azure Blob deferred until the S3 pattern is proven
- Agent personality config: system prompts, temperature, tool subsets (the casting thesis, realized; §17 Q9)
- JSON-RPC client: restart with exponential backoff, streaming notifications
- **Deliverable:** Users can connect Jira, plug in internal APIs via MCP, store artifacts in S3, run test-and-fix loops safely

### Phase 4 Project Refactor — YAML registry + agent flavors
**Goal:** Make project and agent casting first-class configuration before Phase 5 guardrails attach to accidental project strings. (See `phase_4_project_refactor.md`.)

- Projects declared only in YAML; registry upserted at process start; **no create-on-submit or CLI GetOrCreate** (§6.0, §17 Q16)
- Project blocks own `source_path` / `git` / `test` / `agents` / `trust_external`; submit GUI no longer mutates project config
- Named **flavors** of the three core agent types per project; merge layers include project flavor + frozen issue cast (§8.2, §17 Q9)
- Submit drawer: project select + conditional per-stage flavor selects; cast persisted on the issue
- **Out of scope here:** project membership, invites, SSO polish, admin config GUI
- **Deliverable:** Only YAML-registered projects accept work; multi-flavor projects expose casting at issue create; retries honor the frozen cast

### Phase 5: Guardrails
**Goal:** The system protects itself and the user's wallet. (See `phase_5.md`.)

- Token budget enforcement: hard stop + notification, checked per model call against `events.jsonl` usage
- Effort estimation: Planner tags high/med/low, high requires human confirmation
- Scope detection: basic heuristics to catch overly broad issues
- Admin escalation rules: configurable thresholds
- MCP permission granularity: per-agent, per-**tool** allowlists (tightening Phase 4's per-server grants)
- **Deliverable:** System asks "are you sure?" before burning API budget

### Phase 6: Polish
**Goal:** Shippable product. (See `phase_6.md`.)

- Complete audit logging
- Metrics dashboard: token burn per project, cycle time, human intervention rate
- Documentation: architecture docs, admin guide, user guide, API reference
- Deployment: single binary + Docker Compose (SQLite + filesystem volume); backup/restore guidance
- Retention: workspace/artifact cleanup policies (closes §17 Q14)
- **Deliverable:** Team can deploy to production and audit every decision

---

## 15. Technology Stack

| Component | Choice |
|-----------|--------|
| Language | Go |
| Agent Runtime | Google ADK Go (v2) — tight integration; no wrapper port |
| LLM Integration | ADK `model.LLM` implementations: OpenAI, Anthropic, Gemini built-in; OpenAI-compatible endpoints for local models and gateways |
| App Database | SQLite (WAL, busy_timeout, foreign_keys pragmas; versioned migrations) |
| Storage | Filesystem (hexagonal port, built-in + external adapters); port keys are forward-slash canonical |
| Process Model | One-shot CLI (Phases 1–2) → long-running `serve` daemon with queue + worker pool (Phase 3+) |
| Web Framework | Standard library `net/http` + HTMX server-rendered frontend (soft decision §17 Q1) |
| Auth | OIDC (built-in), SAML (external adapter) |
| Deployment | Single binary + Docker Compose |
| Config | YAML |
| Serialization | `encoding/json` (stdlib) or `github.com/goccy/go-json` |
| Adapter Protocol | JSON-RPC 2.0 over stdio (JSON Lines) |
| Git Operations | Orchestrator-managed via `os/exec` (git/gh CLI prerequisites) |
| Test Execution | Sandboxed subprocess with pre-configured command |

---

## 16. Decisions Log

| Decision | Rationale |
|----------|-----------|
| ADK Go for agent runtime only | ADK owns session state; we need filesystem-based state. Use ADK for single-agent reasoning, custom orchestrator for multi-agent pipeline. |
| Filesystem as agent memory | Crash resilience, human readability, free-form content, no schema lock-in on agent outputs. |
| result.json in filesystem, not SQLite | Keeps all agent-phase state co-located with artifacts. Orchestrator manages it; agents ignore it. |
| Agent identities as Go structs | Not a generic orchestrator. Core agent types are first-class citizens with baked-in behavior. |
| Goroutines die on human gates | No long-running sleep loops. Go concurrency is cheap; spawn new goroutines on human decision. |
| CLI-first, dashboard later | Strong YAML config enables future web-based configuration migration. Dashboard starts as observation + adjudication only. |
| Free-form agent outputs | Content determined by system prompt + LLM. Human's job is to configure model fit. Orchestrator only enforces minimal status envelope. |
| Adjudication as port, not agent type | System doesn't care WHO adjudicates. Human, self, or null are interchangeable implementations. |
| External process adapters (JSON-RPC stdio) | Go `plugin.Open()` is dead. Static binary + external adapters is the Unix-native way to extend a Go binary without recompilation. |
| Built-in + external hybrid | Common adapters compiled into core for zero-config. External processes for exotic/enterprise needs. |
| JSON-RPC over stdio | Simple, debuggable, language-agnostic, natural fit for MCP. |
| Serialization via Go JSON | `encoding/json` or `goccy/go-json` provides acceptable performance for target scale. |
| WASM deferred | Over-complicates MVP. Revisit if external process overhead becomes measurable. |
| Secure by default: no shell for core agents | Agents get typed tools, not raw shell. `run_test` is pre-configured and immutable. |
| `write_output` vs `write_file` | Researcher/Planner write only to their designated output file via orchestrator-resolved tool. Implementer writes to workspace. |
| Git workspace model | Orchestrator manages branch creation, workspace copy, and commit/push. Agent never touches git. |
| Pre-existing git repo | No agent can create repos. Human sets up project git config. `git` and `gh` are prerequisites. |
| `run_test` as core tool | Pre-configured project command in sandboxed subprocess. Enables test-and-fix loops without arbitrary shell access. |
| MCP deny-by-default | MCP servers only connected if explicitly configured. All tools from enabled servers available to all agents in MVP. |

**Added 2026-07-08 (post Phase 2 Part 1 review):**

| Decision | Rationale |
|----------|-----------|
| ADK-native LLM integration (recorded) | Phase 2 deleted the custom LLMProviderPort in favor of tight ADK `model.LLM` integration. Recorded here because it supersedes the original port design; external LLM gateways plug in via OpenAI-compatible endpoints or in-process `model.LLM` implementations, not JSON-RPC adapters. |
| Unified adjudication axis | Handoff modes and adjudicators were two names for one mechanism and produced ambiguous configs. Every boundary = adjudicator (null/self/human) + max_attempts + loops. |
| Loops are refinement | Loop *i* receives loop *i−1*'s output. Fresh-context loops that overwrite each other are pure token burn. |
| Attempt versioning + feedback on retry | Retries never overwrite; each attempt gets a directory; adjudicator feedback is stored (`decisions.feedback` + `feedback.md`) and injected into the retry context. A blind retry wastes the human gate. |
| `events.jsonl` transcripts | Tool calls, model turns, and per-call usage are persisted per phase. Substrate for the dashboard activity stream, token accounting, debugging, and audit — without it, agent runs are black boxes and Phase 3 has nothing to display. |
| Read-only source access in Phase 2 | A research pipeline that cannot read the codebase until Phase 4 produces hallucinated research. Orchestrator snapshots project source per issue (§6.6); full git workspace model still lands in Phase 4. |
| Filesystem is authoritative | `result.json` wins over SQLite on divergence; SQLite is a reconciled index. `result.json` is written at phase start (`in_progress`) to make crash detection possible (§9.4). |
| Container sandbox required for `run_test` | An immutable command is not a security boundary when the agent writes the code the command runs. Container isolation is a Phase 4 acceptance criterion; no bare-subprocess fallback. |
| Explicit adapter registry | Adapters are declared in config, not discovered by scanning a directory for executables. Manifest declares `binary` explicitly; core verifies it is a regular executable file. |
| Storage keys are slash-canonical | Port paths are forward-slash relative keys; adapters translate. Prevents OS-separator leakage into S3/Azure object keys. |
| Daemonization is a named workstream | Phase 3 begins by converting the one-shot CLI into a `serve` daemon with queue + workers; the engine is embeddable from Phase 2 onward so this is a front-end change, not a rewrite. |
| Issue row is the daemon queue | No separate jobs table: workers claim issues with `status=queued`. Human gates set `waiting_human` and free the worker; decisions re-queue. Filesystem remains authoritative for phase state. |
| OIDC-only + local dev auth | Phase 3 ships built-in OIDC and a non-production local password mode for tests/first-boot. SAML stays an unimplemented external adapter. |
| Notifications: console + Slack + email | Console is built-in; Slack webhook and SMTP email are the first real JSON-RPC notification adapters. |

**Added 2026-07-10 (project registry + agent flavors):**

| Decision | Rationale |
|----------|-----------|
| YAML is source of truth for projects | Projects own git, test, agent casting, and (later) budgets. Creating them via free-text issue submit made config accidental. Registry is upserted from `projects:` at process start; SQLite is a runtime index only. |
| No GetOrCreate on user paths | CLI, API, dashboard, and triggers hard-fail on unknown project names. Registration is always maintainer YAML. |
| Named agent flavors of core types only | Casting thesis without inventing free-form agent types. Go structs remain the only identities; flavors are named model/prompt overlays per project. |
| Freeze cast on the issue at submit | Changing project YAML mid-flight must not retarget in-progress issues. Retries and crash recovery reuse the same flavor names. |
| Source path off the submit form | Project configuration, not issue input. Future admin UI may edit it; members do not. |
| Project membership deferred | Local/dev testing uses global roles. Invites/SSO multi-tenant scoping land later without a second config authority. |
| Dashboard is a vertical expandable-card feed | Not a kanban board. Status-tinted cards, multi-expand, adjudication on the expanded card, full artifacts in a right drawer. Dark-only theme: blue-greys + neon pink accent (§11.5). |
| `cancelled` is a distinct status | Ctrl-C / shutdown is not a failure; it must not be reported as one. |
| Empty output fails the loop | An attempt with neither a `write_output` call nor final model text fails immediately rather than being marked done and flagged later. |
| Markdown docs are canonical | The spec is the living document; phase docs freeze at completion; HTML renderings are unmaintained presentation artifacts. |
| Git worktree workspace model (Phase 4) | Bare clone cache + source/implementer worktrees replace per-issue file snapshots when git is configured; agents never run git. |
| Adapter credentials stay in adapter env | External adapters authenticate to their backends via their own env/config; core does not mediate secrets into agent artifacts (§17 Q12). |
| S3 first for cloud storage | Phase 4 ships S3 only; Azure Blob follows once the JSON-RPC storage pattern is proven. |
| MCP per-agent server allowlists with triggers | Per-server grants land in Phase 4 alongside external triggers (§5.6); per-tool grants are Phase 5. |

---

## 17. Open Questions & Soft Decisions

*(Revised 2026-07-08: questions with a defensible default are recorded as **soft decisions** — adopted unless new evidence overturns them; a few are closed outright because working code already decided them. Genuinely open items remain at the bottom.)*

### 17.1 Soft Decisions

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | Frontend technology | **HTMX + server-rendered HTML** *(soft; hard for Phase 3)* | Matches the stdlib-only philosophy; React drags in a build pipeline the project will resent. |
| 2 | Shell execution sandbox | **Containers (Docker/Podman)** — *hard requirement, not soft* | See §6.5. A subprocess timeout is not a sandbox; `run_test` executes agent-authored code. |
| 4 | Dashboard real-time updates | **Server-Sent Events** *(soft; hard for Phase 3)* | One-way updates fit the need; stdlib-friendly; no websocket dependency. Polling as degraded fallback. |
| 5 | Notification delivery | **Console (built-in) + Slack webhook + SMTP email (external process adapters)** *(soft)* | Port once, two thin adapters; config enables zero or more sinks. Closed 2026-07-09 with Phase 3 plan. |
| 6 | SSO scope | **OIDC-only MVP** *(soft)* | SAML remains a documented external adapter (spec §3.3 / §4.5) but is **not implemented** in Phase 3. Local username/password mode exists for dev/test only. Closed 2026-07-09. |
| 7 | Agent output versioning | **Append `attempts/N/` directories; never overwrite** *(soft)* | Preserves retry history and gives adjudication feedback a home (§7.1). |
| 8 | Project/issue ID scheme | **Auto-increment integers** — *closed* | Decided de facto by Phase 1 code. |
| 10 | Local LLM integration | **OpenAI-compatible endpoint** (`base_url` on the OpenAI implementation) — *closed* | Decided de facto by Phase 2 code; covers llama.cpp, Ollama, and most gateways. |
| 11 | Adapter binary discovery | **Explicit registry in config** *(soft)* | Scanning a directory and spawning whatever executables appear there is a supply-chain risk. See §4.3. |
| 9 | YAML agent config schema | **Merge layers:** built-in defaults → global `default_model` → global `agents.<type>` → **project flavor** → **frozen issue cast** → orchestrator policy. Fields: model, temperature, max_tokens, system_prompt / system_prompt_append, core tool subset, mcp_servers, adjudication, token_budget (stored Phase 4; enforced Phase 5). **Named flavors** of core types only (no free-form agent types). Submit selects a flavor per stage when `len(flavors) > 1`. *(hard for Phase 4 project refactor)* | Casting thesis; extended 2026-07-10. See §8.2 and `phase_4_project_refactor.md`. |
| 12 | Adapter authentication | **Adapters own backend credentials** via process env and/or adapter-local config. Core never puts secrets in agent-visible artifacts. *(soft; hard for Phase 4)* | Matches Slack/email; applies to S3/Jira/GitHub. Closed 2026-07-09 with Phase 4 plan. |
| 13 | Git commit strategy | **Single commit per implementer run** *(soft; hard for Phase 4)* | Granular commits are dashboard polish, not MVP. Revisit if review workflows demand it. |
| 15 | Test command secrets | **Maintainer-only secrets config, injected into the test container environment, never rendered into any agent-readable artifact** *(soft)* | `task.json` is agent-readable; secrets must never flow through it. See §6.5. |
| 16 | Project lifecycle & config authority | **YAML-only project registration.** Upsert registry at process start from `projects:` map. Submit/CLI/API/triggers hard-fail on unknown project. No GetOrCreate. `source_path`/git/test/agents live in project YAML, not the submit form. *(hard for Phase 4 project refactor)* | Projects are administrative entities (git, budgets, casting), not accidental strings. Membership/invites deferred. See §6.0 and `phase_4_project_refactor.md`. Closed 2026-07-10. |

### 17.2 Still Open

| # | Question | Notes |
|---|----------|-------|
| 3 | Codebase search tool | Pure-Go grep is implemented; tree-sitter / vector semantic search remain candidates for later phases (not Phase 4). |
| 14 | Workspace cleanup / retention | Auto-delete vs. archive for audit — decide by Phase 6. |
| 17 | Project membership & invites | Deferred. Global roles only until multi-user SSO/email invite work. When landed: YAML (or admin UI writing the same model) scopes members/viewers per project; admin still sees all. |

---

*End of Specification Document*
