// Package notify implements NotificationPort, console sink, and multi-sink dispatch.
package notify

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/tuffrabit/gorchestrator/internal/adapters"
	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// Kind constants.
const (
	KindHumanGate = "human_gate"
	KindBadOutput = "bad_output"
	KindInfo      = "info"
)

// Notification is a message to deliver to a human operator.
type Notification struct {
	Kind      string
	Recipient string
	Subject   string
	Body      string
	IssueID   int64
}

// Port sends notifications.
type Port interface {
	Send(ctx context.Context, n Notification) error
}

// Console logs notifications to the process log.
type Console struct{}

// Send implements Port.
func (c *Console) Send(ctx context.Context, n Notification) error {
	_ = ctx
	log.Printf("notify[%s] to=%s subject=%q body=%s", n.Kind, n.Recipient, n.Subject, n.Body)
	return nil
}

// Dispatcher fans out to multiple sinks and records rows in SQLite.
type Dispatcher struct {
	repo  *sqlite.NotificationRepo
	sinks []Port
}

// NewDispatcher creates a dispatcher with the given sinks (console is typically first).
func NewDispatcher(repo *sqlite.NotificationRepo, sinks ...Port) *Dispatcher {
	return &Dispatcher{repo: repo, sinks: sinks}
}

// Send records a pending row, delivers to all sinks, and marks sent/failed.
func (d *Dispatcher) Send(ctx context.Context, n Notification) error {
	var issueID *int64
	if n.IssueID > 0 {
		id := n.IssueID
		issueID = &id
	}
	row, err := d.repo.Create(issueID, n.Kind, n.Recipient, n.Subject, n.Body)
	if err != nil {
		return fmt.Errorf("record notification: %w", err)
	}

	var firstErr error
	for _, sink := range d.sinks {
		if err := sink.Send(ctx, n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		_ = d.repo.MarkFailed(row.ID, firstErr.Error())
		return firstErr
	}
	return d.repo.MarkSent(row.ID)
}

// NotifyHumanGate is a convenience for the engine gate path.
func NotifyHumanGate(ctx context.Context, d *Dispatcher, issueID int64, phase, project, title string, adminEmails []string) {
	if d == nil {
		return
	}
	body := fmt.Sprintf("Issue #%d (%s) phase %s needs a human decision.\nTitle: %s", issueID, project, phase, title)
	recipients := adminEmails
	if len(recipients) == 0 {
		recipients = []string{"console"}
	}
	for _, r := range recipients {
		_ = d.Send(ctx, Notification{
			Kind:      KindHumanGate,
			Recipient: r,
			Subject:   fmt.Sprintf("Human gate: issue #%d %s", issueID, phase),
			Body:      body,
			IssueID:   issueID,
		})
	}
}

// NotifyBadOutput alerts on failed/empty/timeout phases.
func NotifyBadOutput(ctx context.Context, d *Dispatcher, issueID int64, phase, errMsg string, adminEmails []string) {
	if d == nil {
		return
	}
	body := fmt.Sprintf("Issue #%d phase %s failed: %s", issueID, phase, errMsg)
	recipients := adminEmails
	if len(recipients) == 0 {
		recipients = []string{"console"}
	}
	for _, r := range recipients {
		_ = d.Send(ctx, Notification{
			Kind:      KindBadOutput,
			Recipient: r,
			Subject:   fmt.Sprintf("Bad output: issue #%d %s", issueID, phase),
			Body:      body,
			IssueID:   issueID,
		})
	}
}

// AdapterSink sends notifications via a JSON-RPC stdio adapter (port: notification).
type AdapterSink struct {
	client *adapters.Client
	name   string
}

// NewAdapterSink spawns the adapter binary from a loaded manifest.
func NewAdapterSink(m *adapters.Manifest) (*AdapterSink, error) {
	c, err := adapters.NewClient(m.Binary)
	if err != nil {
		return nil, fmt.Errorf("start notify adapter %s: %w", m.Name, err)
	}
	return &AdapterSink{client: c, name: m.Name}, nil
}

// Send implements Port.
func (a *AdapterSink) Send(ctx context.Context, n Notification) error {
	_, err := a.client.Call(ctx, "notification.send", map[string]any{
		"kind":      n.Kind,
		"recipient": n.Recipient,
		"subject":   n.Subject,
		"body":      n.Body,
		"issue_id":  n.IssueID,
	})
	if err != nil {
		return fmt.Errorf("adapter %s: %w", a.name, err)
	}
	return nil
}

// Close stops the adapter process.
func (a *AdapterSink) Close() error {
	if a.client != nil {
		return a.client.Close()
	}
	return nil
}

// BuildDispatcher creates a dispatcher with console plus any configured
// notification adapters from cfg.Adapters / cfg.Notifications.Adapters.
func BuildDispatcher(repo *sqlite.NotificationRepo, cfg *config.Config) (*Dispatcher, []*AdapterSink, error) {
	sinks := []Port{&Console{}}
	var adapterSinks []*AdapterSink

	wanted := map[string]struct{}{}
	for _, name := range cfg.Notifications.Adapters {
		wanted[name] = struct{}{}
	}
	for _, ac := range cfg.Adapters {
		if len(wanted) > 0 {
			if _, ok := wanted[ac.Name]; !ok {
				continue
			}
		} else {
			// No explicit notifications.adapters list: only load adapters whose
			// manifests declare port notification (checked after load).
		}
		path := ac.ManifestPath
		if path == "" {
			continue
		}
		if stringsHasTilde(path) {
			// expand is caller's responsibility via config Load; keep as-is.
		}
		m, err := adapters.LoadManifest(path)
		if err != nil {
			// Only fail hard if explicitly listed; otherwise skip.
			if _, ok := wanted[ac.Name]; ok {
				return nil, nil, fmt.Errorf("load notify adapter %s: %w", ac.Name, err)
			}
			log.Printf("notify: skip adapter %s: %v", ac.Name, err)
			continue
		}
		if m.Port != "notification" {
			continue
		}
		if len(wanted) == 0 {
			// without explicit list, still only use port=notification adapters
		}
		sink, err := NewAdapterSink(m)
		if err != nil {
			if _, ok := wanted[ac.Name]; ok {
				return nil, nil, err
			}
			log.Printf("notify: skip adapter %s: %v", ac.Name, err)
			continue
		}
		sinks = append(sinks, sink)
		adapterSinks = append(adapterSinks, sink)
		log.Printf("notify: enabled adapter %s (%s)", m.Name, filepath.Base(m.Binary))
	}
	return NewDispatcher(repo, sinks...), adapterSinks, nil
}

func stringsHasTilde(s string) bool {
	return len(s) > 0 && s[0] == '~'
}
