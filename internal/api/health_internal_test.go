package api

import (
	"slices"
	"testing"

	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
)

func TestHealthReasonMessages(t *testing.T) {
	h := healthOf(monitor.ResourceStatus{
		Health: monitor.HealthDown,
		Native: &monitor.NativeStatus{Reasons: []string{"disk:/var", resource.ReasonStale}},
		Checks: []monitor.CheckStatus{{Name: "http", Status: monitor.StatusDown}, {Name: "tcp", Status: monitor.StatusUp}},
	})
	wantText := []string{"disk:/var", resource.ReasonStale, "check http is down"}
	if !slices.Equal(h.Reasons, wantText) || len(h.ReasonMessages) != len(wantText) {
		t.Fatalf("health = %+v", h)
	}
	for i, code := range []string{"health.threshold", "health.stale", "health.checkDown"} {
		if m := h.ReasonMessages[i]; m.Code != code || m.Message != wantText[i] {
			t.Errorf("reason %d = %+v, want code %s", i, m, code)
		}
	}
	if p := h.ReasonMessages[0].Params; p["metric"] != "disk" || p["target"] != "/var" {
		t.Errorf("threshold params = %v", p)
	}
	if p := h.ReasonMessages[2].Params; p["check"] != "http" {
		t.Errorf("check params = %v", p)
	}
	if h := healthOf(monitor.ResourceStatus{Health: monitor.HealthHealthy}); h.Reasons == nil || h.ReasonMessages == nil {
		t.Fatalf("healthy reasons must be empty lists: %+v", h)
	}
}
