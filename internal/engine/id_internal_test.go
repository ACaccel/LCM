package engine

import (
	"sort"
	"testing"
	"time"
)

func TestULIDMonotonic(t *testing.T) {
	var g idGen
	now := time.UnixMilli(1_700_000_000_000)
	ids := make([]string, 0, 1000)
	for i := range 1000 {
		// The clock stands still for most ids and moves backwards once.
		ts := now
		if i == 500 {
			ts = now.Add(-time.Second)
		}
		if i > 700 {
			ts = now.Add(time.Millisecond)
		}
		ids = append(ids, g.next(ts))
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("ids not in creation order")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if len(id) != 26 || seen[id] {
			t.Fatalf("bad or duplicate id %q", id)
		}
		seen[id] = true
	}
	// The timestamp prefix encodes the milliseconds.
	if got, want := encodeULID(0, 0, 0), "00000000000000000000000000"; got != want {
		t.Fatalf("zero = %s", got)
	}
	if got := encodeULID(1<<48-1, 0, 0); got[:10] != "7ZZZZZZZZZ" {
		t.Fatalf("max time = %s", got)
	}
}

func TestBackoff(t *testing.T) {
	p := retryPolicy{max: 5, backoff: time.Second, maxBackoff: 5 * time.Second}
	for attempt, base := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 5 * time.Second, 9: 5 * time.Second} {
		for range 50 {
			d := backoff(p, attempt)
			if d < base/2 || d > base {
				t.Fatalf("attempt %d: %s outside [%s, %s]", attempt, d, base/2, base)
			}
		}
	}
	if backoff(retryPolicy{max: 1}, 1) != 0 {
		t.Fatal("zero backoff should retry immediately")
	}
}
