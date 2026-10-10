package app

import (
	"maps"
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

const (
	SignInAttempts = 10
	SignInWindow   = 5 * time.Minute
)

const (
	AuthPerIP        = 20
	DeviceStartPerIP = 10
	AuthPerIPWindow  = time.Minute
	limiterMaxKeys   = 10_000
)

type authLimiters struct {
	signIn      *authAttempts
	perIP       *authAttempts
	deviceStart *authAttempts
	devicePoll  *authAttempts
}

func (a *App) limits() *authLimiters { return a.authLimits }

func (a *App) limited(l *authAttempts, key string) error {
	if wait := l.take(key, a.Now()); wait > 0 {
		return &domain.RateLimitError{RetryAfterSeconds: (wait + 999) / 1000}
	}
	return nil
}

type authAttempts struct {
	mu     sync.Mutex
	limit  int
	window int64
	hits   map[string]*authWindow
	swept  int64
}

type authWindow struct {
	start int64
	n     int
}

func newAuthAttempts(limit int, window time.Duration) *authAttempts {
	return &authAttempts{limit: limit, window: window.Milliseconds(), hits: map[string]*authWindow{}}
}

func (l *authAttempts) take(key string, now int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now-l.swept >= l.window {
		l.sweep(now)
		l.swept = now
	}
	w := l.hits[key]
	if w == nil || now-w.start >= l.window {
		if w == nil && len(l.hits) >= limiterMaxKeys {
			l.sweep(now)
			for k := range l.hits {
				if len(l.hits) < limiterMaxKeys {
					break
				}
				delete(l.hits, k)
			}
		}
		l.hits[key] = &authWindow{start: now, n: 1}
		return 0
	}
	if w.n >= l.limit {
		return w.start + l.window - now
	}
	w.n++
	return 0
}

func (l *authAttempts) sweep(now int64) {
	maps.DeleteFunc(l.hits, func(_ string, w *authWindow) bool { return now-w.start >= l.window })
}

func (l *authAttempts) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
