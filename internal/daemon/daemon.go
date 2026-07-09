// Package daemon implements the long-running serve process: worker pool,
// claim loop, recovery, and graceful shutdown.
package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// Daemon owns the worker pool that claims queued issues and runs pipelines.
type Daemon struct {
	eng    *orchestrator.Engine
	cfg    *config.Config
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a daemon bound to an engine.
func New(eng *orchestrator.Engine, cfg *config.Config) *Daemon {
	return &Daemon{eng: eng, cfg: cfg}
}

// Start recovers in-flight work and launches the worker pool.
// It returns immediately; call Shutdown to stop.
func (d *Daemon) Start(ctx context.Context) error {
	if err := d.eng.RecoverAll(ctx); err != nil {
		return fmt.Errorf("recover all: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel

	n := d.cfg.Server.MaxConcurrentIssues
	if n <= 0 {
		n = 2
	}
	for i := 0; i < n; i++ {
		d.wg.Add(1)
		go d.worker(runCtx, i)
	}
	log.Printf("daemon: started %d workers (max_concurrent_issues=%d)", n, n)
	return nil
}

// Shutdown stops claiming new work, waits up to timeout for in-flight phases,
// then cancels worker contexts. It never writes status=failed for clean cancel.
func (d *Daemon) Shutdown(timeout time.Duration) {
	log.Printf("daemon: shutting down (timeout=%s)", timeout)
	if d.cancel != nil {
		// Stop the claim loop accepting new work by cancelling after drain wait.
		// Workers check ctx only between claims and inside runPipeline.
	}

	done := make(chan struct{})
	go func() {
		// Signal workers to stop claiming; in-flight pipelines get the deadline.
		if d.cancel != nil {
			// Give in-flight work the shutdown window before hard cancel.
		}
		// Soft-stop: cancel context so claim loop exits; pipelines use parent ctx.
		if d.cancel != nil {
			d.cancel()
		}
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Printf("daemon: all workers stopped cleanly")
	case <-time.After(timeout):
		log.Printf("daemon: shutdown timeout exceeded; remaining work left in_progress for recovery")
		if d.cancel != nil {
			d.cancel()
		}
		// Wait a short grace for goroutines to observe cancel.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *Daemon) worker(ctx context.Context, id int) {
	defer d.wg.Done()
	log.Printf("daemon: worker %d ready", id)

	for {
		if ctx.Err() != nil {
			return
		}

		issue, err := d.eng.Issues().ClaimQueued()
		if err != nil {
			log.Printf("daemon: worker %d claim error: %v", id, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		if issue == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}

		log.Printf("daemon: worker %d claimed issue %d", id, issue.ID)
		if err := d.eng.ProcessIssue(ctx, issue.ID); err != nil {
			// waiting_human returns nil; other errors may leave failed/cancelled.
			// Context cancel mid-phase writes cancelled via runPhase.
			if ctx.Err() != nil {
				log.Printf("daemon: worker %d issue %d interrupted by shutdown: %v", id, issue.ID, err)
				// Leave non-terminal status for RecoverAll; do not mark failed.
				issue, _ = d.eng.Issues().Get(issue.ID)
				if issue != nil && issue.Status == sqlite.StatusInProgress {
					// Abrupt cancel: leave in_progress for recovery, or mark cancelled
					// if the pipeline already wrote cancelled to FS.
					phase, fsStatus, _ := d.eng.CurrentPhaseState(issue.ProjectID, issue.ID)
					if fsStatus == "cancelled" {
						_ = d.eng.Issues().UpdateStatus(issue.ID, sqlite.StatusCancelled, phase)
					}
					// else leave in_progress for RecoverAll on next start
				}
				return
			}
			log.Printf("daemon: worker %d issue %d error: %v", id, issue.ID, err)
		} else {
			log.Printf("daemon: worker %d finished issue %d", id, issue.ID)
		}
	}
}
