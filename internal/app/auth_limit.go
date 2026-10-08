package app

import (
	"sync"
	"time"
)

// Sign-in limiter: at most SignInAttempts tries per (client IP, email) in SignInWindow; a
// successful sign-in clears the count. In memory, so a restart forgets it (Better Auth's limiter
// was in-memory too, auth-orgs.md §4.6). Each try costs a password hash, which is the resource
// this protects as much as the accounts.
const (
	SignInAttempts = 10
	SignInWindow   = 5 * time.Minute
)

var authLimiterMu sync.Mutex

func (a *App) signInAttempts() *authAttempts {
	authLimiterMu.Lock()
	defer authLimiterMu.Unlock()
	if a.signIns == nil {
		a.signIns = newAuthAttempts(SignInAttempts, SignInWindow.Milliseconds())
	}
	return a.signIns
}

// authAttempts is a fixed-window counter per key.
type authAttempts struct {
	mu     sync.Mutex
	limit  int
	window int64 // ms
	hits   map[string]*authWindow
	swept  int64
}

type authWindow struct {
	start int64
	n     int
}

func newAuthAttempts(limit int, window int64) *authAttempts {
	return &authAttempts{limit: limit, window: window, hits: map[string]*authWindow{}}
}

// take counts one attempt for key. It returns 0 when the attempt may go ahead, else how many
// milliseconds until the window frees up.
func (l *authAttempts) take(key string, now int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now-l.swept >= l.window {
		for k, w := range l.hits {
			if now-w.start >= l.window {
				delete(l.hits, k)
			}
		}
		l.swept = now
	}
	w := l.hits[key]
	if w == nil || now-w.start >= l.window {
		l.hits[key] = &authWindow{start: now, n: 1}
		return 0
	}
	if w.n >= l.limit {
		return w.start + l.window - now
	}
	w.n++
	return 0
}

// reset forgets key's attempts (after a successful sign-in).
func (l *authAttempts) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
