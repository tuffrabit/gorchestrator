# AI Agent Orchestration System — Product Specification

> **Session Date:** 2026-07-04 (original planning) · **Last Revised:** 2026-09-24 (user-selected agent flows)  
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
| 2026-07-10 | **Issue input + multi-phase artifacts (post Phase 4 polish).** Optional issue **description** + text-like **attachments** at submit (`issue.md` + `attachments/` on disk; description also in SQLite). Agent context inlines title/description and lists attachment paths. Artifact drawer is **phase-scoped** (Research / Plan / Implementation tabs); implementer **Workspace** tree with per-file expand diffs; **workspace.zip** download when implementation is `done`. Full unified Diff tab removed. See §7.1, §8.2.5, §8.3, §11.5. |
| 2026-07-12 | **Phase 5 Guardrails landed.** Provider session token budgets (context-window gate, not wallet); planner effort gate; scope hold at submit; MCP per-tool grants + constraints; YAML escalation with dedupe; admin `/permissions` and `/escalation` read-only pages. See §13, `phase_5.md`. |
| 2026-09-24 | **User-selected agent handoff flows.** The fixed researcher → planner → implementer pipeline and the per-project agent **flavor** system are replaced by user-defined agent flows: at submit the user picks an ordered list of agent ids (1..N, cap 8) from the top-level `agents:` config block; each agent has a **required** `system_prompt` and gets the **full core tool list** by default unless it narrows via `tools:`; the planner effort gate is **removed** — the per-step human adjudicator is the safety mechanism; phase dirs become `step-1`..`step-N` with an issue-level `workspace/`; legacy issues (empty `pipeline_json`) keep their `research`/`plan`/`implementation` dirs forever. The frozen flow persists as `pipeline_json` on the issue row (`agent_flavors_json` remains for legacy reads only). **Breaking config change:** `projects.<name>.agents`, `system_prompt_append`, and `guardrails.effort_gate_min` are rejected at load with a clear error. See §5.2, §6.0, §7.1, §8.1–§8.3, §11.5, §13.3. |

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
- **Secure by default.** Agents do not get raw filesystem, shell, or git access — only typed, orchestrator-mediated tools whose paths resolve against a hard allowlist. Every agent receives the **full core tool list** by default; per-agent narrowing is explicit user configuration (`tools:`), not a runtime permissions layer. See §5.2.

---

## 3. Architecture Overview

### 3.1 High-Level Pipeline

```
Trigger → Step 1 (agent A) → [Adjudicate] → Step 2 (agent B) → [Adjudicate] → … → Step N → Done
```

The ordered list of agents (the **flow**) is chosen by the user at submit from the agent ids defined under `agents:` in YAML (§8.2) and frozen on the issue. Each handoff boundary is configurable. Adjudication is optional and pluggable.

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

Security is not a permissions layer bolted on top — it is the architecture itself. Agents do not get raw filesystem, shell, or git access. They receive orchestrator-mediated tools that are scoped by design: a path allowlist, typed tools only, and an immutable container-isolated test command. Per-agent tool differences are **explicit configuration** (`tools:` narrowing, §8.2), not runtime permission grants.

### 5.2 Core Tool List (Default Full; User-Narrowed)

*(Revised 2026-09-24. Supersedes the per-agent-type Core Tool Matrix.)*

There is **one core tool registry**, and every agent receives the **full list by default** — an agent whose YAML omits `tools:` can read, search, write its output, **and edit the issue workspace**:

| Tool | In default list | Description |
|------|-----------------|-------------|
| `read_file` | ✅ | Two modes: whole-file (subject to a configurable cap) or surgical line-range read. Path resolved by orchestrator against allowlist. See §12.1. |
| `list_directory` | ✅ | List contents of a directory. |
| `grep_search` | ✅ | Search file contents via pattern matching. |
| `write_output` | ✅ | Write to the agent's designated `output.*` file in the current step's attempt directory. Orchestrator resolves path; agent does not know the filesystem layout. |
| `write_file` | ✅ | Write a file within the issue workspace (§6.4). Path is workspace-relative; orchestrator resolves to absolute path. |
| `update_file` | ✅ | Update (patch/overwrite) a file within the issue workspace. |
| `run_test` | ✅ *conditional* | Execute the project's pre-configured test command in a sandboxed subprocess with timeout. Registered **only when the project has a `test:` command configured** (§6.5). |
| `bash` / `shell_exec` | ❌ | **Not available to any agent.** |

**Narrowing is the user's job via `tools:`:** an agent entry under `agents:` may set an explicit `tools:` allowlist (e.g. `tools: [read_file, list_directory, grep_search, write_output]` for a research-only agent). Unknown tool names are **rejected at config load** (fail closed). MCP tools remain opt-in per agent (`mcp_servers`, §5.5) and are never in the default list.

**Per-role capability isolation is gone by design.** The old per-type matrix ("a hijacked researcher can only write its output file") was a capability boundary; with a default-full toolset **any step can edit the shared issue workspace**, and consecutive editing agents can overwrite each other's work. The remaining boundaries, which hold for every agent regardless of `tools:`:

- **Path allowlist** — tool paths resolve only inside the issue dir, the read-only source snapshot, and the issue workspace (§5.4).
- **No shell** — there is no `bash`/`shell_exec`; `run_test` is the only code execution and it is container-isolated with an immutable project-configured command (§6.5).
- **MCP deny-by-default** — external server tools are granted per agent, never by default (§5.5).

Blast-radius control is therefore the user's: pick the flow, narrow `tools:` where a role must not mutate, and keep adjudication human at the boundaries that matter (§9.1).

**`write_output` principle:** `write_output` is an orchestrator-provided tool that **only** writes to the current step's `output.*` path in the attempt directory. The agent does not know the path; the orchestrator resolves it. This is not a "permission" — it is a completely different tool with a single hardcoded destination.

### 5.3 No Shell Access

Agents do not have `bash` or `shell_exec` tools. All operations are mediated through typed tool calls. The only exception is `run_test`, which executes a **pre-configured, immutable command** defined in project configuration. The agent sees stdout/stderr output but cannot modify the command.

Note that command immutability alone is *not* a security boundary — the command executes code an editing step just wrote. See §6.5 for why `run_test` must be treated as arbitrary code execution and container-isolated accordingly.

### 5.4 Path Resolution & Allowlist

The orchestrator maintains an allowlist of resolvable paths for every step, **identical for all agents** — capability differences come from `tools:` (§5.2), not from the path allowlist:

- The issue directory: the read-only source snapshot (§6.6), the issue-level `workspace/` when it exists (§6.4), and the previous steps' accepted output paths (as named in `task.json`).
- Any paths explicitly provided in `task.json`.

Source access matters: without it, an early exploration step has nothing to investigate and produces hallucinated findings. It is part of Phase 2 (§6.6), not deferred to the Phase 4 git model.

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

**Phase 5 (landed):**
- Per-server **tool allowlists** under global `mcp_servers` (empty tools = all tools from that server — Phase 4 compat)
- Simple call-time **constraints** on allowed tools (`arg_prefix`, `arg_deny_substring`, `url_allowlist`)
- Agent still allowlists **servers only** (`mcp_servers: [name]`); tools/constraints come from the global server entry
- Admin read-only **Permissions** page (`/permissions`) for auditability

The MVP "all tools to all agents" posture is acceptable only while all pipeline input is human-authored. It must tighten before external triggers (GitHub/Jira) land — see §5.6.

### 5.6 Untrusted Input & Prompt Injection

The pipeline consumes untrusted text: issue titles/bodies arriving via external triggers (GitHub, Jira, email) and the contents of repository files the agents read. Any of it can contain instructions crafted to manipulate an agent. Combined with capable tools — especially MCP servers exposing database or API access — this is an exfiltration and abuse vector.

Mitigations, by architecture and by phase:
- Agents may hold editing tools by default (§5.2). The remaining boundaries are the path allowlist (§5.4), container-isolated `run_test` (§6.5), and MCP deny-by-default (§5.5). Narrow an agent's `tools:` when its role must not mutate the workspace.
- Adjudication gates are the primary human backstop. Human review before an editing step is the recommended posture for externally-triggered issues; the trigger layer forces `adjudicator: human` on any step whose effective tool list can edit files, for external sources, unless `trust_external` is set.
- Per-agent MCP server allowlists ship in Phase 4 alongside external triggers — not after.
- `run_test` container isolation (§6.5) bounds what a hijacked editing step can execute.
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
| `default_flow` | Optional ordered list of agent ids (from top-level `agents:`, §8.2) used to prefill the submit flow picker and as the fallback for CLI/webhook/adapter submits that carry no flow (§8.2.5) |
| `trust_external` | Optional override for untrusted-input editing-step gate (§5.6) |

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
    default_flow: [researcher, planner, coder]   # optional; ids from top-level agents:
    # trust_external: false
```

A project with a `default_flow` prefills the submit drawer's flow builder with it (still editable). With **no** `default_flow`, dashboard users must pick the flow by hand, and programmatic submits (CLI/webhook/adapter) that do not carry a flow are **rejected** — there is no silent fallback pipeline (§8.2.5). Agents themselves are global: they are defined once under the top-level `agents:` block (§8.2), not per project.

#### 6.0.4 Authorization (current vs deferred)

- **Current:** Global roles only (`admin` / `member` / `viewer`). All authenticated users see all YAML-registered projects. Sufficient for local/dev testing.
- **Deferred:** Per-project membership and email invites. When added, membership is expected to live in YAML (or an admin surface that writes YAML-equivalent config) and to filter list/submit/decide by project. Do not invent a second parallel config authority.

### 6.1 Git Philosophy

The core toolset is geared around a **targeted, pre-existing git repository** — editing steps work in an orchestrator-managed checkout of it (§6.4). No agent can create a new git repository or GitHub/Bitbucket project via core tools. Repository management is a human responsibility. Project registration (including git settings) is maintainer configuration (§6.0), not an agent or member action.

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

### 6.4 Issue Workspace Lifecycle (Orchestrator-Managed)

*(Revised 2026-09-24: the workspace is now **issue-level** and shared by every step that edits — not per-implementer-run.)*

The agent never touches git. The orchestrator handles the entire git lifecycle:

1. **Bare clone cache:** Per project at `{storage_root}/repos/{project_id}.git`. Fetch before each run. Validated at project registration (`git` on PATH; optional `gh`).
2. **Source worktree:** At issue creation, orchestrator creates a read-only worktree at `projects/{pid}/issues/{iid}/source/` checked out to `base_branch` (replaces the §6.6 snapshot copy when git is configured).
3. **Branch + issue workspace:** The mutable workspace lives at `projects/{pid}/issues/{iid}/workspace/` and is created **once per issue**, lazily, before the first step whose agent's effective tool list includes an editing tool (`write_file` / `update_file` / `run_test`):
   ```bash
   git worktree add -b ai-implementer/{issue_id} workspace <base>
   ```
   One branch per issue (`ai-implementer/{issue_id}`), not per run. Legacy issues keep reading `implementation/workspace` through a fallback helper — their on-disk layout is never migrated.
4. **Agent Execution:** Steps whose agent has editing tools receive `write_file`, `update_file` (and `run_test`) scoped to the issue workspace path; the agent sees relative paths and the orchestrator resolves them. Agents that are not meant to edit are controlled by `tools:` narrowing, **not** by the path allowlist (§5.4) — the allowlist admits the workspace for every step. Agents never run git.
5. **Post-Execution:** After **each step whose result is `done`**, the orchestrator stages changes and commits on the issue branch (a no-op when there is nothing to commit), optionally pushes (`git.push`, default false), optionally opens a PR via `gh` (`git.create_pr`, default false). The human decides whether to merge. Per-step commits mean an intermediate `waiting_human` hold shows exactly what the agent wrote.

**Local-only projects:** If `git.repo_url` is unset but `source_path` is set, the Phase 2 snapshot path is retained.

**Parallelism:** One workspace and one branch per issue. Parallel *issues* have distinct workspaces and branches — no collision. The orchestrator tracks `workspace_id` and `branch_name` in SQLite.

### 6.5 Test Execution (`run_test` Tool)

`run_test` is a core tool available to **any agent whose effective tool list includes it** (the default full list does; §5.2). It executes the project's pre-configured test command in a sandboxed subprocess. It is registered **only when the project has a `test:` block with a non-empty command**.

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
- Executed against the issue workspace
- Agent receives stdout/stderr (size-capped) but cannot modify the command
- Enables test-and-fix loops in any step that holds the tool, without opening arbitrary shell access

**Security posture (revised 2026-07-08).** An immutable command is *not* a security boundary: the command executes code an editing step just wrote, so a hostile or hijacked agent can put anything in a test file — read env vars, hit the network, escape the workspace. `run_test` is arbitrary code execution by proxy and must be treated as such:

- Execution **must** be container-isolated (Docker/Podman): no network, workspace-only mount, CPU/memory/time limits. This is a Phase 4 acceptance criterion, not an optimization.
- A bare subprocess with a timeout is **not** an acceptable fallback on shared or credentialed hosts. If no container runtime is available, `run_test` refuses to run rather than degrading.
- Test-environment secrets are injected from maintainer-only configuration into the container environment and are never written to any agent-readable artifact (see §17 Q15).

### 6.6 Phase 2 Interim: Read-Only Source Snapshot

The full git workspace model above lands in Phase 4. From Phase 2 onward, the orchestrator provides read-only source access so research, planning, and implementation operate on the real codebase instead of a vacuum:

- Per project, YAML configuration points at a local `source_path` (or git settings — §6.0 / §6.3). Source is **not** supplied at issue submit time.
- On issue creation, the orchestrator copies `source_path` to `projects/{pid}/issues/{iid}/source/` (excluding `.git`) as an immutable snapshot when git is not configured.
- The snapshot is in every agent's read allowlist. The issue-level `workspace/` is seeded from the snapshot (§6.4) when the first editing step prepares it, so edits land on real code.
- Phase 4 replaces snapshot copies with git-managed branch checkouts when `git.repo_url` is set (and eliminates the per-issue copy overhead).

---

## 7. Filesystem Artifact Contract

### 7.1 Directory Structure (Per Issue)

```
projects/{project_id}/issues/{issue_id}/
├── issue.md                 ← orchestrator writes at submit: title + optional description + attachment index
├── attachments/             ← optional user-uploaded text-like context files (extension-gated at upload)
├── source/                  ← read-only project source snapshot / source worktree (orchestrator-created; §6.6)
├── workspace/               ← issue-level mutable tree, shared by every step that edits (§6.4)
├── step-1/                  ← one directory per flow step; the step key is "step-N"
│   ├── task.json            ← orchestrator writes: instructions, model config, loop/adjudication config
│   ├── result.json          ← orchestrator writes: status envelope (at phase START and completion; §9.4)
│   ├── events.jsonl         ← orchestrator writes: append-only run transcript (model turns, tool calls, usage)
│   └── attempts/
│       ├── 1/
│       │   ├── output.*     ← agent writes free-form content: .md, .json, .py, .xlsx, etc.
│       │   └── feedback.md  ← orchestrator writes adjudicator feedback if this attempt was rejected
│       └── 2/ ...           ← one directory per adjudication attempt; retries never overwrite
├── step-2/
│   └── (same structure)
└── step-N/
    └── (same structure)
```

*(Revised 2026-07-08: added `source/`, `events.jsonl`, and the `attempts/` layout; removed the separate `adjudication/` phase directory — adjudication is a boundary evaluation (§9), not a phase, and its record lives in `feedback.md` + SQLite decisions. Revised 2026-07-10: `issue.md` + `attachments/` for submit-time human context. Revised 2026-09-24: user-selected flows — phase dirs are `step-1`..`step-N` from the issue's frozen flow, and `workspace/` moves to issue level because any step may edit. **Legacy issues** (created before this change: `agent_flavors_json` set, `pipeline_json` empty) keep their `research/`, `plan/`, `implementation/` dirs — including `implementation/workspace/` — forever; they are read through a fallback helper and never migrated on disk.)*

### 7.2 The Minimal Contract

- **issue.md** — Orchestrator writes at issue create (submit / CLI / webhook / trigger). Contains the title, optional description body, and a list of attachment paths. Dual-written with the SQLite `issues.description` column in one `persistIssueContext` flow so they cannot drift. Agents receive the description **inlined** in the user message and may also `read_file` this path.
- **attachments/** — Optional text-like files uploaded at submit (extension allowlist only at upload time — no content sniffing). Basenames are sanitized; agents get attachment paths listed in context and may open them with tools. Not copied into the issue workspace.
- **task.json** — Orchestrator writes. Contains: agent id, system prompt, model config, tool list, adjudication/loop config, input context (paths to `issue.md`, attachments, and the previous step's accepted output), readable path allowlist. **Never contains secrets** — it is agent-readable.
- **result.json** — Orchestrator writes **at phase start** (`status: in_progress`) and again on completion. Contains: status, error message (if any), attempt count, loop count, tokens consumed, duration, done_rationale (self adjudication only), pointer to the latest attempt, timestamp. Writing it at phase start is what makes crash detection possible (§9.4).
- **events.jsonl** — Orchestrator writes, append-only, one JSON object per line: model turns, tool calls and results (size-capped), and per-call token usage. This is the substrate for the Phase 3 activity stream, token accounting, debugging, and the audit trail. Without it, agent runs are black boxes.
- **output.*** — Agent writes, into the current attempt directory. **Completely free-form.** Content is determined by the agent's system prompt and the underlying LLM. The orchestrator never parses this file. The next agent receives the content of the accepted attempt's output as input context.
- **feedback.md** — Orchestrator writes into an attempt directory when an adjudicator rejects it: the decision and the feedback/rationale. The next attempt receives this content in its input context (§8.3).
- **workspace/** — Issue-level mutable deliverable tree, shared by every editing step (§6.4; legacy issues: `implementation/workspace/`). When the issue is `done` — or the last step's `result.json` status is `done` — members/viewers may download the full tree as `GET /api/issues/{id}/workspace.zip`.

**Critical rule:** No agent reads or parses `result.json` or `events.jsonl`. Only the orchestrator manages status. Agents only read their `task.json` inputs and write their `output.*`.

**Authority rule:** where filesystem and SQLite disagree, **the filesystem is authoritative**. SQLite is a queryable index over filesystem truth, reconciled at startup (§9.4, §10.3). Exception for convenience: issue **description** is dual-written at submit only via a single orchestrator path (DB + `issue.md`); it is not independently edited after create in MVP.

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

### 8.1 Agent Identities (User-Defined)

*(Revised 2026-09-24. Supersedes "Base Agent Identities (Baked into Go Code)" — there are no built-in agent types anymore.)*

An **agent** is a YAML entry under the top-level `agents:` block — not a Go struct, not a baked-in role. Its key is an **agent id**: a user-chosen slug, validated at load (`^[a-z0-9][a-z0-9_-]{0,31}$`, lowercase; case-insensitive duplicates rejected). `researcher`, `planner`, and `implementer` remain legal ids but are nothing special — examples, not magic words. Casting (which model plays which role, with which tools and prompt) is entirely the user's configuration. Typical ids a user might define, purely by convention: `researcher` (deep investigation), `planner` (structured decomposition), `coder` (code generation), `reviewer` (output critique). The system imposes no roles and no fixed pipeline — a submit fails only if the picked flow references an id not configured under `agents:` (§8.2.4).

### 8.2 Agent Configuration (YAML) — User-Defined Agent Ids, Required System Prompts, Default-Full Toolset

*(Revised 2026-09-24. Replaces the global/flavor/cast layering: flavors are gone; agent definitions are global under top-level `agents:`; the per-issue choice is the **flow**, frozen at submit — §8.2.4.)*

```yaml
agents:
  researcher:
    system_prompt: "You are a context compiler agent..."   # REQUIRED — no built-in default prompt
    model: { provider: openai, model: o3-mini }
    adjudicator: human          # default
  planner:
    system_prompt: "You are a planning agent..."
    tools: [read_file, list_directory, grep_search, write_output]   # OPTIONAL narrowing; omit = full core list (§5.2)
  coder:
    system_prompt: "You are an implementer..."
    mcp_servers: []             # optional; default = no MCP. ["*"] = every configured server
  reviewer:
    system_prompt: "You are a reviewer..."
    single_shot: true           # allowed for ANY agent
```

Removed from YAML — leftover keys are rejected at load as unknown fields (breaking config change; the load error names the offending key): `projects.<name>.agents`, `agents.<id>.system_prompt_append`, `projects.<name>.guardrails.effort_gate_min` (§13.3).

#### 8.2.1 Configurable fields (per agent)

- `system_prompt` (**required**) — the agent's instruction. There is no built-in default prompt; load fails with `agents.<id>: system_prompt is required` when it is missing or empty.
- LLM model/provider (and `base_url` / `api_key_env` where applicable); defaults to top-level `default_model`
- Temperature, max tokens
- **Token budgets are provider-scoped** (top-level `providers.<name>.token_budget`) — not per-agent fields (agent `token_budget` rejected at config load). See §13.2
- `tools` — optional core-tool allowlist; **empty/omitted = the full core list** (§5.2). Unknown tool names are rejected at load.
- MCP server allowlist (`mcp_servers`; the special value `"*"` grants every configured server)
- Boundary configuration: `adjudicator`, `max_attempts`, `loops`, `rubric` (§9.1)
- Single-shot execution (`single_shot`, **any** agent): one no-tools completion whose reply text is the step output; repo context is pre-stuffed via `single_shot_context_bytes` (auto digest budget) or `context_files` (explicit paths). A single-shot step receives **no tools at all**, so it can never edit the workspace even if its `tools:` list names editing tools. Design: `gorchestrator-local-model-swap-plan.md`

#### 8.2.2 Agent ids

- Match `^[a-z0-9][a-z0-9_-]{0,31}$`, lowercase; reject duplicates that differ only by case.
- Any number of agents (one is fine); ids are arbitrary slugs (`coder`, `review_1`, …).
- Agent ids are **global** — defined once under top-level `agents:` and usable by every project. Projects do not redefine agents; a project may only name a `default_flow` of existing ids (§6.0).

#### 8.2.3 Merge order

Resolved **per step** when building `task.json` / the agent run:

1. Generic defaults (model from top-level `default_model`, `adjudicator: human`, `max_attempts: 3`, `loops: 1`, default rubric)
2. Global `agents.<id>` entry
3. Orchestrator policy overrides (external trigger + editing-capable step → force `adjudicator: human` unless `triggers.trust_external` / `projects.<name>.trust_external` — §5.6)

There is no per-project overlay and no built-in prompt layer. What is frozen per issue is only the **flow** (§8.2.4) — the ordered agent ids. Agent config (prompt/model/tools for a given id) is looked up from the loaded config whenever a step runs, so editing `agents:` and restarting affects subsequent steps of in-flight issues, but never changes which agents those steps are.

#### 8.2.4 Frozen flow (frozen at submit)

At issue creation the submitter's ordered agent picks are persisted on the issue row as `pipeline_json` — a JSON array of agent ids:

```json
["researcher","planner","coder"]
```

- `current_phase` on insert is `step-1` (§7.1).
- CLI/API/webhook/adapter submits may omit the flow; it then resolves to `projects.<name>.default_flow`, or the submit is **rejected** with a clear error (`no agent flow: pick agents in the UI or set projects.<name>.default_flow`). There is no silent fallback pipeline.
- Every id must exist under `agents:` → hard error at submit naming the offending id and listing the configured ids.
- Flow length 1..8.
- Retries, crash recovery, and resume use the same frozen flow.
- The legacy `agent_flavors_json` column remains on the issue row for **legacy reads only**: new issues write `{}` and never read it. **Legacy issue** = created before this change (`agent_flavors_json` set, `pipeline_json` empty) — such issues resolve to the fixed research/plan/implementation steps and keep their old phase dirs forever (§7.1).

#### 8.2.5 Submit UX (member)

See also §11.5. The New-issue drawer:

- **Project:** required select of YAML-registered projects only (no free text, no create-by-typing).
- **Title:** required short label (feed card, PR subjects).
- **Description:** optional longer textarea — the “meat” of the issue (acceptance criteria, constraints, problem statement). API/trigger field name is `body` (alias `description`).
- **Attachments:** optional multi-file upload of **text-like extensions only** (e.g. `.md`, `.txt`, `.log`, `.json`, `.yaml`, `.go`, …). Gate is extension-at-upload only (no content sniffing). Multipart form on the dashboard; CLI `--attach` / `--body-file` supported.
- **Flow:** the **flow builder** — an ordered list of agent selects (one per step, populated from the globally configured agent ids) with add/remove, prefilled from the project's `default_flow` when one is set (still editable; an inline note says so). Length 1..8. This replaces the old per-stage flavor selects.
- **Dry run:** checkbox.
- **Source path:** **not present** (project YAML only).

### 8.3 Context Flow

The default context recipe for each step is:

1. **Issue title** (always).
2. **Issue description** inlined when present (optional).
3. **Attachment paths** listed when present (`attachments/{name}` — agents open with `read_file` / `list_directory`; large files are not dumped wholesale into the first turn).
4. **Immediately-previous step’s accepted `output.*`** in full. The first step receives only (1)–(3).

*(Revised 2026-09-24: input is exactly the immediately previous step's accepted output — not a transcript of all previous steps.)*

`task.json` `input_context_paths` includes `issue.md`, each attachment key, and the previous step's accepted output path. Per-agent config may add further inputs — e.g., give a later step an earlier step's output as well. No automatic summarization or distillation by the orchestrator — the human's job is to configure agents such that Agent N's output is useful as Agent N+1's input.

Webhook / external triggers already carry a `body` field; it is persisted as the issue description the same way as the dashboard (no longer dropped at the `SubmitIssue` boundary).

Two additional context rules (revised 2026-07-08):

- **Refinement loops feed forward.** When an agent runs multiple loops within an attempt, loop *i* receives loop *i−1*'s output as context. Fresh-context loops that overwrite each other are pure token burn; the loop mechanism exists for iterative refinement.
- **Retries see why they failed.** When an adjudicator rejects an attempt, the next attempt's context includes the rejected `output.*` and the adjudicator's `feedback.md`. A blind retry discards the most valuable signal a human gate produces.

---

## 9. Handoff & Adjudication (Unified Model)

*(Revised 2026-07-08: the previously separate "handoff modes" — `n_loops` / `self_done` / `human_gate` — and "adjudicators" — null / self / human — overlapped almost 1:1 and produced ambiguous configurations ("`loop_mode: self_done` with `adjudicator: human` — what happens?"). They are merged into a single axis.)*

### 9.1 The Boundary Model

Every step boundary is configured with exactly three settings — a **step** is one position in the issue's frozen flow (§8.2.4); the storage/engine vocabulary still says "phase", and the phase *name* is the step key (`step-1`…`step-N`; legacy: `research`/`plan`/`implementation`):

| Setting | Meaning | Default |
|---------|---------|---------|
| `adjudicator` | Who decides whether the step output is accepted: `null`, `self`, `human` *(future: `agent`)* | `human` |
| `max_attempts` | Maximum adjudication attempts before the step is marked `failed` | 3 |
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
- Issue queue (issue_id, project_id, title, status, current_phase, created_at, updated_at, dry_run, source, external_id, **pipeline_json** frozen flow — §8.2.4; legacy **agent_flavors_json** retained for legacy reads only, never written for new issues)
- Agent run history (run_id, issue_id, agent_type — stores the agent id, model, tokens_used, duration, result_status, timestamp, workspace_id, branch_name)
- User/team accounts, roles, SSO mappings (global roles today; project membership deferred — §6.0.4)
- Audit log (user_id, action, target_issue, timestamp, details)
- Notification queue (notification_id, issue_id, agent_type, status, recipient, sent_at)
- Human decision queue (decision_id, issue_id, phase, requested_at, decided_at, decision, **feedback**, decided_by)

### 10.2 Filesystem (Agent Memory & Artifacts)

- Agent output directories (`step-1/` … `step-N/`; legacy issues: `research/`, `plan/`, `implementation/`)
- Agent free-form outputs (`attempts/N/output.*` files) and adjudication feedback (`attempts/N/feedback.md`)
- Read-only source snapshots (`source/`, §6.6)
- Issue workspace directories (`workspace/` under the issue dir; legacy fallback `implementation/workspace/` — §6.4)
- Per-phase run transcripts (`events.jsonl`)
- Configuration files (YAML) — **source of truth for projects, agents, server, auth, adapters**
- Logs (optional, per-project)
- Dashboard static assets

### 10.3 Authority & Concurrency (added 2026-07-08)

- **Filesystem is authoritative** for phase/agent state. SQLite is a queryable index over filesystem truth, reconciled at startup (§9.4). When they disagree, the filesystem wins. (Both store status; a crash between the two writes can make them diverge — this rule resolves it.)
- **YAML is authoritative** for project definition and agent definitions (§6.0, §8.2). SQLite project rows are a synced index; issue rows hold the **frozen flow** (`pipeline_json`) chosen at submit so runs remain stable if YAML later changes.
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
- **Artifact viewer:** rendered Markdown, syntax-highlighted code/JSON, activity from `events.jsonl`, per-step artifacts and the issue-level Workspace tree — primarily in a **slide-out drawer** (§11.5)
- **Adjudication UI:** pass/fail/retry **plus a first-class feedback text field** on the **expanded issue card**, available at any handoff boundary regardless of configuration (§9.2, §9.3)
- **Token burn display:** per-run and cumulative — from `runs` / `events.jsonl`
- **Notification center:** pending human gates, admin alerts on failures
- **Submit issue:** from the top bar (member+), not from a board column — project select from YAML registry plus the agent **flow builder** (ordered picks from the configured agent ids; §8.2.5, §11.5)

Layout, visual language, and interaction details are normative in **§11.5**.

### 11.4 Admin Features

- Admin users always receive notifications on "bad" agent output (errors, timeouts, exceptions, empty outputs)
- Configurable admin escalation rules (YAML + read-only `/escalation` page — Phase 5 landed)
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
| **Expanded card** | Inline summary of current phase + truncated `result.json` fields; optional **description** + **attachment list**; adjudication |
| **Artifact drawer** | Right-hand slide-out: **step-scoped** Result / Output / Activity for any step of the issue's flow (`1 · scout`, `2 · coder`, …), plus the issue-level Workspace tree |
| **Submit drawer** | Right-hand slide-out form opened from **New issue**: project select (YAML-registered only), title, optional description + text attachments, dry-run, and the **flow builder** — ordered agent selects prefilled from `projects.<name>.default_flow` when set (§8.2.5). No free-text project. No source path field. |
| **Notifications** (`/notifications`) | Pending human gates + recent notification rows (same dark shell) |
| **Login** (`/login`) | Minimal centered card; no marketing chrome |

Deep link: `/issues/{id}` renders the feed with that card **pre-expanded** (and optional `?drawer=result|output|activity&phase=step-1|step-2|…` to open the artifact drawer on load; legacy issues use their legacy phase names). Legacy `?drawer=diff` maps to the Workspace tab. There is no separate full-page issue detail layout — expansion + drawer *are* the detail experience.

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

1. **Phase strip** — the issue's frozen flow, one entry per step (`1 · scout`, `2 · coder`, …); completed = check, current = neon-pink pulse/dot, future = dim (display of pipeline state; step selection for artifacts lives in the drawer).
2. **Result summary** — truncated fields from the current step's `result.json`: `status`, `attempt`, `loop_count`, `tokens_used`, `duration_ms`, `error` / `done_rationale` (one short paragraph max; overflow ellipsis).
3. **Issue description** (when present) and **attachment basenames** (when present).
4. **Artifact actions** — text buttons/links open the drawer for the **current step** by default:
   - **result.json** → Result tab
   - **output** → Output tab (the step's accepted output markdown)
   - **activity** → Activity (`events.jsonl`)
   - **workspace** → Workspace tab (issue-level tree, per-file diffs)
5. **Adjudication block** (member+; always shown for the current phase when a decision is meaningful per §9.3 — especially `waiting_human`, and for manual intervene):
   - Feedback **textarea** first-class (placeholder encourages “why”)
   - **Pass** / **Fail** / **Retry** buttons
   - Empty feedback on Fail/Retry: client-side warning, still submittable; server may echo a warning
   - Viewer role: block visible but controls disabled with short explanation

Live updates: when SSE reports a status/phase change for an expanded card, the header chips, tint, phase strip, and summary refresh in place (HTMX swap of the card partial).

#### 11.5.4 Slide-out drawer (artifacts & submit)

- **Position:** fixed to the **right**, full viewport height, width ~min(520px, 92vw) on desktop; near-full width on small viewports.
- **Behavior:** slides in over a dimmed scrim; **Esc** or scrim click or ✕ closes; body scroll lock while open; focus trapped while open.
- **Step tabs (artifact drawer):** one tab per flow step, labeled `N · <agent-id>` (legacy issues show their legacy phase names) — always visible; switching step keeps the content tab. Opening the drawer without an explicit step lands on the issue’s **current phase**.
- **Content tabs (artifact drawer):** `Result` | `Output` | `Activity` for every step. The **Workspace** tab is dedicated and issue-level (directory tree of `workspace/`, changed files marked; expand a file to lazy-load a **per-file** source-vs-workspace diff); it is present whenever a workspace exists for the issue, independent of step selection. There is **no** full unified Diff tab (removed — per-file tree is the model).
- **Download:** when the issue is `done` (or the last step's `result.json` status is `done`), the Workspace view shows **Download workspace (.zip)** (`GET /api/issues/{id}/workspace.zip`, full tree, viewer+). Not available before then.
- Content is server-rendered HTML partials (goldmark for Markdown; `<pre>` + highlight.js for JSON/code). Large payloads: size-cap with “truncated” notice.
- **Submit drawer:** form fields only (no tabs); multipart-capable; success closes drawer and inserts/refreshes the new card at the top of the feed via HTMX. Fields:
  - **Project** — `<select>` of registered projects (required). Changing project may HTMX-refresh the flow fields for that project.
  - **Title** — required text.
  - **Description** — optional textarea.
  - **Attachments** — optional multi-file (text-like extensions only).
  - **Agent flow** — the **flow builder**: one `<select>` per step (`flow_agent`, ordered, populated from the globally configured agent ids) with **+ Add agent** / **Remove**, max 8 steps. Prefilled from the project's `default_flow` when one is set (still editable; an inline note says so). An empty flow fails submit with a clear error.
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

Small, native Go toolset. **Every agent receives the full list by default** (§5.2); an agent's `tools:` allowlist narrows it:

| Tool | Availability | Description |
|------|-------------|-------------|
| `read_file` | All agents (default) | Two explicit modes. **(1) Whole-file:** no range args — returns the full file, subject to a **configurable cap** (default 64KB / ~2,000 lines, whichever first) with an explicit truncation marker and total line count so the agent knows to switch modes. **(2) Surgical:** line-number range (offset + limit) — the intended follow-up to `grep_search`, which returns file + line numbers precisely so agents can read just the relevant region instead of whole files. The tool description teaches this grep → targeted-read workflow to the model. Paths resolved against orchestrator allowlist. |
| `list_directory` | All agents (default) | List directory contents. |
| `grep_search` | All agents (default) | Pattern search across files. Respects `.gitignore`, skips binaries, caps result count. |
| `write_output` | All agents (default) | Write to the agent's designated `output.*` file. Orchestrator resolves path. |
| `write_file` | All agents (default) | Write a file within the issue workspace. |
| `update_file` | All agents (default) | Update/patch a file within the issue workspace. |
| `run_test` | All agents, only when the project has a `test:` command (§6.5) | Execute pre-configured test command in sandboxed subprocess. |

### 12.2 MCP Adapter Port

- Users can connect any MCP server for custom tools (stdio in Phase 4; streamable HTTP later if needed)
- MCP servers are declared in project/global config (**deny by default**)
- Core implements the MCP client (official Go SDK); tools from a server are discovered at connect time for schemas
- **Phase 4:** Per-agent, per-**server** allowlists — an agent only receives tools from servers listed in its `mcp_servers` config. If a server is allowed, all of its tools are exposed (name-prefixed to avoid collisions)
- **Phase 5 (landed):** Per-server tool allowlists + simple constraints under global `mcp_servers`; agent still lists servers only. See §5.5 / §13.5
- MCP tool calls are recorded in `events.jsonl` like core tools

---

## 13. Guardrails (Phase 5)

*(Landed 2026-07-12. See `phase_5.md`. Product intent: human casting chooses cost; orchestrator bounds runaway sessions and inserts cheap human holds — not a spend/wallet system.)*

### 13.1 "Bad Output" Definition (MVP)

- LLM API errors or timeouts
- Agent exceptions during tool execution
- Configuration errors or missing required config
- Empty output: an attempt that produces neither a `write_output` call nor final model text is a **failed loop**, enforced at the orchestrator — not a silent success flagged later by heuristic
- Session token budget breach (`budget_exceeded`) — hard mid-phase stop

### 13.2 Token Budgets (provider session / context-window gate)

- **Config axis:** top-level `providers.<name>.token_budget` (+ optional `warn_pct`, default 80). Missing/zero → unlimited.
- **Not** a wallet: no issue total, project total, or per-agent `token_budget` fields. Agent-level `token_budget` is rejected at config load.
- **Unit:** per issue × **per step run**. Each step that uses provider `P` gets a **fresh** ceiling of `providers.P.token_budget` (new ADK session / context window). Counters do not accumulate across steps.
- **Enforcement:** wrap `model.LLM` so each `GenerateContent` checks spent vs ceiling **before** the call; rehydrate spent from phase `events.jsonl` usage rows (per-call). Breach → phase `failed` with `budget_exceeded`; admin notify.
- **Resume:** human Decide may set absolute per-provider ceilings on the issue (`budget_overrides_json`); replaces the provider default for remaining/retried phases on that issue.
- Warning at `warn_pct` → at most one notify per phase attempt.

### 13.3 Effort Estimation — REMOVED

*(Removed 2026-09-24 in the user-selected-flow refactor.)*

The planner-only `effort` field and the `projects.<name>.guardrails.effort_gate_min` hold only made sense in a fixed pipeline where a "planner" always preceded "implementation". With user-defined flows there is no planner role to tag effort and no fixed implementation boundary to gate. **The replacement safety mechanism is the per-step human adjudicator** — `adjudicator: human` is the default (§9.1), so a human reviews each accepted step output before the flow advances, and the trigger layer additionally forces human adjudication on editing-capable steps of externally-sourced issues (§5.6).

What was deleted: the planner-only `effort` schema field and its parsing, `PhaseResult.Effort`, `maybeHoldForEffort` / all effort-hold plumbing, and the `guardrails:` project block (`effort_gate_min` was its only remaining key). What remains: **legacy issues never produce effort holds** — an in-flight legacy hold resumes as a normal human decision, and no new effort holds are ever created.

### 13.4 Scope Detection

- At submit (all entry points), after context + source prepare: heuristics over **title + description + attachment basenames + size-capped light reads** of small text attachments (forbidden phrases, extreme length).
- Flagged → issue `waiting_human` on the **first step** (`step-1`; legacy issues: `research`) — the flow does not start; reason in `result.json` + pending decision feedback (`scope: …`).
- Pass clears the first-step hold result (does **not** mark it done) and queues the flow.

### 13.5 MCP per-tool grants

- Global `mcp_servers[].tools[]` optional allowlist; empty = all tools from that server (Phase 4 compat).
- Optional simple constraints at call time: `arg_prefix`, `arg_deny_substring`, `url_allowlist`.
- Agents still list **server names** only. Admin `/permissions` is read-only audit UI (does not replace artifact drawer).

### 13.6 Admin escalation rules

- YAML `escalation.rules[]`: `when` ∈ consecutive_failures | budget_exceeded | sandbox_refused | phase_failed; project filter; threshold; notify admin|console.
- In-process dedupe: one fire per (rule, subject) until reset (e.g. success clears consecutive count). No alert storms.
- Admin `/escalation` read-only page: rules + recent fires this process.

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
- Real-time feed via SSE: card re-tint/chips; full artifacts in a **right slide-out drawer** (per-phase Result / Output / Activity; implementation Workspace tree) (§17 Q4; later refined 2026-07-10 — §11.5)
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
*(Superseded 2026-09-24: the flavor/cast model below was replaced by user-defined agent flows — global `agents:` ids, a per-issue frozen `pipeline_json`, and `default_flow` per project. Kept as the historical record of this phase.)*

- Projects declared only in YAML; registry upserted at process start; **no create-on-submit or CLI GetOrCreate** (§6.0, §17 Q16)
- Project blocks own `source_path` / `git` / `test` / `trust_external`; submit GUI no longer mutates project config *(the per-project `agents` block this phase introduced was removed again in 2026-09-24)*
- Named **flavors** of the three core agent types per project; merge layers include project flavor + frozen issue cast (§8.2, §17 Q9)
- Submit drawer: project select + conditional per-stage flavor selects; cast persisted on the issue
- **Out of scope here:** project membership, invites, SSO polish, admin config GUI
- **Deliverable:** Only YAML-registered projects accept work; multi-flavor projects expose casting at issue create; retries honor the frozen cast

### Post–Phase 4 polish (landed 2026-07-10; not a numbered phase)
**Goal:** Close dashboard and issue-input gaps that Phase 3/4 left incomplete so Phase 5 guardrails attach to a real human input model.

- Multi-phase artifact drawer (step tabs + Result/Output/Activity; issue-level Workspace tree with per-file diffs; `workspace.zip` when the issue is done) — §11.5 *(tab/workspace model revised 2026-09-24)*
- Optional issue **description** + text **attachments** at submit; `issue.md` + `attachments/` dual-write with SQLite description — §7.1, §8.2.5, §8.3
- Webhook/trigger `body` wired into description (no longer dropped)
- **Do not regress in Phase 5+:** plans that touch submit, context, retention, or the artifact drawer must preserve these contracts (see `phase_5.md` / `phase_6.md` “Landed foundations”)

### Phase 5: Guardrails — ✅ Complete (see `phase_5.md`)
**Goal:** Cheap human holds and hard mid-session stops so unattended volume cannot runaway. **Not** a spend wallet — casting chooses cost; provider budgets gate context-window scale per agent session.

- Scope detection at submit (title + description + attachments) → `waiting_human` before the flow starts
- Planner effort tag + project `guardrails.effort_gate_min` → hold before implementation *(removed 2026-09-24 — see §13.3; superseded by the per-step human adjudicator default)*
- Provider session token budgets (`providers.<name>.token_budget`) with mid-run hard stop + Decide override
- MCP per-tool allowlists + simple constraints; admin `/permissions`
- YAML escalation rules with dedupe; admin `/escalation`
- **Deliverable (met):** runaway work stopped at submit (scope), pre-implement (effort — since removed), and mid-session (budget); permissions and escalations auditable

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
| Agent identities as Go structs *(superseded 2026-09-24)* | Not a generic orchestrator. Core agent types are first-class citizens with baked-in behavior. **Superseded:** agents are user-defined YAML entries under top-level `agents:` with required system prompts — no identities are baked into Go code (§8.1). |
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
| `write_output` vs `write_file` | `write_output` writes only to the current step's designated output file via an orchestrator-resolved path. `write_file` / `update_file` write to the shared issue workspace. *(Revised 2026-09-24: which of these an agent holds is user configuration via `tools:` — no longer hard-wired by role.)* |
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
| Named agent flavors of core types only *(superseded 2026-09-24)* | Casting thesis without inventing free-form agent types. Go structs remain the only identities; flavors are named model/prompt overlays per project. **Superseded:** flavors gave way to user-defined agent ids under global `agents:` and per-issue frozen flows (§8.2). |
| Freeze cast on the issue at submit *(revised 2026-09-24)* | Changing project YAML mid-flight must not retarget in-progress issues. Retries and crash recovery reuse the same flavor names. **Revised:** what freezes on the issue is the **flow** (`pipeline_json`, ordered agent ids); agent config itself is re-read from current YAML per step. Legacy `agent_flavors_json` remains readable for pre-change issues only. |
| Source path off the submit form | Project configuration, not issue input. Future admin UI may edit it; members do not. |
| Project membership deferred | Local/dev testing uses global roles. Invites/SSO multi-tenant scoping land later without a second config authority. |
| Dashboard is a vertical expandable-card feed | Not a kanban board. Status-tinted cards, multi-expand, adjudication on the expanded card, full artifacts in a right drawer. Dark-only theme: blue-greys + neon pink accent (§11.5). |
| `cancelled` is a distinct status | Ctrl-C / shutdown is not a failure; it must not be reported as one. |
| Empty output fails the loop | An attempt with neither a `write_output` call nor final model text fails immediately rather than being marked done and flagged later. |
| Markdown docs are canonical | The spec is the living document; phase docs freeze at completion; HTML renderings are presentation companions (regenerate when MD changes). |
| Git worktree workspace model (Phase 4) | Bare clone cache + source/implementer worktrees replace per-issue file snapshots when git is configured; agents never run git. |
| Adapter credentials stay in adapter env | External adapters authenticate to their backends via their own env/config; core does not mediate secrets into agent artifacts (§17 Q12). |
| S3 first for cloud storage | Phase 4 ships S3 only; Azure Blob follows once the JSON-RPC storage pattern is proven. |
| MCP per-agent server allowlists with triggers | Per-server grants land in Phase 4 alongside external triggers (§5.6); per-tool grants + constraints landed Phase 5 (§5.5 / §13.5). |

**Added 2026-07-10 (issue input + multi-phase artifacts):**

| Decision | Rationale |
|----------|-----------|
| Title + optional description | Feed needs a short label; agents need the meat. Description is optional so webhook/CLI stay simple. |
| Description dual-write (SQLite + `issue.md`) | Single `persistIssueContext` path keeps UI and agent memory aligned; FS remains the agent-readable artifact. |
| Text-like attachments by extension only | Cheap upload gate; no content sniffing. Cheaters who rename binaries own the fallout. |
| Inline description + attachment paths in agent context | Matches §8.3; avoids stuffing large files into the first turn. |
| Phase-scoped artifact drawer | Completed research/plan artifacts must remain viewable after the pipeline advances; hard-wiring the drawer to `current_phase` was a bug. |
| Implementer Workspace tree + per-file diffs | Implementer has no `write_output`; the workspace *is* the deliverable. Full unified Diff tab removed as redundant. |
| `workspace.zip` only when implementation is done | Avoids partial downloads mid-run; available to viewer+. *(Revised 2026-09-24: gate is the issue done, or the last step's result done — §7.2.)* |

**Added 2026-09-24 (user-selected agent flows):**

| Decision | Rationale |
|----------|-----------|
| User-defined agent ids under global `agents:` | The fixed three-role pipeline and the per-project flavor layer encoded one team's workflow in Go code. User-chosen ids (validated slugs, arbitrary count) make the casting thesis fully configurable; `researcher`/`planner`/`implementer` remain legal ids but are no longer magic words (§8.1). |
| `system_prompt` required, no built-in prompts | A generic orchestrator cannot know the user's intent; a missing prompt is a config error, not a silent default (§8.2.1). |
| Default-full core toolset with optional `tools:` narrowing | Consistent, predictable, and impossible to widen by accident; narrowing is explicit, validated at load, and is the user's security dial (§5.2). Accepted trade-off: per-role capability isolation is gone by design; the remaining boundaries are the path allowlist, no shell, container-isolated `run_test`, and MCP deny-by-default. |
| Flow frozen on the issue as `pipeline_json` | Changing agent YAML must not retarget in-flight issues; retries, crash recovery, and resume reuse the same ordered agent ids (§8.2.4). `agent_flavors_json` stays readable for legacy issues only — no destructive migration. |
| Issue-level `workspace/` shared by all steps | Any step may edit, so the workspace can no longer live under one phase; prepared lazily before the first editing-capable step, committed after each `done` step, one branch per issue (§6.4). |
| Effort gate removed | Meaningful only in a fixed planner→implementer pipeline. The per-step human adjudicator default is the replacement safety mechanism (§13.3). |
| `projects.<name>.default_flow` as the only submit fallback | Webhook/adapter/CLI submits cannot run a UI picker; a named default keeps them working without silently inventing a pipeline (§6.0). |

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
| 9 | YAML agent config schema | **Agents are user-defined:** every agent is a key under the top-level `agents:` map (id = validated slug), with a **required** `system_prompt` and no built-in default prompt. **Merge layers:** generic defaults → global `agents.<id>` → orchestrator policy. Fields: model, temperature, max_tokens, `system_prompt`, `tools` (optional narrowing; empty = full core list), `mcp_servers`, adjudication fields. **Token budgets are provider-scoped** (`providers.<name>.token_budget` — Phase 5). Single-shot allowed for any agent. Projects may set `default_flow` (ordered agent ids) to prefill/fallback submits; the issue row freezes the chosen flow as `pipeline_json`. *(Revised 2026-09-24 — supersedes the 2026-07-10 flavor merge layers and the per-project `agents` block; see the revision log.)* | Casting thesis; revised 2026-09-24. See §8.2. |
| 12 | Adapter authentication | **Adapters own backend credentials** via process env and/or adapter-local config. Core never puts secrets in agent-visible artifacts. *(soft; hard for Phase 4)* | Matches Slack/email; applies to S3/Jira/GitHub. Closed 2026-07-09 with Phase 4 plan. |
| 13 | Git commit strategy | **Commit after each `done` step** on the per-issue branch *(revised 2026-09-24; was "single commit per implementer run")* | Per-step commits let an intermediate `waiting_human` hold show exactly what the agent wrote; the commit is a no-op when nothing changed. |
| 15 | Test command secrets | **Maintainer-only secrets config, injected into the test container environment, never rendered into any agent-readable artifact** *(soft)* | `task.json` is agent-readable; secrets must never flow through it. See §6.5. |
| 16 | Project lifecycle & config authority | **YAML-only project registration.** Upsert registry at process start from `projects:` map. Submit/CLI/API/triggers hard-fail on unknown project. No GetOrCreate. `source_path`/git/test live in project YAML, not the submit form; agent definitions are global under `agents:`, and a project may name a `default_flow` of them. *(Revised 2026-09-24 — the per-project `agents` flavor block was removed.)* | Projects are administrative entities (git, budgets, casting), not accidental strings. Membership/invites deferred. See §6.0 and `phase_4_project_refactor.md`. Closed 2026-07-10. |

### 17.2 Still Open

| # | Question | Notes |
|---|----------|-------|
| 3 | Codebase search tool | Pure-Go grep is implemented; tree-sitter / vector semantic search remain candidates for later phases (not Phase 4). |
| 14 | Workspace cleanup / retention | Auto-delete vs. archive for audit — decide by Phase 6. |
| 17 | Project membership & invites | Deferred. Global roles only until multi-user SSO/email invite work. When landed: YAML (or admin UI writing the same model) scopes members/viewers per project; admin still sees all. |

---

*End of Specification Document*
