package orchestrator

import "sync"

// gitWorkspaceLock is a process-wide keyed mutex set serializing git
// workspace mutations (EnsureCache fetches, worktree add/remove/prune) per
// project. Chat turns and pipeline phases both mutate the same bare cache
// (repos/<id>.git); in particular a concurrent `worktree prune` can race
// against a sibling `worktree add`. Keys serialize independently.
type gitWorkspaceLock struct {
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

// gitWorkspaceLocks is the singleton guarding all per-project git workspaces.
var gitWorkspaceLocks = &gitWorkspaceLock{locks: map[int64]*sync.Mutex{}}

// lock returns the mutex for a project's git workspace; callers must unlock
// it when their fetch/worktree mutation is done.
func (l *gitWorkspaceLock) lock(projectID int64) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.locks[projectID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[projectID] = m
	}
	return m
}
