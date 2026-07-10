// Package trigger defines TriggerPort: sources of new issues for the daemon queue.
package trigger

import "context"

// Submission is a new issue emitted by a trigger source.
type Submission struct {
	Project    string
	Title      string
	Body       string
	Source     string // manual | webhook | github | jira | ...
	ExternalID string
	Metadata   map[string]string
}

// Port is a long-running source of issue submissions.
type Port interface {
	Name() string
	// Start emits submissions until ctx is cancelled. Blocks until done.
	Start(ctx context.Context, out chan<- Submission) error
}

// IsExternal reports whether the source should get the untrusted-input posture
// (forced human adjudicator on the implementer boundary by default).
func IsExternal(source string) bool {
	switch source {
	case "", "manual", "cli", "api", "dashboard":
		return false
	default:
		return true
	}
}
