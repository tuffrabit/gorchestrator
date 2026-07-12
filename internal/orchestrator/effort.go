package orchestrator

import (
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/config"
)

// Effort levels for planner finish_task and project guardrails.
const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
)

// effortRank orders low < medium < high for threshold comparison.
func effortRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case EffortLow:
		return 1
	case EffortMedium:
		return 2
	case EffortHigh:
		return 3
	default:
		return 0
	}
}

// NormalizeEffort returns low|medium|high or empty if invalid/missing.
func NormalizeEffort(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case EffortLow, EffortMedium, EffortHigh:
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return ""
	}
}

// EffectiveEffort returns the planner effort used for gating. Missing/invalid → high.
func EffectiveEffort(s string) string {
	if e := NormalizeEffort(s); e != "" {
		return e
	}
	return EffortHigh
}

// EffortGateMin returns the project threshold (default high).
func EffortGateMin(pc config.ProjectConfig) string {
	min := NormalizeEffort(pc.Guardrails.EffortGateMin)
	if min == "" {
		return EffortHigh
	}
	return min
}

// EffortRequiresGate reports whether effort meets or exceeds the project minimum.
func EffortRequiresGate(effort, min string) bool {
	e := effortRank(EffectiveEffort(effort))
	m := effortRank(EffectiveEffort(min))
	if m == 0 {
		m = effortRank(EffortHigh)
	}
	return e >= m
}

// IsEffortHoldError reports whether a phase result error is an effort gate reason.
func IsEffortHoldError(errMsg string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(errMsg)), "effort:")
}

// IsPrePhaseHoldError is scope or effort hold (result must not be marked done on pass).
func IsPrePhaseHoldError(errMsg string) bool {
	return IsScopeHoldError(errMsg) || IsEffortHoldError(errMsg)
}
