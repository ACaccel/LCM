package trigger

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

// nexts returns the next n fire times after t in UTC.
func nexts(t *testing.T, spec string, loc *time.Location, after time.Time, n int) []time.Time {
	t.Helper()
	s, err := ParseSchedule(spec, loc)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", spec, err)
	}
	var out []time.Time
	for range n {
		next := s.Next(after)
		if next.IsZero() {
			t.Fatalf("%q: no next fire after %s", spec, after)
		}
		out = append(out, next.UTC())
		after = next
	}
	return out
}

func checkTimes(t *testing.T, got, want []time.Time) {
	t.Helper()
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestScheduleBasics(t *testing.T) {
	base := utc(2026, 1, 1, 0, 0)
	tests := []struct {
		spec string
		want []time.Time
	}{
		{"*/15 * * * *", []time.Time{utc(2026, 1, 1, 0, 15), utc(2026, 1, 1, 0, 30), utc(2026, 1, 1, 0, 45), utc(2026, 1, 1, 1, 0)}},
		{"30 2 * * *", []time.Time{utc(2026, 1, 1, 2, 30), utc(2026, 1, 2, 2, 30)}},
		{"0 9 * * mon-fri", []time.Time{utc(2026, 1, 1, 9, 0), utc(2026, 1, 2, 9, 0), utc(2026, 1, 5, 9, 0)}}, // Jan 1 2026 is a Thursday
		{"0 0 1 * *", []time.Time{utc(2026, 2, 1, 0, 0), utc(2026, 3, 1, 0, 0)}},
		{"0 0 29 2 *", []time.Time{utc(2028, 2, 29, 0, 0), utc(2032, 2, 29, 0, 0)}},
		{"0 12 1,15 jan,jul *", []time.Time{utc(2026, 1, 1, 12, 0), utc(2026, 1, 15, 12, 0), utc(2026, 7, 1, 12, 0)}},
		{"5/20 0 * * *", []time.Time{utc(2026, 1, 1, 0, 5), utc(2026, 1, 1, 0, 25), utc(2026, 1, 1, 0, 45), utc(2026, 1, 2, 0, 5)}},
		{"@hourly", []time.Time{utc(2026, 1, 1, 1, 0), utc(2026, 1, 1, 2, 0)}},
		{"0 0 * * 7", []time.Time{utc(2026, 1, 4, 0, 0), utc(2026, 1, 11, 0, 0)}}, // 7 is Sunday
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			checkTimes(t, nexts(t, tt.spec, time.UTC, base, len(tt.want)), tt.want)
		})
	}
}

func TestScheduleDayOfMonthOrWeek(t *testing.T) {
	// Both day fields restricted: either may match (classic cron rule).
	got := nexts(t, "0 0 13 * fri", time.UTC, utc(2026, 2, 1, 0, 0), 4)
	want := []time.Time{utc(2026, 2, 6, 0, 0), utc(2026, 2, 13, 0, 0), utc(2026, 2, 20, 0, 0), utc(2026, 2, 27, 0, 0)}
	checkTimes(t, got, want)
}

func TestScheduleTimezone(t *testing.T) {
	taipei := mustLoc(t, "Asia/Taipei") // UTC+8, no DST
	got := nexts(t, "0 9 * * *", taipei, utc(2026, 1, 1, 0, 0), 2)
	checkTimes(t, got, []time.Time{utc(2026, 1, 1, 1, 0), utc(2026, 1, 2, 1, 0)})
}

func TestScheduleSpringForward(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08 02:00 EST jumps to 03:00 EDT. 02:30 does not exist that day.
	got := nexts(t, "30 2 * * *", ny, utc(2026, 3, 7, 0, 0), 3)
	want := []time.Time{
		utc(2026, 3, 7, 7, 30), // 02:30 EST
		utc(2026, 3, 8, 7, 0),  // runs right after the jump: 03:00 EDT
		utc(2026, 3, 9, 6, 30), // 02:30 EDT
	}
	checkTimes(t, got, want)

	// A wildcard-hour job is not adjusted: it simply has no 02:xx runs.
	got = nexts(t, "30 * * * *", ny, utc(2026, 3, 8, 6, 0), 3)
	checkTimes(t, got, []time.Time{utc(2026, 3, 8, 6, 30), utc(2026, 3, 8, 7, 30), utc(2026, 3, 8, 8, 30)})
}

func TestScheduleFallBack(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-11-01 02:00 EDT falls back to 01:00 EST, so 01:30 occurs twice.
	got := nexts(t, "30 1 * * *", ny, utc(2026, 10, 31, 12, 0), 3)
	want := []time.Time{
		utc(2026, 11, 1, 5, 30), // first 01:30 (EDT)
		utc(2026, 11, 2, 6, 30), // the repeated 01:30 EST is skipped
		utc(2026, 11, 3, 6, 30),
	}
	checkTimes(t, got, want)

	// Hourly jobs run by elapsed time, so the repeated hour fires twice.
	got = nexts(t, "0 * * * *", ny, utc(2026, 11, 1, 4, 30), 4)
	checkTimes(t, got, []time.Time{utc(2026, 11, 1, 5, 0), utc(2026, 11, 1, 6, 0), utc(2026, 11, 1, 7, 0), utc(2026, 11, 1, 8, 0)})
}

func TestScheduleSouthernHemisphereAndHalfHourZones(t *testing.T) {
	// Sydney springs forward on 2026-10-04 at 02:00 AEST (UTC+10) to AEDT.
	syd := mustLoc(t, "Australia/Sydney")
	got := nexts(t, "30 2 * * *", syd, utc(2026, 10, 3, 0, 0), 2)
	checkTimes(t, got, []time.Time{
		utc(2026, 10, 3, 16, 0),  // 02:30 does not exist on Oct 4; runs at 03:00 AEDT
		utc(2026, 10, 4, 15, 30), // 02:30 AEDT (UTC+11)
	})

	kolkata := mustLoc(t, "Asia/Kolkata") // UTC+5:30
	got = nexts(t, "0 9 * * *", kolkata, utc(2026, 1, 1, 0, 0), 1)
	checkTimes(t, got, []time.Time{utc(2026, 1, 1, 3, 30)})
}

func TestScheduleInvalid(t *testing.T) {
	bad := map[string]string{
		"* * * *":        "5 fields",
		"60 * * * *":     "outside",
		"* 24 * * *":     "outside",
		"* * 0 * *":      "outside",
		"* * * 13 *":     "outside",
		"*/0 * * * *":    "step",
		"5-1 * * * *":    "backwards",
		"a * * * *":      "invalid value",
		"0 0 31 2 *":     "no month",
		"0 0 30 feb *":   "no month",
		"* * * foo *":    "invalid value",
		"1,,2 * * * *":   "invalid value",
		"0 0 * * 8":      "outside",
		"0 0 * * mon-xx": "invalid value",
	}
	for spec, want := range bad {
		_, err := ParseSchedule(spec, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseSchedule(%q) = %v, want error containing %q", spec, err, want)
		}
	}
}

func TestScheduleNextIsStrictlyAfter(t *testing.T) {
	s, _ := ParseSchedule("0 * * * *", time.UTC)
	at := utc(2026, 1, 1, 5, 0)
	if next := s.Next(at); !next.Equal(utc(2026, 1, 1, 6, 0)) {
		t.Fatalf("Next(%s) = %s", at, next)
	}
	if next := s.Next(at.Add(-time.Nanosecond)); !next.Equal(at) {
		t.Fatalf("Next just before a fire time = %s", next)
	}
}
