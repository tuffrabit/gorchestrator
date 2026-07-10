package adapters

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// SupervisorConfig tunes restart behavior for a long-lived adapter process.
type SupervisorConfig struct {
	// MinBackoff is the initial delay after a crash (default 1s).
	MinBackoff time.Duration
	// MaxBackoff caps exponential backoff (default 60s).
	MaxBackoff time.Duration
	// MaxDown is how long the adapter may stay unhealthy before the
	// supervisor stops restarting (default 10m). Zero means never give up.
	MaxDown time.Duration
	// Client options applied on every spawn.
	Client ClientOptions
}

func (c *SupervisorConfig) defaults() SupervisorConfig {
	out := *c
	if out.MinBackoff <= 0 {
		out.MinBackoff = time.Second
	}
	if out.MaxBackoff <= 0 {
		out.MaxBackoff = 60 * time.Second
	}
	if out.MaxDown <= 0 {
		out.MaxDown = 10 * time.Minute
	}
	return out
}

// Supervisor keeps a JSON-RPC adapter process alive across crashes and
// fans out notifications from successive client instances into one channel.
type Supervisor struct {
	binary string
	cfg    SupervisorConfig

	mu      sync.Mutex
	client  *Client
	stopped bool
	dead    bool // permanent failure after MaxDown
	downAt  time.Time
	backoff time.Duration

	// clock allows tests to inject a fake sleeper.
	sleep func(context.Context, time.Duration) error

	notifCh chan Notification
	wg      sync.WaitGroup

	// generation increments on each successful (re)start so forwarders exit.
	gen atomic.Uint64
}

// NewSupervisor starts an adapter and a background restarter.
func NewSupervisor(binary string, cfg SupervisorConfig) (*Supervisor, error) {
	cfg = cfg.defaults()
	s := &Supervisor{
		binary:  binary,
		cfg:     cfg,
		notifCh: make(chan Notification, defaultNotifBuf),
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		backoff: cfg.MinBackoff,
	}
	if err := s.startLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// startLocked creates a new client. Caller must not hold s.mu for the
// long initialize, so we unlock during NewClientWithOptions.
func (s *Supervisor) startLocked() error {
	// Called without holding mu from NewSupervisor; with care from restart.
	client, err := NewClientWithOptions(s.binary, s.cfg.Client)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.client = client
	s.downAt = time.Time{}
	s.backoff = s.cfg.MinBackoff
	s.dead = false
	gen := s.gen.Add(1)
	s.mu.Unlock()

	s.wg.Add(1)
	go s.forwardNotifications(gen, client)
	s.wg.Add(1)
	go s.watch(gen, client)
	return nil
}

func (s *Supervisor) forwardNotifications(gen uint64, client *Client) {
	defer s.wg.Done()
	for n := range client.Notifications() {
		if s.gen.Load() != gen {
			return
		}
		select {
		case s.notifCh <- n:
		default:
			log.Printf("jsonrpc supervisor[%s]: dropping notification %s", s.binary, n.Method)
		}
	}
}

func (s *Supervisor) watch(gen uint64, client *Client) {
	defer s.wg.Done()
	<-client.Done()
	if s.gen.Load() != gen {
		return
	}
	s.mu.Lock()
	if s.stopped || s.dead {
		s.mu.Unlock()
		return
	}
	if s.client == client {
		s.client = nil
	}
	if s.downAt.IsZero() {
		s.downAt = time.Now()
	}
	backoff := s.backoff
	downAt := s.downAt
	s.mu.Unlock()

	// Fail in-flight is handled by the dead client's Call returning process-exited.

	for {
		s.mu.Lock()
		if s.stopped || s.dead {
			s.mu.Unlock()
			return
		}
		if s.cfg.MaxDown > 0 && !downAt.IsZero() && time.Since(downAt) > s.cfg.MaxDown {
			s.dead = true
			s.mu.Unlock()
			log.Printf("jsonrpc supervisor[%s]: giving up after %v down", s.binary, s.cfg.MaxDown)
			return
		}
		s.mu.Unlock()

		ctx := context.Background()
		if err := s.sleep(ctx, backoff); err != nil {
			return
		}

		s.mu.Lock()
		if s.stopped || s.dead {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		client, err := NewClientWithOptions(s.binary, s.cfg.Client)
		if err != nil {
			log.Printf("jsonrpc supervisor[%s]: restart failed: %v", s.binary, err)
			s.mu.Lock()
			s.backoff = nextBackoff(s.backoff, s.cfg.MaxBackoff)
			backoff = s.backoff
			s.mu.Unlock()
			continue
		}

		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			_ = client.Close()
			return
		}
		s.client = client
		s.downAt = time.Time{}
		s.backoff = s.cfg.MinBackoff
		newGen := s.gen.Add(1)
		s.mu.Unlock()

		log.Printf("jsonrpc supervisor[%s]: restarted (pid %d)", s.binary, client.Pid())
		s.wg.Add(1)
		go s.forwardNotifications(newGen, client)
		s.wg.Add(1)
		go s.watch(newGen, client)
		return
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	if next < cur {
		return max // overflow
	}
	return next
}

// Call invokes a method on the current client. If the process is down, waits
// briefly for a restart (up to ctx) and retries once.
func (s *Supervisor) Call(ctx context.Context, method string, params any) (*Response, error) {
	client, err := s.currentClient(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.Call(ctx, method, params)
	if err == nil {
		return resp, nil
	}
	// Retry once if the process died under us.
	if ctx.Err() != nil {
		return nil, err
	}
	client, err2 := s.currentClient(ctx)
	if err2 != nil {
		return nil, err
	}
	return client.Call(ctx, method, params)
}

func (s *Supervisor) currentClient(ctx context.Context) (*Client, error) {
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return nil, fmt.Errorf("adapter supervisor stopped")
		}
		if s.dead {
			s.mu.Unlock()
			return nil, fmt.Errorf("adapter supervisor permanently failed")
		}
		c := s.client
		s.mu.Unlock()
		if c != nil {
			select {
			case <-c.Done():
				// fall through to wait
			default:
				return c, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("adapter not available")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Notifications returns a stable channel of notifications across restarts.
// It is closed when Close is called.
func (s *Supervisor) Notifications() <-chan Notification {
	return s.notifCh
}

// Close stops the supervisor and the current client. Safe to call once.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	client := s.client
	s.client = nil
	s.gen.Add(1) // invalidate watchers/forwarders
	s.mu.Unlock()

	var err error
	if client != nil {
		err = client.Close()
	}
	// Wait for background goroutines with a bound.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeTimeout + time.Second):
	}
	close(s.notifCh)
	return err
}
