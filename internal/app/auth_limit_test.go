package app

import (
	"testing"
	"time"
)

func TestAuthAttemptsWindow(t *testing.T) {
	l := newAuthAttempts(3, time.Second)
	steps := []struct {
		key  string
		at   int64
		wait int64
	}{
		{"a", 0, 0},
		{"a", 100, 0},
		{"a", 200, 0},
		{"a", 300, 700},
		{"b", 300, 0},
		{"a", 999, 1},
		{"a", 1000, 0},
		{"a", 1001, 0},
		{"a", 1002, 0},
		{"a", 1003, 997},
	}
	for i, s := range steps {
		if got := l.take(s.key, s.at); got != s.wait {
			t.Fatalf("step %d (%s at %d): wait %d, want %d", i, s.key, s.at, got, s.wait)
		}
	}
	l.reset("a")
	if got := l.take("a", 1004); got != 0 {
		t.Fatalf("after reset: wait %d", got)
	}
	l.take("c", 1500)
	l.take("d", 5000)
	if _, ok := l.hits["c"]; ok {
		t.Fatal("stale key kept")
	}
}
