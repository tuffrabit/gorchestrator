# gorchestrator

gorchestrator is a tight human + AI agent collaboration platform for software engineering teams. It coordinates AI agents through a structured pipeline of research, planning, and implementation phases, with explicit human adjudication gates and an external-adapter model for storage, triggers, and notifications.

## Current status

Phase 2 Part 1 (ADK migration) is complete. Phase 2 Project Cleanup is in progress: the single-Researcher dry-run flow works on the revised artifact contract (`attempts/1/output.md`, `events.jsonl`, `result.json` written at start and completion), and the known post-migration defects are being closed.

## Quick start

```bash
# Build
go build .

# Copy the example config and edit as needed
mkdir -p ~/.config/gorchestrator
cp configs/config.example.yaml ~/.config/gorchestrator/config.yaml

# Run a single Researcher phase in dry-run mode
./gorchestrator run --issue="add auth" --project=foo --dry-run
```

Dry-run artifacts are written to `~/.config/gorchestrator/storage/projects/{project_id}/issues/{issue_id}/research/`.

## Documentation convention

- `spec.md` is the living design document and single source of truth.
- `phase_*.md` files are frozen changelogs for completed phases.
- Markdown is canonical; companion `phase_*.html` renderings are for human convenience only. If a `.md` and its `.html` disagree, the `.md` wins.
