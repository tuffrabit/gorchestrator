package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/trigger"
)

// maxFlowSteps caps a single issue's agent flow so the UI, artifact layout,
// and event streams stay sane.
const maxFlowSteps = 8

// resolveFlow validates the requested agent flow against the config and
// falls back to the project's default_flow when nothing was given. It never
// silently falls back to a built-in pipeline: an empty request with no
// default_flow is a submit error. The returned list is the frozen flow
// (ordered agent ids) to persist in pipeline_json.
func (e *Engine) resolveFlow(projectName string, requested []string) ([]string, error) {
	flow := requested
	if len(flow) == 0 {
		if pc, ok := e.cfg.Projects[projectName]; ok {
			flow = pc.DefaultFlow
		}
	}
	if len(flow) == 0 {
		return nil, fmt.Errorf("no agent flow: pick agents in the UI or set projects.%s.default_flow", projectName)
	}
	if len(flow) > maxFlowSteps {
		return nil, fmt.Errorf("agent flow too long: max %d agents, got %d", maxFlowSteps, len(flow))
	}
	seen := make(map[string]bool, len(flow))
	for _, id := range flow {
		if _, ok := e.cfg.Agents[id]; !ok {
			return nil, fmt.Errorf("agent %q is not configured under agents: in config (configured: %s)", id, strings.Join(e.cfg.AgentIDs(), ", "))
		}
		if seen[id] {
			return nil, fmt.Errorf("agent %q appears more than once in the flow", id)
		}
		seen[id] = true
	}
	return flow, nil
}

// marshalFlowJSON encodes the frozen flow for issues.pipeline_json.
func marshalFlowJSON(flow []string) (string, error) {
	data, err := json.Marshal(flow)
	if err != nil {
		return "", fmt.Errorf("marshal flow: %w", err)
	}
	return string(data), nil
}

// stepsForIssue returns the issue's frozen flow as steps. Legacy issues
// (empty pipeline_json) resolve to the fixed research/plan/implementation
// pipeline and never touch the new step-N layout.
func (e *Engine) stepsForIssue(issue *sqlite.Issue) ([]sqlite.Step, error) {
	return sqlite.StepsForIssue(issue)
}

// StepsForIssue is the exported form of stepsForIssue for out-of-package
// callers (daemon, server).
func (e *Engine) StepsForIssue(issue *sqlite.Issue) ([]sqlite.Step, error) {
	return e.stepsForIssue(issue)
}

// WorkspaceKey is the exported form of workspaceKey for out-of-package
// callers (server): new layout first, legacy fallback for old issues.
func (e *Engine) WorkspaceKey(ctx context.Context, issue *sqlite.Issue) (string, error) {
	return e.workspaceKey(ctx, issue)
}

// stepIndex finds the 0-based position of a step key in the flow.
func stepIndex(steps []sqlite.Step, key string) int {
	for i, s := range steps {
		if s.Key == key {
			return i
		}
	}
	return -1
}

// nextStepKey returns the key of the following step, or "" for the last one.
func nextStepKey(steps []sqlite.Step, key string) string {
	i := stepIndex(steps, key)
	if i < 0 || i == len(steps)-1 {
		return ""
	}
	return steps[i+1].Key
}

// prevStepKey returns the key of the preceding step, or "" for the first one.
func prevStepKey(steps []sqlite.Step, key string) string {
	i := stepIndex(steps, key)
	if i <= 0 {
		return ""
	}
	return steps[i-1].Key
}

// prevStep returns the step immediately preceding key, or nil for the first.
func prevStep(steps []sqlite.Step, key string) *sqlite.Step {
	i := stepIndex(steps, key)
	if i <= 0 {
		return nil
	}
	return &steps[i-1]
}

// stepAgentConfig resolves the effective config for one step of one issue:
// the user-configured global agent plus the untrusted-input rule (external
// triggers force a human adjudicator on any step whose effective tool list
// includes an editing tool, unless trust is granted). It replaces the old
// flavor-cast lookup; there is no per-project overlay anymore.
func (e *Engine) stepAgentConfig(issue *sqlite.Issue, step sqlite.Step) (config.AgentConfig, error) {
	cfg, err := e.cfg.AgentMust(step.AgentID)
	if err != nil {
		return cfg, fmt.Errorf("resolve agent for %s: %w", step.Key, err)
	}
	if cfg.HasEditingTool() && trigger.IsExternal(issue.Source) {
		if project, perr := e.projects.Get(issue.ProjectID); perr == nil && project != nil {
			if pc, perr := e.typedProjectConfig(project); perr == nil {
				if trust := e.cfg.Triggers.TrustExternal || pc.TrustExternal; !trust {
					cfg.Adjudicator = "human"
				}
			}
		}
	}
	return cfg, nil
}

// flowAgentIDs returns the ordered agent IDs of the flow.
func flowAgentIDs(steps []sqlite.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.AgentID)
	}
	return out
}

// workspaceKey resolves the issue-level mutable workspace storage key. New
// issues use projects/{pid}/issues/{iid}/workspace; legacy issues keep their
// workspace under implementation/workspace and are never rewritten on disk,
// so readers fall back to the legacy key when the new one does not exist.
func (e *Engine) workspaceKey(ctx context.Context, issue *sqlite.Issue) (string, error) {
	key := storage.WorkspacePath(issue.ProjectID, issue.ID)
	exists, err := e.store.Exists(ctx, key)
	if err != nil {
		return "", fmt.Errorf("check workspace %s: %w", key, err)
	}
	if exists {
		return key, nil
	}
	if sqlite.IsLegacyIssue(issue) {
		return storage.LegacyWorkspacePath(issue.ProjectID, issue.ID), nil
	}
	return key, nil
}
