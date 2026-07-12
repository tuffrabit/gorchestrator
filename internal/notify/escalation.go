package notify

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/tuffrabit/gorchestrator/internal/config"
)

// Escalation event kinds (match config escalation.rules[].when).
const (
	WhenConsecutiveFailures = "consecutive_failures"
	WhenBudgetExceeded      = "budget_exceeded"
	WhenSandboxRefused      = "sandbox_refused"
	WhenPhaseFailed         = "phase_failed"
)

// KindEscalation is the notification kind for escalation alerts.
const KindEscalation = "escalation"

// Event is a signal from the orchestrator for rule evaluation.
type Event struct {
	When    string // consecutive_failures | budget_exceeded | sandbox_refused | phase_failed
	Project string // project name (for consecutive / filter)
	IssueID int64
	Phase   string
	Message string
	// Success clears consecutive_failures for Project when true.
	Success bool
}

// Escalator evaluates YAML rules with in-process dedupe (no alert storms).
type Escalator struct {
	mu          sync.Mutex
	rules       []config.EscalationRule
	adminEmails []string
	dispatcher  *Dispatcher

	// consecutive failure counts per project name.
	failCounts map[string]int
	// fired keys: ruleName|subject — silence until reset.
	fired map[string]struct{}
	// recent fired events for admin page (ring buffer).
	recent []FiredEvent
}

// FiredEvent is a past escalation for the read-only admin page.
type FiredEvent struct {
	Rule    string
	When    string
	Project string
	IssueID int64
	Phase   string
	Message string
}

// NewEscalator builds an escalator from config. Nil if no rules.
func NewEscalator(cfg *config.Config, d *Dispatcher) *Escalator {
	if cfg == nil || len(cfg.Escalation.Rules) == 0 {
		return nil
	}
	rules := append([]config.EscalationRule(nil), cfg.Escalation.Rules...)
	return &Escalator{
		rules:       rules,
		adminEmails: append([]string(nil), cfg.Auth.BootstrapAdminEmails...),
		dispatcher:  d,
		failCounts:  map[string]int{},
		fired:       map[string]struct{}{},
	}
}

// Rules returns a copy of configured rules (admin page).
func (e *Escalator) Rules() []config.EscalationRule {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]config.EscalationRule(nil), e.rules...)
}

// Recent returns recent fired escalations (newest last).
func (e *Escalator) Recent() []FiredEvent {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]FiredEvent(nil), e.recent...)
}

// Observe evaluates rules against an event. Safe for concurrent use.
func (e *Escalator) Observe(ctx context.Context, ev Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if ev.Success {
		if ev.Project != "" {
			delete(e.failCounts, ev.Project)
			// Allow consecutive_failures rules to fire again after recovery.
			for _, r := range e.rules {
				if r.When == WhenConsecutiveFailures {
					delete(e.fired, e.dedupeKey(r.Name, "project:"+ev.Project))
				}
			}
		}
		return
	}

	// Update consecutive failure counter for failure-class events.
	if isFailureClass(ev.When) && ev.Project != "" {
		e.failCounts[ev.Project]++
	}

	for _, r := range e.rules {
		if !e.ruleMatches(r, ev) {
			continue
		}
		subject, countOK := e.subjectAndCount(r, ev)
		if !countOK {
			continue
		}
		key := e.dedupeKey(r.Name, subject)
		if _, already := e.fired[key]; already {
			continue // no alert storm
		}
		e.fired[key] = struct{}{}
		e.fireLocked(ctx, r, ev, subject)
	}
}

func isFailureClass(when string) bool {
	switch when {
	case WhenConsecutiveFailures, WhenPhaseFailed, WhenBudgetExceeded, WhenSandboxRefused:
		return true
	default:
		return false
	}
}

func (e *Escalator) ruleMatches(r config.EscalationRule, ev Event) bool {
	// Project filter.
	if r.Project != "" && r.Project != "*" && r.Project != ev.Project {
		return false
	}
	switch r.When {
	case WhenConsecutiveFailures:
		// Any failure-class event contributes to the consecutive counter.
		return isFailureClass(ev.When)
	case WhenBudgetExceeded:
		return ev.When == WhenBudgetExceeded
	case WhenSandboxRefused:
		return ev.When == WhenSandboxRefused
	case WhenPhaseFailed:
		return ev.When == WhenPhaseFailed
	default:
		return false
	}
}

func (e *Escalator) subjectAndCount(r config.EscalationRule, ev Event) (subject string, ok bool) {
	switch r.When {
	case WhenConsecutiveFailures:
		if ev.Project == "" {
			return "", false
		}
		n := e.failCounts[ev.Project]
		if n < r.Threshold {
			return "", false
		}
		return "project:" + ev.Project, true
	case WhenBudgetExceeded, WhenSandboxRefused, WhenPhaseFailed:
		// IssueID may be 0 in unit tests without an issues row (FK-safe).
		return fmt.Sprintf("issue:%d:%s", ev.IssueID, ev.Phase), true
	default:
		return "", false
	}
}

func (e *Escalator) dedupeKey(ruleName, subject string) string {
	return ruleName + "|" + subject
}

func (e *Escalator) fireLocked(ctx context.Context, r config.EscalationRule, ev Event, subject string) {
	body := fmt.Sprintf("Escalation rule %q (%s) fired.\nProject: %s\nIssue: #%d\nPhase: %s\nSubject: %s\nDetail: %s",
		r.Name, r.When, ev.Project, ev.IssueID, ev.Phase, subject, ev.Message)
	subjectLine := fmt.Sprintf("Escalation: %s", r.Name)
	log.Printf("escalation: %s", body)

	e.recent = append(e.recent, FiredEvent{
		Rule:    r.Name,
		When:    r.When,
		Project: ev.Project,
		IssueID: ev.IssueID,
		Phase:   ev.Phase,
		Message: ev.Message,
	})
	if len(e.recent) > 50 {
		e.recent = e.recent[len(e.recent)-50:]
	}

	if e.dispatcher == nil {
		return
	}
	recipients := e.recipients(r)
	for _, rcpt := range recipients {
		_ = e.dispatcher.Send(ctx, Notification{
			Kind:      KindEscalation,
			Recipient: rcpt,
			Subject:   subjectLine,
			Body:      body,
			IssueID:   ev.IssueID,
		})
	}
}

func (e *Escalator) recipients(r config.EscalationRule) []string {
	switch r.Notify {
	case "console":
		return []string{"console"}
	default: // admin
		if len(e.adminEmails) == 0 {
			return []string{"console"}
		}
		return e.adminEmails
	}
}

// ClassifyFailure maps a phase error string to an escalation When kind.
func ClassifyFailure(errMsg string) string {
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "budget_exceeded") {
		return WhenBudgetExceeded
	}
	if strings.Contains(lower, "sandbox") && (strings.Contains(lower, "refus") || strings.Contains(lower, "no container") || strings.Contains(lower, "docker") || strings.Contains(lower, "podman")) {
		return WhenSandboxRefused
	}
	return WhenPhaseFailed
}
