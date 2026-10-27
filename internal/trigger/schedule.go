package trigger

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must resolve on hosts without a zoneinfo database
)

// Schedule is a parsed five-field cron expression (minute hour day-of-month
// month day-of-week) evaluated in a time zone.
//
// Daylight saving time follows the usual cron conventions: a job with a
// fixed hour whose time does not exist on the day clocks jump forward runs
// once right after the jump, and one whose time occurs twice when clocks
// fall back runs only the first time. Jobs with a wildcard hour (such as
// "*/10 * * * *" or "0 * * * *") run by elapsed time and are not adjusted.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	// domAny and dowAny record a field that starts with "*": when exactly
	// one of the day fields is restricted it alone decides, and when both
	// are the day matches if either does.
	domAny, dowAny bool
	hourAny        bool
	loc            *time.Location
}

// searchYears bounds Next; every valid expression fires within it (the
// longest gap is February 29th across a skipped leap year, at most 8 years).
const searchYears = 9

type fieldSpec struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

	fields = [5]fieldSpec{
		{"minute", 0, 59, nil},
		{"hour", 0, 23, nil},
		{"day-of-month", 1, 31, nil},
		{"month", 1, 12, monthNames},
		{"day-of-week", 0, 7, dayNames},
	}

	macros = map[string]string{
		"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
		"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
	}
)

// ParseSchedule parses spec for evaluation in loc. A nil loc means UTC.
func ParseSchedule(spec string, loc *time.Location) (*Schedule, error) {
	if loc == nil {
		loc = time.UTC
	}
	spec = strings.TrimSpace(spec)
	if m, ok := macros[strings.ToLower(spec)]; ok {
		spec = m
	}
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("schedule %q: want 5 fields (minute hour day-of-month month day-of-week), got %d", spec, len(parts))
	}
	var bits [5]uint64
	for i, p := range parts {
		b, err := parseField(p, fields[i])
		if err != nil {
			return nil, fmt.Errorf("schedule %q: %w", spec, err)
		}
		bits[i] = b
	}
	// Sunday may be written 0 or 7.
	if bits[4]&(1<<7) != 0 {
		bits[4] = bits[4]&^(1<<7) | 1
	}
	s := &Schedule{
		minute: bits[0], hour: bits[1], dom: bits[2], month: bits[3], dow: bits[4],
		domAny: isWild(parts[2]), dowAny: isWild(parts[4]), hourAny: isWild(parts[1]), loc: loc,
	}
	if err := s.checkReachable(); err != nil {
		return nil, fmt.Errorf("schedule %q: %w", spec, err)
	}
	return s, nil
}

func isWild(f string) bool { return strings.HasPrefix(f, "*") || strings.HasPrefix(f, "?") }

func parseField(field string, fs fieldSpec) (uint64, error) {
	var bits uint64
	for part := range strings.SplitSeq(field, ",") {
		b, err := parseRange(part, fs)
		if err != nil {
			return 0, err
		}
		bits |= b
	}
	return bits, nil
}

func parseRange(part string, fs fieldSpec) (uint64, error) {
	rng, stepStr, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%s: invalid step %q", fs.name, stepStr)
		}
		step = n
	}
	lo, hi := fs.min, fs.max
	switch {
	case rng == "*" || rng == "?":
		if fs.name == "day-of-week" {
			hi = 6 // 7 is an alias of 0; keep "*" from counting Sunday twice
		}
	case strings.Contains(rng, "-"):
		a, b, _ := strings.Cut(rng, "-")
		var err error
		if lo, err = parseValue(a, fs); err != nil {
			return 0, err
		}
		if hi, err = parseValue(b, fs); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("%s: range %q runs backwards", fs.name, rng)
		}
	default:
		v, err := parseValue(rng, fs)
		if err != nil {
			return 0, err
		}
		lo, hi = v, v
		if hasStep {
			hi = fs.max // "5/15" means 5-max/15
		}
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << v
	}
	return bits, nil
}

func parseValue(s string, fs fieldSpec) (int, error) {
	if v, ok := fs.names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid value %q", fs.name, s)
	}
	if v < fs.min || v > fs.max {
		return 0, fmt.Errorf("%s: %d is outside %d-%d", fs.name, v, fs.min, fs.max)
	}
	return v, nil
}

// checkReachable rejects expressions that can never fire, such as
// "0 0 31 2 *".
func (s *Schedule) checkReachable() error {
	if s.dowAny {
		// Day-of-month alone decides the day; with a restricted day-of-week
		// the OR rule makes every expression reachable.
		daysIn := [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
		for m := 1; m <= 12; m++ {
			if s.month&(1<<m) == 0 {
				continue
			}
			for d := 1; d <= daysIn[m]; d++ {
				if s.dom&(1<<d) != 0 {
					return nil
				}
			}
		}
		return errors.New("no month has a matching day (for example day 31 in February)")
	}
	return nil
}

func (s *Schedule) dayMatches(t time.Time) bool {
	domOK := s.dom&(1<<t.Day()) != 0
	dowOK := s.dow&(1<<int(t.Weekday())) != 0
	if s.domAny || s.dowAny {
		return domOK && dowOK
	}
	return domOK || dowOK
}

// matchFields reports whether the wall-clock fields of t match.
func (s *Schedule) matchFields(t time.Time) bool {
	return s.month&(1<<int(t.Month())) != 0 && s.dayMatches(t) &&
		s.hour&(1<<t.Hour()) != 0 && s.minute&(1<<t.Minute()) != 0
}

// Next returns the first fire time strictly after t, or the zero time when
// there is none.
func (s *Schedule) Next(t time.Time) time.Time {
	start := t.Truncate(time.Minute).Add(time.Minute)
	limit := start.AddDate(searchYears, 0, 0)
	next := s.nextWall(start, limit)
	bound := limit
	if !next.IsZero() {
		bound = next
	}
	if gap := s.nextGapFire(t, bound); !gap.IsZero() {
		return gap
	}
	return next
}

func (s *Schedule) nextWall(c, limit time.Time) time.Time {
	for c.Before(limit) {
		lt := c.In(s.loc)
		switch {
		case s.month&(1<<int(lt.Month())) == 0:
			c = time.Date(lt.Year(), lt.Month()+1, 1, 0, 0, 0, 0, s.loc)
		case !s.dayMatches(lt):
			c = time.Date(lt.Year(), lt.Month(), lt.Day()+1, 0, 0, 0, 0, s.loc)
		case s.hour&(1<<lt.Hour()) == 0:
			c = c.Add(time.Duration(60-lt.Minute())*time.Minute - time.Duration(lt.Second())*time.Second)
		case s.minute&(1<<lt.Minute()) == 0, s.repeatedWall(c):
			c = c.Add(time.Minute)
		default:
			return c
		}
	}
	return time.Time{}
}

// repeatedWall reports whether c is the second occurrence of its wall-clock
// time after a fall-back transition, which fixed-hour jobs skip.
func (s *Schedule) repeatedWall(c time.Time) bool {
	if s.hourAny {
		return false
	}
	lt := c.In(s.loc)
	start, _ := lt.ZoneBounds()
	if start.IsZero() {
		return false
	}
	_, now := lt.Zone()
	_, before := start.Add(-time.Second).In(s.loc).Zone()
	return before > now && c.Sub(start) < time.Duration(before-now)*time.Second
}

// nextGapFire returns the first transition after t and before bound at which
// clocks jump forward over a wall-clock time that the schedule matches.
func (s *Schedule) nextGapFire(t, bound time.Time) time.Time {
	if s.hourAny {
		return time.Time{}
	}
	for range 64 {
		_, end := t.In(s.loc).ZoneBounds()
		if end.IsZero() || !end.Before(bound) {
			return time.Time{}
		}
		_, after := end.In(s.loc).Zone()
		_, before := end.Add(-time.Second).In(s.loc).Zone()
		if after > before {
			// Walk the wall-clock minutes that were skipped, [old wall, new
			// wall), using a UTC face so the fields read as written.
			from := end.Add(time.Duration(before) * time.Second).UTC()
			to := end.Add(time.Duration(after) * time.Second).UTC()
			for w := from; w.Before(to); w = w.Add(time.Minute) {
				if s.matchFields(w) {
					return end
				}
			}
		}
		t = end
	}
	return time.Time{}
}
