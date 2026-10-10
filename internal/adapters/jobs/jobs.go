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
	pending map[string]*time.Timer
	anon    map[*time.Timer]struct{}
	running map[string]bool
	wg      sync.WaitGroup
}

var _ app.Jobs = (*Scheduler)(nil)

func New(log *slog.Logger) *Scheduler {
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

func (s *Scheduler) run(name string, fn func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("job panicked", "job", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn(s.ctx)
}

func (s *Scheduler) Pending(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[key]
	return ok
}

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
