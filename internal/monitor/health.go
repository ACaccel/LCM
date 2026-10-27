package monitor

import (
	"slices"
)

// healthOf aggregates check statuses into a service health: down if any
// critical check is down, else degraded if any non-critical check is down,
// else healthy if any check is up, else unknown. Checks without a result
// do not count against the service.
//
// With effective set, a stale check (its stored status predates a restart
// by more than twice its interval) counts as unknown; without it the stored
// status is used. Change detection must use the stored view so that a
// restart does not look like a health change.
func healthOf(checks map[string]*entry, effective bool) Health {
	var up, critDown, otherDown bool
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
		}
	}
	switch {
	case critDown:
		return HealthDown
	case otherDown:
		return HealthDegraded
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
