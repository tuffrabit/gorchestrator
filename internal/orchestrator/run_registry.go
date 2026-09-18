package orchestrator

import "context"

// registerRun derives a cancellable context for issueID's pipeline run and
// records it so StopIssue can cancel the run. The returned unregister must be
// deferred by the caller; it removes the registry entry and cancels the
// derived context.
func (e *Engine) registerRun(issueID int64, parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	e.runMu.Lock()
	e.runRegs[issueID] = &runReg{cancel: cancel}
	e.runMu.Unlock()
	return ctx, func() {
		e.runMu.Lock()
		delete(e.runRegs, issueID)
		e.runMu.Unlock()
		cancel()
	}
}

// requestStop marks the run as user-stopped and cancels its context.
// Returns false when no run is registered for the issue.
func (e *Engine) requestStop(issueID int64) bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	reg, ok := e.runRegs[issueID]
	if !ok {
		return false
	}
	reg.stopped = true
	reg.cancel()
	return true
}

// stopRequested reports whether the issue's active run was cancelled by a
// user stop (as opposed to daemon shutdown).
func (e *Engine) stopRequested(issueID int64) bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	reg, ok := e.runRegs[issueID]
	return ok && reg.stopped
}

// runActive reports whether a pipeline run is currently registered for the
// issue.
func (e *Engine) runActive(issueID int64) bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	_, ok := e.runRegs[issueID]
	return ok
}
