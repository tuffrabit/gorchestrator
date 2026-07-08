# Phase 1 Implementation Plan — The Engine (CLI-Only)

> **Status:** ✅ Complete — frozen historical record (2026-07-08)  
> **Scope:** End-to-end CLI path that creates an issue, runs a single Researcher agent, writes filesystem artifacts, and records state in SQLite.  
> **Note:** Per the documentation convention in `spec.md`, this document is frozen as a changelog of what Phase 1 planned and built. Details superseded by later phases (e.g., the custom `LLMProviderPort`, directory-scan adapter discovery, the pre-ADK tool interface) are recorded as revisions in `spec.md`, not edited here. Two planned items did not ship as written: `internal/trigger/` was never created (TriggerPort formalization deferred to Phase 4), and the grep tool does not yet respect `.gitignore` (fixed in Phase 2 Project Cleanup).

---

## Goal

A single binary, `gorchestrator`, provides a CLI command:

```bash
go run . run --issue="add auth" --project=foo
```

The command:

1. Creates project `foo` and a new issue if they do not exist.
2. Spawns a Researcher agent goroutine.
3. Runs the Researcher for `n_loops` (default `1`), overwriting `output.md` on each loop.
4. Writes the artifact triad under `projects/{pid}/issues/{iid}/research/`:
   - `task.json` — orchestrator-written instructions/config
   - `output.md` — agent-written free-form result
   - `result.json` — orchestrator-written status envelope
5. Persists project/issue/run records in SQLite.
6. Supports `--dry-run` so the agent returns a canned response without calling an LLM.

---

## Decisions

| Topic | Decision |
|-------|----------|
| Language / runtime | Go 1.26+, native Go only, no CGO |
| CLI library | stdlib `flag` |
| Agent runtime | `google.golang.org/adk/v2` |
| SQLite driver | `modernc.org/sqlite` (pure Go) |
| Config format / location | YAML in `~/.config/gorchestrator/config.yaml` |
| Project/issue IDs | Auto-increment integers |
| Storage backend | Local filesystem via `StoragePort` |
| Handoff mode | `n_loops` only; each loop overwrites `output.md` |
| Grep tool | Pure-Go file walker adapted from `tuffrabit/flamingode/internal/tools/grep.go` |
| LLM adapters | OpenAI HTTP, local HTTP (llama.cpp/Ollama), plus `--dry-run` stub |
| External adapters | Foundation only: manifest parsing + JSON-RPC stdio client; no required external adapters |
| Entry point | `main.go` at project root |

---

## Dependencies

```text
google.golang.org/adk/v2
modernc.org/sqlite
gopkg.in/yaml.v3
```

No other third-party packages.

---

## Package Layout

```text
.
├── main.go                  # CLI entrypoint
├── internal/
│   ├── adapters/            # adapter manifest + JSON-RPC stdio client
│   ├── agents/              # Researcher agent definition
│   ├── config/              # ~/.config/gorchestrator/config.yaml loader
│   ├── llm/                 # LLMProviderPort + OpenAI/local/dry-run adapters
│   ├── orchestrator/        # run lifecycle, goroutines, n_loops handoff
│   ├── sqlite/              # schema + repositories
│   ├── storage/             # StoragePort + OS filesystem adapter
│   ├── tools/               # read_file, list_directory, grep_search, write_output
│   └── trigger/             # TriggerPort + manual CLI trigger
├── adapters/                # external adapter binary directory
│   └── (empty or a no-op test adapter only in Phase 1)
└── configs/
    └── config.example.yaml
```

---

## Implementation Steps

Execute in this order. Each step should be committed independently.

### 1. Bootstrap

- `go mod init github.com/tuffrabit/gorchestrator` (or appropriate module path).
- Add `main.go` with a minimal CLI using `flag`.
- Add placeholder subcommands: `run`, `version`.

### 2. Configuration Loader

- Load YAML from `~/.config/gorchestrator/config.yaml`.
- Required fields:
  - `storage_root` — base directory for projects and workspaces.
  - `adapters_dir` — directory to scan for external adapter binaries.
  - `default_model` — provider, model name, API key env var, timeout.
- Create `configs/config.example.yaml`.

### 3. Storage Port

- Define `StoragePort` interface: `Read`, `Write`, `List`, `Exists`, `Mkdir`.
- Implement OS filesystem adapter.
- Add artifact path helpers:
  - `IssueDir(projectID, issueID)`
  - `PhaseDir(projectID, issueID, phase)`
  - `TaskPath`, `ResultPath`, `OutputPath`
- Enforce that all paths are constrained under `storage_root`.

### 4. SQLite Schema & Repositories

- Open `~/.config/gorchestrator/gorchestrator.db`.
- Tables:
  - `projects` — `id INTEGER PRIMARY KEY`, `name TEXT UNIQUE`, `config_json TEXT`, `created_at`.
  - `issues` — `id INTEGER PRIMARY KEY`, `project_id`, `title TEXT`, `status TEXT`, `current_phase TEXT`, `created_at`, `updated_at`.
  - `runs` — `id INTEGER PRIMARY KEY`, `issue_id`, `agent_type`, `model TEXT`, `status TEXT`, `tokens_used INTEGER`, `duration_ms INTEGER`, `loop_count INTEGER`, `created_at`.
- Repositories implement create/get/update for each table.

### 5. Adapter Discovery Foundation

- Scan `adapters_dir` for `.yaml` manifests next to executable files.
- Parse manifest fields: `name`, `version`, `protocol`, `port`, `capabilities`.
- Implement JSON-RPC 2.0 over stdio client:
  - spawn process, keep stdin/stdout pipes open
  - line-delimited JSON request/response
  - request ID tracking
- Include a tiny no-op test adapter under `adapters/` to exercise the plumbing.

### 6. LLM Provider Port

- Define `LLMProviderPort` interface: `Generate(ctx, systemPrompt, userPrompt, tools) (response, usage, error)`.
- Implement:
  - `openai` adapter — chat completions API.
  - `local` adapter — OpenAI-compatible HTTP endpoint for llama.cpp/Ollama.
  - `dryrun` adapter — returns a canned markdown response, ignores tools.

### 7. Core Tools

Implement native Go tools scoped to the agent type:

- `read_file` — read full or partial file content, path resolved against allowlist.
- `list_directory` — list directory contents.
- `grep_search` — adapted from `tuffrabit/flamingode/internal/tools/grep.go`; pure-Go regex/literal search with `.gitignore` respect, binary skip, and output limits.
- `write_output` — Researcher-only; writes to the current phase's `output.md`.

Tools are bound to the agent at runtime; the orchestrator resolves all paths and rejects out-of-scope requests before they reach `StoragePort`.

### 8. Researcher Agent

- Define `Researcher` struct in `internal/agents/`.
- Default system prompt describes its role and how to use tools.
- Bind core tools: `read_file`, `list_directory`, `grep_search`, `write_output`.
- Build an ADK bridge so ADK runs the agent loop while filesystem remains the source of truth.

### 9. Orchestrator Run Command

Implement `run`:

- Parse flags: `--issue`, `--project`, `--dry-run`, `--loops`.
- Load or create project and issue in SQLite.
- Write `task.json` to `research/` with:
  - agent type
  - system prompt
  - model config
  - loop config (`n_loops`)
  - input context (issue title/body)
  - readable path allowlist
- Spawn a goroutine that runs the Researcher.
- On each loop completion, overwrite `output.md`.
- After `n_loops`, write `result.json` with:
  - `status`: `done`
  - `loop_count`
  - `tokens_used`
  - `duration_ms`
  - `timestamp`
- Update `issues.status` and insert a `runs` record.
- Support context cancellation (Ctrl-C) cleanly.

### 10. Integration Test

- A Go test that runs the orchestrator in `--dry-run` mode against a temp storage root.
- Asserts:
  - Project and issue exist in SQLite.
  - `research/task.json`, `research/output.md`, and `research/result.json` exist.
  - `result.json` status is `done`.
  - `runs` table has one record.

---

## Artifact Contract

For each Researcher run:

```text
{storage_root}/projects/{project_id}/issues/{issue_id}/
└── research/
    ├── task.json      ← orchestrator writes
    ├── output.md      ← agent writes (free-form, overwritten each loop)
    └── result.json    ← orchestrator writes on completion
```

`task.json` fields:

```json
{
  "agent_type": "researcher",
  "system_prompt": "...",
  "model": { "provider": "openai", "model": "gpt-4o-mini" },
  "loop_mode": "n_loops",
  "n_loops": 1,
  "input": "add auth",
  "allowlist": ["..."]
}
```

`result.json` fields:

```json
{
  "status": "done",
  "error": "",
  "loop_count": 1,
  "tokens_used": 0,
  "duration_ms": 1234,
  "done_rationale": "",
  "timestamp": "2026-07-08T00:00:00Z"
}
```

---

## Success Criteria

- `go run . run --issue="add auth" --project=foo --dry-run` completes without error.
- Filesystem artifacts exist and are valid JSON/Markdown.
- SQLite records exist for project, issue, and run.
- `go test ./...` passes.
- No CGO dependencies.

---

## Out of Scope for Phase 1

- Planner and Implementer agents.
- `self_done` and `human_gate` handoff modes.
- Adjudicator port (no null/self/human adapters yet).
- Git workspace management.
- Web dashboard / HTTP server.
- MCP, SSO, notifications.
- Token budgets or guardrails.

These are deferred to Phase 2+.
