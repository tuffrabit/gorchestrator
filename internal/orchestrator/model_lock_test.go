package orchestrator

import (
	"sync"
	"testing"
	"time"
)

func newTestLockSet() *modelLockSet {
	return &modelLockSet{locks: map[string]*sync.Mutex{}}
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
