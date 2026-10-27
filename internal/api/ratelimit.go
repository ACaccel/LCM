package api

import (
	"sync"
	"time"
)

// failureLimiter counts failed logins per client IP in a fixed window. It is
// deliberately simple: in-memory, reset on restart.
type failureLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	max      int
	window   time.Duration
	entries  map[string]*failureEntry
	lastScan time.Time
}

type failureEntry struct {
	count int
	start time.Time
}

func newFailureLimiter(now func() time.Time, max int, window time.Duration) *failureLimiter {
	return &failureLimiter{now: now, max: max, window: window, entries: map[string]*failureEntry{}}
}

// blocked reports whether key has used up its failures, and how long until
// the window resets.
func (l *failureLimiter) blocked(key string) (retryAfter time.Duration, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return 0, false
	}
	now := l.now()
	if now.Sub(e.start) >= l.window {
		delete(l.entries, key)
		return 0, false
	}
	if e.count >= l.max {
		return e.start.Add(l.window).Sub(now), true
	}
	return 0, false
}

// fail records a failed attempt.
func (l *failureLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	e, ok := l.entries[key]
	if !ok || now.Sub(e.start) >= l.window {
		l.entries[key] = &failureEntry{count: 1, start: now}
		return
	}
	e.count++
}

// reset forgets key after a successful login.
func (l *failureLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep drops expired entries at most once per window so memory stays
// bounded by the number of distinct failing clients in one window.
func (l *failureLimiter) sweep(now time.Time) {
	if now.Sub(l.lastScan) < l.window {
		return
	}
	l.lastScan = now
	for k, e := range l.entries {
		if now.Sub(e.start) >= l.window {
			delete(l.entries, k)
		}
	}
}
