# Implementation Plan — User-Selected Agent Handoff Flows

**Status:** plan only (no code written yet)
**Owner note:** this plan is written so it can be executed step-by-step by someone who is not designing the system. Follow the steps in order. Every step lists the files to touch, what to change, and how to prove it works. Do not improvise outside the "Out of scope" list.

---

## 1. What is changing, in one paragraph

Today the pipeline is hard-coded as `research → plan → implementation`, the three agent names `researcher` / `planner` / `implementer` are hard-coded in config validation and in the engine, each stage gets a hard-coded tool list and a hard-coded system prompt, and "flavors" are a per-project override layer on those three names. This plan replaces all of that with a **user-chosen ordered flow of agents**: when an issue is created the user picks an ordered list of agent ids (1..N) that exist in the top-level `agents:` block of the config YAML. Those ids are arbitrary names chosen by the user. Each agent must define its own `system_prompt` (required, no built-in default). By default every agent gets the **full core tool list** unless the agent's YAML sets an explicit `tools:` list. The `projects.<name>.agents` flavor system is deleted.

---

## 2. Current state (verified in tree)

Key facts you need before changing anything:

| Area | File | What is hard-coded today |
|---|---|---|
| Pipeline order | `internal/orchestrator/engine.go` `runPipeline` (line ~600), `currentPhaseState` (~1760), `previousPhase`, `nextPhaseName`, `phaseAgentType` | `[]string{"research","plan","implementation"}` appears in 3 places; mapping phase→agent type in `phaseAgentType` |
| Phase artifacts | `internal/storage/paths.go` | `PhaseDir(...phase)` is already generic, but `WorkspacePath` hard-codes `implementation/workspace` |
| Agent config keys | `internal/config/config.go` | `CoreAgentTypes = {"researcher","planner","implementer"}`, `defaultAgentConfig()` injects built-in prompts, `ProjectAgentConfig`/`FlavorCatalog`/`ResolveCast`/`FlavorOverlay` |
| Tool assignment | `internal/tools/tools.go` | `NewResearcherRegistry`, `NewPlannerRegistry`, `NewImplementerRegistry` (three overlapping lists) |
| Tool selection | `engine.go` `runAgentLoop` (~1360) and `buildTask` (~1590) | `switch phase { case "research": ... }` |
| Agent construction | `internal/agents/{researcher,planner,implementer}.go`, `engine.go` `buildAgent` | three types + three default prompts; `plannerFinishTaskSchema` is planner-only (adds `effort`) |
| Single-shot prompts | `internal/config/config.go` `DefaultSingleShotPrompt`, `internal/orchestrator/single_shot.go` `resolveSingleShotPrompt` | single-shot only allowed for researcher/planner; prompt-sniffing to detect user override |
| Frozen cast | `internal/sqlite/issues.go` (`agent_flavors_json`), `engine.go` `resolveAndMarshalCast`, `parseIssueCast` | map of type → flavor name |
| Effort gate | `internal/orchestrator/effort.go`, `service.go` `maybeHoldForEffort`, `config.go` `ProjectGuardrails.EffortGateMin` | planner-specific effort → human gate before `implementation` |
| Scope hold | `service.go` `maybeHoldForScope` | writes hold `result.json` into the `research` phase dir |
| Web UI submit | `internal/server/dashboard.go` (`submitFormData`, `flavorSelectsForProject`, `handlePartialSubmitPost`), `internal/web/templates/partials/drawer_submit.html`, `partials/submit_flavors.html` | three fixed flavor selects named `agent_flavor_<type>` |
| Web UI drawer | `internal/server/drawer.go` (`knownPhases`, `normalizePhase`, `phaseLabel`, `phaseAgent`), `dashboard.go` `handlePartialDrawer` | three fixed phase tabs |
| API | `internal/server/api.go` `submitIssueRequest.AgentFlavors`, `issueToJSON`, `handleListProjects` | `agent_flavors` field |
| Chat drawer | `internal/server/chat.go` (`validChatAgentType`, `validateChatFlavor`, `chatIdentityOptions`, `defaultChatIdentity`), `internal/orchestrator/chat.go` (~line 290 `FlavorOverlay`) | chat identity = core type + project flavor |
| DB | `internal/sqlite/db.go` migration 8 (`agent_flavors_json`), `issues.current_phase DEFAULT 'research'`, `runs.agent_type` | fixed default phase, fixed cast column |

---

## 3. Target design (the vocabulary we will use)

**Agent** — one entry under top-level `agents:` in YAML. Its key is an *agent id*: a user-chosen slug (`^[a-z0-9][a-z0-9_-]{0,31}$`, lowercase, validated). `researcher`, `planner`, `implementer` remain legal ids but are now just examples, not magic words.

**Flow (a.k.a. pipeline)** — the ordered list of agent ids chosen for one issue, e.g. `["scout", "architect", "coder", "reviewer"]`. Length 1..N (recommend a hard cap of 8 to keep the UI and artifacts sane). Frozen at submit.

**Step** — one position in the flow. A step has:
* `Index` (1-based),
* `AgentID` (from the frozen flow),
* `Key` — the artifact directory name: **`step-1`, `step-2`, … `step-N`**.

**Phase** — the existing word for "a run with its own `task.json` / `result.json` / `events.jsonl` / `attempts/`". We keep the word "phase" in storage/engine code; the phase *name* is now the step key instead of `research|plan|implementation`.

**Legacy issue** — any issue created before this change: `agent_flavors_json != ''` and `pipeline_json` empty. Legacy issues keep their old phase names (`research`, `plan`, `implementation`) and are never migrated on disk.

Artifact layout after the change:

```
projects/{pid}/issues/{iid}/
  issue.md
  attachments/
  source/                     # read-only snapshot / source worktree (unchanged)
  workspace/                  # NEW: issue-level, mutable, shared by every step that edits
  step-1/task.json result.json events.jsonl attempts/1/output.md attempts/1/feedback.md
  step-2/...
```

`workspace/` moves out of `implementation/` because any agent in the flow may now edit files (it gets the full tool list by default). Legacy issues keep reading `implementation/workspace` via a fallback helper.

---

## 4. Open questions / decisions to confirm before coding

Answer these (or accept the **recommended** answer, which is what the rest of this plan assumes). **operator edit - all recommendations are approved, consider these questions answered**

1. **Default flow when nobody picks one.** Webhook/adapter/CLI submits cannot run a flow picker.
   *Recommended:* add optional `projects.<name>.default_flow: [a, b, c]`. Used for webhook/adapter/CLI submits when no flow is given, and used to prefill the web form. If absent and nothing is given → reject the submit with a clear error (`no agent flow: pick agents in the UI or set projects.<name>.default_flow`). Do **not** silently fall back to researcher/planner/implementer.

2. **Effort gate (`guardrails.effort_gate_min`) and planner-only `effort`.** It only makes sense in a pipeline where a "planner" precedes "implementation".
   *Recommended:* **remove** the effort guardrail entirely (`ProjectGuardrails`, `maybeHoldForEffort`, `effort.go`, `plannerFinishTaskSchema`, `PhaseResult.Effort`, `Effort` parsing in `runAgentLoop`, `Effort` special-casing in `acquirePhaseModel` keep-resident logic). Human safety is now provided by the per-step human adjudicator default, which the user configures. If you want to keep it, the alternative is: make `effort` an *optional* field on the generic `finish_task` schema for every agent and gate before the **last** step. That is more code for a feature the user is now choosing manually — flag it, don't sneak it in.

3. **MCP default.** "Full tool list by default" — does it include MCP tools? MCP servers are external processes; today they are deny-by-default (`mcp_servers` allowlist per agent, spec §5.5).
   *Recommended:* keep MCP **opt-in** (core tools are full by default, MCP is not). Add the special value `mcp_servers: ["*"]` to mean "every configured MCP server". Document this clearly in README/spec. (If you decide MCP should be full-by-default too, it is a one-line change in `runAgentLoop` + `chat.go`, but it silently widens the security surface for every agent.)

4. **`run_test` in the full list.** `NewImplementerRegistry` currently has `run_test` **commented out** (tools.go). The full default list cannot silently re-enable a sandboxed arbitrary-code-execution tool.
   *Recommended:* register `run_test` in the core registry **only when the project has a `test:` block configured**, and keep `tools: [ ... ]` as the way to hide it. Keep the existing commented-out block as a TODO marker (delete the comment, add the conditional).

5. **Who prepares the git workspace, and when do we commit?** Because editing tools are now available to every step, `implementation/workspace` cannot be created lazily "at the implementer phase" as today (`runPhase`: `if phase == "implementation"`).
   *Recommended:* prepare the issue workspace **once, lazily, before the first step that has any editing tool** (`write_file`/`update_file`/`run_test` in its effective tool list), and `CommitAll` after **each step whose result is `done`** (it is already a no-op when there are no staged changes: `CommitAll` returns `created=false`). Branch name becomes per-issue (`gorchgit.BranchName(issue.ID, 0)` → see step 6.7) rather than per-run, because the workspace is no longer per-run.
   Confirm: per-step commits vs one commit at the end. Per-step is safer (an intermediate `waiting_human` hold then shows the human what the agent actually wrote) and needs no new git code.

6. **What does a step see as input?** Today a phase sees the issue + the immediately previous phase's accepted output. Keep exactly that (previous **step**). Do not build a transcript of all previous steps in this change; note it as a possible follow-up.

7. **Untrusted external input gate.** Today: external triggers force `adjudicator: human` on the `implementation` phase unless trust is granted.
   *Recommended:* apply the same rule to any step whose **effective tool list contains an editing tool** (`write_file`, `update_file`, `run_test`). Same config flags (`triggers.trust_external`, `projects.<name>.trust_external`).

8. **Chat drawer identities.** Chat today offers core type + project flavor.
   *Recommended:* chat identity list becomes the configured global agent ids (no flavor). `chat_threads.flavor` column stays, always written as `''` (no destructive migration).

9. **Existing issues.** The user/operator will start this application in a new directory with no legacy issues, or will delete them. No need for legacy issue migration or special handling.

---

## 5. Config: new YAML shape (target)

```yaml
# Every key under agents: is a user-chosen agent id. Any number of them (1 is fine).
# system_prompt is REQUIRED. tools is OPTIONAL: omit = full core list.
agents:
  researcher:
    system_prompt: "You are a context compiler agent..."
    model: { provider: openai, model: o3-mini }
    adjudicator: human          # default
  planner:
    system_prompt: "You are a Planner agent..."
    tools: [read_file, list_directory, grep_search, write_output]   # explicit narrowing
  coder:
    system_prompt: "You are an implementer..."
    mcp_servers: []             # optional; default = no MCP
  reviewer:
    system_prompt: "You are a reviewer..."
    single_shot: true           # allowed for ANY agent now

projects:
  acme:
    source_path: /path/to/acme
    default_flow: [researcher, planner, coder]   # optional; see Q1
    # git:, test:, guardrails: (minus effort_gate_min), trust_external:
```

Removed from YAML: `projects.<name>.agents` (whole block), `agents.<id>.system_prompt_append`, `projects.<name>.guardrails.effort_gate_min` (see Q2).
`config.LoadFrom` uses `dec.KnownFields(true)`, so a leftover `agents:` block under a project or a leftover `system_prompt_append` now fails loudly at load with an "unknown field" parse error. That is the desired behavior; make the error message friendlier (see 6.1).

---

## 6. Implementation steps (do them in this order)

### 6.1 — `internal/config/config.go`: agent ids become user-defined

1. Delete `CoreAgentTypes`, `ProjectAgentConfig`, `AgentFlavorInfo`, `ProjectConfig.Agents`, `FlavorCatalog`, `ResolveCast`, `FlavorOverlay`.
2. `AgentConfig`: delete the `SystemPromptAppend` field and every line that handles it in `MergeAgent` (the `overlay.SystemPromptAppend` branch and the "apply append after merge" block).
3. `AgentConfig.Tools` stays as a field but its doc comment changes to: *optional allowlist; empty = full core tool list*.
4. Delete `defaultAgentConfig`'s switch on name (the three built-in prompts) and the whole `DefaultSystemPrompt` / `DefaultSingleShotPrompt` / `singleShot*Prompt` / `default*Prompt` functions. Rename `defaultAgentConfig(defaultModel ModelConfig) AgentConfig` (no name argument) and keep only the generic defaults: model from `default_model`, `Adjudicator: "human"`, `MaxAttempts: 3`, `Loops: 1`, `Rubric: "..."`.
5. Replace `func (c *Config) Agent(name string) AgentConfig` with:
   ```go
   // Agent returns the config for a configured agent id. ok=false when the id
   // is not defined under agents:.
   func (c *Config) Agent(id string) (AgentConfig, bool)
   // AgentMust is the strict form used by the pipeline.
   func (c *Config) AgentMust(id string) (AgentConfig, error) // error: `agent %q is not configured under agents: in config`
   // AgentIDs returns sorted configured ids (for UI/API).
   func (c *Config) AgentIDs() []string
   // HasEditingTool reports whether the agent's effective tool list includes a file-mutating tool.
   func (a AgentConfig) HasEditingTool() bool // empty Tools → true; else intersect with {write_file, update_file, run_test}
   ```
   `MergeAgent` keeps its current shape (base = generic defaults, overlay = user config). Note: since `SystemPrompt` has no default, merging never invents one — validation guarantees it exists.
6. Add validation in `LoadFrom` (new `validateAgents(cfg)`):
   * id matches `^[a-z0-9][a-z0-9_-]{0,31}$`; reject duplicates that differ only by case (`strings.ToLower` map).
   * `SystemPrompt` non-empty → error `agents.%s: system_prompt is required`.
   * `Tools` names must be from the known core set `{read_file, list_directory, grep_search, write_output, write_file, update_file, run_test}` → error `agents.%s: unknown tool %q` (fail closed; this replaces today's silent drop in `FilterByNames`). Define that canonical list in **one** place; recommended: `config.KnownToolNames` slice, and `internal/tools` re-exports/uses it so the two never drift.
   * `MCPServers` names must exist in `mcp_servers:` except the literal `"*"` → error.
   * keep the existing checks for `model.timeout` parseability and `adjudicator` ∈ {null,self,human}; delete the `agentType == "implementer" && single_shot` ban (single-shot is now legal for any agent — but document that a single-shot step produces reply text as its output and cannot edit files).
   * `single_shot_context_bytes` check unchanged.
7. `normalizeProjects`: delete the `agents` type-name switch and flavor/default consistency check. Add optional validation of `default_flow`: every id must exist in `cfg.Agents`, list non-empty, length ≤ 8.
8. Add `DefaultFlow []string \`yaml:"default_flow" json:"default_flow,omitempty"\`` to `ProjectConfig`. Delete `ProjectGuardrails.EffortGateMin` (and `ProjectGuardrails` if it becomes empty — prefer deleting the struct entirely and removing `guardrails:` from example YAML).
9. `KnownFields(true)` error friendliness: it is acceptable to let yaml.v3 produce `field agents not found in type config.ProjectConfig`. Optionally add a pre-pass that greps the decoded map for `projects.*.agents` and returns a human message: `projects.<name>.agents was removed; agent ids are now global under top-level agents: and flows are chosen per issue`. Nice-to-have, not required.

**Tests** (`internal/config/config_test.go`): rewrite flavor tests → agent-id tests. New cases: unknown key rejection for `projects.x.agents`, missing `system_prompt` error, `system_prompt_append` now rejected, arbitrary id accepted (`coder`, `review_1`), tools allowlist validation (unknown tool rejected), `default_flow` validated against `agents:`, two-agent and one-agent configs load successfully.

---

### 6.2 — `internal/agents`: one generic task agent

1. Delete `researcher.go`, `planner.go`, `implementer.go`.
2. New file `internal/agents/agent.go`:
   ```go
   // Task is a generic tool-loop agent. Instruction is the user's configured
   // system_prompt; there is no built-in prompt.
   type Task struct{ ID, Instruction string }
   func NewTask(id, instruction string) *Task
   func (t *Task) Build(model model.LLM, tools []tool.Tool) (agent.Agent, error) // llmagent.New Mode: ModeTask, OutputSchema: finishTaskSchema
   ```
3. Keep `finishTaskSchema` (done + rationale) as the single schema. Delete `plannerFinishTaskSchema`.
4. `chat.go` in this package (`NewChat`) is unchanged except it takes the agent id instead of agent type (no code change needed beyond naming).

---

### 6.3 — `internal/tools`: one core registry

1. Replace the three registries with one:
   ```go
   // NewCoreRegistry returns every core tool: read_file, list_directory,
   // grep_search, write_output, write_file, update_file, and run_test when
   // bt.Test is set. Callers narrow with FilterByNames.
   func NewCoreRegistry(bt *BoundTools) ([]tool.Tool, error)
   ```
   Keep `FilterByNames` exactly as-is (empty allow → all).
2. Update `BoundTools` doc comments: `WorkspacePath` is now "the issue workspace (any editing agent)", not "implementer workspace". Tool descriptions in `write_file.go` / `update_file.go` say "the issue workspace".
3. `run_test`: register it inside `NewCoreRegistry` only when `bt.Test != nil && bt.Test.Command != ""` (see Q4). Delete the commented-out block.
4. `chat.go`'s `NewHostReadOnlyRegistry` is unchanged (chat is read-only by design).

**Tests** (`internal/tools/declaration_test.go` and a new `core_registry_test.go`): assert the full list of names, assert `run_test` present/absent with/without `Test`, assert `FilterByNames` keeps order-independent subsets.

---

### 6.4 — `internal/storage/paths.go`: workspace moves to issue level

1. Add:
   ```go
   func WorkspacePath(projectID, issueID int64) string { return path.Join(IssueDir(projectID, issueID), "workspace") }
   func LegacyWorkspacePath(projectID, issueID int64) string { return path.Join(PhaseDir(projectID, issueID, "implementation"), "workspace") }
   ```
2. `PhaseDir` already takes any phase name; no change needed.
3. Everywhere `WorkspacePath` is consumed, for legacy issues fall back: read `WorkspacePath`, and if it does not exist and the issue is legacy, use `LegacyWorkspacePath`. Implement this as a single helper on the engine — `func (e *Engine) workspaceKey(issue *sqlite.Issue) string` — rather than sprinkling conditionals. Git code takes absolute paths from the caller, so it just receives whichever key applies.

**Tests** (`internal/storage/paths_test.go`): new expected values + legacy helper.

---

### 6.5 — SQLite: store the frozen flow

1. `internal/sqlite/db.go`: append migration 13:
   ```sql
   ALTER TABLE issues ADD COLUMN pipeline_json TEXT NOT NULL DEFAULT '[]';
   ```
   Do not drop `agent_flavors_json` (legacy read).
2. `internal/sqlite/issues.go`:
   * `Issue.PipelineJSON string` added to the struct, `issueColumns`, both `scanIssue`/`scanIssues`, and `normalizeJSON` (empty → `"[]"`).
   * Every `Create*` signature gains `currentPhase string` (pass `"step-1"` for new issues; pass `"research"` for legacy-shaped test helpers) so the `DEFAULT 'research'` column default is never relied on. Keep `agentFlavorsJSON` parameter (write `"{}"` for new issues).
   * Add `Pipeline` helpers in this package (used by both engine and server):
     ```go
     type Step struct { Index int; Key string; AgentID string }
     // ParsePipeline decodes pipeline_json into steps ("step-N").
     func ParsePipeline(raw string) ([]Step, error)
     // LegacyPipeline returns research/plan/implementation steps for pre-change issues.
     func LegacyPipeline() []Step
     // StepsForIssue returns ParsePipeline(pipeline_json) when non-empty, else LegacyPipeline().
     func StepsForIssue(i *Issue) ([]Step, error)
     ```
     Put `Step` in `internal/sqlite` (leaf package) so `orchestrator`, `server`, and `web` templates can all use it without import cycles.
3. `runs.agent_type` keeps its column name; it now stores the agent id. No migration.

**Tests** (`internal/sqlite/db_test.go`, new `issues_pipeline_test.go`): migration applies, round-trip `pipeline_json`, `StepsForIssue` picks legacy when empty.

---

### 6.6 — `internal/orchestrator`: the pipeline walks the frozen flow

`internal/orchestrator/engine.go` is the biggest change. Work through it method by method.

1. **Delete** `phaseAgentType`, `previousPhase`, `nextPhaseName`. Replace with step helpers (new file `internal/orchestrator/pipeline.go`):
   ```go
   func (e *Engine) stepsForIssue(issue *sqlite.Issue) ([]sqlite.Step, error) // wraps sqlite.StepsForIssue
   func stepIndex(steps []sqlite.Step, key string) int
   func nextStepKey(steps []sqlite.Step, key string) string // "" for last
   func prevStepKey(steps []sqlite.Step, key string) string // "" for first
   func (e *Engine) stepAgentConfig(issue *sqlite.Issue, step sqlite.Step) (config.AgentConfig, error)
   ```
   `stepAgentConfig` = `cfg.AgentMust(step.AgentID)` + the untrusted-input rule from Q7 (if `cfg.HasEditingTool()` and `trigger.IsExternal(issue.Source)` and neither `triggers.trust_external` nor `pc.TrustExternal` → `Adjudicator = "human"`). It **replaces** `agentConfigForIssue`; there is no cast parse and no flavor overlay anymore. Delete `resolveAndMarshalCast`, `parseIssueCast`, and the `AgentFlavors` field of `RunOptions`.
2. `RunOptions`:
   ```go
   Flow []string // ordered agent ids; required (or project default_flow)
   ```
3. `SubmitIssue` and `Run` (both create issues): resolve + validate the flow before insert, then freeze it:
   ```go
   func (e *Engine) resolveFlow(projectName string, requested []string) ([]string, error)
   ```
   Rules: reject an empty list unless `default_flow` exists; each id must be in `cfg.Agents` (error names the offending id and lists the configured ids); length ≤ 8; marshal to `pipeline_json` as `["a","b"]`. `current_phase` on insert = `"step-1"`.
4. `runPipeline`: replace the fixed `phases` slice with `steps, err := e.stepsForIssue(issue)` and iterate `for _, step := range steps`. `currentPhaseState` also iterates `steps`. Everything else in the loop (model residency acquire/release, status updates, events) stays the same, with `phaseName` → `step.Key`.
   * Keep-resident logic: delete the `if keep && phaseName == "plan"` effort-gate carve-out (Q2). Replace with: `keep := result.Status == "done" && nextStepKey(steps, step.Key) != ""`. If the **next** step's agent has a different model, do not keep residency (`keep = keep && nextCfg.Model.Model == curCfg.Model.Model`) — this prevents the next acquire from having to unload a foreign model and is a cheap improvement.
   * Final status update: `_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusDone, steps[len(steps)-1].Key)`.
5. `runPhase`: parameter `phase string` → `step sqlite.Step`; every internal use of `phase` becomes `step.Key`, and every use of `phaseAgentType(phase)` becomes `step.AgentID`.
   * `if phase == "implementation"` for workspace prep → `if stepNeedsWorkspace` where:
     ```go
     func (e *Engine) prepareIssueWorkspaceOnce(ctx, project, issue, run) error
     ```
     created once per issue (check `store.Exists(workspaceKey)` / existing `run.BranchName` on a prior run for git mode) and called before any step whose agent `HasEditingTool()` (Q5).
   * `if phase == "implementation"` for commit → commit after each `done` step when the workspace exists (`CommitAll` is a no-op with no changes). Branch name: `gorchgit.BranchName(issue.ID, 0)` — pass runID 0 and confirm `BranchName` produces `ai-implementer/{issue}-0`; if you prefer no collision with old per-run branches, add `IssueBranchName(issueID)` in `internal/git/git.go` returning `ai-implementer/{issueID}`.
   * allowlist: unchanged (`IssueDir`, `SourcePath`) plus `WorkspacePath` **for every step** (workspace is now issue-level; agents that are not meant to edit are controlled by `tools:`, not by the allowlist — call this out in a code comment so nobody thinks the allowlist is a capability boundary any more).
6. `runAgentLoop`: replace the `switch phase` tool-registry block with:
   ```go
   registry, err := tools.NewCoreRegistry(bt)
   ...
   registry = tools.FilterByNames(registry, cfg.Tools)
   if e.mcp != nil && len(cfg.MCPServers) > 0 { ... } // unchanged; optionally support "*" here (Q3)
   ```
   BoundTools setup: set `bt.WorkspacePath` / `bt.WorkspaceHostPath` / `bt.BasePath = wsKey` **always** (not only for implementation); `bt.Test` from project test config whenever the project has one. Keep `bt.OutputPath`.
   Delete the `finishEffort` extraction and `Effort` plumbing (Q2). Keep the "no `write_output` → fall back to final model text" path, and keep "if the workspace exists and no output was written, write a summary output.md" but change its condition from `phase == "implementation"` to "the step had editing tools".
7. `buildAgent(phase, cfg, ...)` → `buildStepAgent(step, cfg, model, tools)`: single call `agents.NewTask(step.AgentID, cfg.SystemPrompt).Build(model, tools)`. Remove the `if cfg.SystemPrompt != ""` guards (config validation guarantees it) — if it is empty, return an error instead.
8. `buildTask`: `PhaseTask.AgentType` → keep the JSON key `agent_type` (artifact compatibility) but set it to `step.AgentID`; add `StepKey string \`json:"step_key"\`` and `Flow []string \`json:"flow"\``. Tool list: `tools.NewCoreRegistry(bt)` + `FilterByNames(cfg.Tools)`. `inputPaths`: previous **step** output via `prevStepKey`.
9. `buildBaseInput`: `previousPhase(phase)` → `prevStepKey(steps, step.Key)`. Text: `Accepted %s output:` → `Accepted step output (%s agent):\n%s` using the previous step's agent id (helps the model understand who wrote it).
10. `pathGuide`: drop the `phase == "implementation"` conditional; always list the workspace, and word it as "mutable workspace (only if you have write_file/update_file)".
11. `currentPhaseState(projectID, issueID)` must take the steps: `func (e *Engine) currentStepState(projectID, issueID int64, steps []sqlite.Step) (string, string, error)`. Update all callers: `Resume`, `recoverIssue`, `issueView`, `Decide`, `CurrentPhaseState` (public wrapper — its signature must gain the issue or steps; callers in `server`/`cli` pass the issue).
12. `sideloadedModels()`: walk `cfg.Agents` only (project flavor walk deleted).
13. `buildPhaseModel`, budget wrapping, `failIssuePhaseError`, `failIssueInferenceError`: unchanged except phase name → step key.

`internal/orchestrator/service.go`:
1. `PhaseStep{Name, Agent, State, Status}` → `PhaseStep{Key, AgentID, Index, State, Status}` (drop `Name`/`Agent`; the templates use `Key` and `AgentID`).
2. `buildPhaseSteps` iterates `e.stepsForIssue(issue)` instead of the fixed names list. The "behind the current pointer" logic (`curIdx`) is unchanged in shape.
3. `maybeHoldForScope`: write the hold to `steps[0].Key` instead of `"research"`, and in `applyHumanDecisionWithBy` the scope-hold clearing branch keys on `IsScopeHoldError(result.Error)` plus `stepIndex(steps, phase) == 0` (today it hard-codes `phase == "research"`).
4. `maybeHoldForEffort` + `IsEffortHoldError` + `effort.go` + `EffectiveEffort`/`EffortRequiresGate`: **delete** (Q2). Then delete the two `IsEffortHoldError` branches in `applyHumanDecisionWithBy` and the `IsPrePhaseHoldError` helper's effort half. `PhaseResult.Effort` field removed; `runPipeline`'s `plan`-specific hold block removed.
5. `Decide`, `StopIssue`, `DeleteIssue`, `ProcessIssue`, `RecoverAll`: unchanged apart from phase-name plumbing.
6. `issueView`: `FailureReason`/`HoldReason` lookups already key on `issue.CurrentPhase`; they keep working with step keys.

`internal/orchestrator/single_shot.go`:
* `resolveSingleShotPrompt` deleted entirely. `runSingleShot` uses `cfg.SystemPrompt` directly (error if empty). Delete the "single-shot not supported for phase" branch — every step may be single-shot. Keep `single_shot_context_bytes` / `context_files` digest behavior (`buildRepoDigest` unchanged).
* In `runPipeline` the digest pre-stuff condition (`phaseCfg.SingleShot`) is unchanged.

`internal/orchestrator/model_lock.go`, `breaker.go`, `budget.go`, `chat.go` (orchestrator side):
* `chat.go`: replace `eng.cfg.Agent(thread.AgentType)` + `pc.FlavorOverlay` with `eng.cfg.AgentMust(thread.AgentType)`; delete the flavor branch. `ChatRepo` thread keys stay the same shape (`agent_type` now holds an agent id, `flavor` is always `''`).
* `budget.go`: phase-keyed budget lookup unchanged (it keys on the phase string); verify nothing assumes the three names — grep for `plan`/`research`/`implementation` in `budget.go` before declaring it done.

**Tests to add/rework in `internal/orchestrator`:** `pipeline_test.go` (new): a 1-step flow (`["coder"]` only) runs exactly one phase and lands `done`; a 4-step flow (`["a","b","c","d"]`) writes `step-1..step-4` artifact dirs, feeds each step the previous step's accepted output, and sets `current_phase` to the next step key; an unconfigured agent id fails at submit with a message naming the id; a legacy issue (empty `pipeline_json`, `agent_flavors_json` set) still resolves to `research/plan/implementation` dirs and reads its existing `result.json` files. Also rework `model_pipeline_test.go`, `phase_status_test.go`, `single_shot_test.go`, `scope_test.go`, `gate_feedback_test.go`, `stop_issue_test.go`, `delete_issue_test.go`, `submit_deps_test.go`, `run_test.go`, `chat_test.go`, `projects_registry_test.go`, `diagnose_test.go`, `budget_test.go`, `model_lock_test.go`, `modelctl_test.go` for the new names (many of them currently build configs with `agents: researcher/planner/implementer` and expect phase dirs named `research`/`plan`/`implementation`; add `system_prompt` to every fixture agent and change expected dir names to `step-N`). Delete `effort_test.go` (Q2).

---

### 6.7 — `internal/git`: one branch per issue

`internal/git/git.go`: add `func IssueBranchName(issueID int64) string` (`ai-implementer/{issueID}`) and use it in `prepareIssueWorkspaceOnce`. `CreateImplementerWorktree(ctx, projectID, abs, branch, cfg)` needs no change (it takes the absolute path from the caller — pass the new `workspace/` key's absolute path). Keep `BranchName(issueID, runID)` for legacy reads.

**Tests:** `internal/git/git_test.go` add the new name helper.

---

### 6.8 — HTTP API (`internal/server/api.go`, `server.go`)

1. `submitIssueRequest`: remove `AgentFlavors`, add
   ```go
   Flow      []string `json:"flow"`
   AgentFlow []string `json:"agent_flow"` // accepted alias
   ```
   If a request still sends `agent_flavors`, respond 422: `agent_flavors was removed; send flow: ["id1","id2",...]`.
2. `issueToJSON`: replace `agent_flavors` with `"flow": ParsePipeline(...)` (array of `{"index":1,"agent":"researcher","key":"step-1"}` — a flat `["researcher","coder"]` is also acceptable; pick the object form so the UI can show step numbers). Keep `current_phase` as-is (it will show `step-2`).
3. `handleListProjects`: replace the per-type flavor catalog with `"agents": {"flow_default": [...], "available": ["a","b","c"]}` sourced from `e.Cfg().AgentIDs()` and `pc.DefaultFlow`. `RegisteredProject.Agents` type changes to a small struct (`RegisteredProject{Project, AvailableAgents []string, DefaultFlow []string}`).
4. Add `GET /api/agents` (RoleViewer) returning `{"agents":[{"id":"researcher"}, ...]}` — ids only. **Do not return system prompts in this change** (exposing prompts is the later feature; note it in a code comment).
5. `isSubmitClientError`: add the flow error strings (`"flow"`, `"not configured"`, `"agent flow"`) so bad picks return 422, not 500.
6. `server.go` routes: rename `GET /partials/submit/flavors` → `GET /partials/submit/flow`. Keep `/partials/submit` and `POST /partials/submit`.

**Tests:** rework `internal/server/server_test.go` `TestAPI_Submit_AgentFlavors` → `TestAPI_Submit_Flow` (submit `{"flow":["a","b"]}`, assert the issue JSON `flow` array and `current_phase == "step-1"`), add `TestAPI_Submit_FlowUnknownAgent` (422), `TestAPI_Submit_LegacyAgentFlavorsRejected` (422), `TestAPI_Agents`.

---

### 6.9 — Submit drawer: the flow picker

Files: `internal/server/dashboard.go`, `internal/web/templates/partials/drawer_submit.html`, `partials/submit_flavors.html` (rename to `partials/submit_flow.html`), `internal/web/static/js` if a helper is needed.

`dashboard.go`:
1. Delete `flavorSelectsForProject`, `flavorSelect`, `flavorSelectOption`.
2. Add:
   ```go
   type flowStepOption struct{ ID string; Selected bool }
   type flowStep struct{ Index int; Options []flowStepOption }
   func (s *Server) flowStepsForProject(project string, current []string) []flowStep
   func (s *Server) submitFormData(r *http.Request, project string, flow []string, title string, dryRun bool) map[string]any
   ```
   `submitFormData` gets `"Flow": current`, `"FlowSteps": s.flowStepsForProject(...)`, `"AgentIDs": s.eng.Cfg().AgentIDs()`, `"DefaultFlow": pc.DefaultFlow`.
3. `handlePartialSubmitFlow` (replaces `handlePartialSubmitFlavors`): read `project` and the current flow from a `flow` query param (comma-separated ids, order significant), re-render `partials/submit_flow.html`.
4. `handlePartialSubmitPost`: build the flow from the form. Use **document order**: N `<select name="flow_agent">` elements serialize in DOM order, so
   ```go
   // r.PostForm[key] is a []string in document order — exactly what we need.
   raw := r.PostForm["flow_agent"] // after parseRequestForm(r)
   flow := make([]string, 0, len(raw))
   for _, v := range raw { if v = strings.TrimSpace(v); v != "" { flow = append(flow, v) } }
   ```
   Pass `flow` to `SubmitIssue`. Audit details: `"flow": flow`.

`partials/submit_flow.html` (sketch — keep it HTMX-driven, no JS build step):
```html
{{define "partials/submit_flow.html"}}
<div class="flow-builder" id="flow-builder">
  <p class="card-meta">Pick the agents and the order they hand off to. Configure them under
  <code>agents:</code> in your config YAML.</p>
  {{range .FlowSteps}}
  <div class="form-row flow-row">
    <label>Step {{.Index}}</label>
    <select name="flow_agent" hx-get="/partials/submit/flow" hx-trigger="change"
            hx-target="#flow-builder" hx-swap="outerHTML" hx-include="#flow-builder">
      <option value="" disabled>Select an agent…</option>
      {{$sel := .Selected}}
      {{range $.AgentIDs}}
      <option value="{{.}}" {{if eq . $sel}}selected{{end}}>{{.}}</option>
      {{end}}
    </select>
    <button type="button" class="btn" name="flow_remove" value="{{.Index}}"
      hx-get="/partials/submit/flow" hx-target="#flow-builder" hx-swap="outerHTML"
      hx-include="#flow-builder">Remove</button>
  </div>
  {{end}}
  <input type="hidden" id="flow-state" name="flow_state" value="{{.FlowCSV}}">
  <button type="button" class="btn" name="flow_add" value="1"
    hx-get="/partials/submit/flow" hx-target="#flow-builder" hx-swap="outerHTML"
    hx-include="#flow-builder">+ Add agent</button>
</div>
{{end}}
```
Implementation rule for the handler: build the *next* flow from `flow_state` (the authoritative ordered list) plus the changed select's value (`r.URL.Query().Get("flow_agent")` for the change event) or the `flow_remove` index, or append a blank slot for `flow_add`. Re-render from that list. Keep the max-steps cap (8) enforced server-side and hide "+ Add agent" at the cap. Show an inline warning when the selected project has a `default_flow` (it is prefilled, editable).

`drawer_submit.html`: replace the `<div id="agent-flavors">{{template "partials/submit_flavors.html" .}}</div>` block with `<div id="agent-flow">{{template "partials/submit_flow.html" .}}</div>`. The project `<select>`'s `hx-get` target becomes `/partials/submit/flow` targeting `#agent-flow`.

**Tests:** rework `internal/server/submit_drawer_test.go` + `internal/server/drawer_test.go` + `internal/web/templates_test.go` + `internal/web/responsive_test.go`: assert the flow section renders one select per step, `+ Add agent` exists, and posting `flow_agent=a&flow_agent=b` freezes `["a","b"]`.

---

### 6.10 — Artifact drawer: tabs come from the issue's flow

Files: `internal/server/drawer.go`, `internal/server/dashboard.go` (`handlePartialDrawer`), `internal/web/templates/partials/drawer_artifact.html`.

1. Delete `knownPhases`, `normalizePhase`, `phaseLabel`, `phaseAgent`, `phaseResearch/phasePlan/phaseImplementation` constants.
2. `drawerContent(r, view, tab, stepKey string)`: resolve `stepKey` against `view.Phases` (`sqlite.StepsForIssue(view.Issue)`); when empty/unknown use `view.Issue.CurrentPhase`, falling back to the **last** step for the workspace tab and the **first** step otherwise.
3. The workspace tab condition `phase == phaseImplementation` → "**this issue has an editing-capable step and this is the last step**", or simpler and recommended: keep one dedicated **Workspace** tab, always present when `store.Exists(workspaceKey)` (or legacy workspace exists), independent of step selection. That matches reality now: the workspace is issue-level. Update `handlePartialDrawer` to build `PhaseTabs` from `view.Phases` with `Label = fmt.Sprintf("%d · %s", p.Index, p.AgentID)` and `Current = p.Key == stepKey`.
4. `implementationDone(...)` → `workspaceHasProgress(...)`/`issueDone(...)`: gate `workspace.zip` on the issue being `done` **or** the last step's result being `done`.
5. `partials/drawer_artifact.html`: iterate `PhaseTabs` (already dynamic) — remove any hard-coded labels; `partials/issue_card.html`: delete the legacy static strip block (the `{{if eq $issue.CurrentPhase "research"}}…` fallback lines ~46–50) so only the `{{range .Issue.Phases}}` strip remains, showing `{{$p.Index}} {{$p.AgentID}}`.

**Tests:** `internal/server/drawer_test.go` — a 2-step issue renders 2 tabs named `1 · scout`, `2 · coder`; a legacy issue still renders `research`/`plan`/`implementation` tabs and reads its legacy artifacts.

---

### 6.11 — Chat drawer identity list

Files: `internal/server/chat.go`, `internal/web/templates/partials/drawer_chat.html` (no template change needed beyond labels).

1. Delete `validChatAgentType` (core-type check), `validateChatFlavor`, `defaultChatIdentity`'s flavor logic, and the flavor loop in `chatIdentityOptions`.
2. Identity options = `s.eng.Cfg().AgentIDs()`, sorted; default selection = first id. Validate the posted identity against `cfg.Agents` (`400` when unknown, with a message listing configured ids).
3. `chat_threads.flavor` is always written `''`; `ThreadKey`/`parseChatIdentity` stop splitting on `:` (reject `:` in identity values so old `researcher:cheap` values fail cleanly with "identity no longer available").
4. `internal/orchestrator/chat.go`: `cfg := eng.cfg.AgentMust(thread.AgentType)`; delete the flavor overlay.

**Tests:** `internal/server/chat_test.go`, `internal/orchestrator/chat_test.go`, `internal/sqlite/chat_test.go`: identities come from `agents:`; unknown identity rejected; threads survive an agent rename by starting a new thread (old thread row remains for history).

---

### 6.12 — CLI + triggers + webhooks

1. `internal/cli/run.go`: add `-flow` (comma-separated ordered ids, same `multiFlag` pattern used by `-attach` or a single comma-split). `-flow=researcher,coder`. Pass to `RunOptions.Flow`. Update the usage text.
2. `internal/cli/resume.go`: unchanged (resume continues the frozen flow).
3. `internal/cli/validate.go`: print configured agent ids and each project's `default_flow` — cheap and useful for debugging config errors.
4. `internal/server/webhook.go` + `internal/trigger/adapter.go` submit paths: they call `SubmitIssue` with no `Flow`, which resolves to `projects.<name>.default_flow` or errors. Confirm by test that a webhook submit with no `default_flow` returns a clear error.
5. `internal/notify/*`: unchanged (it forwards phase strings it is given).

---

### 6.13 — Docs and example config

1. `configs/config.example.yaml`: rewrite the `agents:` block per §5 (ids arbitrary, `system_prompt` required, `tools` optional narrowing, `mcp_servers` optional with `"*"` note), delete the flavor examples, delete `guardrails.effort_gate_min`, add `default_flow`.
2. `README.md`: update the pipeline description ("you choose the agent flow at submit"), the config table (`agents.*`, `projects.*` rows), the single-shot paragraph (any agent, uses your own prompt), and the submit drawer description.
3. `spec.md`: add a dated revision-log entry at the top; rewrite §5.2 (Core Tool Matrix — now "default full core tool list per agent; narrowing is the user's job via `tools:`"; keep the *path allowlist* and `run_test` container isolation as the remaining boundaries and state plainly that per-role capability isolation is gone by design), §7.1 (directory structure with `step-N` + issue-level `workspace/`), §8.1 (delete "baked-in identities"), §8.2 (rewrite as "Agent configuration (YAML) — user-defined agent ids, required system prompts, default-full toolset"), §8.3 (context flow = previous step), §9.1 (boundary model unchanged but phase = step), §11.5 (submit drawer = flow builder; drawer tabs = per-flow), §13.3 (mark effort gate removed), §6.0 (add `default_flow`).
4. Add a short operator-facing section to README: "Choosing a flow" with 3 examples (one-shot coder, research → coder, research → plan → coder → reviewer) and a warning that the default toolset includes file-editing tools, so a research-only agent should narrow its `tools:` list.

---

## 7. Sequencing, and how to prove each milestone

Run `go build ./... && go vet ./... && go test ./...` at the end of every milestone.

| Milestone | Contents | Done when |
|---|---|---|
| M1 | 6.1 config | `go test ./internal/config` green; example YAML loads; an old YAML with `projects.x.agents` fails with a readable error |
| M2 | 6.2 agents pkg, 6.3 tools pkg | tool/agent package tests green; nothing else compiles yet is acceptable only inside this milestone — finish with a green build by keeping temporary `NewResearcherRegistry` shims if needed, then delete them in M4 |
| M3 | 6.4 storage paths, 6.5 sqlite | storage + sqlite tests green; migration 13 applies to an existing DB file |
| M4 | 6.6 orchestrator (pipeline, single_shot, service, chat config), 6.7 git | `go test ./internal/orchestrator` green incl. the new 1-step / 4-step / legacy tests; dryrun end-to-end: `gorchestrator run -project acme -issue x -flow coder` with `provider: dryrun` produces `step-1/` artifacts and issue `done` |
| M5 | 6.8 API, 6.9 submit UI, 6.10 drawer tabs, 6.11 chat, 6.12 CLI/triggers | `go test ./internal/server ./internal/web ./internal/cli` green; manual check in the browser: pick 2 agents, submit, drawer shows `1 · <id>` / `2 · <id>`, artifacts land under `step-1`/`step-2` |
| M6 | 6.13 docs, delete all leftover flavor/effort/single-shot-prompt code, `grep` sweep | `grep -rn "CoreAgentTypes\|FlavorOverlay\|FlavorCatalog\|ResolveCast\|agent_flavor\|SystemPromptAppend\|phaseAgentType\|knownPhases\|effort_gate\|DefaultSingleShotPrompt" --include=*.go --include=*.html --include=*.yaml --include=*.md .` returns **only** legacy-compat mentions |

---

## 8. Compatibility rules (do not break these)

* Legacy issues: never rewrite their on-disk artifacts, never rewrite `agent_flavors_json`. They read through `LegacyPipeline()` and keep their phase dirs. A legacy issue with an in-flight phase must still be resumable.
* `runs`, `decisions`, `notifications`, `audit_log`, `chat_*` schemas are additive-only in this change (one new column).
* `task.json` / `result.json` / `events.jsonl` field names are additive-only (add `step_key`, `flow`; keep `agent_type`, `status`, `attempt`, `latest_output`, `done_rationale`, `tokens_used`, …) because the drawer and `sumUsageFromEvents` parse them.
* `POST /api/issues` keeps accepting `title`/`project`/`description`/`body`/`attachments`/`depends_on`/`dry_run`. Only `agent_flavors` changes meaning (now a 422).
* `Config.MergeAgent`, adjudicator outcomes, breaker, budgets, model residency, storage allowlist/path enforcement: unchanged.

---

## 9. Known risks to call out in the PR/commit message

1. **Security posture change.** spec §5.2's per-role tool matrix was a capability boundary ("a hijacked researcher can only write its output file"). With a default-full toolset any step can edit the workspace. Remaining boundaries: storage path allowlist (issue dir + source + workspace only), no shell (`run_test` is containerized and project-configured), MCP deny-by-default. Mitigation available to users: per-agent `tools:` narrowing. This must be stated in README/spec, not buried.
2. **Config migration is breaking** for every existing config file (system prompts are now required, project flavors removed). Provide the rewritten `configs/config.example.yaml` as the migration template and make the load error messages name the exact offending agent.
3. **Workspace semantics**: agents in the same flow now share one mutable tree. Two consecutive editing agents can overwrite each other. Accepted by design (user owns the flow); mention it in the README "Choosing a flow" section.
4. **Single-shot + editing tools**: a single-shot step gets no tools at all, so it can never edit the workspace even though its `tools:` list may name editing tools. Document it.

---

## 10. Out of scope for this change (do not implement)

* Showing agent system prompts / tool lists in the web UI (next feature; the flow picker shows ids only).
* Flow templates / saved presets beyond `projects.<name>.default_flow`.
* Parallel agents, branching or conditional flows (the flow stays a linear list).
* Transcripts of every previous step being fed forward (only the immediately previous step's accepted output is inlined).
* Any change to budgets, escalation rules, notifications, adapters, storage backends, auth.
