package jobs

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/app"
)

type Scheduler struct {
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	quit   chan struct{}

	mu      sync.Mutex
	stopped bool
	pending map[string]bool
	wg      sync.WaitGroup
}

var _ app.Jobs = (*Scheduler)(nil)

func New(log *slog.Logger) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{log: log, ctx: ctx, cancel: cancel, quit: make(chan struct{}), pending: map[string]bool{}}
}

func (s *Scheduler) After(key string, delay time.Duration, fn func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.pending[key] {
		return
	}
	if key != "" {
		s.pending[key] = true
	}
	time.AfterFunc(delay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.pending, key)
		if s.stopped {
			return
		}
		s.wg.Go(func() { s.run(key, fn) })
	})
}

func (s *Scheduler) Every(name string, interval time.Duration, fn func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	ticker := time.NewTicker(interval)
	s.wg.Go(func() {
		defer ticker.Stop()
		for {
			select {
			case <-s.quit:
				return
			case <-ticker.C:
				s.run(name, fn)
			}
		}
	})
}

func (s *Scheduler) run(name string, fn func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("job panicked", "job", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn(s.ctx)
}

func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.quit)
	}
	s.mu.Unlock()

	err := s.Wait(ctx)
	s.cancel()
	return err
}

func (s *Scheduler) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
