# gorchestrator

gorchestrator is a tight human + AI agent collaboration platform for software engineering teams. It coordinates AI agents through a **user-defined flow**: at submit time you pick an ordered list of agent ids declared under the top-level `agents:` block of the config YAML (1–8 agents, frozen at submit). Each position in the flow — a **step** — runs one agent with its own configured `system_prompt` and tool list, and hands its accepted output to the next step, with explicit human adjudication gates between steps. An external-adapter model covers storage, triggers, and notifications.

The flow is picked per issue: in the dashboard submit form's flow builder, via the `flow` field of the submit API, or with `-flow` on the CLI. Webhook/adapter/CLI submits that carry no flow use the project's `default_flow`; if neither is set, the submit is rejected. Steps get artifact dirs named `step-1`…`step-N` under the issue dir, and steps that edit files share one issue-level `workspace/`.

## Current status

**Phases 1–5 are complete.** Phase 4 extensibility (git workspaces, container `run_test`, MCP, triggers, S3), YAML project registry + user-defined agent flows, and post–Phase 4 polish (description + text attachments, multi-step artifact drawer, `workspace.zip`) are in tree. **Phase 5 guardrails** landed: scope hold at submit, provider session token budgets, MCP per-tool grants + constraints, YAML escalation with dedupe, admin `/permissions` and `/escalation` pages. The old planner effort gate was removed in the flows refactor — per-step human adjudication is the gate now.

The system runs as a long-lived daemon (`gorchestrator serve`) with a worker pool, HTTP API, OIDC/local auth, HTMX dashboard, and notification sinks (console + optional Slack/email adapters). CLI one-shot `run` / `resume` still work without a daemon.

**Next:** Phase 6 polish & ship (`phase_6.md`) — audit completeness, metrics, retention, docs, deployment packaging. Do **not** regress landed foundations (projects registry, user-defined agent flows, description/attachments, multi-step drawer).

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
`source_path` / git / test / `default_flow` live in that block — not on the submit form.

```bash
# Run an issue through a chosen agent flow (dry-run LLM)
./gorchestrator run --issue="add auth" --project=foo --flow=researcher,coder --dry-run

# Without -flow the project's default_flow is used; without either, the submit is rejected

# Optional description and text attachments
./gorchestrator run --issue="add auth" --project=foo \
  --body="Users cannot sign in after SSO change." \
  --attach=./notes.md --flow=researcher,coder --dry-run

# Resume a step waiting for human adjudication
./gorchestrator resume --project=foo --issue=1 --decision=pass --feedback="looks good"

# Retry a failed step with better context
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

1. **+ New** — submit an issue (required project from YAML registry, title, optional **description** and text **attachments**, ordered **agent flow** builder with add/remove steps, dry-run).
2. Expand a card — step strip, description/attachments if present, artifact buttons, Pass / Fail / Retry with feedback.
3. Open the **artifact drawer** — step tabs (`1 · researcher`, `2 · coder`, …); Result / Output / Activity; **Workspace** tab with the issue-level tree, per-file diffs, and zip download when the issue is done.
4. **Notifications** — pending human gates and recent alerts.

Artifacts live under `~/.config/gorchestrator/storage/projects/{project_id}/issues/{issue_id}/` (`issue.md`, `attachments/`, `source/`, shared `workspace/`, per-step `step-1/`…`step-N/` dirs each with `task.json`, `result.json`, `events.jsonl`, `attempts/`).

## Configuration highlights

See `configs/config.example.yaml` for the full surface.

| Block | Purpose |
|-------|---------|
| `default_model` | Provider (`openai`, `anthropic`, `gemini`, `dryrun`), model id, `api_key_env`, optional `base_url` for OpenAI-compatible endpoints |
| `projects` | **YAML registry of projects** (source of truth). Each entry may set `source_path`, `git`, `test`, `trust_external`, and `default_flow` (fallback agent flow for submits that don't pick one). Synced into SQLite at process start. |
| `server` | `listen`, `max_concurrent_issues`, `shutdown_timeout`, `public_base_url` |
| `auth` | `mode: local \| oidc`, local password env, OIDC issuer/client, bootstrap admin emails |
| `notifications.adapters` | Optional names of JSON-RPC adapters with `port: notification` (Slack webhook, SMTP email) |
| `agents.*` | Global per-agent config keyed by user-chosen agent id. `system_prompt` is **required** (no built-in prompts). Optional: model overrides, `adjudicator`, `max_attempts`, `single_shot` (allowed for any agent), `tools` (narrowing allowlist — omitted = full core tool list, which includes file editing), `mcp_servers` (deny-by-default; `"*"` = every configured server) |

### Choosing a flow

The flow is an ordered list of agent ids from the top-level `agents:` block, frozen at submit. Three shapes cover most teams:

1. **One-shot coder** — `flow: [coder]`. One agent reads the issue and source, writes the change, and finishes. Good for small, well-scoped issues.
2. **Research → coder** — `flow: [researcher, coder]`. The first step compiles context and constraints into its output; the coder works from that instead of re-deriving it.
3. **Research → plan → coder → reviewer** — `flow: [researcher, planner, coder, reviewer]`. The classic pipeline with an explicit review pass before a human sees the result.

**Capability, not role, is the boundary.** Every agent gets the full core tool list — including `write_file` / `update_file` — unless its `tools:` list narrows it. If you add a research-only or reviewer agent, narrow its `tools:` to read-only tools (`read_file`, `list_directory`, `grep_search`, `write_output`) so a hijacked or over-eager step cannot edit the workspace. The remaining hard boundaries are the storage path allowlist (issue dir + source + workspace only), no shell (`run_test` runs only the project's configured command, containerized), and MCP servers being deny-by-default.

Also: consecutive editing steps share **one mutable workspace**. A later step sees — and can overwrite — what an earlier step wrote. That is by design (you own the flow); order your agents accordingly.

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

### Single-shot steps (slow local models)

For models too slow for a multi-round tool loop (e.g. a disk-streamed MoE behind
llama-swap), any agent can run **single-shot**: one no-tools completion whose
reply text becomes the step output.

```yaml
agents:
  scout:
    system_prompt: "You are a context compiler agent..."
    single_shot: true                # bypass the tool loop entirely
    single_shot_context_bytes: 32768 # repo digest stuffed into the prompt
    # context_files: [main.go, ...]  # or pin exact files instead of the digest
    max_attempts: 1
    max_tokens: 1024                 # always cap generation on slow models
    model: { provider: openai, model: deepseek-v4-flash, timeout: 24h }
```

- The single-shot call uses the agent's own `system_prompt` — there are no built-in prompts. The digest is built from the issue's source snapshot (gitignore-filtered, binaries skipped, byte-capped with a truncation marker).
- A single-shot step gets **no tools at all**: its reply text is the step output and it can never edit the workspace, even if its `tools:` list names editing tools. Single-shot fits research/plan-style steps, not coding steps.
- There is no `finish_task`; self-adjudication always passes single-shot output, and a human `retry` re-runs the whole generation (with feedback injected).
- `model.timeout` covers the full request including llama-swap's model-swap wait; it is validated at config load (a typo like `6hours` fails instead of silently falling back to 60s).

### Auth modes

- **`local`** (default for dev): username from `auth.local_username` (default `admin`), password from env `auth.local_password_env` (`GORCH_LOCAL_PASSWORD`). Bootstrap user is admin.
- **`oidc`**: set `issuer_url`, `client_id`, and `client_secret_env`. Redirect URI is `{public_base_url}/auth/callback`.
- **`disabled`**: tests only — never use in production.

## Commands

| Command | Role |
|---------|------|
| `run` | Create issue + run its agent flow in-process (does not use the queue) |
| `resume` | Apply human decision and continue the flow in-process |
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
| 4 refactor | YAML project registry + user-defined agent flows (this change) | Complete — see `plan_agent_handoff_flows.md` |
| Post-4 polish | Description/attachments; multi-step drawer; workspace.zip | Complete — see `spec.md` §7 / §8.3 / §11.5 |
| 5 | Guardrails (scope hold, provider budgets, MCP tools, escalation) | Complete — see `phase_5.md` |
| 6 | Polish & ship | Draft — see `phase_6.md` |
