package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type countingStore struct {
	mu                 sync.Mutex
	active, peak, read int
}

func (s *countingStore) Read(context.Context, func(Tx) error) error {
	s.mu.Lock()
	s.active++
	s.read++
	s.peak = max(s.peak, s.active)
	s.mu.Unlock()
	time.Sleep(200 * time.Microsecond)
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return errors.New("database down")
}

func (s *countingStore) Write(context.Context, func(Tx) error) error {
	return errors.New("database down")
}

type goJobs struct{ wg sync.WaitGroup }

func (j *goJobs) After(_ string, delay time.Duration, fn func(context.Context)) {
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		time.Sleep(delay)
		fn(context.Background())
	}()
}

func (j *goJobs) Every(string, time.Duration, func(context.Context)) {}

type stubSwarm struct{ Swarm }

func TestApplyQueueSerializesPerNode(t *testing.T) {
	store, jobs := &countingStore{}, &goJobs{}
	a := New(App{Store: store, Jobs: jobs, Swarm: stubSwarm{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	var callers sync.WaitGroup
	for i := range 50 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			a.scheduleApply(applyRequest{nodeID: "x", revision: i})
		}()
	}
	callers.Wait()
	jobs.wg.Wait()
	if store.peak != 1 || store.read < 50 {
		t.Fatalf("peak %d reads %d", store.peak, store.read)
	}
	rt := a.deployRuntime()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.applies) != 0 {
		t.Fatalf("queues left: %v", rt.applies)
	}
}
