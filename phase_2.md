# Phase 2 Implementation Plan — The Pipeline

> **Status:** Approved  
> **Scope:** Full Research → Plan → Implement pipeline with configurable handoffs, built on top of a Google ADK Go v2 agent runtime.  
> **Session Split:** Part 1 (ADK migration) is the focus of this session. Part 2 (multi-agent pipeline) follows in a subsequent session.

---

## Goal

A single binary, `gorchestrator`, can execute the full pipeline:

```bash
gorchestrator run --issue="add auth" --project=foo
```

The command:

1. Creates/loads project `foo` and a new issue.
2. Runs a `Researcher` agent.
3. Runs a `Planner` agent, using the Researcher's `output.md` as input.
4. Runs an `Implementer` agent, using the Planner's `output.md` as input, writing files to a scratch workspace.
5. Applies configurable handoff modes (`n_loops`, `self_done`, `human_gate`) between phases.
6. Writes the artifact triad for every phase under `projects/{pid}/issues/{iid}/`.
7. Persists project/issue/run records and token usage in SQLite.
8. Supports resuming a `waiting_human` issue via a `resume` subcommand.

---

## Decisions

| Topic | Decision |
|-------|----------|
| Agent runtime | Migrate to `google.golang.org/adk/v2` (tight integration, not a wrapper). |
| LLM providers | Use ADK's `model.LLM` interface. Gemini via `model/gemini` built-in; OpenAI via a custom `model.LLM` adapter that speaks OpenAI chat completions and maps to/from `genai` types. Drop the existing custom `internal/llm` provider port. |
| Session/state | ADK session is ephemeral per phase. Filesystem is the only persisted memory/state. Use `session.InMemoryService()` for each run. |
| Tools | Native ADK function tools (`tool/functiontool`). Define typed arg/result structs. Keep tool implementations generic enough to support core tools today and standard MCP tools in Phase 4. |
| Implementer workspace | Scratch directory under `projects/{pid}/issues/{iid}/implementation/workspace/` for Phase 2. Git workspace model remains Phase 4. |
| Human gate (CLI-only) | Orchestrator writes `waiting_human` status, exits, and records a pending decision in SQLite. A `resume` subcommand lets a human pass/fail/retry. |
| Phase split | Part 1 = ADK migration. Part 2 = pipeline, agents, handoffs, adjudicators. |

---

## Dependencies

```text
google.golang.org/adk/v2 v2.0.0
modernc.org/sqlite
gopkg.in/yaml.v3
```

`google.golang.org/genai` is pulled in transitively by ADK.

---

## Package Layout

```text
.
├── main.go                          # CLI entrypoint; adds `resume` subcommand
├── adapters/                        # external adapter binary directory
│   ├── noop/
│   ├── webhook/                     # NEW: example trigger adapter (Phase 2 Part 2)
│   └── email/                       # NEW: example notification adapter (Phase 2 Part 2)
├── internal/
│   ├── adapters/                    # manifest parsing + JSON-RPC stdio client (existing)
│   ├── agents/                      # NEW: ADK-native Researcher, Planner, Implementer builders
│   ├── cli/                         # run + resume subcommands
│   ├── config/                      # config loader + per-agent overrides
│   ├── llm/                         # REDESIGNED: custom OpenAI model.LLM adapter; Gemini helpers
│   ├── orchestrator/                # phase machine, handoffs, adjudicators
│   ├── sqlite/                      # schema + repositories (+ decisions table)
│   ├── storage/                     # StoragePort + OS filesystem adapter (existing)
│   ├── tools/                       # REDESIGNED: ADK function tools (read_file, list_directory, grep_search, write_output, write_file, update_file)
│   └── adjudication/                # NEW: Adjudicator port + Null/Self adapters
└── configs/
    └── config.example.yaml
```

---

## Part 1: ADK Migration (This Session)

### 1. Bootstrap ADK dependency

- `go get google.golang.org/adk/v2@v2.0.0` (already done).
- Verify module builds with new dependency.

### 2. Redesign `internal/tools` as ADK function tools

Replace the custom `Tool` interface with ADK's `tool.Tool`:

- Each tool becomes a `functiontool.New[TArgs, TResult](...)` wrapper around a typed Go function.
- Arg/result structs are plain Go structs with JSON tags; ADK infers JSON Schema.
- Existing tools migrate as follows:
  - `read_file` → `ReadFileArgs{Path, Offset, Limit}`, `ReadFileResult{Path, Content, Size}`.
  - `list_directory` → `ListDirArgs{Path}`, `ListDirResult{Path, Entries}`.
  - `grep_search` → `GrepArgs{Path, Pattern, Regex}`, `GrepResult{...}`.
  - `write_output` → `WriteOutputArgs{Content}`, `WriteOutputResult{...}`.
- Keep path allowlist enforcement in the tool implementations (same logic as today, moved into the function handler).
- Keep `BoundTools` as the dependency bag (storage port, root path, allowlist, output path).

### 3. Redesign `internal/llm` around ADK's `model.LLM`

- Delete `internal/llm/port.go`, `openai.go`, `local.go`, `dryrun.go`.
- Add `internal/llm/gemini.go`: helper to build a Gemini model from config (`GOOGLE_API_KEY`, model name, timeout).
- Add `internal/llm/openai.go`: custom `model.LLM` implementation that:
  - Calls OpenAI chat completions (or any OpenAI-compatible endpoint).
  - Translates `genai.Content` / `genai.FunctionDeclaration` requests to OpenAI messages/tools.
  - Translates OpenAI responses back to `*model.LLMResponse` with `genai.Content` and usage metadata.
- Add `internal/llm/dryrun.go`: a `model.LLM` that returns a canned response without HTTP calls, for tests.
- Add `internal/llm/factory.go`: builds a `model.LLM` from provider name (`gemini`, `openai`, `dryrun`) and config.

### 4. Redesign `internal/agents/researcher.go` as an ADK LLMAgent

- Remove the custom `Run` loop.
- `NewResearcher(model model.LLM) agent.Agent` returns an `llmagent.New(llmagent.Config{...})`:
  - `Name: "researcher"`
  - `Instruction`: default system prompt.
  - `Model`: provided `model.LLM`.
  - `Mode`: `llmagent.ModeTask` (agent must call `finish_task` to complete).
  - `Tools`: researcher toolset.
- The agent runs through ADK's runner and emits events. The orchestrator drains the events and writes `output.md` from the final content or from a tool result.

### 5. Rewrite `internal/orchestrator/run.go` to drive ADK

- Keep project/issue creation and SQLite logic.
- For a single Researcher run (still Phase 1 scope), build an ADK `Runner`:
  - `Agent`: Researcher agent.
  - `SessionService`: `session.InMemoryService()`.
  - `AutoCreateSession: true`.
- Invoke `runner.Run(ctx, userID, sessionID, userContent, runConfig)` and collect events.
- Extract final text / output and write `output.md` + `result.json`.
- Preserve token usage from ADK events if available; otherwise estimate from `UsageMetadata`.
- Keep `--dry-run` working via the dry-run `model.LLM`.

### 6. Update integration test

- `TestRun_DryRun` should still pass end-to-end with the ADK backend.
- Add a unit test for the OpenAI adapter translation layer (no network).

### Part 1 Success Criteria

- `go run . run --issue="add auth" --project=foo --dry-run` completes and writes the same artifacts as Phase 1.
- All existing tests pass.
- `go mod tidy` produces a clean module.
- No custom agent loop remains.

---

## Part 2: Multi-Agent Pipeline + Handoffs (Follow-Up Session)

### 7. Add `Planner` and `Implementer` agents

- `internal/agents/planner.go`: ADK LLMAgent with researcher-style tools (`read_file`, `list_directory`, `grep_search`, `write_output`).
- `internal/agents/implementer.go`: ADK LLMAgent with implementer toolset (`read_file`, `list_directory`, `grep_search`, `write_file`, `update_file`).
- Each agent has its own system prompt tuned for the next phase's consumer.

### 8. Add implementer tools

- `write_file`: write bytes to a workspace-relative path.
- `update_file`: overwrite/patch a workspace-relative path.
- Both enforce paths within `projects/{pid}/issues/{iid}/implementation/workspace/`.

### 9. Define `Adjudicator` port

```go
type Adjudicator interface {
    Evaluate(ctx context.Context, phase string, output []byte, task Task) (Decision, error)
}
```

- `Decision` = `Pass`, `Fail`, `Retry`.
- `NullAdjudicator`: always `Pass`.
- `SelfAdjudicator`: parses agent's self-assessment (from `output.*` or a `finish_task` result) and returns `Pass`/`Retry`.

### 10. Implement handoff modes

- `n_loops`: run exactly N loops, then proceed.
- `self_done`: agent calls `finish_task` with a done rationale; SelfAdjudicator decides.
- `human_gate`: after phase output, orchestrator writes `waiting_human`, inserts a row into a new `decisions` table, and exits. A `resume` command reads the pending decision and continues.

### 11. Build the phase machine

- Orchestrator executes phases in order: `research` → `plan` → `implementation`.
- Each phase:
  - Reads the previous phase's `output.md` as input context (first phase uses issue title).
  - Writes its own `task.json` with agent type, model config, tools, loop config, allowlist, and input context path.
  - Runs the ADK agent.
  - Writes `output.md` and `result.json`.
  - Applies the configured adjudicator at the boundary.
  - On `Retry`, re-runs the same phase (up to a max retry count).
  - On `Pass`, proceeds to the next phase.
  - On `Fail`/`human_gate`, stops and records status.

### 12. SQLite decisions table + resume command

- New table:
  ```sql
  CREATE TABLE decisions (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      issue_id INTEGER NOT NULL,
      phase TEXT NOT NULL,
      requested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
      decided_at DATETIME,
      decision TEXT,
      decided_by TEXT,
      FOREIGN KEY (issue_id) REFERENCES issues(id)
  );
  ```
- `gorchestrator resume --project=foo --issue=N --decision=pass|fail|retry` updates the decision, updates `issues.status`, and continues the pipeline.

### 13. External adapter examples

- `adapters/webhook/`: a JSON-RPC stdio adapter that listens on an HTTP port and emits trigger events.
- `adapters/email/`: a JSON-RPC stdio notification adapter that sends SMTP alerts.
- These exercise the existing adapter discovery/JSON-RPC plumbing but are not wired into core flow in Phase 2.

### 14. Tests

- End-to-end dry-run test for full pipeline (`research` → `plan` → `implementation`).
- Adjudicator unit tests.
- Resume flow test.
- Implementer tool path-enforcement tests.

---

## Artifact Contract

```text
{storage_root}/projects/{project_id}/issues/{issue_id}/
├── research/
│   ├── task.json          ← orchestrator writes
│   ├── output.md          ← agent writes
│   └── result.json        ← orchestrator writes
├── plan/
│   ├── task.json
│   ├── output.md
│   └── result.json
└── implementation/
    ├── task.json
    ├── output.md
    ├── result.json
    └── workspace/         ← implementer writes files here
        └── ...
```

`task.json` fields:

```json
{
  "agent_type": "researcher",
  "system_prompt": "...",
  "model": { "provider": "gemini", "model": "gemini-2.0-flash" },
  "loop_mode": "n_loops",
  "n_loops": 1,
  "input_context_path": "projects/1/issues/1/research/output.md",
  "allowlist": ["projects/1/issues/1"],
  "tools": [ ... ]
}
```

`result.json` fields (same as Phase 1):

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

## Success Criteria (Full Phase 2)

- `go run . run --issue="add auth" --project=foo --dry-run` executes all three phases and writes all artifacts.
- `go run . resume --project=foo --issue=1 --decision=retry` continues a `waiting_human` issue.
- `go test ./...` passes.
- No custom agent loop or old `LLMProviderPort` code remains.
- Token usage and run history are recorded per phase.

---

## Out of Scope for Phase 2

- Git workspace model (branching, committing, pushing) → Phase 4.
- `run_test` tool → Phase 4.
- MCP adapter port → Phase 4.
- Web dashboard / auth → Phase 3.
- Token budgets / guardrails → Phase 5.
- Advanced notification delivery (Slack, SMTP fully wired) → Phase 3/4.

---

*End of Phase 2 Plan*
