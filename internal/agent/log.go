package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

func NewLogger(w io.Writer) *Logger { return &Logger{w: w, now: time.Now} }

func (l *Logger) Log(scope, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s [%s] %s\n", l.now().UTC().Format("2006-01-02T15:04:05.000Z"), scope, msg)
}

func (l *Logger) Logf(scope, format string, args ...any) {
	l.Log(scope, fmt.Sprintf(format, args...))
}

func errorText(err error) string {
	return truncate(strings.Join(strings.Fields(err.Error()), " "), 300)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
