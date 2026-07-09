package adjudication

import (
	"context"
	"testing"
)

func TestNullAdjudicator_Passes(t *testing.T) {
	a := &NullAdjudicator{}
	d, err := a.Evaluate(context.Background(), "research", Attempt{Done: false, DoneRationale: "ignored"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Outcome != Pass {
		t.Fatalf("expected pass, got %v", d.Outcome)
	}
}

func TestSelfAdjudicator_DonePasses(t *testing.T) {
	a := &SelfAdjudicator{}
	d, err := a.Evaluate(context.Background(), "plan", Attempt{Done: true, DoneRationale: "looks good"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Outcome != Pass {
		t.Fatalf("expected pass, got %v", d.Outcome)
	}
	if d.Feedback != "looks good" {
		t.Fatalf("feedback = %q, want looks good", d.Feedback)
	}
}

func TestSelfAdjudicator_NotDoneRetries(t *testing.T) {
	a := &SelfAdjudicator{}
	d, err := a.Evaluate(context.Background(), "implementation", Attempt{Done: false, DoneRationale: "missing tests"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Outcome != Retry {
		t.Fatalf("expected retry, got %v", d.Outcome)
	}
	if d.Feedback != "missing tests" {
		t.Fatalf("feedback = %q, want missing tests", d.Feedback)
	}
}

func TestHumanAdjudicator_Waits(t *testing.T) {
	a := &HumanAdjudicator{}
	d, err := a.Evaluate(context.Background(), "research", Attempt{Done: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Outcome != WaitingHuman {
		t.Fatalf("expected waiting_human, got %v", d.Outcome)
	}
}

func TestParseOutcome(t *testing.T) {
	cases := map[string]Outcome{
		"pass":          Pass,
		"fail":          Fail,
		"retry":         Retry,
		"waiting_human": WaitingHuman,
		"unknown":       Fail,
	}
	for s, want := range cases {
		if got := ParseOutcome(s); got != want {
			t.Fatalf("ParseOutcome(%q) = %v, want %v", s, got, want)
		}
	}
}
