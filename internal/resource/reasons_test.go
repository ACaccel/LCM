package resource_test

import (
	"maps"
	"testing"

	"github.com/ACaccel/lcm/internal/resource"
)

func TestReasonMessage(t *testing.T) {
	cases := []struct {
		reason string
		code   string
		params map[string]string
	}{
		{resource.ReasonStale, "health.stale", map[string]string{}},
		{resource.ReasonNotObserved, "health.notObserved", map[string]string{}},
		{resource.ReasonObservationUnavailable, "health.observationUnavailable", map[string]string{}},
		{resource.ReasonUnreachable, "health.unreachable", map[string]string{}},
		{resource.ReasonAuthRejected, "health.authRejected", map[string]string{}},
		{resource.ReasonNoStatus, "health.noStatus", map[string]string{}},
		{resource.ReasonNoConnection, "health.noConnection", map[string]string{}},
		{resource.ReasonConnectionType, "health.connectionType", map[string]string{}},
		{resource.ReasonConnectionDenied, "health.connectionDenied", map[string]string{}},
		{"cpu", "health.threshold", map[string]string{"metric": "cpu", "target": ""}},
		{"disk:/var", "health.threshold", map[string]string{"metric": "disk", "target": "/var"}},
		{"inode:/", "health.threshold", map[string]string{"metric": "inode", "target": "/"}},
		{"unit:nginx.service", "health.threshold", map[string]string{"metric": "unit", "target": "nginx.service"}},
		{resource.NotReported("memory"), "health.notReported", map[string]string{"metric": "memory", "target": ""}},
		{"something else", "", map[string]string{}},
		{"disk:", "", map[string]string{}},
	}
	for _, c := range cases {
		m := resource.ReasonMessage(c.reason)
		if m.Code != c.code || !maps.Equal(m.Params, c.params) || m.Message != c.reason {
			t.Errorf("ReasonMessage(%q) = %+v, want code %q params %v", c.reason, m, c.code, c.params)
		}
	}
	if got := resource.ReasonMessages(nil); got == nil || len(got) != 0 {
		t.Fatalf("ReasonMessages(nil) = %#v, want empty", got)
	}
}

func TestEventReasonMessage(t *testing.T) {
	if m := resource.EventReasonMessage(resource.Event{Kind: "machine.save"}); m != nil {
		t.Fatalf("event without reason = %+v", m)
	}
	cases := []struct {
		kind, reason string
		code         string
		params       map[string]string
	}{
		{"machine.threshold.firing", "disk:/", "metric", map[string]string{"metric": "disk", "target": "/"}},
		{"router.threshold.recovered", "cpu", "metric", map[string]string{"metric": "cpu", "target": ""}},
		{"machine.health.changed", "degraded", "health.state", map[string]string{"health": "degraded"}},
		{"router.reachability.changed", "unreachable", "reachability", map[string]string{"reachability": "unreachable"}},
		{"machine.boot.changed", resource.ReasonBootChanged, "event.bootChanged", map[string]string{}},
		{"router.boot.changed", resource.ReasonUptimeRestarted, "event.uptimeRestarted", map[string]string{}},
		{"machine.collector.changed", "partial", "coverage.state", map[string]string{"state": "partial"}},
		{"router.collector.changed", "wan unavailable", "coverage.sourceState", map[string]string{"source": "wan", "state": "unavailable"}},
		{"machine.collector.gap", "lookback_exceeded", "coverage.reason", map[string]string{"reason": "lookback_exceeded"}},
		{"machine.operation.changed", "observing", "operation.state", map[string]string{"state": "observing"}},
		{"router.operation.changed", resource.ReasonResolved, "operation.resolved", map[string]string{}},
		{"machine.other", "free text", "", map[string]string{}},
	}
	for _, c := range cases {
		m := resource.EventReasonMessage(resource.Event{Kind: c.kind, Reason: c.reason})
		if m == nil || m.Code != c.code || !maps.Equal(m.Params, c.params) || m.Message != c.reason {
			t.Errorf("EventReasonMessage(%s, %q) = %+v, want code %q params %v", c.kind, c.reason, m, c.code, c.params)
		}
	}
}
