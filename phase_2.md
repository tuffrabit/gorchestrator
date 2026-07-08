# Phase 2 Implementation Plan — The Pipeline

> **Status:** Part 1 ✅ complete (frozen). Part 2 revised 2026-07-08 to align with the spec revision — not yet started.  
> **Scope:** Full Research → Plan → Implement pipeline with unified adjudication, built on top of a Google ADK Go v2 agent runtime, operating against real project source.  
> **Sequencing:** Part 1 (ADK migration) is complete as written below. A **Phase 2 Project Cleanup** (`phase_2_cleanup.md`) now sits between Part 1 and Part 2 — it fixes the defects found in the post-Part-1 review and lands the revised artifact contract (`events.jsonl`, `attempts/`, in-progress `result.json`, slash-canonical storage keys) that Part 2 builds on. Do not start Part 2 before the cleanup phase completes.

---

## Goal

A single binary, `gorchestrator`, can execute the full pipeline:

```bash
gorchestrator run --issue="add auth" --project=foo
```

The command:

1. Creates/loads project `foo` and a new issue, snapshotting the project's source into the issue directory (spec §6.6).
2. Runs a `Researcher` agent against the source snapshot.
3. Runs a `Planner` agent, using the issue plus the Researcher's accepted output as input.
4. Runs an `Implementer` agent, using the issue plus the Planner's accepted output as input, editing a workspace seeded from the source snapshot.
5. Applies the unified adjudication model (`adjudicator` + `max_attempts` + `loops`, spec §9.1) at each boundary, with feedback injected into retries.
6. Writes the artifact contract for every phase under `projects/{pid}/issues/{iid}/` (`task.json`, `result.json`, `events.jsonl`, `attempts/`).
7. Persists project/issue/run records and per-call token usage in SQLite.
8. Supports resuming a `waiting_human` issue via a `resume` subcommand (with `--feedback`), and recovers crashed phases per spec §9.4.

---

## Decisions

| Topic | Decision |
|-------|----------|
| Agent runtime | Migrate to `google.golang.org/adk/v2` (tight integration, not a wrapper). |
| LLM providers | Use ADK's `model.LLM` interface. Gemini via `model/gemini` built-in; OpenAI via a custom `model.LLM` adapter that speaks OpenAI chat completions and maps to/from `genai` types. Drop the existing custom `internal/llm` provider port. **Part 2 adds a native Anthropic `model.LLM` implementation** — the casting thesis needs the strongest coding models reachable. |
| Session/state | ADK session is ephemeral per phase. Filesystem is the only persisted memory/state. Use `session.InMemoryService()` for each run. |
| Tools | Native ADK function tools (`tool/functiontool`). Define typed arg/result structs. Keep tool implementations generic enough to support core tools today and standard MCP tools in Phase 4. |
| Source access *(revised 2026-07-08)* | Per spec §6.6: read-only source snapshot copied to `projects/{pid}/issues/{iid}/source/` at issue creation; in every agent's read allowlist. Without it the researcher has nothing to research. |
| Implementer workspace *(revised)* | `projects/{pid}/issues/{iid}/implementation/workspace/`, **seeded as a copy of the source snapshot** so implementation edits real code. Git workspace model remains Phase 4. |
| Adjudication *(revised 2026-07-08)* | Unified boundary model per spec §9.1: `adjudicator` (null/self/human) + `max_attempts` + `loops`. The separate handoff-mode axis is gone. Adjudicators return decision **and feedback**; feedback is stored and injected into retries. |
| Agent mode *(revised)* | Agents run in ADK task mode (`llmagent.ModeTask`) and complete via `finish_task`. Part 1 shipped `ModeChat` for the Researcher; Part 2 reconciles this, since self-adjudication reads the `finish_task` rationale. |
| Human gate (CLI-only) | Orchestrator writes `waiting_human` status, exits, and records a pending decision in SQLite. A `resume` subcommand lets a human pass/fail/retry **with a `--feedback` string**. |
| Crash recovery *(added)* | Per spec §9.4: `result.json` written `in_progress` at phase start; interrupted phases detected and re-run from `task.json`. Covered by a kill-mid-phase test. |
| Phase split *(revised)* | Part 1 = ADK migration (✅ complete). **Project Cleanup** (`phase_2_cleanup.md`) = defect fixes + revised artifact contract. Part 2 = pipeline, agents, adjudication. Webhook/email example adapters are cut from Phase 2 and move to the phases that actually wire them (3/4). |

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
│   └── noop/                        # test adapter (webhook/email examples moved to Phases 3/4 where they are wired)
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

## Part 2: Multi-Agent Pipeline + Adjudication (Follow-Up Session)

*(Revised 2026-07-08 to align with the spec revision. Prerequisite: Phase 2 Project Cleanup (`phase_2_cleanup.md`) is complete — Part 2 assumes the `attempts/` layout, `events.jsonl`, in-progress `result.json`, feed-forward loops, and the hardened path containment are already in place.)*

### 7. Project source snapshot (spec §6.6)

- Project config gains a `source_path` (local directory; a clone-once repo URL is acceptable if cheap to add).
- On issue creation, the orchestrator copies `source_path` → `projects/{pid}/issues/{iid}/source/`, excluding `.git`.
- The snapshot path is added to every agent's read allowlist.
- The implementer's `workspace/` is initialized as a **copy of the snapshot** — implementation edits real code, not an empty directory.
- CLI: `run --source=/path/to/repo` sets/updates the project's source path (stored in `projects.config_json`).

### 8. Add `Planner` and `Implementer` agents

- `internal/agents/planner.go`: ADK LLMAgent with researcher-style tools (`read_file`, `list_directory`, `grep_search`, `write_output`).
- `internal/agents/implementer.go`: ADK LLMAgent with implementer toolset (`read_file`, `list_directory`, `grep_search`, `write_file`, `update_file`).
- Each agent has its own system prompt tuned for the next phase's consumer.
- **All agents move to `llmagent.ModeTask`** and complete via `finish_task` (reconciles the Part 1 `ModeChat` divergence); the `finish_task` rationale feeds self-adjudication.

### 9. Wire implementer tools

- `write_file` / `update_file` exist from Part 1/cleanup; wire `NewImplementerRegistry` with `WorkspacePath` set to the issue's workspace.
- Both enforce paths within `projects/{pid}/issues/{iid}/implementation/workspace/` (hardened containment from cleanup).

### 10. Define the `Adjudicator` port (unified model, spec §9)

```go
type Decision struct {
    Outcome  Outcome // Pass, Fail, Retry
    Feedback string  // why — stored in decisions.feedback and attempts/N/feedback.md,
                     // injected into the retry context
}

type Adjudicator interface {
    Evaluate(ctx context.Context, phase string, attempt Attempt, task Task) (Decision, error)
}
```

- `NullAdjudicator`: always `Pass` (after configured loops complete).
- `SelfAdjudicator`: reads the agent's `finish_task` rationale against the configured rubric; returns `Pass` or `Retry` with the rationale as feedback.
- Human adjudication is not an in-process implementation in Part 2: the boundary writes `waiting_human` and exits; `resume` supplies the decision.
- Per-boundary config: `adjudicator` + `max_attempts` + `loops` (spec §9.1). There is no separate handoff-mode setting.

### 11. Build the phase machine

- Orchestrator executes phases in order: `research` → `plan` → `implementation`.
- **Recovery scan first** (spec §9.4): before running, inspect the issue's phase directories; a phase with `result.json: in_progress` is treated as crashed and re-run from its `task.json`.
- Each phase:
  - Builds input context per the spec §8.3 recipe: issue title/body + the previous phase's accepted `output.md` (config may add more; first phase gets issue only).
  - Writes `result.json` with `status: in_progress`, then `task.json` (agent type, model config, tools, adjudication config, allowlist, input context paths).
  - Runs the ADK agent for up to `loops` iterations, each loop receiving the prior loop's output (feed-forward).
  - Writes the attempt output under `attempts/N/`, appends to `events.jsonl` throughout.
  - Applies the configured adjudicator at the boundary.
  - On `Retry` (and attempts remaining): writes `feedback.md` into the rejected attempt, starts attempt N+1 with the rejected output + feedback in context.
  - On `Pass`: finalizes `result.json` as `done`, proceeds to the next phase.
  - On `Fail` or attempts exhausted: `failed`, stop.
  - On human boundary: `waiting_human`, record pending decision, exit.
  - On context cancellation: `cancelled` (not `failed`).

### 12. SQLite decisions table + resume command

- New table (via a versioned migration, per cleanup):
  ```sql
  CREATE TABLE decisions (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      issue_id INTEGER NOT NULL,
      phase TEXT NOT NULL,
      requested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
      decided_at DATETIME,
      decision TEXT,
      feedback TEXT,
      decided_by TEXT,
      FOREIGN KEY (issue_id) REFERENCES issues(id)
  );
  ```
- `gorchestrator resume --project=foo --issue=N --decision=pass|fail|retry [--feedback="..."]`:
  - `pass` → finalize the phase as `done`, continue the pipeline.
  - `retry` → new attempt with the feedback injected (feedback strongly encouraged; warn when omitted).
  - `fail` → phase and issue marked `failed`; pipeline stops.
- Resume reconstructs pipeline position from filesystem + SQLite (filesystem authoritative).

### 13. Anthropic `model.LLM` implementation

- `internal/llm/anthropic.go`: custom `model.LLM` speaking the Anthropic Messages API, translating to/from `genai` types (mirror of the OpenAI adapter, including retry/backoff from cleanup).
- Registered in the factory as provider `anthropic`; config keys identical in shape to OpenAI (`model`, `api_key_env`, `base_url`, `timeout`).

### 14. Tests

- End-to-end dry-run test for the full pipeline (`research` → `plan` → `implementation`), asserting the `attempts/` layout, context chaining, and workspace seeding from the source snapshot.
- Adjudicator unit tests (null, self; retry-with-feedback path).
- Resume flow tests (pass / retry-with-feedback / fail).
- Implementer tool path-enforcement tests (including symlink escape from a snapshot-seeded workspace).
- **Crash recovery test:** kill the process mid-phase (scripted dry-run model that blocks), restart, assert the phase re-runs and the pipeline completes.
- Anthropic adapter translation unit test (no network).

---

## Artifact Contract (Part 2, post-cleanup layout)

```text
{storage_root}/projects/{project_id}/issues/{issue_id}/
├── source/                ← read-only snapshot (orchestrator writes at issue creation)
├── research/
│   ├── task.json          ← orchestrator writes
│   ├── result.json        ← orchestrator writes (in_progress at start; final on completion)
│   ├── events.jsonl       ← orchestrator appends (model turns, tool calls, usage)
│   └── attempts/
│       └── 1/
│           ├── output.md  ← agent writes
│           └── feedback.md (only if rejected)
├── plan/
│   └── (same structure)
└── implementation/
    ├── (same structure)
    └── workspace/         ← seeded from source/; implementer writes here
```

`task.json` fields:

```json
{
  "agent_type": "researcher",
  "system_prompt": "...",
  "model": { "provider": "anthropic", "model": "..." },
  "adjudicator": "self",
  "max_attempts": 3,
  "loops": 1,
  "input_context_paths": ["<issue text ref>", "projects/1/issues/1/research/attempts/1/output.md"],
  "allowlist": ["projects/1/issues/1", "projects/1/issues/1/source"],
  "tools": [ ... ]
}
```

`result.json` fields:

```json
{
  "status": "done",
  "error": "",
  "attempt": 1,
  "loop_count": 1,
  "tokens_used": 0,
  "duration_ms": 1234,
  "done_rationale": "",
  "latest_output": "attempts/1/output.md",
  "timestamp": "2026-07-08T00:00:00Z"
}
```

---

## Success Criteria (Full Phase 2)

- `go run . run --issue="add auth" --project=foo --source=/path/to/repo --dry-run` executes all three phases against the snapshot and writes all artifacts in the revised layout.
- The implementer's workspace starts as a copy of the source snapshot and contains its edits afterward.
- `go run . resume --project=foo --issue=1 --decision=retry --feedback="missing tests"` re-runs the phase with the feedback in the agent's context.
- Kill-mid-phase → restart → the pipeline recovers and completes (automated test).
- `go test ./...` passes.
- No custom agent loop or old `LLMProviderPort` code remains; no `ModeChat` agents remain.
- Token usage (per model call) and run history are recorded per phase.

---

## Out of Scope for Phase 2

- Git workspace model (branching, committing, pushing) → Phase 4 (snapshot copies are the interim, spec §6.6).
- `run_test` tool → Phase 4 (container sandbox is an acceptance criterion there).
- MCP adapter port → Phase 4.
- Web dashboard / auth / daemonization → Phase 3.
- Token budgets / guardrails → Phase 5.
- Notification delivery (Slack, SMTP) → Phase 3/4; the webhook/email example adapters formerly listed here are cut and land where they are wired.

---

*End of Phase 2 Plan*
