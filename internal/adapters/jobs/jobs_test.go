package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func newTest(t *testing.T) (*Scheduler, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	s := New(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	return s, buf
}

// waitFor polls cond for up to 2 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAfterCoalescesPendingKey(t *testing.T) {
	s, _ := newTest(t)
	var a, b atomic.Int32
	s.After("observe:n1", 30*time.Millisecond, func(context.Context) { a.Add(1) })
	s.After("observe:n1", 0, func(context.Context) { b.Add(1) }) // dropped: same key pending
	if !s.Pending("observe:n1") {
		t.Fatal("first job should be pending")
	}
	waitFor(t, "first job", func() bool { return a.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	if b.Load() != 0 {
		t.Fatalf("second call with a pending key ran %d times", b.Load())
	}
	if s.Pending("observe:n1") {
		t.Fatal("key still pending after the job ran")
	}

	// Once the job has started, the key is free again.
	s.After("observe:n1", 0, func(context.Context) { b.Add(1) })
	waitFor(t, "rescheduled job", func() bool { return b.Load() == 1 })
}

func TestAfterKeyFreedWhenRunning(t *testing.T) {
	s, _ := newTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	var second atomic.Int32
	s.After("k", 0, func(context.Context) {
		close(started)
		<-release
	})
	<-started
	// The first job is running, not pending: this one is scheduled.
	s.After("k", 0, func(context.Context) { second.Add(1) })
	waitFor(t, "second job while the first runs", func() bool { return second.Load() == 1 })
	close(release)
}

func TestAfterDistinctAndEmptyKeys(t *testing.T) {
	s, _ := newTest(t)
	var n atomic.Int32
	inc := func(context.Context) { n.Add(1) }
	s.After("a", 5*time.Millisecond, inc)
	s.After("b", 5*time.Millisecond, inc)
	s.After("", 5*time.Millisecond, inc) // empty key: never coalesced
	s.After("", 5*time.Millisecond, inc)
	waitFor(t, "four jobs", func() bool { return n.Load() == 4 })
}

func TestEveryNoOverlap(t *testing.T) {
	s, _ := newTest(t)
	var runs, active, maxActive atomic.Int32
	job := func(context.Context) {
		cur := active.Add(1)
		for {
			m := maxActive.Load()
			if cur <= m || maxActive.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(25 * time.Millisecond) // much longer than the interval
		active.Add(-1)
		runs.Add(1)
	}
	s.Every("resync", 2*time.Millisecond, job)
	s.Every("resync", 2*time.Millisecond, job) // same name registered twice: still one at a time
	waitFor(t, "three runs", func() bool { return runs.Load() >= 3 })
	if m := maxActive.Load(); m != 1 {
		t.Fatalf("runs of the same job overlapped: %d at once", m)
	}
}

func TestEveryDifferentNamesRunConcurrently(t *testing.T) {
	s, _ := newTest(t)
	var a, b atomic.Int32
	block := make(chan struct{})
	defer close(block)
	s.Every("slow", 2*time.Millisecond, func(context.Context) {
		if a.Add(1) == 1 {
			<-block
		}
	})
	s.Every("fast", 2*time.Millisecond, func(context.Context) { b.Add(1) })
	waitFor(t, "fast job while the slow one blocks", func() bool { return b.Load() >= 3 })
}

func TestPanicRecovered(t *testing.T) {
	s, logs := newTest(t)
	var after, every atomic.Int32
	s.After("boom", 0, func(context.Context) { panic("kaboom") })
	s.Every("flaky", 2*time.Millisecond, func(context.Context) {
		if every.Add(1) == 1 {
			panic("first tick")
		}
	})
	waitFor(t, "panic logged", func() bool { return strings.Contains(logs.String(), "kaboom") })
	// The scheduler keeps working: another After runs, and the Every loop survived its panic.
	s.After("next", 0, func(context.Context) { after.Add(1) })
	waitFor(t, "job after a panic", func() bool { return after.Load() == 1 })
	waitFor(t, "Every after its own panic", func() bool { return every.Load() >= 3 })
	out := logs.String()
	for _, want := range []string{"job panicked", "job=boom", "job=flaky", "first tick", "stack="} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestStopCancelsPendingAndWaitsForRunning(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	var pendingRan, lateRan, everyRan atomic.Int32
	s.After("later", 50*time.Millisecond, func(context.Context) { pendingRan.Add(1) })
	s.After("", 50*time.Millisecond, func(context.Context) { pendingRan.Add(1) })

	started := make(chan struct{})
	var finished atomic.Bool
	var sawCancel atomic.Bool
	s.After("running", 0, func(ctx context.Context) {
		close(started)
		time.Sleep(40 * time.Millisecond)
		sawCancel.Store(ctx.Err() != nil)
		finished.Store(true)
	})
	<-started

	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !finished.Load() {
		t.Fatal("Stop returned before the running job finished")
	}
	if sawCancel.Load() {
		t.Fatal("the running job's context was cancelled although Stop had time to wait")
	}

	// Nothing is accepted after Stop, and the pending timers never fire.
	s.After("late", 0, func(context.Context) { lateRan.Add(1) })
	s.Every("late-every", time.Millisecond, func(context.Context) { everyRan.Add(1) })
	time.Sleep(80 * time.Millisecond)
	if n := pendingRan.Load() + lateRan.Load() + everyRan.Load(); n != 0 {
		t.Fatalf("%d jobs ran after Stop", n)
	}
	if s.Pending("later") {
		t.Fatal("pending job still listed after Stop")
	}
	// Stop twice is fine.
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestStopDeadlineCancelsJobContext(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	started, cancelled := make(chan struct{}), make(chan struct{})
	s.After("stuck", 0, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(cancelled)
	})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want deadline exceeded", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("job context not cancelled after Stop's deadline")
	}
}

// After Stop gives up and cancels, Wait lets the cancelled jobs finish what they do on
// cancellation (serve closes the database only after that).
func TestWaitAfterStopDeadline(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	started := make(chan struct{})
	var recorded atomic.Bool
	s.After("apply", 0, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		time.Sleep(30 * time.Millisecond) // writes its outcome
		recorded.Store(true)
	})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want deadline exceeded", err)
	}
	if recorded.Load() {
		t.Fatal("Stop waited past its deadline")
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	if err := s.Wait(wctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !recorded.Load() {
		t.Fatal("Wait returned before the cancelled job finished")
	}

	// Wait honours its own deadline too.
	s2 := New(slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	defer func() { _ = s2.Stop(context.Background()) }() // runs after release is closed
	release := make(chan struct{})
	defer close(release)
	running := make(chan struct{})
	s2.After("stuck", 0, func(context.Context) { close(running); <-release })
	<-running
	short, scancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer scancel()
	if err := s2.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want deadline exceeded", err)
	}
}

func TestStopEndsEveryLoopsAfterCurrentRun(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	started := make(chan struct{}, 1)
	var runs atomic.Int32
	var done atomic.Bool
	s.Every("tick", time.Millisecond, func(context.Context) {
		if runs.Add(1) == 1 {
			started <- struct{}{}
			time.Sleep(30 * time.Millisecond)
			done.Store(true)
		}
	})
	<-started
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !done.Load() {
		t.Fatal("Stop did not wait for the running Every job")
	}
	n := runs.Load()
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != n {
		t.Fatal("Every kept running after Stop")
	}
}
