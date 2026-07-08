# Phase 2 Project Cleanup — Implementation Plan

> **Status:** Ready to implement  
> **Position:** Between Phase 2 Part 1 (ADK migration, ✅ complete) and Phase 2 Part 2 (multi-agent pipeline).  
> **Scope:** Fix the defects found in the post-Part-1 code review and align the implementation with the revised spec (2026-07-08). **No new pipeline features** — after this phase the system still runs a single Researcher, but on the revised artifact contract with the known defects closed.  
> **Why a dedicated phase:** Part 2 builds the phase machine directly on top of path containment, token accounting, the artifact layout, and the SQLite layer. Fixing these first means Part 2 is built on the final contract instead of churning it mid-build.

---

## Goal

`go run . run --issue="add auth" --project=foo --dry-run` behaves as it does today, except:

- Artifacts land in the revised layout (`attempts/1/output.md`, `events.jsonl`, `result.json` written at start and completion).
- The known security, correctness, and robustness defects listed below are fixed, each with a test.
- Dead code and duplication from the ADK migration are removed.

---

## Work Items

Execute roughly in order within each group; groups A–C are independent of each other and can interleave. Each item should be committed independently.

### Group A — Security & Correctness Fixes

**A1. Separator-aware path containment** — `internal/storage/fs.go`
- `resolve()` uses `strings.HasPrefix(realAbs, fs.root)`, so a root of `/data/gorch` admits `/data/gorch-evil`.
- Replace with a `filepath.Rel`-based check (or prefix comparison against `fs.root + separator`).
- Unit test: sibling directory whose name is a prefix-extension of the root must be rejected.

**A2. Symlink resolution policy** — `internal/storage/fs.go` (+ spec §5.4)
- `resolve()` never calls `filepath.EvalSymlinks`; a symlink inside the root pointing outside defeats containment. Workspaces copied from real repositories will contain symlinks (Phase 2 Part 2), so this must be closed now.
- Policy per revised spec: resolve symlinks on the deepest existing ancestor of the target path, then re-check containment; reject escapes.
- Unit tests: symlinked file escaping the root; symlinked intermediate directory escaping the root.

**A3. Per-call token accounting** — `internal/orchestrator/run.go`
- `loopTokens = int(ev.UsageMetadata.TotalTokenCount)` overwrites on each event, so a multi-tool-call loop counts only the final model call.
- Accumulate usage per model call (this becomes trivial once events are recorded — see C2).
- Extend the dry-run model to support a scripted multi-turn sequence (tool call → tool response → final text) and assert the total is the sum, not the last value.

**A4. Truthful `result.json`** — `internal/orchestrator/run.go`
- `LoopCount` reports `opts.Loops` even when the run failed on loop 1 of 5 — report the actual count reached.
- Stop stuffing the final output into `done_rationale` (that field is reserved for self-adjudication rationale, spec §7.2).
- Context cancellation (Ctrl-C) produces `status: cancelled`, not `failed` (spec §7.3).

**A5. Empty output fails the loop** — `internal/orchestrator/run.go`
- Today, if the agent neither calls `write_output` nor produces final text, the fallback writes a zero-byte output and the run is marked `done`.
- Per revised spec §13.1: this is a failed loop. Keep the fallback for non-empty final text.

**A6. `read_file` two-mode design + configurable cap** — `internal/tools/read_file.go`, `internal/config/config.go`
- Unbounded reads currently flow whole files into memory and the model context. Restructure as two explicit modes (spec §12.1):
  1. **Whole-file:** no range args → return the full file, subject to a cap. On truncation, the result carries an explicit marker plus the file's total line count, so the agent knows the read was partial and can switch to mode 2.
  2. **Surgical:** `offset` (1-based line) + `limit` (line count) → return exactly that range. This is the intended follow-up to `grep_search`, which already returns file + line numbers — grep to locate, then read the relevant region.
- The cap is **configurable** (`tools.read_file.max_bytes` / `max_lines` in config, wired through the loader and `config.example.yaml`) with sane defaults: 64KB / ~2,000 lines, whichever is hit first. Per-agent overrides arrive with agent personality config in Phase 4 — global config is enough here.
- Update the tool's description string to teach the grep → surgical-read workflow; the tool contract should make the efficient pattern the obvious one for a competent model.
- Tests: whole-file under cap (no marker), whole-file over cap (marker + total line count), surgical range mid-file, range past EOF, cap override from config.

### Group B — Robustness

**B1. JSON-RPC client hardening** — `internal/adapters/jsonrpc.go`
- `bufio.Scanner` default 64KB line limit truncates large payloads — set an explicit generous buffer (e.g., 10MB) and surface oversize as an error, not a silent skip.
- Implement the `initialize` handshake from spec §4.3 (currently only the no-op test adapter knows the method).
- `Close()` blocks forever on a child that ignores stdin closure — wait with a timeout, then kill.
- Capture child stderr into the core's logs; adapter failures are currently invisible.
- *Deferred to Phase 4 (when adapters become load-bearing):* restart with exponential backoff, streaming notifications.

**B2. Explicit adapter registry** — `internal/adapters/manifest.go`, `internal/config/config.go`, `internal/orchestrator/run.go`
- Per revised spec §4.3 and §17 Q11: adapters are declared in config (name + manifest path); remove directory scanning.
- Manifest gains an explicit `binary:` field; loading verifies it is a **regular executable file** — today `os.Stat` accepts a directory, and the repo's own `adapters/noop.yaml` next to the `adapters/noop/` source directory passes discovery and would fail at exec.
- Remove the decorative `_, _ = adapters.Discovery(...)` call from the orchestrator; load registered adapters properly at startup (and log them), or not at all until something uses them.
- Update `manifest_test.go` and the noop adapter layout (build the binary, point the manifest's `binary:` at it).

**B3. OpenAI adapter resilience** — `internal/llm/openai.go`
- Retry with exponential backoff on 429 and 5xx (respect `Retry-After`); a single rate-limit blip currently fails an entire phase.
- Fall back to `m.model` when `req.Model` is empty rather than trusting ADK to have populated it.
- *Still deferred:* streaming (the `stream` flag remains ignored).

**B4. SQLite pragmas + versioned migrations** — `internal/sqlite/db.go`
- Open with `PRAGMA journal_mode=WAL`, `busy_timeout` (suggested 5s), `PRAGMA foreign_keys=ON` (foreign keys are silently unenforced without it).
- Replace the accreting `CREATE TABLE IF NOT EXISTS` block with a minimal versioned migration mechanism (a `schema_migrations` table + ordered migration list). Phase 2 Part 2 (`decisions`) and Phase 3 (users, notifications, audit) then arrive as migrations instead of schema drift.
- Test: migrations are idempotent across repeated opens.

### Group C — Artifact Contract Alignment (revised spec §7)

**C1. `result.json` at phase start** — `internal/orchestrator/run.go`
- Write `{"status": "in_progress", ...}` before the agent runs; finalize on completion. This is the substrate for crash detection (§9.4) — recovery itself lands in Part 2.

**C2. `events.jsonl` transcripts** — `internal/orchestrator/run.go` (+ small event-writer helper)
- Append-only, one JSON object per line: model turns, tool calls and results (size-capped), per-call token usage, timestamps.
- Source: the ADK event stream the orchestrator already drains and currently discards.
- Written through the StoragePort so external storage adapters inherit it.
- Test: dry-run produces events including at least the model turn and usage record.

**C3. `attempts/` layout + feed-forward loops** — `internal/storage/paths.go`, `internal/orchestrator/run.go`, `internal/tools/write_output.go`
- Outputs move to `attempts/1/output.md`; `result.json` gains `attempt` and `latest_output` fields. (Single-researcher flow always uses attempt 1 — multiple attempts arrive with adjudication in Part 2, but the layout must not churn then.)
- Loop *i* receives loop *i−1*'s output in its context (spec §8.3); loops within an attempt may overwrite that attempt's `output.md`, with intermediate states visible in `events.jsonl`.
- Update the integration test for the new layout.

**C4. Slash-canonical storage keys** — `internal/storage/paths.go`
- Use `path.Join` (forward slash) for port keys, not `filepath.Join`; the FS adapter translates to OS separators. Prevents separator leakage into future S3/Azure keys (spec §3.3).

**C5. `.gitignore`-aware grep** — `internal/tools/grep.go`
- Honor `.gitignore` as the Phase 1 plan promised (in addition to the existing `.git` and binary skips). Matters as soon as source snapshots arrive in Part 2 (`node_modules`, build dirs).

### Group D — Hygiene

**D1. Dead code removal**
- `internal/orchestrator/run.go`: `finishTaskResult` (unused until Part 2's task mode), `lastOutput` misuse, `_ = loopStart`.
- `internal/agents/researcher.go`: `MarshalTask` (unused).
- Consolidate the duplicated `schemaToMap` (orchestrator + llm packages) into one shared helper.
- `internal/tools/tools.go`: `mustTool` panics — propagate errors to the CLI instead.

**D2. README.md**
- What the project is (three sentences), current status (Phase 2 Part 1 complete; cleanup in progress), how to run the dry-run demo, and the documentation convention (spec.md is living; phase docs freeze; markdown canonical, HTML unmaintained).

---

## Tests Summary

| Area | Test |
|------|------|
| A1 | Sibling-prefix root rejection |
| A2 | Symlink escape rejection (file + directory) |
| A3 | Multi-turn token accumulation vs. scripted dry-run |
| A4 | Actual loop count on mid-run failure; `cancelled` on context cancel |
| A5 | Empty-output attempt → failed |
| A6 | Whole-file mode: under/over cap (marker + total line count); surgical mode: mid-file range, past-EOF; configurable cap override |
| B1 | >64KB JSON-RPC payload round-trip; close-with-timeout |
| B2 | Manifest rejects directory/non-executable; registry loads only configured adapters |
| B3 | 429 → retry → success (httptest) |
| B4 | Pragmas active; migrations idempotent |
| C1–C3 | Integration test asserts in-progress-then-done result.json, events.jsonl contents, attempts/ layout |

---

## Success Criteria

- `go run . run --issue="add auth" --project=foo --dry-run` completes; artifacts appear in the revised layout.
- All tests above pass; `go vet ./...` clean; `go mod tidy` clean.
- No dead code from the ADK migration remains; no duplicated schema helpers.
- SQLite opens with WAL/busy_timeout/foreign_keys; schema is migration-versioned.
- No behavioral feature additions — the pipeline is still a single Researcher run.

---

## Out of Scope (→ Phase 2 Part 2)

- Planner/Implementer agents, adjudication, `resume`, decisions table.
- Source snapshot and workspace seeding (feature, not cleanup).
- `ModeChat` → `ModeTask` migration (tied to `finish_task` handling and self-adjudication).
- Crash-recovery *behavior* (the `in_progress` result.json written here is its substrate).
- Anthropic adapter.

---

*End of Phase 2 Project Cleanup Plan*
