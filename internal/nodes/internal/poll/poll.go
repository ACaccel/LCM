// Package poll holds the bookkeeping shared by nodes that wait by
// suspending and re-running themselves (wait.until, ci.status waitFor,
// lock waiting): the deadline and attempt count travel in the suspension
// output, which the engine stores durably and hands back on resume.
package poll

import (
	"time"

	"github.com/ACaccel/lcm/internal/node"
)

// State is carried across suspensions.
type State struct {
	Started  time.Time
	Deadline time.Time
	Polls    int
}

// Begin returns the state of a first run that may last up to timeout.
func Begin(now time.Time, timeout time.Duration) State {
	return State{Started: now, Deadline: now.Add(timeout), Polls: 0}
}

// Load restores the state from a resumed node. ok is false on a first run
// or when the stored output is not in the expected shape.
func Load(r *node.Resumed) (State, bool) {
	if r == nil {
		return State{}, false
	}
	m, ok := r.Output.(map[string]any)
	if !ok {
		return State{}, false
	}
	started, ok1 := ms(m["startedMs"])
	deadline, ok2 := ms(m["deadlineMs"])
	polls, _ := ms(m["polls"])
	if !ok1 || !ok2 {
		return State{}, false
	}
	return State{Started: time.UnixMilli(started), Deadline: time.UnixMilli(deadline), Polls: int(polls)}, true
}

// Output encodes the state for node.Suspend.Output; extra entries are
// merged in and may describe progress for the run trace.
func (s State) Output(extra map[string]any) map[string]any {
	out := map[string]any{
		"startedMs": s.Started.UnixMilli(), "deadlineMs": s.Deadline.UnixMilli(), "polls": int64(s.Polls),
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// Expired reports whether now is at or past the deadline.
func (s State) Expired(now time.Time) bool { return !now.Before(s.Deadline) }

// NextWake returns when to check again: after interval, but not later than
// the deadline, so the timeout is noticed on time.
func (s State) NextWake(now time.Time, interval time.Duration) time.Time {
	at := now.Add(interval)
	if at.After(s.Deadline) {
		return s.Deadline
	}
	return at
}

func ms(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	}
	return 0, false
}
