package trigger

import (
	"context"
	"fmt"
	"time"
)

const (
	// onTime is how late a fire may be and still count as on time rather
	// than missed: cron has minute resolution and timers can be late.
	onTime = time.Minute
	// maxLookback bounds how far back missed fires are searched, so that a
	// minutely schedule after a very long outage does not enumerate years.
	maxLookback = 366 * 24 * time.Hour
	// noFireRetry is how long a watcher with no future fire time waits
	// before checking again (the calendar file may have been extended).
	noFireRetry = 6 * time.Hour
)

type cronWatcher struct {
	baseWatcher
	props CronProps
	loc   *time.Location
	sched *Schedule
	cal   *calendarFile
}

func newCronWatcher(b baseWatcher, p CronProps) (*cronWatcher, error) {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", p.Timezone, err)
	}
	sched, err := ParseSchedule(p.Schedule, loc)
	if err != nil {
		return nil, err
	}
	w := &cronWatcher{baseWatcher: b, props: p, loc: loc, sched: sched}
	if p.Calendar != "" {
		path, err := resolveCalendarPath(b.m.cfg.CalendarDir, p.Calendar)
		if err != nil {
			return nil, err
		}
		w.cal = &calendarFile{path: path}
		// A calendar that cannot be read at start would silently change which
		// days fire, so the trigger refuses to start and the manager retries.
		if _, err := w.cal.get(); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// allows reports whether the calendar permits firing on t's local date.
func (w *cronWatcher) allows(t time.Time) bool {
	if w.cal == nil {
		return true
	}
	cal, err := w.cal.get()
	if err != nil {
		// Keep the last good calendar; surface the problem in the log.
		w.log.Warn("calendar reload failed; using the previous contents", "error", err)
	}
	if cal == nil {
		return false
	}
	listed := cal.contains(t.In(w.loc))
	if w.props.CalendarMode == "only" {
		return listed
	}
	return !listed
}

// nextFire returns the first fire time after t that the calendar permits.
func (w *cronWatcher) nextFire(t time.Time) time.Time {
	// Days the calendar rules out are skipped whole, so a sparse "only"
	// calendar does not cost one iteration per schedule match.
	for range 3000 {
		n := w.sched.Next(t)
		if n.IsZero() {
			return time.Time{}
		}
		if w.allows(n) {
			return n
		}
		ln := n.In(w.loc)
		t = time.Date(ln.Year(), ln.Month(), ln.Day()+1, 0, 0, 0, 0, w.loc).Add(-time.Minute)
	}
	return time.Time{}
}

func (w *cronWatcher) run(ctx context.Context) {
	last, ok := w.lastFire(ctx)
	if !ok {
		return
	}
	for {
		now := w.m.clock.Now()
		w.process(ctx, last, now)
		last = now
		if err := w.saveState(ctx, map[string]any{"lastFire": now.UnixMilli()}); err != nil && ctx.Err() == nil {
			w.log.Warn("cannot save cron trigger state", "error", err)
		}
		next := w.nextFire(now)
		wait := noFireRetry
		if !next.IsZero() {
			wait = next.Sub(now)
		}
		if !w.m.sleep(ctx, wait) {
			return
		}
	}
}

// lastFire returns the time up to which fires are already handled. A
// trigger seen for the first time starts "now": there is nothing to catch up.
func (w *cronWatcher) lastFire(ctx context.Context) (time.Time, bool) {
	now := w.m.clock.Now()
	st, err := w.loadState(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("cannot read cron trigger state", "error", err)
		}
		return time.Time{}, false
	}
	ms, found := numberAt(st, "lastFire")
	if !found {
		return now, true
	}
	last := time.UnixMilli(ms)
	if last.After(now) {
		// The clock moved backwards; do not wait for fires already made.
		return now, true
	}
	if now.Sub(last) > maxLookback {
		last = now.Add(-maxLookback)
	}
	return last, true
}

// process fires what is due in (last, now] according to the catch-up mode.
func (w *cronWatcher) process(ctx context.Context, last, now time.Time) {
	var due []time.Time
	dropped := 0
	for t := last; ; {
		n := w.nextFire(t)
		if n.IsZero() || n.After(now) {
			break
		}
		t = n
		// Only the oldest MaxCatchUp are kept for "all"; the latest alone
		// matters for the other modes.
		switch {
		case w.props.CatchUp == "all" && len(due) < MaxCatchUp:
			due = append(due, n)
		case w.props.CatchUp == "all":
			dropped++
		default:
			if len(due) > 0 {
				dropped++
			}
			due = []time.Time{n}
		}
	}
	if len(due) == 0 {
		return
	}
	if dropped > 0 {
		w.log.Warn("missed cron fires not started", "count", dropped, "catchUp", w.props.CatchUp)
	}
	for _, t := range due {
		missed := now.Sub(t) > onTime
		if missed && w.props.CatchUp == "skip" {
			w.log.Info("missed cron fire skipped", "scheduled", t.In(w.loc).Format(time.RFC3339))
			continue
		}
		w.fire(ctx, map[string]any{
			"scheduled": t.In(w.loc).Format(time.RFC3339),
			"fired":     now.UTC().Format(time.RFC3339),
			"catchUp":   missed,
		})
	}
}
