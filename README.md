# gorchestrator

gorchestrator is a tight human + AI agent collaboration platform for software engineering teams. It coordinates AI agents through a structured pipeline of research, planning, and implementation phases, with explicit human adjudication gates and an external-adapter model for storage, triggers, and notifications.

## Current status

**Phases 1–3 are complete.** **Phase 4 extensibility and the project-registry/flavors refactor are largely in tree** (git workspaces, container `run_test`, MCP allowlists, triggers, S3 adapter path, YAML projects + agent flavors). Post–Phase 4 polish also landed: optional issue **description** + text **attachments**, multi-phase artifact drawer (Workspace tree + `workspace.zip` when implementation is done).

The system runs as a long-lived daemon (`gorchestrator serve`) with a worker pool, HTTP API, OIDC/local auth, HTMX dashboard, and notification sinks (console + optional Slack/email adapters). CLI one-shot `run` / `resume` still work without a daemon.

**Next:** Phase 5 guardrails (`phase_5.md`) — budgets, effort gate, scope detection — must **not** regress the landed foundations called out in that plan.

## Quick start

```bash
# Build
go build .

# Copy the example config and edit as needed
mkdir -p ~/.config/gorchestrator
cp configs/config.example.yaml ~/.config/gorchestrator/config.yaml
```

### One-shot CLI (no daemon)

Projects must be declared under `projects:` in config YAML before use (no create-on-submit).
`source_path` / git / test / agent flavors live in that block — not on CLI flags or the submit form.

```bash
# Full pipeline for a YAML-registered project (dry-run LLM)
./gorchestrator run --issue="add auth" --project=foo --dry-run

# Optional description and text attachments
./gorchestrator run --issue="add auth" --project=foo \
  --body="Users cannot sign in after SSO change." \
  --attach=./notes.md --dry-run

# Resume a phase waiting for human adjudication
./gorchestrator resume --project=foo --issue=1 --decision=pass --feedback="looks good"

# Retry a failed phase with better context
./gorchestrator resume --project=foo --issue=1 --decision=retry --feedback="focus on the auth middleware"
```

### Daemon + dashboard

```bash
# Local auth password (dev only; not for production)
export GORCH_LOCAL_PASSWORD='devpass'

# Optional: if using OpenAI-compatible remote inference (llama.cpp, Ollama, …)
# set default_model.base_url in config, and:
export OPENAI_API_KEY='sk-local'   # often ignored by local servers

./gorchestrator serve --config ~/.config/gorchestrator/config.yaml
# open http://127.0.0.1:8080  → sign in (default user: admin)
```

From the dashboard:

1. **+ New** — submit an issue (required project from YAML registry, title, optional **description** and text **attachments**, optional agent flavor selects when a stage has multiple flavors, dry-run).
2. Expand a card — phase strip, description/attachments if present, artifact buttons, Pass / Fail / Retry with feedback.
3. Open the **artifact drawer** — phase tabs (research / plan / implementation); Result / Output / Activity; for implementation, **Workspace** tree with per-file diffs and zip download when that phase is done.
4. **Notifications** — pending human gates and recent alerts.

Artifacts live under `~/.config/gorchestrator/storage/projects/{project_id}/issues/{issue_id}/` (`issue.md`, `attachments/`, `source/`, per-phase `task.json`, `result.json`, `events.jsonl`, `attempts/`, `implementation/workspace/`).

## Configuration highlights

See `configs/config.example.yaml` for the full surface.

| Block | Purpose |
|-------|---------|
| `default_model` | Provider (`openai`, `anthropic`, `gemini`, `dryrun`), model id, `api_key_env`, optional `base_url` for OpenAI-compatible endpoints |
| `projects` | **YAML registry of projects** (source of truth). Each entry may set `source_path`, `git`, `test`, `trust_external`, and named agent **flavors**. Synced into SQLite at process start. |
| `server` | `listen`, `max_concurrent_issues`, `shutdown_timeout`, `public_base_url` |
| `auth` | `mode: local \| oidc`, local password env, OIDC issuer/client, bootstrap admin emails |
| `notifications.adapters` | Optional names of JSON-RPC adapters with `port: notification` (Slack webhook, SMTP email) |
| `agents.*` | Global per-role adjudicator, max_attempts, model overrides (overlaid by project flavors) |

### OpenAI-compatible local / remote inference

```yaml
default_model:
  provider: openai
  model: your-model-name          # must match the server’s model id
  api_key_env: OPENAI_API_KEY
  base_url: http://INFERENCE_HOST:8080/v1
  timeout: 300s
```

Tool schemas are sent as standard JSON Schema (lowercase types) so strict servers such as llama.cpp accept them.

### Auth modes

- **`local`** (default for dev): username from `auth.local_username` (default `admin`), password from env `auth.local_password_env` (`GORCH_LOCAL_PASSWORD`). Bootstrap user is admin.
- **`oidc`**: set `issuer_url`, `client_id`, and `client_secret_env`. Redirect URI is `{public_base_url}/auth/callback`.
- **`disabled`**: tests only — never use in production.

## Commands

| Command | Role |
|---------|------|
| `run` | Create issue + run pipeline in-process (does not use the queue) |
| `resume` | Apply human decision and continue pipeline in-process |
| `serve` | Recover in-flight work, start workers + HTTP/dashboard, block until signal |
| `version` | Print version |

## Development

```bash
go test ./...
go build .
```

Dashboard static assets (vendored, no build step):

- `internal/web/static/js/htmx.min.js` — [htmx 2.0.4](https://htmx.org/)
- `internal/web/static/js/highlight.min.js` — [highlight.js 11.11.1](https://highlightjs.org/)
- `internal/web/static/css/highlight-dark.css` — github-dark theme
- `internal/web/static/js/app.js` — drawer + SSE card refresh helpers

## Documentation convention

- `spec.md` is the living design document and single source of truth.
- `phase_*.md` files are frozen changelogs for completed phases.
- Markdown is canonical; companion `phase_*.html` renderings are for human convenience only. If a `.md` and its `.html` disagree, the `.md` wins.

## Phase history

| Phase | Focus | Status |
|-------|--------|--------|
| 1 | Engine / CLI | Complete |
| 2 | ADK pipeline, adjudication, crash recovery | Complete |
| 3 | Daemon, dashboard, auth, notifications | Complete |
| 4 | Extensibility (git, run_test, MCP, triggers, S3, …) | In tree — see `phase_4.md` |
| 4 refactor | YAML project registry + agent flavors | Complete — see `phase_4_project_refactor.md` |
| Post-4 polish | Description/attachments; multi-phase drawer; workspace.zip | Complete — see `spec.md` §7 / §8.3 / §11.5 |
| 5 | Guardrails (budgets, effort, scope) | Draft — see `phase_5.md` (do not regress polish) |
| 6 | Polish & ship | Draft — see `phase_6.md` |
