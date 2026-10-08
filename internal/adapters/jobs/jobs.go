// Package jobs is the in-memory app.Jobs: Convex's scheduler.runAfter and crons, without
// durability. A restart loses pending jobs; serve's recovery pass (app.Recover) rebuilds what
// matters. See docs/go/ARCHITECTURE.md, "Use cases".
package jobs

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/app"
)

// Scheduler runs one-shot (After) and periodic (Every) jobs in goroutines.
//
//   - After coalesces by key: while a job with the same key is pending (its timer has not fired
//     yet), further After calls with that key are dropped. Once it starts running the key is
//     free again, so an After during the run schedules a new one. An empty key never coalesces.
//   - Every runs fn each interval, first after one interval. Runs of the same name never
//     overlap: a tick that finds the previous run still going is skipped.
//   - A panicking job is recovered and logged; the scheduler and other jobs keep going.
//   - Stop cancels pending timers and Every loops, then waits for running jobs.
//
// Every job gets the scheduler's context, which is cancelled only when Stop gives up waiting.
type Scheduler struct {
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	quit   chan struct{} // closed by Stop: Every loops exit

	mu      sync.Mutex
	stopped bool
	pending map[string]*time.Timer   // keyed After jobs waiting for their timer
	anon    map[*time.Timer]struct{} // After jobs with an empty key waiting for their timer
	running map[string]bool          // Every jobs currently running, by name
	wg      sync.WaitGroup           // running After jobs and Every loops
}

var _ app.Jobs = (*Scheduler)(nil)

// New is a running scheduler. log may be nil (slog.Default).
func New(log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		log:     log,
		ctx:     ctx,
		cancel:  cancel,
		quit:    make(chan struct{}),
		pending: map[string]*time.Timer{},
		anon:    map[*time.Timer]struct{}{},
		running: map[string]bool{},
	}
}

// After runs fn once after delay, unless a job with the same key is already pending (then this
// call is dropped). After Stop it does nothing.
func (s *Scheduler) After(key string, delay time.Duration, fn func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if key != "" {
		if _, ok := s.pending[key]; ok {
			return
		}
	}
	var t *time.Timer
	t = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if key == "" {
			delete(s.anon, t)
		} else if s.pending[key] == t {
			delete(s.pending, key)
		}
		if s.stopped {
			s.mu.Unlock()
			return
		}
		s.wg.Add(1)
		s.mu.Unlock()
		defer s.wg.Done()
		s.run(key, fn)
	})
	if key == "" {
		s.anon[t] = struct{}{}
	} else {
		s.pending[key] = t
	}
}

// Every runs fn every interval (first run after one interval) until Stop. A tick while the
// previous run of the same name is still going is skipped. After Stop it does nothing.
func (s *Scheduler) Every(name string, interval time.Duration, fn func(context.Context)) {
	if interval <= 0 {
		s.log.Error("job: Every needs a positive interval", "job", name, "interval", interval)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.quit:
				return
			case <-ticker.C:
			}
			s.mu.Lock()
			if s.stopped || s.running[name] {
				skipped := !s.stopped
				s.mu.Unlock()
				if skipped {
					s.log.Debug("job: previous run still going, tick skipped", "job", name)
				}
				continue
			}
			s.running[name] = true
			s.mu.Unlock()

			s.run(name, fn)

			s.mu.Lock()
			delete(s.running, name)
			s.mu.Unlock()
		}
	}()
}

// run calls fn, turning a panic into a log line.
func (s *Scheduler) run(name string, fn func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("job panicked", "job", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn(s.ctx)
}

// Pending reports whether an After job with key is waiting for its timer (tests, debugging).
func (s *Scheduler) Pending(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[key]
	return ok
}

// Stop drops pending After jobs, ends Every loops and waits for running jobs. Running jobs get
// until ctx is done to return on their own; then their context is cancelled and Stop returns
// ctx.Err() without waiting further. Later After/Every calls are ignored. Safe to call twice.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		for key, t := range s.pending {
			t.Stop()
			delete(s.pending, key)
		}
		for t := range s.anon {
			t.Stop()
			delete(s.anon, t)
		}
		close(s.quit)
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	}
}
