package orchestrator

import "sync"

// modelLockSet is a process-wide keyed mutex set serializing phases against
// an inference endpoint (key = inference base_url) in exclusive mode. Keys
// serialize independently, so adding a second server later needs no redesign.
type modelLockSet struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// modelLocks is the singleton guarding all exclusive-mode inference servers.
var modelLocks = &modelLockSet{locks: map[string]*sync.Mutex{}}

func (s *modelLockSet) get(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	return m
}

// Lock acquires the lock for key, blocking until it is free.
func (s *modelLockSet) Lock(key string) {
	s.get(key).Lock()
}

// TryLock acquires the lock for key only if it is currently free.
func (s *modelLockSet) TryLock(key string) bool {
	return s.get(key).TryLock()
}

// Unlock releases the lock for key.
func (s *modelLockSet) Unlock(key string) {
	s.get(key).Unlock()
}
