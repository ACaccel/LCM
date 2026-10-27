// Package guard provides policy guard nodes. Guards let an event pass or
// block it; they never perform actions themselves.
package guard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must resolve on hosts without a zoneinfo database

	"github.com/ACaccel/lcm/internal/node"
)

// CategoryGuard is the category of the nodes in this package.
const CategoryGuard = "guard"

// PortBlocked is the port guard.window takes outside its windows.
const PortBlocked = "blocked"

// WindowSpec is one recurring time window.
type WindowSpec struct {
	Days  []string `json:"days" desc:"mon, tue, wed, thu, fri, sat, sun; the window starts on these days (default: every day)"`
	Start string   `json:"start" lcm:"required" desc:"HH:MM, inclusive"`
	End   string   `json:"end" lcm:"required" desc:"HH:MM, exclusive; 24:00 means midnight; an end before the start runs into the next day"`
}

// WindowProps configures guard.window.
type WindowProps struct {
	Windows  []WindowSpec  `json:"windows" lcm:"required" desc:"the event passes inside any of these windows"`
	Timezone string        `json:"timezone" default:"UTC" desc:"IANA zone such as Asia/Taipei"`
	Wait     bool          `json:"wait" desc:"outside the windows, wait for the next one to open instead of blocking"`
	MaxWait  node.Duration `json:"maxWait" desc:"with wait: block instead when the next window is further away than this (0: no limit)"`
}

type window struct {
	days       [7]bool
	startMin   int
	endMin     int // 1..1440
	overnight  bool
	startClock [2]int
	endClock   [2]int
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func parseClock(s string) (h, m int, err error) {
	hs, ms, ok := strings.Cut(s, ":")
	if !ok || len(hs) != 2 || len(ms) != 2 {
		return 0, 0, fmt.Errorf("%q: want HH:MM", s)
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || m < 0 || m > 59 || h < 0 || h > 24 || (h == 24 && m != 0) {
		return 0, 0, fmt.Errorf("%q: want HH:MM between 00:00 and 24:00", s)
	}
	return h, m, nil
}

func compile(specs []WindowSpec) ([]window, error) {
	out := make([]window, 0, len(specs))
	for i, s := range specs {
		var w window
		sh, sm, err := parseClock(s.Start)
		if err != nil {
			return nil, fmt.Errorf("windows[%d].start: %w", i, err)
		}
		if sh == 24 {
			return nil, fmt.Errorf("windows[%d].start: 24:00 is only valid as an end", i)
		}
		eh, em, err := parseClock(s.End)
		if err != nil {
			return nil, fmt.Errorf("windows[%d].end: %w", i, err)
		}
		w.startClock, w.endClock = [2]int{sh, sm}, [2]int{eh, em}
		w.startMin, w.endMin = sh*60+sm, eh*60+em
		if w.startMin == w.endMin {
			return nil, fmt.Errorf("windows[%d]: start and end are equal; use 00:00 to 24:00 for a whole day", i)
		}
		w.overnight = w.endMin < w.startMin
		if len(s.Days) == 0 {
			for d := range w.days {
				w.days[d] = true
			}
		}
		for _, name := range s.Days {
			d, ok := dayNames[strings.ToLower(name)]
			if !ok {
				return nil, fmt.Errorf("windows[%d].days: unknown day %q", i, name)
			}
			w.days[d] = true
		}
		out = append(out, w)
	}
	return out, nil
}

// bounds returns the instants at which the window that starts on the date
// of day (in loc) opens and closes.
func (w window) bounds(day time.Time, loc *time.Location) (open, closeAt time.Time) {
	y, mo, d := day.Date()
	open = time.Date(y, mo, d, w.startClock[0], w.startClock[1], 0, 0, loc)
	endDay := d
	if w.overnight || w.endMin == 1440 {
		endDay++
	}
	eh := w.endClock[0]
	if eh == 24 {
		eh = 0
	}
	closeAt = time.Date(y, mo, endDay, eh, w.endClock[1], 0, 0, loc)
	return open, closeAt
}

// state reports whether t falls inside a window and, if so, when it closes;
// otherwise it returns when the next window opens.
func state(ws []window, loc *time.Location, t time.Time) (index int, open bool, closesAt, nextOpen time.Time) {
	local := t.In(loc)
	// A window that started yesterday may still be running, so look one day
	// back; DST can move a boundary but never by a day.
	for back := -1; back <= 0; back++ {
		day := local.AddDate(0, 0, back)
		for i, w := range ws {
			if !w.days[day.Weekday()] {
				continue
			}
			o, c := w.bounds(day, loc)
			if !t.Before(o) && t.Before(c) {
				return i, true, c, time.Time{}
			}
		}
	}
	// No window is open: find the earliest opening after t within a week.
	for fwd := 0; fwd <= 8; fwd++ {
		day := local.AddDate(0, 0, fwd)
		var best time.Time
		for _, w := range ws {
			if !w.days[day.Weekday()] {
				continue
			}
			o, _ := w.bounds(day, loc)
			if o.After(t) && (best.IsZero() || o.Before(best)) {
				best = o
			}
		}
		if !best.IsZero() {
			return -1, false, time.Time{}, best
		}
	}
	return -1, false, time.Time{}, time.Time{}
}

func validateWindow(p WindowProps) error {
	if len(p.Windows) == 0 {
		return errors.New("windows: at least one window is required")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return fmt.Errorf("timezone: unknown zone %q", p.Timezone)
	}
	if p.MaxWait < 0 {
		return errors.New("maxWait: must not be negative")
	}
	ws, err := compile(p.Windows)
	if err != nil {
		return err
	}
	// A window whose days list nothing could never open.
	for i, w := range ws {
		if !slices.Contains(w.days[:], true) {
			return fmt.Errorf("windows[%d].days: no day selected", i)
		}
	}
	return nil
}

// Register adds the guard node types to r.
func Register(r *node.Registry) {
	node.Register(r, node.Spec[WindowProps]{
		Type: "guard.window", Category: CategoryGuard,
		Description: "Passes only inside recurring time windows (weekdays, HH:MM ranges, time zone). Outside them it takes the blocked port, or waits for the next window with wait. Dry runs never wait.",
		Ports:       []string{node.PortOut, PortBlocked},
		Validate:    validateWindow,
		Run:         runWindow,
	})
}

func runWindow(_ context.Context, rc *node.RunContext, p WindowProps) (node.Result, error) {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return node.Result{}, node.Permanent(err)
	}
	ws, err := compile(p.Windows)
	if err != nil {
		return node.Result{}, node.Permanent(err)
	}
	now := rc.Now()
	idx, open, closesAt, nextOpen := state(ws, loc, now)
	if open {
		return node.Result{Output: map[string]any{
			"allowed": true, "window": int64(idx), "closesAt": closesAt.In(loc).Format(time.RFC3339),
		}}, nil
	}

	blocked := map[string]any{"allowed": false}
	if !nextOpen.IsZero() {
		blocked["nextOpen"] = nextOpen.In(loc).Format(time.RFC3339)
	}
	switch {
	case !p.Wait || nextOpen.IsZero():
		return node.Result{Port: PortBlocked, Output: blocked}, nil
	case rc.Mode == node.ModeDry:
		blocked["dry"] = true
		return node.Result{Output: blocked}, nil
	case p.MaxWait > 0 && nextOpen.Sub(now) > p.MaxWait.D():
		return node.Result{Port: PortBlocked, Output: blocked}, nil
	}
	at := nextOpen.UTC()
	return node.Result{Suspend: &node.Suspend{ResumeAt: &at, Output: map[string]any{"opensAt": at.Format(time.RFC3339)}}}, nil
}
