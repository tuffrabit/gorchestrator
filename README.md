# gorchestrator

gorchestrator is a tight human + AI agent collaboration platform for software engineering teams. It coordinates AI agents through a structured pipeline of research, planning, and implementation phases, with explicit human adjudication gates and an external-adapter model for storage, triggers, and notifications.

## Current status

Phase 2 Part 2 (the multi-agent pipeline) is complete. `gorchestrator run` executes the full Research → Plan → Implement pipeline against a project source snapshot, with unified adjudication (null/self/human), human gates resumable via `gorchestrator resume`, crash recovery, and an Anthropic `model.LLM` adapter.

## Quick start

```bash
# Build
go build .

# Copy the example config and edit as needed
mkdir -p ~/.config/gorchestrator
cp configs/config.example.yaml ~/.config/gorchestrator/config.yaml

# Run the full pipeline against a project source directory in dry-run mode
./gorchestrator run --issue="add auth" --project=foo --source=/path/to/repo --dry-run

# Resume a phase waiting for human adjudication
./gorchestrator resume --project=foo --issue=1 --decision=pass --feedback="looks good"
```

Artifacts are written to `~/.config/gorchestrator/storage/projects/{project_id}/issues/{issue_id}/`, including `source/`, per-phase `task.json`, `result.json`, `events.jsonl`, and `attempts/`.

## Documentation convention

- `spec.md` is the living design document and single source of truth.
- `phase_*.md` files are frozen changelogs for completed phases.
- Markdown is canonical; companion `phase_*.html` renderings are for human convenience only. If a `.md` and its `.html` disagree, the `.md` wins.
