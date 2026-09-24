package sqlite

import (
	"encoding/json"
	"fmt"
)

// Step is one position in an issue's frozen agent flow. Legacy issues map to
// the fixed research/plan/implementation steps.
type Step struct {
	Index   int
	Key     string // artifact directory name: "step-N" (legacy: research|plan|implementation)
	AgentID string
}

// ParsePipeline decodes a pipeline_json value (JSON array of agent ids) into
// steps. Empty or "[]" yields no steps.
func ParsePipeline(raw string) ([]Step, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("parse pipeline: %w", err)
	}
	steps := make([]Step, 0, len(ids))
	for i, id := range ids {
		steps = append(steps, Step{Index: i + 1, Key: fmt.Sprintf("step-%d", i+1), AgentID: id})
	}
	return steps, nil
}

// LegacyPipeline returns the fixed research/plan/implementation steps for
// issues created before the flow change (empty pipeline_json).
func LegacyPipeline() []Step {
	return []Step{
		{Index: 1, Key: "research", AgentID: "researcher"},
		{Index: 2, Key: "plan", AgentID: "planner"},
		{Index: 3, Key: "implementation", AgentID: "implementer"},
	}
}

// IsLegacyIssue reports whether the issue predates the flow change: it has
// no frozen flow (empty pipeline_json). New issues always carry a non-empty
// pipeline_json, so an empty one means the row was created before the flow
// feature existed.
func IsLegacyIssue(i *Issue) bool {
	return i.PipelineJSON == "" || i.PipelineJSON == "[]"
}

// StepsForIssue returns the frozen flow steps when pipeline_json is
// non-empty, otherwise the legacy fixed pipeline.
func StepsForIssue(i *Issue) ([]Step, error) {
	raw := i.PipelineJSON
	if raw == "" || raw == "[]" {
		return LegacyPipeline(), nil
	}
	return ParsePipeline(raw)
}
