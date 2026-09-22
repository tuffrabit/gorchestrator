package orchestrator

import (
	"sync"
	"testing"
	"time"
)

func newTestLockSet() *modelLockSet {
	return &modelLockSet{locks: map[string]*keyedModelLock{}}
}

func TestModelLock_SameKeySerializes(t *testing.T) {
	ls := newTestLockSet()
	ls.Lock("http://server-a")

	acquired := make(chan struct{})
	go func() {
		ls.Lock("http://server-a")
		close(acquired)
		ls.Unlock("http://server-a")
	}()

	select {
	case <-acquired:
		t.Fatal("second Lock on held key acquired immediately")
	case <-time.After(100 * time.Millisecond):
	}

	ls.Unlock("http://server-a")
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second Lock not acquired after Unlock")
	}
}

func TestModelLock_DifferentKeysDontBlock(t *testing.T) {
	ls := newTestLockSet()
	ls.Lock("http://server-a")
	defer ls.Unlock("http://server-a")

	acquired := make(chan struct{})
	go func() {
		ls.Lock("http://server-b")
		close(acquired)
		ls.Unlock("http://server-b")
	}()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("Lock on a different key blocked behind a held key")
	}
}

func TestModelLock_TryLock(t *testing.T) {
	ls := newTestLockSet()
	if !ls.TryLock("http://server-a") {
		t.Fatal("TryLock on free key = false, want true")
	}
	if ls.TryLock("http://server-a") {
		t.Fatal("TryLock on held key = true, want false")
	}
	ls.Unlock("http://server-a")
	if !ls.TryLock("http://server-a") {
		t.Fatal("TryLock after Unlock = false, want true")
	}
	ls.Unlock("http://server-a")
}

// waitForQueued blocks until n waiters are enqueued on key, so the test can
// deterministically control the queue state before Unlocking.
func waitForQueued(t *testing.T, ls *modelLockSet, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ls.mu.Lock()
		l := ls.locks[key]
		queued := 0
		if l != nil {
			queued = len(l.waiters)
		}
		ls.mu.Unlock()
		if queued >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d queued waiters on %q", n, key)
}

func TestModelLock_PriorityOrder(t *testing.T) {
	ls := newTestLockSet()
	key := "http://server-a"
	ls.Lock(key)

	acquired := make(chan string, 3)
	var wg sync.WaitGroup
	start := func(name string, lockFn func(string)) chan struct{} {
		release := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			lockFn(key)
			acquired <- name
			<-release
			ls.Unlock(key)
		}()
		return release
	}

	relNormal1 := start("normal-1", ls.Lock)
	waitForQueued(t, ls, key, 1)
	relNormal2 := start("normal-2", ls.Lock)
	waitForQueued(t, ls, key, 2)
	relPriority := start("priority-1", ls.LockPriority)
	waitForQueued(t, ls, key, 3)

	rels := map[string]chan struct{}{
		"normal-1":   relNormal1,
		"normal-2":   relNormal2,
		"priority-1": relPriority,
	}

	ls.Unlock(key)

	// Priority waiter jumps both queued normal waiters; the normal waiters
	// then run in FIFO order.
	want := []string{"priority-1", "normal-1", "normal-2"}
	for i, w := range want {
		select {
		case name := <-acquired:
			if name != w {
				t.Fatalf("acquisition %d = %q, want %q", i, name, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("acquisition %d (%q) timed out", i, w)
		}
		close(rels[w])
	}
	wg.Wait()
}
