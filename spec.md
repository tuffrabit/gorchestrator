# AI Agent Orchestration System — Product Specification

> **Session Date:** 2026-07-04  
> **Status:** Planning Complete — Ready for Phase 1 Implementation  
> **Purpose:** This document captures all architectural decisions, constraints, and phase definitions agreed upon during the planning session. Future implementation sessions should reference this document as the single source of truth.

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
- **Crash resilience by design.** If the process crashes mid-execution, agents resume from filesystem state on restart.
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
| **Orchestrator** | Pipeline state machine, handoff logic, goroutine lifecycle, git workspace management | Custom Go |
| **Agent Runtime** | Single-agent reasoning, tool calling, LLM interaction | Google ADK Go |
| **Storage (App State)** | Projects, issues, users, run history, audit log | SQLite |
| **Storage (Agent Memory)** | Agent outputs, shared artifacts, free-form content | Filesystem (hexagonal port) |
| **LLM Provider** | Model-agnostic LLM API access | ADK Go + LiteLLM wrapper + custom adapters |
| **Tools** | Core capabilities + custom extensions | Native Go tools + MCP adapter port |
| **Triggers** | How work enters the system | Hexagonal port with multiple adapters |
| **Adjudication** | Quality gates between phases | Hexagonal port (null / self / human) |
| **Dashboard** | Human visibility and intervention | Go HTTP server + lightweight frontend |

### 3.3 Hexagonal Ports (Defined)

1. **StoragePort** — Filesystem operations (read, write, list, watch)
   - Built-in adapters: OS filesystem (default)
   - External adapters: S3, Azure Blob (via JSON-RPC stdio)
2. **LLMProviderPort** — LLM API abstraction
   - Built-in adapters: OpenAI, Anthropic, Gemini, local llama.cpp
   - External adapters: Enterprise gateways, custom providers (via JSON-RPC stdio)
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
1. Core discovers adapter binaries in a configured directory (e.g., `./adapters/`)
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
capabilities: [read, write, list, watch]
```

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
| **LLMProviderPort** | OpenAI, Anthropic, Gemini, local HTTP (llama.cpp/Ollama) | Enterprise gateways, custom APIs | Common providers are built-in for zero-config setup. Exotic providers use external process. |
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
| `read_file` | ✅ | ✅ | ✅ | Read full or partial content of a file. Path resolved by orchestrator against allowlist. |
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

### 5.4 Path Resolution & Allowlist

The orchestrator maintains an allowlist of readable paths for each agent run:
- **Researcher/Planner:** Can read from the issue's agent output directories (previous phases) and any paths explicitly provided in `task.json`.
- **Implementer:** Can read from its workspace directory and the issue's previous agent output directories.

All tool paths provided by the agent are resolved by the orchestrator against this allowlist. Attempts to access paths outside the allowlist are rejected by the orchestrator before reaching the StoragePort.

### 5.5 MCP Permission Model

MCP servers are the real permission surface because they expose arbitrary capabilities (database access, API calls, internal service invocations).

**MVP Model:**
- MCP servers are **project-level configuration**
- All tools from an enabled MCP server are available to all agents
- The human maintainer decides which MCP servers to connect
- **Default: deny** — no MCP servers are connected unless explicitly configured

**Future (Phase 4+):**
- Per-agent, per-tool allowlists
- Tool-level permission granularity (e.g., `query_database` only allows `SELECT`)
- Endpoint restrictions for API tools

---

## 6. Git Workspace Model

### 6.1 Philosophy

The core implementer agent and toolset are geared around a **targeted, pre-existing git repository**. No agent can create a new git repository or GitHub/Bitbucket project via core tools. Repository management is a human responsibility.

### 6.2 Prerequisites

- `git` must be installed and configured in the environment
- `gh` CLI is optional but recommended for GitHub-specific workflows
- SSH keys, tokens, or `gh` profiles must be configured by the human maintainer outside the application
- The application does not manage auth setup; it assumes the environment is ready

### 6.3 Project-Level Git Configuration

```yaml
project:
  name: "auth-service"
  git:
    repo_url: "git@github.com:myorg/auth-service.git"
    base_branch: "develop"
    auth:
      type: "ssh_key"  # or "token", "gh_cli"
      ssh_key_path: "/secrets/deploy_key"
    # or:
    # type: "gh_cli"
    # profile: "work"  # uses `gh auth switch` or env var
```

### 6.4 Implementer Workspace Lifecycle (Orchestrator-Managed)

The agent never touches git. The orchestrator handles the entire git lifecycle:

1. **Branch Creation:** Orchestrator creates a unique branch per implementer run:
   ```bash
   git checkout -b ai-implementer/{issue_id}-{timestamp}
   ```
2. **Workspace Copy:** Orchestrator copies repo files into the configured storage location under a workspace path:
   ```
   workspaces/{issue_id}/implementer-{run_id}/
   ```
3. **Agent Execution:** Implementer receives `read_file`, `write_file`, `update_file` tools scoped to that workspace path. The agent sees relative paths (e.g., `src/auth.go`) but the orchestrator resolves them to the workspace absolute path.
4. **Post-Execution:** Orchestrator stages changes, commits with a structured message, and optionally pushes. The human decides whether to merge.

**Parallel Implementers:** Each implementer run gets its own branch and workspace directory. No collision. The orchestrator tracks `workspace_id` and `branch_name` in SQLite.

### 6.5 Test Execution (`run_test` Tool)

The implementer has access to a `run_test` core tool that executes the project's pre-configured test command in a sandboxed subprocess.

```yaml
project:
  test_command: "go test ./..."
  test_timeout: "60s"
  # Or more complex:
  # test_command: "docker run --rm -v $(pwd):/app myproject-test-runner"
  # test_command: "./scripts/run-integration-tests.sh"
  # test_command: "python -m pytest tests/"
```

**Properties:**
- Command is **immutable** — defined in project config, not modifiable by the agent
- Executed in the implementer's workspace directory
- Sandboxed subprocess with timeout and output capture
- Agent receives stdout/stderr but cannot modify the command
- Enables implementer test-and-fix loops without opening arbitrary shell access

---

## 7. Filesystem Artifact Contract

### 7.1 Directory Structure (Per Issue)

```
projects/{project_id}/issues/{issue_id}/
├── research/
│   ├── task.json          ← Orchestrator writes: instructions, model config, loop config
│   ├── result.json          ← Orchestrator writes: status, error, tokens_used, done_rationale
│   └── output.*             ← Agent writes free-form content: .md, .json, .py, .xlsx, etc.
├── plan/
│   ├── task.json
│   ├── result.json
│   └── output.*
├── adjudication/
│   ├── task.json
│   ├── result.json
│   └── output.*
└── implementation/
    ├── task.json
    ├── result.json
│   └── output.*
```

### 7.2 The Minimal Contract

- **task.json** — Orchestrator writes. Contains: agent type, system prompt reference, model config, tool list, loop mode, handoff config, input context (path to previous agent's output), readable path allowlist.
- **result.json** — Orchestrator writes after agent completes. Contains: status (`in_progress`, `done`, `failed`), error message (if any), tokens consumed, duration, done_rationale (for self_done mode), loop count, timestamp.
- **output.*** — Agent writes. **Completely free-form.** Content is determined by the agent's system prompt and the underlying LLM. The orchestrator never parses this file. The next agent receives the content of this file as its input context.

**Critical rule:** No agent reads or parses `result.json`. Only the orchestrator manages status. Agents only read their `task.json` and write their `output.*`.

### 7.3 Status Values

| Status | Meaning |
|--------|---------|
| `in_progress` | Agent goroutine is running |
| `done` | Agent completed successfully, ready for handoff evaluation |
| `failed` | Agent encountered an error, timeout, or exception |
| `waiting_human` | HumanAdjudicator gate triggered, goroutine exited, awaiting human decision |
| `retry` | Human or self-adjudicator rejected output, agent should re-run |
| `skipped` | Phase was skipped (e.g., adjudication configured as null) |

---

## 8. Agent Design

### 8.1 Base Agent Identities (Baked into Go Code)

Agent types are **structs in the Go codebase**, not generic configurations. Each has a default system prompt and toolset, but can be altered via YAML config.

| Agent | Role | Typical LLM Profile | Core Tools |
|-------|------|---------------------|------------|
| **Researcher** | Requirements analysis, investigation, root cause identification, solution discovery | Deep thinker, high context window, thorough | `read_file`, `list_directory`, `grep_search`, `write_output` |
| **Planner** | Implementation planning, dependency analysis, effort estimation, task decomposition | Fast, structured, good at planning | `read_file`, `list_directory`, `grep_search`, `write_output` |
| **Implementer** | Code generation, file writing, test scaffolding, spreadsheet creation | Coding-competent, instruction-following | `read_file`, `list_directory`, `grep_search`, `write_file`, `update_file`, `run_test` |

### 8.2 Agent Configuration (YAML)

Per-project or per-issue YAML overrides:
- LLM model/provider
- Temperature, max tokens, token budget
- System prompt override or extension
- Tool subset (enable/disable specific tools)
- Loop mode and parameters
- Handoff configuration

### 8.3 Context Flow

Agent N+1 receives the **entire content** of Agent N's `output.*` file as its prompt context. The human's job is to configure agents such that Agent N's output is useful as Agent N+1's input. No automatic summarization or distillation by the orchestrator.

---

## 9. Handoff & Loop Configuration

### 9.1 Handoff Boundary Modes (Per-Agent Configurable)

| Mode | Description |
|------|-------------|
| **n_loops** | Agent runs exactly N times. Orchestrator tracks loop count in `result.json`. After N loops, status becomes `done` regardless of output quality. |
| **self_done** | Agent evaluates itself against an English rubric (defined in config). Agent writes its assessment to `output.*` (free-form). Orchestrator reads `result.json` status. If agent reports itself done, status becomes `done`. |
| **human_gate** | Agent completes, status becomes `waiting_human`. Goroutine **exits**. Orchestrator writes to SQLite notification queue. Human reviews via dashboard and clicks pass/fail/retry. New goroutine spawned on decision. |

### 9.2 Adjudication Port

Adjudication is a **handoff boundary evaluation**, not an agent type. The system does not care WHO adjudicates.

- **NullAdjudicator** — Auto-pass. No evaluation.
- **SelfAdjudicator** — Agent evaluates itself (used with `self_done` mode).
- **HumanAdjudicator** — Pauses pipeline, notifies human, waits for dashboard interaction.
- *(Future)* **AgentAdjudicator** — Dedicated AI reviewer agent (external process adapter).

### 9.3 Goroutine Lifecycle

- Agents run as goroutines spawned by the orchestrator.
- On `human_gate`, the goroutine writes `waiting_human` status and **dies**. No long-running sleep/poll loops.
- On restart, the orchestrator reads SQLite for `in_progress` and `waiting_human` states, checks filesystem `result.json`, and resumes or re-spawns goroutines as needed.
- Humans can pass/fail/retry any agent output at any time via dashboard, regardless of whether the handoff was configured for human gate.

---

## 10. SQLite vs. Filesystem Boundary

### 10.1 SQLite (Application State)

- Project registry (project_id, name, created_at, config_ref, git_config_json)
- Issue queue (issue_id, project_id, title, status, current_phase, created_at, updated_at)
- Agent run history (run_id, issue_id, agent_type, model, tokens_used, duration, result_status, timestamp, workspace_id, branch_name)
- User/team accounts, roles, SSO mappings
- Audit log (user_id, action, target_issue, timestamp, details)
- Notification queue (notification_id, issue_id, agent_type, status, recipient, sent_at)
- Human decision queue (decision_id, issue_id, phase, requested_at, decided_at, decision, decided_by)

### 10.2 Filesystem (Agent Memory & Artifacts)

- Agent output directories (`research/`, `plan/`, `adjudication/`, `implementation/`)
- Agent free-form outputs (`output.*` files)
- Workspace directories (`workspaces/{issue_id}/implementer-{run_id}/`)
- Configuration files (YAML)
- Logs (optional, per-project)
- Dashboard static assets

---

## 11. Human Interface (Dashboard)

### 11.1 Phase 1-2: CLI Only

Configuration and operation via YAML files and CLI commands. No web interface.

### 11.3 Phase 3: Web Dashboard (Observation + Adjudication)

- **Real-time status view:** Issue list, current phase, agent status, progress indicators
- **Artifact viewer:** Rendered Markdown, file tree, code/syntax highlighting, diff viewer
- **Adjudication UI:** Pass/fail/retry buttons available at any handoff boundary, regardless of configuration
- **Agent activity log:** Stream of agent actions, tool calls, outputs
- **Token burn display:** Per-run and cumulative token usage
- **Notification center:** Human gate alerts, admin alerts on failures

### 11.4 Admin Features

- Admin users always receive notifications on "bad" agent output (errors, timeouts, exceptions, empty outputs)
- Configurable admin escalation rules (Phase 5)

---

## 12. Tool Strategy

### 12.1 Core First-Class Tools

Small, native Go toolset available to agents based on agent type:

| Tool | Availability | Description |
|------|-------------|-------------|
| `read_file` | Researcher, Planner, Implementer | Read file content (full or partial). Paths resolved against orchestrator allowlist. |
| `list_directory` | Researcher, Planner, Implementer | List directory contents. |
| `grep_search` | Researcher, Planner, Implementer | Pattern search across files. |
| `write_output` | Researcher, Planner | Write to the agent's designated `output.*` file. Orchestrator resolves path. |
| `write_file` | Implementer only | Write a file within the implementer's workspace. |
| `update_file` | Implementer only | Update/patch a file within the implementer's workspace. |
| `run_test` | Implementer only | Execute pre-configured test command in sandboxed subprocess. |

### 12.2 MCP Adapter Port

- Users can connect any MCP server for custom tools
- MCP tools are explicitly registered in agent config (not dynamically discovered)
- MCP is natively an external process protocol; core implements the MCP client
- Keeps core simple while supporting ecosystem extensibility
- **MVP:** All MCP tools from enabled servers available to all agents
- **Future:** Per-agent, per-tool permission granularity

---

## 13. Guardrails (Phase 5)

### 13.1 "Bad Output" Definition (MVP)

- LLM API errors or timeouts
- Agent exceptions during tool execution
- Configuration errors or missing required config
- Empty output files (heuristic: output.* has zero bytes)

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

### Phase 1: The Engine (CLI-Only)
**Goal:** A running system that can accept a trigger, run a no-op agent, and write artifacts to storage.

- StoragePort interface + OS filesystem adapter (built-in)
- Minimal artifact contract: `task.json` + `result.json` + free-form `output.*`
- Trigger: manual CLI adapter (built-in)
- LLM Port: OpenAI + local llama.cpp adapters (built-in) via ADK Go
- Orchestrator: goroutine-based agent runner with context cancellation
- One agent type: Researcher (basic)
- Handoff: `n_loops` mode only
- SQLite: project and issue registry, run history
- Adapter discovery framework: scan `./adapters/`, load manifests, spawn external processes
- JSON-RPC stdio protocol foundation
- **Deliverable:** `cli run --issue="add auth" --project=foo` creates issue, runs researcher, writes to filesystem, marks done in SQLite

### Phase 2: The Pipeline (Still CLI)
**Goal:** Full Research → Plan → Implement pipeline with configurable handoffs.

- Agent types: Researcher, Planner, Implementer (Go structs)
- Core toolset: `read_file`, `list_directory`, `grep_search`, `write_output`, `write_file`, `update_file`
- Handoff modes: `n_loops`, `self_done`, `human_gate`
- Adjudication port: NullAdjudicator, SelfAdjudicator (built-in)
- Context chaining: Agent N+1 receives Agent N's `output.*` as context
- Token tracking per run, SQLite logging
- External process adapter examples: webhook trigger, email notification
- **Deliverable:** Full automated pipeline from issue to code, configurable per-agent via YAML

### Phase 3: Human Interface (Dashboard + Auth)
**Goal:** Humans can see what's happening and intervene.

- Web dashboard: Go HTTP server + lightweight frontend (HTMX or minimal React)
- Real-time view: issue list, current phase, agent status, artifact viewer
- Adjudication UI: pass/fail/retry at any handoff boundary
- HumanAdjudicator adapter (built-in): pauses, writes to SQLite notification queue, dashboard polls
- User/team model: SQLite-backed, roles (admin, member, viewer)
- SSO: OIDC adapter (built-in)
- Admin notifications: email/Slack webhook (external process adapters)
- **Deliverable:** Team can log in, watch agents work, click retry, get Slack alerts

### Phase 4: Extensibility
**Goal:** Users can plug in their own world.

- MCP adapter port: custom tools via MCP servers
- Trigger adapters: GitHub Issues, Jira, Linear (external process)
- Storage adapters: S3, Azure Blob (external process)
- Agent personality config: system prompts, temperature, tool subsets
- Git workspace model: branch creation, workspace isolation, commit/push
- `run_test` core tool with project-configured command
- **Deliverable:** Users can connect Jira, plug in internal APIs via MCP, store artifacts in cloud, run test-and-fix loops

### Phase 5: Guardrails
**Goal:** The system protects itself and the user's wallet.

- Effort estimation: Planner tags high/med/low, high requires human confirmation
- Token budget enforcement: hard stop + notification
- Scope detection: basic heuristics to catch overly broad issues
- Admin escalation rules: configurable thresholds
- MCP permission granularity: per-agent, per-tool allowlists
- **Deliverable:** System asks "are you sure?" before burning API budget

### Phase 6: Polish
**Goal:** Shippable product.

- Complete audit logging
- Metrics dashboard: token burn per project, cycle time, human intervention rate
- Documentation: architecture docs, admin guide, user guide, API reference
- Deployment: single binary + Docker Compose (SQLite + filesystem volume)
- **Deliverable:** Team can deploy to production and audit every decision

---

## 15. Technology Stack

| Component | Choice |
|-----------|--------|
| Language | Go |
| Agent Runtime | Google ADK Go (v2) |
| App Database | SQLite |
| Storage | Filesystem (hexagonal port, built-in + external adapters) |
| Web Framework | Standard library `net/http` + lightweight frontend (HTMX or minimal React) |
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

---

## 17. Open Questions for Future Sessions

1. **Frontend technology:** HTMX vs. minimal React vs. pure server-rendered HTML?
2. **Shell execution sandbox:** Docker container, restricted user, or simple command timeout?
3. **Codebase search tool:** Tree-sitter, ripgrep, or vector semantic search?
4. **Dashboard real-time updates:** Server-Sent Events, WebSockets, or polling?
5. **Notification delivery:** Email (SMTP), Slack webhook, or both?
6. **SSO scope:** OIDC only for MVP, or SAML too?
7. **Agent output versioning:** Overwrite or append? How to handle retries?
8. **Project/issue ID scheme:** UUID, auto-increment, or human-readable?
9. **YAML config schema:** Per-project, per-issue, or per-agent-type defaults with overrides?
10. **Local LLM integration:** llama.cpp server, Ollama, or direct llama.cpp embedding?
11. **Adapter binary discovery:** Static directory scan, PATH search, or configurable registry?
12. **Adapter authentication:** How do external adapters authenticate to their backends (AWS credentials for S3, API keys for Jira)?
13. **Git commit strategy:** Single commit per implementer run, or granular commits per file change?
14. **Workspace cleanup:** Auto-delete old workspaces, or archive for audit?
15. **Test command environment:** How to inject secrets/env vars into test subprocess without exposing them to the agent?

---

*End of Specification Document*
