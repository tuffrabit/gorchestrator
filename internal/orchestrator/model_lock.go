package orchestrator

import "sync"

// modelLockSet is a process-wide keyed mutex set serializing phases against
// an inference endpoint (key = inference base_url) in exclusive mode. Keys
// serialize independently, so adding a second server later needs no redesign.
//
// Each key has a FIFO waiter queue with a priority lane: Lock waiters queue
// in arrival order, while LockPriority waiters are inserted ahead of all
// currently queued normal waiters (FIFO among priority waiters, which always
// sit at the front of the queue). TryLock only succeeds when the key is
// completely free: no owner and no queued waiters.
type modelLockSet struct {
	mu    sync.Mutex
	locks map[string]*keyedModelLock
}

type modelLockWaiter struct {
	ch       chan struct{}
	priority bool
}

type keyedModelLock struct {
	locked  bool
	waiters []modelLockWaiter
}

// modelLocks is the singleton guarding all exclusive-mode inference servers.
var modelLocks = &modelLockSet{locks: map[string]*keyedModelLock{}}

func (s *modelLockSet) get(key string) *keyedModelLock {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[key]
	if !ok {
		l = &keyedModelLock{}
		s.locks[key] = l
	}
	return l
}

// Lock acquires the lock for key, blocking until it is free. Normal-priority
// waiters are served FIFO, behind any priority waiters ahead of them.
func (s *modelLockSet) Lock(key string) {
	s.enqueue(key, false)
}

// LockPriority acquires the lock for key, blocking until it is free, but
// queued ahead of all currently waiting Lock waiters. Priority waiters are
// served FIFO among themselves.
func (s *modelLockSet) LockPriority(key string) {
	s.enqueue(key, true)
}

func (s *modelLockSet) enqueue(key string, priority bool) {
	l := s.get(key)
	s.mu.Lock()
	if !l.locked && len(l.waiters) == 0 {
		l.locked = true
		s.mu.Unlock()
		return
	}
	w := modelLockWaiter{ch: make(chan struct{}), priority: priority}
	if priority {
		// Insert after the last priority waiter already queued: ahead of
		// all normal waiters, FIFO among priority waiters.
		i := 0
		for i < len(l.waiters) && l.waiters[i].priority {
			i++
		}
		l.waiters = append(l.waiters, modelLockWaiter{})
		copy(l.waiters[i+1:], l.waiters[i:])
		l.waiters[i] = w
	} else {
		l.waiters = append(l.waiters, w)
	}
	s.mu.Unlock()
	<-w.ch
}

// TryLock acquires the lock for key only if it is completely free: no owner
// and no queued waiters.
func (s *modelLockSet) TryLock(key string) bool {
	l := s.get(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.locked || len(l.waiters) > 0 {
		return false
	}
	l.locked = true
	return true
}

// Unlock releases the lock for key, handing off directly to the head of the
// waiter queue (priority waiters always sit at the front).
func (s *modelLockSet) Unlock(key string) {
	l := s.get(key)
	s.mu.Lock()
	if !l.locked {
		s.mu.Unlock()
		panic("modelLockSet: Unlock of unlocked key")
	}
	if len(l.waiters) == 0 {
		l.locked = false
		s.mu.Unlock()
		return
	}
	// Hand off to the head waiter: ownership transfers to it, and locked
	// stays true until its own Unlock.
	w := l.waiters[0]
	l.waiters = l.waiters[1:]
	s.mu.Unlock()
	close(w.ch)
}
