# gorchestrator

gorchestrator is a tight human + AI agent collaboration platform for software engineering teams. It coordinates AI agents through a structured pipeline of research, planning, and implementation phases, with explicit human adjudication gates and an external-adapter model for storage, triggers, and notifications.

## Current status

**Phase 3 is complete.** The system runs as a long-lived daemon (`gorchestrator serve`) with a worker pool, HTTP API, OIDC/local auth, HTMX dashboard (expandable status-tinted issue cards, artifact drawer, SSE live updates), and notification sinks (console + optional Slack/email adapters). CLI one-shot `run` / `resume` still work without a daemon.

Phases 1–2 remain available as the embeddable engine and CLI path under the same binary.

## Quick start

```bash
# Build
go build .

# Copy the example config and edit as needed
mkdir -p ~/.config/gorchestrator
cp configs/config.example.yaml ~/.config/gorchestrator/config.yaml
```

### One-shot CLI (no daemon)

```bash
# Full pipeline against a project source directory (dry-run LLM)
./gorchestrator run --issue="add auth" --project=foo --source=/path/to/repo --dry-run

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

1. **+ New** — submit an issue (project, title, optional source path, dry-run checkbox).
2. Expand a card — phase strip, artifacts, Pass / Fail / Retry with feedback.
3. **Notifications** — pending human gates and recent alerts.

Artifacts live under `~/.config/gorchestrator/storage/projects/{project_id}/issues/{issue_id}/` (`source/`, per-phase `task.json`, `result.json`, `events.jsonl`, `attempts/`).

## Configuration highlights

See `configs/config.example.yaml` for the full surface.

| Block | Purpose |
|-------|---------|
| `default_model` | Provider (`openai`, `anthropic`, `gemini`, `dryrun`), model id, `api_key_env`, optional `base_url` for OpenAI-compatible endpoints |
| `server` | `listen`, `max_concurrent_issues`, `shutdown_timeout`, `public_base_url` |
| `auth` | `mode: local \| oidc`, local password env, OIDC issuer/client, bootstrap admin emails |
| `notifications.adapters` | Optional names of JSON-RPC adapters with `port: notification` (Slack webhook, SMTP email) |
| `agents.*` | Per-role adjudicator, max_attempts, model overrides |

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
| 3 | Daemon, dashboard, auth, notifications | **Complete** |
| 4+ | Extensibility (MCP, triggers, git, …) | Planned — see `spec.md` / `phase_4.md` |
