package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/tuffrabit/gorchestrator/internal/adapters"
)

// AdapterTrigger listens for trigger.issue JSON-RPC notifications from a
// supervised external adapter process (GitHub, Jira, fixtures).
type AdapterTrigger struct {
	name string
	sup  *adapters.Supervisor
}

// NewAdapterTrigger wraps a supervised JSON-RPC adapter as a TriggerPort.
func NewAdapterTrigger(name string, sup *adapters.Supervisor) *AdapterTrigger {
	return &AdapterTrigger{name: name, sup: sup}
}

// Name implements Port.
func (a *AdapterTrigger) Name() string { return a.name }

// Start implements Port.
func (a *AdapterTrigger) Start(ctx context.Context, out chan<- Submission) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case n, ok := <-a.sup.Notifications():
			if !ok {
				return fmt.Errorf("trigger adapter %s closed", a.name)
			}
			if n.Method != "trigger.issue" {
				continue
			}
			sub, err := parseSubmission(n.Params, a.name)
			if err != nil {
				log.Printf("trigger[%s]: bad notification: %v", a.name, err)
				continue
			}
			select {
			case out <- sub:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// Close stops the supervised adapter.
func (a *AdapterTrigger) Close() error {
	if a.sup != nil {
		return a.sup.Close()
	}
	return nil
}

func parseSubmission(params json.RawMessage, defaultSource string) (Submission, error) {
	var p struct {
		Project    string            `json:"project"`
		Title      string            `json:"title"`
		Body       string            `json:"body"`
		Source     string            `json:"source"`
		ExternalID string            `json:"external_id"`
		Metadata   map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return Submission{}, err
	}
	if p.Title == "" {
		return Submission{}, fmt.Errorf("title required")
	}
	if p.Project == "" {
		return Submission{}, fmt.Errorf("project required")
	}
	src := p.Source
	if src == "" {
		src = defaultSource
	}
	return Submission{
		Project:    p.Project,
		Title:      p.Title,
		Body:       p.Body,
		Source:     src,
		ExternalID: p.ExternalID,
		Metadata:   p.Metadata,
	}, nil
}
