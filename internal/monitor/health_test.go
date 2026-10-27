package monitor

import (
	"testing"

	"github.com/ACaccel/lcm/internal/model"
)

func TestHealthOfUnknownCriticalCheck(t *testing.T) {
	f := false
	crit := model.Check{}
	soft := model.Check{Critical: &f}
	e := func(c model.Check, st Status) *entry { return &entry{check: c, status: st} }

	tests := []struct {
		name   string
		checks map[string]*entry
		want   Health
	}{
		{"no checks", nil, HealthUnknown},
		{"critical unknown", map[string]*entry{"a": e(crit, StatusUnknown)}, HealthUnknown},
		{"critical up and critical unknown", map[string]*entry{"a": e(crit, StatusUp), "b": e(crit, StatusUnknown)}, HealthUnknown},
		{"critical down beats unknown", map[string]*entry{"a": e(crit, StatusDown), "b": e(crit, StatusUnknown)}, HealthDown},
		{"degraded beats unknown", map[string]*entry{"a": e(soft, StatusDown), "b": e(crit, StatusUnknown)}, HealthDegraded},
		{"soft unknown does not block healthy", map[string]*entry{"a": e(crit, StatusUp), "b": e(soft, StatusUnknown)}, HealthHealthy},
		{"only soft unknown", map[string]*entry{"a": e(soft, StatusUnknown)}, HealthUnknown},
		{"all up", map[string]*entry{"a": e(crit, StatusUp), "b": e(soft, StatusUp)}, HealthHealthy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthOf(tc.checks, true); got != tc.want {
				t.Errorf("healthOf = %s, want %s", got, tc.want)
			}
		})
	}

	stale := &entry{check: crit, status: StatusUp, stale: true}
	if got := healthOf(map[string]*entry{"a": stale}, true); got != HealthUnknown {
		t.Errorf("stale critical check = %s, want unknown", got)
	}
	if got := healthOf(map[string]*entry{"a": stale}, false); got != HealthHealthy {
		t.Errorf("stored view of a stale check = %s, want healthy", got)
	}
}
