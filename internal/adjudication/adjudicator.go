// Package adjudication provides the boundary evaluation port and built-in
// adjudicators used by the phase machine.
package adjudication

import (
	"context"
	"strings"
)

// Outcome is the result of a boundary evaluation.
type Outcome int

const (
	// Pass accepts the attempt and allows the pipeline to proceed.
	Pass Outcome = iota
	// Fail rejects the attempt and stops the phase as failed.
	Fail
	// Retry rejects the attempt but allows another attempt with feedback.
	Retry
	// WaitingHuman pauses the pipeline for a human decision.
	WaitingHuman
)

// String returns the canonical lowercase name of an outcome.
func (o Outcome) String() string {
	switch o {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	case Retry:
		return "retry"
	case WaitingHuman:
		return "waiting_human"
	default:
		return "unknown"
	}
}

// ParseOutcome converts a lowercase outcome string into an Outcome value.
func ParseOutcome(s string) Outcome {
	switch strings.ToLower(s) {
	case "pass":
		return Pass
	case "fail":
		return Fail
	case "retry":
		return Retry
	case "waiting_human":
		return WaitingHuman
	default:
		return Fail
	}
}

// Decision is the result of evaluating a single attempt at a phase boundary.
type Decision struct {
	Outcome  Outcome
	Feedback string
}

// Attempt captures the artifacts produced by a single phase attempt.
type Attempt struct {
	Output        []byte
	Done          bool
	DoneRationale string
}

// Adjudicator evaluates whether a phase attempt should be accepted.
type Adjudicator interface {
	Evaluate(ctx context.Context, phase string, attempt Attempt) (Decision, error)
}

// NullAdjudicator always passes after the configured loops complete.
type NullAdjudicator struct{}

// Evaluate implements Adjudicator.
func (a *NullAdjudicator) Evaluate(ctx context.Context, phase string, attempt Attempt) (Decision, error) {
	return Decision{Outcome: Pass}, nil
}

// SelfAdjudicator reads the agent's finish_task rationale.
// The agent is responsible for evaluating itself against the rubric baked into
// its system prompt. A done=true finish yields Pass; done=false yields Retry
// with the rationale as feedback.
type SelfAdjudicator struct{}

// Evaluate implements Adjudicator.
func (a *SelfAdjudicator) Evaluate(ctx context.Context, phase string, attempt Attempt) (Decision, error) {
	if attempt.Done {
		return Decision{Outcome: Pass, Feedback: attempt.DoneRationale}, nil
	}
	return Decision{Outcome: Retry, Feedback: attempt.DoneRationale}, nil
}

// HumanAdjudicator always pauses for a human decision.
type HumanAdjudicator struct{}

// Evaluate implements Adjudicator.
func (a *HumanAdjudicator) Evaluate(ctx context.Context, phase string, attempt Attempt) (Decision, error) {
	return Decision{Outcome: WaitingHuman}, nil
}

// New returns the named built-in adjudicator.
func New(name string) Adjudicator {
	switch name {
	case "self":
		return &SelfAdjudicator{}
	case "human":
		return &HumanAdjudicator{}
	default:
		return &NullAdjudicator{}
	}
}
