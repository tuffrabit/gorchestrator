# Phase 3 Implementation Plan — Daemon, Dashboard & Auth

> **Status:** Ready to implement  
> **Prerequisite:** Phase 2 complete (full pipeline, unified adjudication, crash recovery, revised artifact contract).  
> **Scope:** Convert the one-shot CLI into a long-running daemon, then give humans a real-time window and intervention controls over it.  
> **Sequencing:** Five session-sized parts (A–E). Parts are ordered by dependency: daemon core first, then API, auth, dashboard, notifications. A–B can share a long session; C is a hard prerequisite for D; E can trail slightly but success criteria need it.

---

## Goal

```bash
gorchestrator serve
```

A team member logs in via OIDC (or local auth in dev), submits an issue from the dashboard (or CLI/API), watches the pipeline execute live, clicks **retry with a reason** at a human gate, and the agent re-runs with that feedback in context. Killing the daemon mid-run and restarting it recovers every in-flight issue (spec §9.4). Slack and/or email alerts fire on human gates and bad output.

---

## Why daemonization is Part A, not a side effect

Everything in this phase — HTTP submission, real-time views, human gates that respawn workers in-process, parallel issues — presumes a persistent process with a work queue. The spec (§11.0) names this explicitly so it is scheduled, not discovered. The Phase 2 engine is already an embeddable library (`Engine.Run` / `Engine.Resume`); this phase changes the front-end and process model, not the phase machine.

---

## Decisions

| Topic | Decision | Rationale |
|-------|----------|-----------|
| Frontend | **HTMX + server-rendered HTML** — soft decision §17 Q1 promoted to hard for this phase | Matches stdlib-only philosophy; no JS build pipeline. |
| Real-time transport | **SSE** — soft decision §17 Q4 promoted to hard; polling fallback on list/detail | One-way status fits; stdlib-friendly. |
| SSO scope (§17 Q6) | **OIDC-only** in Phase 3. SAML remains a documented external adapter; **no SAML code** this phase. | Spec already places SAML outside the core. OIDC covers cloud IdPs (Okta, Google, Azure AD, Keycloak). |
| Dev / headless auth | **`auth.mode: oidc \| local`**. Local mode uses a bootstrap password user from config; required for tests and first-boot without an IdP. | Pure OIDC blocks automated tests and solo dev. Local is explicitly non-production. |
| Bootstrap admin | Config list `auth.bootstrap_admin_emails`. First matching login becomes/stays `admin`; unknown users default to `member`. | Avoids empty-admin lockout; no interactive setup wizard. |
| Roles | `admin` / `member` / `viewer` as in the draft | viewer = read; member = submit + adjudicate; admin = config surface + escalation recipient. |
| Notification delivery (§17 Q5) | **Console built-in** + **both Slack webhook and SMTP email** as external JSON-RPC adapters. Config enables zero or more sinks. | Port + two thin adapters is cheap once the port exists; team picks what they run. Exercises the hardened JSON-RPC client with real adapters. |
| Engine API | Library methods on `Engine` (or a thin `Service` wrapper): `SubmitIssue`, `Decide`, `GetIssue`, `ListIssues`, `ListProjects`, `Subscribe`, `RecoverAll`. CLI `run`/`resume` stay thin sync callers of the existing path. | Daemon and CLI share core; queue is daemon-only. |
| Queue model | **Issue row is the queue.** Statuses: `queued`, `in_progress`, `waiting_human`, `done`, `failed`, `cancelled`. Workers claim `queued` via `UPDATE … WHERE status='queued'`. No separate jobs table. | Filesystem remains authoritative for phase state; SQLite indexes work. Avoids dual-queue drift. |
| Human gate (daemon) | Worker writes `waiting_human`, **exits**, frees the pool slot. `Decide` applies the verdict and re-sets status to `queued` (or terminal on fail). New worker picks it up. | Spec §9.3: goroutines die on gates; no sleep/poll loops. |
| CLI vs daemon | `run` / `resume` remain one-shot and do **not** require a running daemon. `serve` owns the queue and HTTP surface. Submitting via API/dashboard always goes through the queue. | Headless/local workflows must not regress. |
| Concurrency | `server.max_concurrent_issues` (default **2**). One pipeline per worker goroutine. | WAL + `busy_timeout` already land in cleanup; keep default conservative. |
| Graceful shutdown | On SIGTERM/SIGINT: stop accepting new claims; wait up to `server.shutdown_timeout` (default 30s) for in-flight phases; then cancel contexts. Shutdown never writes `failed` — clean cancel → `cancelled`; abrupt exit leaves `in_progress` for recovery. | Spec §9.4 + §16 (`cancelled` is distinct). |
| HTTP stack | stdlib `net/http` + `html/template` + embed.FS. No chi/gin/echo required. | Consistency with stack table. |
| Markdown | `github.com/yuin/goldmark` (server-side) for artifact rendering. | Pure Go, common, no browser JS dependency for correctness. |
| Syntax highlighting | Vendored **highlight.js** (or similar) static asset for code blocks in the dashboard. | No build step; small; good enough for MVP. |
| Diff view | Server-side unified diff of `source/` vs `implementation/workspace/` (file walk + line diff). Pure Go; no `diff` binary dependency. Shown in the **artifact drawer** Diff tab. | Spec §11.5; Phase 4 git model will supersede snapshot diffs later. |
| Event bus | In-process pub/sub on the daemon. SSE handlers subscribe; producers are the engine (status, phase events, decision requested). Historical activity still read from `events.jsonl`. | Single-process deployment; multi-instance is out of scope. |
| Sessions | SQLite `sessions` table; opaque random token in `HttpOnly; Secure; SameSite=Lax` cookie (Secure off when listen is plain HTTP localhost). | Stateless JWT not needed; revocation is a DELETE. |
| CSRF | Session-bound token on state-changing form POSTs (HTMX). API clients using session cookies must send the token header; pure Bearer-less JSON from same origin uses double-submit. | Dashboard is cookie-authenticated. |
| Webhook / TriggerPort | **Not in Phase 3.** `POST /api/issues` is the HTTP submit path. TriggerPort formalization + GitHub/Jira stay Phase 4. | Avoids double-building triggers. |
| Audit completeness | Wire `audit_log` for decisions, submissions, logins, role changes. Phase 6 “complete audit logging” means metrics UI polish, not the table. | Spec §10.1; enough for “who decided what.” |
| `decided_by` | Store stable user id (or `local:<username>` / `cli`) in `decisions.decided_by` and audit rows. | CLI path keeps `"cli"`. |
| Dashboard layout | **Vertical expandable issue cards** — not a kanban/sprint board. Multi-expand. Collapsed = minimal meta; expanded = result summary + adjudication. Full artifacts in a **right slide-out drawer**. | Spec §11.5. Operator-scannable feed over board theater. |
| Submit UX | Top-bar **New issue** (neon pink primary) opens the **submit drawer** (same shell as artifact drawer). | Keeps the feed clean. |
| Adjudication placement | **On the expanded card** (not drawer-only). Drawer is for full JSON/output/events/diff. | Gate actions stay one expand away. |
| Theme | **Dark only** — no light theme, no switcher. Blue-greys + **hot neon pink** accent. Status washes: blue=queued, green=in_progress, yellow=waiting_human, red=failed. Tokens in §11.5.5. | Operator preference; one palette to maintain. |

---

## Dependencies (new)

```text
github.com/coreos/go-oidc/v3   # OIDC discovery + ID token verify
golang.org/x/oauth2           # authorization-code flow
github.com/yuin/goldmark      # Markdown → HTML for artifact viewer
```

No React, no websocket library, no ORM. Existing: `modernc.org/sqlite`, `gopkg.in/yaml.v3`, ADK stack.

---

## Package Layout (additions)

```text
.
├── main.go                         # adds `serve` subcommand
├── adapters/
│   ├── slack/                      # NEW: Slack webhook JSON-RPC adapter binary
│   │   └── main.go
│   ├── email/                      # NEW: SMTP email JSON-RPC adapter binary
│   │   └── main.go
│   ├── slack.yaml
│   └── email.yaml
├── internal/
│   ├── auth/                       # NEW: sessions, OIDC, local login, roles, middleware
│   ├── daemon/                     # NEW: worker pool, claim loop, shutdown, RecoverAll wiring
│   ├── notify/                     # NEW: NotificationPort, console sink, dispatcher
│   ├── server/                     # NEW: HTTP mux, API handlers, SSE, CSRF, static
│   ├── web/                        # NEW: embed.FS templates + static (htmx, highlight.js, css)
│   │   ├── templates/
│   │   └── static/
│   ├── cli/                        # + serve.go
│   ├── config/                     # + server, auth, notification blocks
│   ├── orchestrator/               # + SubmitIssue, Decide, List*, Subscribe, RecoverAll
│   └── sqlite/                     # migrations: users, sessions, audit_log, notifications;
│                                   # IssueRepo claim/list helpers; User/Session/Audit/Notify repos
└── configs/
    └── config.example.yaml         # + serve/auth/notify examples
```

---

## Config surface (additions)

```yaml
server:
  listen: "127.0.0.1:8080"
  max_concurrent_issues: 2
  shutdown_timeout: 30s
  # public_base_url used for OIDC redirect_uri construction
  public_base_url: "http://127.0.0.1:8080"

auth:
  mode: local                    # local | oidc
  # local mode (dev/test only)
  local_username: admin
  local_password_env: GORCH_LOCAL_PASSWORD
  # oidc mode
  oidc:
    issuer_url: ""
    client_id: ""
    client_secret_env: GORCH_OIDC_CLIENT_SECRET
    # redirect is {public_base_url}/auth/callback unless overridden
    scopes: ["openid", "profile", "email"]
  bootstrap_admin_emails:
    - admin@example.com
  session_ttl: 168h              # 7 days

notifications:
  # built-in console always on when serve runs
  adapters: []                   # entries from top-level adapters: that implement notify
  # e.g. declare under adapters: then list names here, or reuse top-level adapters with port: notification
```

Adapter manifests for Slack/email use `port: notification` (existing registry model from cleanup).

---

## Engine API surface

Build on the existing `Engine` (do not invent a second orchestrator). Add methods; keep `Run`/`Resume` for CLI.

```go
// SubmitIssue creates the issue (snapshot source if configured), sets status
// queued, and returns immediately. Daemon workers pick it up.
func (e *Engine) SubmitIssue(ctx context.Context, opts RunOptions) (*sqlite.Issue, error)

// Decide applies a human decision (pass|fail|retry + feedback), records
// decided_by, writes feedback.md on retry, and either terminalizes or
// re-queues the issue for the worker pool.
func (e *Engine) Decide(ctx context.Context, opts DecideOptions) error

// RecoverAll walks non-terminal issues, reconciles SQLite to filesystem
// truth (§9.4), re-queues recoverable work, leaves waiting_human alone.
func (e *Engine) RecoverAll(ctx context.Context) error

// GetIssue / ListIssues / ListProjects — read models for API + dashboard.
// Include phase status, attempt, token totals (from runs + optional FS peek).

// Subscribe returns a cancellable stream of daemon events for SSE.
func (e *Engine) Subscribe(ctx context.Context, filter EventFilter) <-chan Event
```

**CLI mapping**

| Command | Behavior |
|---------|----------|
| `run` | Sync: create issue + `runPipeline` in-process (unchanged semantics). Does not enqueue. |
| `resume` | Sync: `applyHumanDecision` + `runPipeline` (unchanged). |
| `serve` | `RecoverAll` → start worker pool + HTTP server; blocks until signal. |

**Daemon mapping**

| Action | Behavior |
|--------|----------|
| API/dashboard submit | `SubmitIssue` → `queued` |
| Worker claim | CAS status `queued` → `in_progress`, run pipeline until terminal / waiting_human / error |
| Human gate | status `waiting_human`; worker exits; notification enqueued |
| API/dashboard decide | `Decide` → re-queue or terminal |
| Startup | `RecoverAll`: `in_progress` with no live worker → re-queue; `waiting_human` stays; FS/SQLite reconcile |

---

## Schema migrations (versioned, continue from Phase 2)

**Migration 3 — users & sessions**

```sql
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT UNIQUE NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'member',  -- admin | member | viewer
    oidc_subject TEXT UNIQUE,
    password_hash TEXT,                  -- local mode only; null for OIDC users
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_login_at DATETIME
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,                 -- random opaque token
    user_id INTEGER NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

**Migration 4 — audit_log**

```sql
CREATE TABLE audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,                     -- null for system/cli
    action TEXT NOT NULL,                -- login, submit_issue, decide, ...
    target_type TEXT,                    -- issue, user, config
    target_id TEXT,
    details_json TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX idx_audit_created ON audit_log(created_at);
CREATE INDEX idx_audit_target ON audit_log(target_type, target_id);
```

**Migration 5 — notifications**

```sql
CREATE TABLE notifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id INTEGER,
    kind TEXT NOT NULL,                 -- human_gate | bad_output | info
    recipient TEXT NOT NULL,             -- email, slack channel id, or "console"
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending', -- pending | sent | failed
    error TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    sent_at DATETIME,
    FOREIGN KEY (issue_id) REFERENCES issues(id)
);

CREATE INDEX idx_notifications_status ON notifications(status);
```

Issue status values expand to include `queued` (workers only claim that status). Existing `in_progress` / `waiting_human` / `done` / `failed` / `cancelled` remain.

---

## HTTP API

All `/api/*` routes require an authenticated session (cookie). Authorization checked per role.

| Method | Path | Role | Description |
|--------|------|------|-------------|
| `POST` | `/api/issues` | member+ | Submit issue `{project, title, source?, dry_run?}` → `202` + issue |
| `GET` | `/api/issues` | viewer+ | List issues (`?project=&status=`) |
| `GET` | `/api/issues/{id}` | viewer+ | Status, phase, attempts, token totals, artifact index |
| `GET` | `/api/issues/{id}/artifacts/{path...}` | viewer+ | Read-only artifact bytes (path containment enforced) |
| `POST` | `/api/issues/{id}/decisions` | member+ | `{decision, feedback, phase?}` |
| `GET` | `/api/events` | viewer+ | SSE stream (`?issue_id=` optional filter) |
| `GET` | `/api/projects` | viewer+ | Project list |
| `GET` | `/api/notifications` | viewer+ | Recent notifications / pending gates (for notification center) |

**Auth routes (HTML + redirects)**

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/login` | Local form or “Login with SSO” button |
| `POST` | `/auth/local` | Local username/password → session |
| `GET` | `/auth/oidc/start` | Redirect to IdP |
| `GET` | `/auth/callback` | OIDC code exchange → session |
| `POST` | `/logout` | Destroy session |

**Dashboard pages & HTML partials** (see §11.5 for UX contract)

| Path | Description |
|------|-------------|
| `/` | Issue feed — vertical expandable cards (live via SSE) |
| `/issues/{id}` | Same feed, target card pre-expanded; optional `?drawer=result\|output\|events\|diff` |
| `/notifications` | Pending human gates + recent notifications |
| `/partials/issues` | HTMX: card list fragment (filters applied) |
| `/partials/issues/{id}` | HTMX: single card (`?expanded=1`) |
| `/partials/issues/{id}/drawer` | HTMX: artifact drawer body (`?tab=result\|output\|events\|diff`) |
| `/partials/submit` | HTMX: submit-issue drawer form |

JSON errors: `{"error":"..."}` with appropriate status (401/403/404/409/422/500). No sessions required *beyond* the cookie middleware — no separate API keys in this phase.

---

## Dashboard UX (implementation contract)

Normative product copy lives in **spec §11.5**. This section is the implementer’s checklist so Part D does not invent a different UI.

### Shell

```
┌──────────────────────────────────────────────────────────────────┐
│  gorchestrator    Issues    Notifications (badge)    [+ New]  👤 │  ← --bg-elevated, pink CTA
├──────────────────────────────────────────────────────────────────┤
│  Project ▾   Status ▾   [ Needs you ]   search…                  │  ← filter strip
│                                                                  │
│  ┌─ #14 · acme · Add OIDC ──────── queued · research · 0 tok  › │  ← blue wash
│  └──────────────────────────────────────────────────────────────┘│
│  ┌─ #13 · acme · Fix login ───── in_progress · plan · 12.4k  ⌄  │  ← green wash, EXPANDED
│  │  research ✓ ── plan ● ── implementation ○                     │
│  │  status=in_progress  attempt=1  tokens=4200  1.2s             │
│  │  [result.json] [output] [activity] [diff]                     │
│  │  feedback ┌────────────────────────────┐                      │
│  │           │ why?                       │  [Pass][Fail][Retry] │
│  └──────────────────────────────────────────────────────────────┘│
│  ┌─ #12 · … ──────────────────── waiting_human · research   ›   │  ← yellow + pink pulse edge
│  └──────────────────────────────────────────────────────────────┘│
│                                         ┌───────────────────────┐│
│                                         │ Result│Output│Activity││  ← drawer over scrim
│                                         │ { json … }            ││
│                                         └───────────────────────┘│
└──────────────────────────────────────────────────────────────────┘
```

### Status → card wash

| Status | Wash | Leading bar | Chip text |
|--------|------|-------------|-----------|
| `queued` | blue `#1a2a4a` | `#3d7eff` | waiting its turn |
| `in_progress` | green `#0f2a22` | `#2ee59a` | active |
| `waiting_human` | amber `#2a2410` | `#f5c542` | needs human (+ optional pink edge pulse) |
| `failed` | red `#2a1218` | `#ff4d6a` | failed |
| `done` | slate `#141c24` | `#5b8def` | done |
| `cancelled` | grey `#1a1d24` | `#6b7280` | cancelled |

Full CSS variable table: spec §11.5.5. Implement as `:root { --bg-app: … }` in a single embedded `app.css`.

### Interaction rules

1. **Collapsed card click** toggles expand (multi-expand allowed).
2. **Artifact buttons** open/replace the right drawer; they do not navigate away.
3. **Adjudicate on card** via HTMX POST → swap card partial to new status wash.
4. **New issue** opens submit drawer; success prepends a card and closes drawer.
5. **SSE** replaces individual card partials by id without collapsing siblings.
6. **No theme toggle.** Dark tokens only.

### CSS / assets

- `internal/web/static/css/app.css` — tokens + layout + card + drawer + utilities
- `internal/web/static/js/htmx.min.js` — vendored
- `internal/web/static/js/highlight.min.js` + a dark theme CSS (e.g. github-dark or custom pink-accent highlight)
- Optional tiny `drawer.js` only if HTMX alone is awkward for Esc/focus-trap; prefer HTMX + a few lines of vanilla JS over a framework

### Templates (suggested)

```text
internal/web/templates/
  layout.html          # shell, top bar, scrim+drawer host, SSE connect
  login.html
  feed.html            # filter strip + #issue-feed
  notifications.html
  partials/
    issue_card.html    # collapsed + expanded variants (or one with {{if .Expanded}})
    issue_list.html
    drawer_artifact.html
    drawer_submit.html
```

---

## Work items by part

Execute roughly in order. Each item should be commit-sized. Parts A→E are sequential dependencies.

### Part A — Daemonization & Engine surface

**A1. Engine library methods** — `internal/orchestrator/`
- Add `SubmitIssue`, `Decide` (factor from `Resume`/`applyHumanDecision`), `GetIssue`, `ListIssues`, `ListProjects`, `RecoverAll`.
- `DecideOptions` includes `UserID` / `DecidedBy` string for audit.
- `Run`/`Resume` remain; `Resume` may call `Decide` + sync pipeline for CLI.
- On human boundary in daemon mode: set issue `waiting_human`, create pending decision row, emit event, return from pipeline without error-as-failure (control-flow return).

**A2. In-process event bus** — `internal/orchestrator/` or `internal/daemon/`
- `Subscribe` / `Publish` with buffered channels; drop-on-slow-subscriber policy documented (SSE clients should reconnect).
- Event types: `issue_submitted`, `issue_status`, `phase_started`, `phase_finished`, `decision_requested`, `decision_applied`, `run_event` (optional thin wrapper pointing at latest events.jsonl offset).

**A3. Worker pool** — `internal/daemon/`
- N workers (`max_concurrent_issues`); claim loop on `queued` issues.
- One `runPipeline` per claim; dry-run flag stored on issue row or in a small `issues` extension — prefer a nullable `options_json` column only if needed; otherwise store dry_run in issue title path is wrong — **add `issues.dry_run INTEGER NOT NULL DEFAULT 0`** in migration 3 or a tiny migration 3b if cleaner.
- Prevent double-claim: single-threaded claim in one dispatcher goroutine, or `UPDATE … WHERE id=? AND status='queued'` check rows-affected.

**A4. `serve` subcommand** — `internal/cli/serve.go`, `main.go`
- Load config, `NewEngine`, `RecoverAll`, start pool + (later) HTTP, block on signal, graceful shutdown.

**A5. Startup recovery scan** — `RecoverAll`
- Implement full §9.4 table across all non-terminal issues.
- Re-queue crashed `in_progress`; leave `waiting_human`; reconcile SQLite status/phase from filesystem.
- Integration test: submit via engine → kill mid-phase (reuse dry-run block pattern from Phase 2) → new engine `RecoverAll` + workers → completes.

**A6. Graceful shutdown**
- SIGTERM path as in Decisions; unit/integration test that shutdown does not mark issues `failed`.

**A7. Config: server block** — `internal/config/`, `configs/config.example.yaml`

**Part A tests:** claim concurrency (N issues, M&lt;N workers, no lock errors); recover-after-kill; shutdown cleanliness; CLI `run` still works without `serve`.

---

### Part B — HTTP API (unauthenticated stub → ready for auth)

**B1. `internal/server` skeleton**
- `net/http` mux, JSON helpers, request logging, panic recovery.
- Mount under `serve`.

**B2. Issue API handlers**
- Implement the table above against Engine methods (auth middleware is a no-op or test bypass until Part C — **prefer failing closed with a temporary `auth.mode: disabled` only for unit tests**, never for default config).

**B3. Artifact fetch**
- Resolve `{path...}` through StoragePort; reject `..` and absolute paths; stream bytes with content-type sniffer for md/json/text.

**B4. SSE endpoint**
- `GET /api/events`: `text/event-stream`, subscribe to bus, heartbeat comments every 15s, disconnect on context cancel.
- Polling fallback: list/detail already return current status — document that HTMX can `hx-trigger="every 5s"` if SSE fails.

**B5. Decision API**
- Validate decision enum; warn path when feedback empty on retry/fail (HTTP 200 still applies decision; response body includes `warning`).
- Conflict if issue not `waiting_human` and not mid-pipeline override: **spec §9.3 says humans can pass/fail/retry any output at any time** — support adjudication on the current phase even if adjudicator was null/self, as long as the phase has a terminal or waiting result. Narrow MVP: allow decide when status is `waiting_human` **or** when explicitly overriding a completed phase's last attempt (member+). If override on `done` phase is complex, ship `waiting_human` first and add “adjudicate any boundary” as B5b before Part D UI needs it.

**Clarification (B5):** Ship two capabilities:
1. Resume path for `waiting_human` (required).
2. Manual intervene on latest phase output (`pass`/`fail`/`retry`) regardless of configured adjudicator when issue is `in_progress` at a boundary or phase is `done` but pipeline not finished — match §9.3. Implementation: `Decide` accepts optional force flag; dashboard always shows the controls on the current phase card.

**Part B tests:** httptest submit → (with worker) complete; decide with feedback; SSE receives status events during dry-run; path traversal on artifacts rejected.

---

### Part C — Users, Auth, Audit

**C1. Migrations 3–4 + repos** — users, sessions, audit_log

**C2. Password hashing (local mode)** — `golang.org/x/crypto/bcrypt` for local users only.

**C3. Session middleware**
- Load session from cookie; attach `User` to context; 401 JSON / redirect HTML.

**C4. Local auth**
- Ensure bootstrap user exists on serve start when `auth.mode=local` (create from config if missing).
- Login form + POST handler.

**C5. OIDC auth**
- Authorization-code flow with PKCE if the provider supports it; state + nonce; map `email` claim to user; apply bootstrap admin emails; set `oidc_subject`.
- Reject `auth.mode=oidc` at startup if issuer/client_id missing.

**C6. Role authorization**
- Helpers: `RequireRole(min)`.
- viewer cannot POST decisions/submit; member cannot hit future admin-only routes; unauthenticated rejected.

**C7. Audit writers**
- Login, logout, submit, decide (include feedback excerpt), failed auth.

**C8. Wire `decided_by`** from session user id/email through API → `Decide`.

**Part C tests:** viewer forbidden on decide; unauthenticated 401; local login happy path; session expiry; audit row on decide; OIDC flow unit-tested with a mock OIDC provider or recorded token verify (no real network in CI).

---

### Part D — Dashboard (HTMX) — implements spec §11.5

**D1. Design tokens + app shell** — `internal/web/static/css/app.css`, `templates/layout.html`
- Implement §11.5.5 CSS variables exactly (names + hex values).
- Dark page chrome: top bar, filter strip, main feed column, drawer host + scrim.
- Vendor `htmx.min.js` + highlight.js (dark style). No theme switcher.
- Focus rings, reduced-motion media query, monospace for ids/JSON.

**D2. Collapsed issue card partial** — `partials/issue_card.html`
- Minimal: `#id`, project, title, status chip, phase, attempt, tokens, relative updated.
- Status wash + leading accent bar from status map (§11.5.5).
- Entire header toggles expand via HTMX (`hx-get` card with `expanded=1` or client toggle + swap).
- Multi-expand: each card owns its expanded state; do not collapse siblings.

**D3. Expanded card body**
- Phase strip (research → plan → implementation) with pink “current” marker.
- Truncated `result.json` summary fields.
- Artifact action buttons → load drawer partials (`result`, `output`, `activity`, `diff`).
- Adjudication block on-card: feedback textarea + Pass/Fail/Retry (member+); disabled for viewer.
- Empty feedback warning on fail/retry (client + optional server warning).

**D4. Artifact drawer**
- Right slide-out, tabs Result | Output | Activity | Diff.
- Goldmark for Markdown output; highlighted `<pre>` for JSON/events.
- Diff tab: server-side unified diff when workspace exists; hide tab otherwise.
- Esc / scrim / ✕ close; one drawer at a time; size-cap huge payloads with notice.

**D5. Feed page + live updates** — `/`, `/partials/issues`
- Vertical list, default sort by `updated_at` desc.
- Filters: project, status, optional “Needs you” (`waiting_human`).
- SSE (or HTMX sse extension): on `issue_status`, re-fetch/swap that card partial **without** collapsing other expanded cards.
- Empty state with New-issue affordance for member+.

**D6. Submit drawer** — top-bar **+ New** (accent button)
- Form: project, title, optional source path, dry-run checkbox (member+).
- Success: close drawer, prepend card into `#issue-feed` via HTMX.

**D7. Deep links + notifications page**
- `/issues/{id}` pre-expands card; `?drawer=` opens artifact tab.
- `/notifications`: pending gates (link/scroll to card) + recent notification rows; badge count on top bar.

**D8. CSRF** on all state-changing HTML/HTMX posts.

**D9. Accessibility pass**
- Status text chips (not color-only); `aria-expanded` on cards; `aria-modal` on drawer; keyboard expand/close.

**Part D tests:** template smoke tests with fixture issues in each status (assert status CSS class / wash class present); httptest expand partial, decide POST → card re-render; drawer partial returns 200 for result/output; viewer cannot POST decide; CSRF rejection without token. Manual checklist: multi-expand, SSE re-tint, drawer Esc, neon pink CTA visible on dark bg.

---

### Part E — Notifications

**E1. `NotificationPort`** — `internal/notify/`

```go
type Notification struct {
    Kind      string // human_gate, bad_output, info
    Recipient string
    Subject   string
    Body      string
    IssueID   int64
}

type Port interface {
    Send(ctx context.Context, n Notification) error
}
```

- Built-in `Console` implementation (log).
- Multi-sink fan-out dispatcher implementing `Port`.

**E2. Dispatcher loop**
- Insert row `pending` → call sinks → `sent` / `failed`.
- Triggered on: human gate entered; phase `failed` with error/timeout/empty-output; optional info events later.

**E3. Admin recipients**
- Resolve users with `role=admin` (email as recipient key for email sink; Slack uses config channel/webhook and mentions admins in body).

**E4. Slack webhook adapter** — `adapters/slack/`
- Manifest `port: notification`; JSON-RPC method e.g. `notification.send`.
- Posts incoming webhook JSON `{text}` or blocks-lite.
- Register via config adapters list.

**E5. SMTP email adapter** — `adapters/email/`
- Manifest `port: notification`; params include SMTP host/port/user from adapter env/config (adapter-owned credentials, pattern for §17 Q12 later).
- Send plain-text email.

**E6. Wire HumanAdjudicator / gate path** to enqueue `human_gate` notifications; wire bad-output hooks in engine on failed phase.

**Part E tests:** console sink receives human_gate on waiting_human; dispatcher marks sent; Slack/email adapters unit-tested with httptest/net.Listener fake SMTP or mocked HTTP; end-to-end dry-run with console only in CI.

---

## Tests (phase-level acceptance)

| # | Test | Covers |
|---|------|--------|
| 1 | Daemon lifecycle: submit → workers → artifacts + SQLite consistent | A, B |
| 2 | Kill -9 mid-phase → restart serve → recovery re-runs phase | A |
| 3 | Human gate via API: decide+feedback → retry attempt includes feedback | A, B, C |
| 4 | Concurrency: N parallel issues, M&lt;N workers; no SQLite busy failures | A |
| 5 | AuthZ: viewer cannot decide; unauthenticated rejected | C |
| 6 | SSE: events observed during dry-run pipeline | B |
| 7 | Local login + session cookie round-trip | C |
| 8 | Notification row + console sink on human_gate | E |
| 9 | CLI `run` / `resume` still pass existing Phase 2 tests | A regression |

---

## Success Criteria

- Team can log in via OIDC (or local in dev), watch a live **card feed**, expand an issue, retry with a reason on-card, open full artifacts in the **drawer**, and get a Slack/email/console alert on a human gate.
- Cards re-tint by status (blue/green/yellow/red/…) under SSE without a full page reload; multi-expand works.
- Dark-only theme with neon pink primary actions; no light theme or theme toggle.
- Daemon survives kill/restart with zero lost or corrupted issues (§9.4).
- CLI one-shot mode still works unchanged for local/headless use.
- `go test ./...` green; dry-run demo path documented in README.

---

## Out of Scope

| Item | Phase |
|------|-------|
| MCP, external triggers (GitHub/Jira), formal TriggerPort, git workspace, S3 | 4 |
| Token budgets, effort gates, admin escalation rules UI | 5 |
| SAML SSO adapter implementation | later (port reserved; not scheduled) |
| Multi-node daemon / shared queue across hosts | never (MVP scale) |
| Metrics dashboard polish, retention policies | 6 |
| React/SPA frontend | not planned |

---

## Implementation order (summary)

```text
Part A  Daemon + Engine API + workers + recovery + serve
  ↓
Part B  HTTP API + SSE + artifacts + decisions
  ↓
Part C  Users / sessions / OIDC+local / roles / audit
  ↓
Part D  HTMX dashboard (list, detail, adjudicate, diff, notify center)
  ↓
Part E  Notification port + console + Slack + email adapters
```

---

## Spec alignment

- §17 Q5 / Q6 closed as soft decisions (notifications + OIDC-only).
- **§11.5** is the canonical Dashboard UX contract (layout, tokens, interaction).
- This file’s **Dashboard UX** section + **Part D** are the implementation breakdown of §11.5.

---

*End of Phase 3 Plan*
