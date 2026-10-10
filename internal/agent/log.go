package agent

import (
	"bytes"
	"context"
	"encoding/json"
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

func (l *Logger) Log(scope, msg string, extra ...any) {
	var b strings.Builder
	b.WriteString(isoMillis(l.now()))
	b.WriteString(" [")
	b.WriteString(scope)
	b.WriteString("] ")
	b.WriteString(msg)
	if len(extra) > 0 {
		b.WriteByte(' ')
		b.Write(jsonObject(extra...))
	}
	b.WriteByte('\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, b.String())
}

func isoMillis(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func jsonObject(kv ...any) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := kv[i].(string)
		b.Write(marshal(key))
		b.WriteByte(':')
		b.Write(marshal(kv[i+1]))
	}
	b.WriteByte('}')
	return b.Bytes()
}

func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null")
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func errorText(err error) string {
	return truncate(strings.Join(strings.Fields(err.Error()), " "), 300)
}

func truncate(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
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
