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

// Logger writes the agent's operational lines to stderr in the Bun worker's format, which is
// what operators grep in `docker service logs` (docs/go/spec/swarm-worker.md, Addendum A1):
//
//	<ISO-8601 UTC with milliseconds and Z> [<scope>] <msg>[ <JSON object>]
//
// The JSON suffix is only written when extra key/value pairs are given.
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// NewLogger writes to w (os.Stderr in production).
func NewLogger(w io.Writer) *Logger { return &Logger{w: w, now: time.Now} }

// Log writes one line. extra is alternating keys (strings) and values, kept in order, like the
// object literal the TypeScript passed to JSON.stringify.
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

// isoMillis is JavaScript's Date.prototype.toISOString: UTC, milliseconds, "Z".
func isoMillis(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// jsonObject encodes alternating key/value pairs as one JSON object, keys in the given order.
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

// marshal is json.Marshal without HTML escaping (JSON.stringify does not escape <, >, &).
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null")
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// errorText is the worker's errorText: the message with whitespace runs collapsed to one space,
// trimmed, at most 300 characters.
func errorText(err error) string {
	return truncate(strings.Join(strings.Fields(err.Error()), " "), 300)
}

// truncate keeps the first n characters (runes) of s, like String.prototype.slice(0, n) on text.
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

// sleepCtx waits d, or less when ctx is done (then it returns ctx's error).
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
