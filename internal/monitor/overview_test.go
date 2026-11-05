package monitor

import (
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []OverviewResource{
		{Name: "ok", Type: "service", Managed: true, Health: HealthHealthy},
		{Name: "quiet", Type: "service", Managed: true, Health: HealthUnknown},
		{Name: "alerted", Type: "service", Managed: true, Health: HealthHealthy, FiringAlerts: 2},
		{Name: "failed", Type: "service", Managed: true, State: StateFailed, Health: HealthHealthy},
		{Name: "slow", Type: "service", Managed: true, Health: HealthDegraded, Since: t0},
		{Name: "dead-new", Type: "machine", Managed: true, Health: HealthDown, Since: t0.Add(time.Hour)},
		{Name: "dead-old", Type: "service", Managed: true, Health: HealthDown, Since: t0},
		{Name: "dead-nosince", Type: "service", Managed: true, Health: HealthDown},
		{Name: "gone", Type: "router", Managed: false, Health: HealthDown},
		{Name: "nohealth", Type: "router", Managed: true},
	}
	counts, attention := Summarize(items, []string{"service", "machine"})

	var order []string
	for _, a := range attention {
		order = append(order, a.Name)
	}
	want := []string{"dead-old", "dead-new", "dead-nosince", "slow", "failed", "alerted"}
	if len(order) != len(want) {
		t.Fatalf("attention = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("attention = %v, want %v", order, want)
		}
	}

	if len(counts) != 3 || counts[0].Type != "service" || counts[1].Type != "machine" || counts[2].Type != "router" {
		t.Fatalf("type order = %+v", counts)
	}
	svc := counts[0]
	if svc.Total != 7 || svc.Retired != 0 || svc.Health[HealthHealthy] != 3 || svc.Health[HealthDown] != 2 ||
		svc.Health[HealthDegraded] != 1 || svc.Health[HealthUnknown] != 1 {
		t.Errorf("service counts = %+v", svc)
	}
	router := counts[2]
	if router.Total != 2 || router.Retired != 1 || router.Health[HealthDown] != 0 || router.Health[HealthUnknown] != 1 {
		t.Errorf("router counts = %+v (retired resources do not count toward health)", router)
	}
}
