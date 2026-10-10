package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func newScheduler() *Scheduler { return New(slog.New(slog.DiscardHandler)) }

func TestAfterCoalescesPendingKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		var first, second int
		s.After("observe:n1", time.Second, func(context.Context) { first++ })
		s.After("observe:n1", 0, func(context.Context) { second++ })
		time.Sleep(time.Second)
		synctest.Wait()
		if first != 1 || second != 0 {
			t.Fatalf("first ran %d, second %d times, want 1 and 0", first, second)
		}

		s.After("observe:n1", 0, func(context.Context) { second++ })
		synctest.Wait()
		if second != 1 {
			t.Fatalf("rescheduled job ran %d times", second)
		}
	})
}

func TestAfterKeyFreedWhenRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		release := make(chan struct{})
		defer close(release)
		var second int
		s.After("k", 0, func(context.Context) { <-release })
		synctest.Wait()
		s.After("k", 0, func(context.Context) { second++ })
		synctest.Wait()
		if second != 1 {
			t.Fatalf("second job ran %d times while the first runs", second)
		}
	})
}

func TestAfterDistinctAndEmptyKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		var n atomic.Int32
		inc := func(context.Context) { n.Add(1) }
		for _, key := range []string{"a", "b", "", ""} {
			s.After(key, time.Second, inc)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if n.Load() != 4 {
			t.Fatalf("%d jobs ran, want 4", n.Load())
		}
	})
}

func TestEveryDifferentNamesRunConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		defer s.Stop(context.Background())
		block := make(chan struct{})
		defer close(block)
		var fast int
		s.Every("slow", time.Second, func(context.Context) { <-block })
		s.Every("fast", time.Second, func(context.Context) { fast++ })
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if fast != 3 {
			t.Fatalf("fast job ran %d times while the slow one blocks, want 3", fast)
		}
	})
}

func TestPanicRecovered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		s := New(slog.New(slog.NewTextHandler(&logs, nil)))
		var every, after int
		s.After("boom", 0, func(context.Context) { panic("kaboom") })
		s.Every("flaky", time.Second, func(context.Context) {
			if every++; every == 1 {
				panic("first tick")
			}
		})
		time.Sleep(3 * time.Second)
		s.After("next", 0, func(context.Context) { after++ })
		synctest.Wait()
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if every != 3 || after != 1 {
			t.Fatalf("Every ran %d times, the next job %d, want 3 and 1", every, after)
		}
		for _, want := range []string{"job panicked", "job=boom", "kaboom", "job=flaky", "first tick", "stack="} {
			if !strings.Contains(logs.String(), want) {
				t.Fatalf("log lacks %q:\n%s", want, logs.String())
			}
		}
	})
}

func TestStopCancelsPendingAndWaitsForRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		var ran atomic.Int32
		inc := func(context.Context) { ran.Add(1) }
		s.After("later", time.Second, inc)
		s.After("", time.Second, inc)
		var finished, sawCancel bool
		s.After("running", 0, func(ctx context.Context) {
			time.Sleep(500 * time.Millisecond)
			sawCancel = ctx.Err() != nil
			finished = true
		})
		synctest.Wait()

		if err := s.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if !finished || sawCancel {
			t.Fatalf("running job finished %v, saw its context cancelled %v; want it to finish uncancelled", finished, sawCancel)
		}

		s.After("late", 0, inc)
		s.Every("late-every", time.Millisecond, inc)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if n := ran.Load(); n != 0 {
			t.Fatalf("%d jobs ran after Stop", n)
		}
		if err := s.Stop(context.Background()); err != nil {
			t.Fatalf("second Stop: %v", err)
		}
	})
}

func TestStopDeadlineCancelsJobContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		var cancelled bool
		s.After("stuck", 0, func(ctx context.Context) {
			<-ctx.Done()
			cancelled = true
		})
		synctest.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop = %v, want deadline exceeded", err)
		}
		synctest.Wait()
		if !cancelled {
			t.Fatal("job context not cancelled after Stop's deadline")
		}
	})
}

func TestWaitAfterStopDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		var recorded atomic.Bool
		s.After("apply", 0, func(ctx context.Context) {
			<-ctx.Done()
			time.Sleep(time.Second)
			recorded.Store(true)
		})
		synctest.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop = %v, want deadline exceeded", err)
		}
		if recorded.Load() {
			t.Fatal("Stop waited past its deadline")
		}
		if err := s.Wait(context.Background()); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if !recorded.Load() {
			t.Fatal("Wait returned before the cancelled job finished")
		}
	})
}

func TestWaitDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		release := make(chan struct{})
		defer close(release)
		s.After("stuck", 0, func(context.Context) { <-release })
		synctest.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Wait = %v, want deadline exceeded", err)
		}
	})
}

func TestStopStartsNoRunAfterTheCurrentOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScheduler()
		var started, finished atomic.Int32
		for range 20 {
			s.Every("tick", time.Second, func(context.Context) {
				started.Add(1)
				time.Sleep(5 * time.Second)
				finished.Add(1)
			})
		}
		time.Sleep(2 * time.Second)
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if started.Load() != 20 || finished.Load() != 20 {
			t.Fatalf("%d runs started and %d finished by Stop, want 20 and 20", started.Load(), finished.Load())
		}
	})
}
