package app

import (
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// Sign-in limiter: at most SignInAttempts tries per (client IP, email) in SignInWindow; a
// successful sign-in clears the count. In memory, so a restart forgets it (Better Auth's limiter
// was in-memory too, auth-orgs.md §4.6). Each try costs a password hash, which is the resource
// this protects as much as the accounts.
const (
	SignInAttempts = 10
	SignInWindow   = 5 * time.Minute
)

// Per-IP aggregate limits, whatever the email (auth-orgs.md §4.6; Better Auth capped /sign-in*
// and /sign-up* at 3 per 10 s per IP): spraying one password across accounts, or burning CPU on
// password hashes, is throttled per client address. One CLI polls a device code every 5 s.
const (
	AuthPerIP        = 20 // sign-in + sign-up per IP per AuthPerIPWindow
	DeviceStartPerIP = 10 // device codes per IP per AuthPerIPWindow
	DevicePollPerIP  = 60 // device-token polls per IP per AuthPerIPWindow
	AuthPerIPWindow  = time.Minute
	// limiterMaxKeys bounds each limiter's memory: past it, expired windows are swept and then
	// an arbitrary key is forgotten.
	limiterMaxKeys = 10_000
)

// authLimiters are the app's in-memory auth limiters, made on first use.
type authLimiters struct {
	signIn      *authAttempts // per (IP, email)
	perIP       *authAttempts // sign-in + sign-up per IP
	deviceStart *authAttempts
	devicePoll  *authAttempts
}

var authLimiterMu sync.Mutex

func (a *App) limits() *authLimiters {
	authLimiterMu.Lock()
	defer authLimiterMu.Unlock()
	if a.authLimits == nil {
		w := AuthPerIPWindow.Milliseconds()
		a.authLimits = &authLimiters{
			signIn:      newAuthAttempts(SignInAttempts, SignInWindow.Milliseconds()),
			perIP:       newAuthAttempts(AuthPerIP, w),
			deviceStart: newAuthAttempts(DeviceStartPerIP, w),
			devicePoll:  newAuthAttempts(DevicePollPerIP, w),
		}
	}
	return a.authLimits
}

// limited counts one call against l for key; a refusal is RATE_LIMITED with Retry-After.
func (a *App) limited(l *authAttempts, key string) error {
	if wait := l.take(key, a.Now()); wait > 0 {
		return &domain.RateLimitError{RetryAfterSeconds: (wait + 999) / 1000}
	}
	return nil
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
	for k, w := range l.hits {
		if now-w.start >= l.window {
			delete(l.hits, k)
		}
	}
}

// reset forgets key's attempts (after a successful sign-in).
func (l *authAttempts) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
