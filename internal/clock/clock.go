// Package clock abstracts time so that code with timers, timeouts and
// backoff can be tested deterministically with a fake clock.
package clock

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Clock is a source of time and timers.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine (real clock) or in the goroutine
	// that advances time (fake clock) once d has elapsed.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending AfterFunc call.
type Timer interface {
	// Stop prevents the call if it has not run yet and reports whether it
	// did so.
	Stop() bool
}

// Real returns the system clock.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// WithTimeoutCause returns a context that is cancelled with cause once d has
// elapsed on c. With the real clock this is context.WithTimeoutCause, so the
// context also carries a deadline that libraries such as net/http honour.
func WithTimeoutCause(parent context.Context, c Clock, d time.Duration, cause error) (context.Context, context.CancelFunc) {
	if _, ok := c.(realClock); ok {
		return context.WithTimeoutCause(parent, d, cause)
	}
	ctx, cancel := context.WithCancelCause(parent)
	t := c.AfterFunc(d, func() { cancel(cause) })
	return ctx, func() {
		t.Stop()
		cancel(context.Canceled)
	}
}

// Fake is a manually advanced clock for tests. Timers fire synchronously,
// in deadline order, inside Advance.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	seq     uint64
	timers  []*fakeTimer
	changed chan struct{}
}

// NewFake returns a fake clock set to start.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start, changed: make(chan struct{})}
}

type fakeTimer struct {
	f    *Fake
	at   time.Time
	seq  uint64
	fn   func()
	done bool
}

// Now returns the current fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// AfterFunc schedules fn to run when the clock has been advanced by d. A
// non-positive d fires on the next Advance call, including Advance(0).
func (f *Fake) AfterFunc(d time.Duration, fn func()) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	t := &fakeTimer{f: f, at: f.now.Add(d), seq: f.seq, fn: fn}
	f.timers = append(f.timers, t)
	f.notifyLocked()
	return t
}

// Stop implements Timer.
func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	if t.done {
		return false
	}
	t.done = true
	t.f.removeLocked(t)
	t.f.notifyLocked()
	return true
}

// Advance moves the clock forward by d and runs every timer that becomes
// due, earliest first. Timers created by those callbacks also fire if they
// fall within the new time.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	f.mu.Unlock()
	for {
		f.mu.Lock()
		next := f.nextDueLocked(target)
		if next == nil {
			f.now = target
			f.notifyLocked()
			f.mu.Unlock()
			return
		}
		if next.at.After(f.now) {
			f.now = next.at
		}
		next.done = true
		f.removeLocked(next)
		f.notifyLocked()
		f.mu.Unlock()
		next.fn()
	}
}

// Pending returns the number of timers that have not fired or been stopped.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// WaitForTimers blocks until at least n timers are pending or ctx is done.
// Tests use it to wait until code under test has armed its timers before
// calling Advance.
func (f *Fake) WaitForTimers(ctx context.Context, n int) error {
	for {
		f.mu.Lock()
		if len(f.timers) >= n {
			f.mu.Unlock()
			return nil
		}
		ch := f.changed
		f.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *Fake) nextDueLocked(target time.Time) *fakeTimer {
	sort.Slice(f.timers, func(i, j int) bool {
		if f.timers[i].at.Equal(f.timers[j].at) {
			return f.timers[i].seq < f.timers[j].seq
		}
		return f.timers[i].at.Before(f.timers[j].at)
	})
	if len(f.timers) == 0 || f.timers[0].at.After(target) {
		return nil
	}
	return f.timers[0]
}

func (f *Fake) removeLocked(t *fakeTimer) {
	for i, x := range f.timers {
		if x == t {
			f.timers = append(f.timers[:i], f.timers[i+1:]...)
			return
		}
	}
}

func (f *Fake) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}
