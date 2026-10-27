package monitor

import (
	"slices"
)

// healthOf aggregates check statuses into a service health: down if any
// critical check is down, else degraded if any non-critical check is down,
// else unknown if any critical check has no result yet, else healthy if any
// check is up, else unknown. A service is only reported healthy once every
// critical check has passed, so a failing critical check that has not run
// yet is never hidden behind a passing one. Unknown non-critical checks do
// not hold back a healthy verdict.
//
// With effective set, a stale check (its stored status predates a restart
// by more than twice its interval) counts as unknown; without it the stored
// status is used. Change detection must use the stored view so that a
// restart does not look like a health change.
func healthOf(checks map[string]*entry, effective bool) Health {
	var up, critDown, critUnknown, otherDown bool
	for _, e := range checks {
		st := e.status
		if effective && e.stale {
			st = StatusUnknown
		}
		switch st {
		case StatusUp:
			up = true
		case StatusDown:
			if e.check.IsCritical() {
				critDown = true
			} else {
				otherDown = true
			}
		default:
			if e.check.IsCritical() {
				critUnknown = true
			}
		}
	}
	switch {
	case critDown:
		return HealthDown
	case otherDown:
		return HealthDegraded
	case critUnknown:
		return HealthUnknown
	case up:
		return HealthHealthy
	default:
		return HealthUnknown
	}
}

// failingChecks lists the names of down checks, sorted.
func failingChecks(checks map[string]*entry) []string {
	var out []string
	for name, e := range checks {
		if e.status == StatusDown {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
