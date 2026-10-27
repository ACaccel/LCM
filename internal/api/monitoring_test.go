package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/events"
)

const heartbeatService = `
  checks:
    - { name: beat, type: heartbeat, interval: 1m, props: { grace: 30s } }
    - { name: probe, type: command, interval: 1m, timeout: 5s, props: { command: ["true"] } }
`

func TestChecksHeartbeatAndSeries(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", heartbeatService))
	if err := x.app.SyncMonitor(x.ctx); err != nil {
		t.Fatal(err)
	}

	checks := x.call("GET", v1+"/services/web/checks", nil).want(http.StatusOK).obj()
	list := checks["checks"].([]any)
	if checks["health"] != "unknown" || len(list) != 2 {
		t.Fatalf("checks = %v", checks)
	}
	first := list[0].(map[string]any)
	if first["name"] != "beat" || first["type"] != "heartbeat" || first["intervalMs"] != float64(60000) ||
		first["status"] != "unknown" || first["critical"] != true || first["lastResult"] != nil {
		t.Fatalf("first check = %v", first)
	}

	// Heartbeats are accepted from an ingest-scoped token, by POST and GET.
	hb := v1 + "/heartbeats/web/beat"
	x.call("POST", hb, "all good", bearer(x.ingest)).want(http.StatusAccepted)
	x.call("GET", hb, nil, bearer(x.ingest)).want(http.StatusAccepted)
	x.anon("POST", hb, nil).wantError(http.StatusUnauthorized, "unauthenticated")
	x.call("POST", v1+"/heartbeats/web/ghost", nil, bearer(x.ingest)).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/heartbeats/ghost/beat", nil, bearer(x.ingest)).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/heartbeats/web/probe", nil, bearer(x.ingest)).wantError(http.StatusConflict, "conflict")

	if err := x.app.Monitor.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	checks = x.call("GET", v1+"/services/web/checks", nil).want(http.StatusOK).obj()
	beat := checks["checks"].([]any)[0].(map[string]any)
	if beat["status"] != "up" || beat["message"] != "heartbeat received" || beat["lastResult"] == nil {
		t.Fatalf("beat after heartbeat = %v", beat)
	}
	detail := x.call("GET", v1+"/services/web", nil).obj()
	if len(detail["checks"].([]any)) != 2 {
		t.Fatalf("service detail checks = %v", detail["checks"])
	}

	from, to := epoch.Add(-time.Hour).Format(time.RFC3339), epoch.Add(time.Hour).Format(time.RFC3339)
	ser := x.call("GET", v1+"/services/web/checks/beat/series?from="+from+"&to="+to+"&points=10", nil).want(http.StatusOK).obj()
	pts := ser["points"].([]any)
	if ser["source"] != "raw" || ser["check"] != "beat" || len(pts) != 2 || pts[0].(map[string]any)["failures"] != float64(0) {
		t.Fatalf("series = %v", ser)
	}
	// Without from and to the last hour up to the (fake) now is served.
	x.call("GET", v1+"/services/web/checks/beat/series", nil).want(http.StatusOK)
	x.call("GET", v1+"/services/web/checks/beat/series?from="+to+"&to="+from, nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/services/web/checks/beat/series?from=yesterday", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/services/web/checks/beat/series?points=0", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/services/web/checks/ghost/series", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/services/ghost/checks", nil).wantError(http.StatusNotFound, "not_found")

	for _, w := range []string{"", "24h", "7d", "30d"} {
		path := v1 + "/services/web/summary"
		if w != "" {
			path += "?window=" + w
		}
		sum := x.call("GET", path, nil).want(http.StatusOK).obj()
		if sum["service"] != "web" || sum["checks"].(map[string]any)["beat"] == nil {
			t.Fatalf("summary %q = %v", w, sum)
		}
	}
	x.call("GET", v1+"/services/web/summary?window=5y", nil).wantError(http.StatusBadRequest, "bad_request")

	// A service without checks has empty results rather than an error.
	x.createService(serviceDoc("plain", ""))
	empty := x.call("GET", v1+"/services/plain/checks", nil).want(http.StatusOK).obj()
	if len(empty["checks"].([]any)) != 0 || empty["health"] != "unknown" {
		t.Fatalf("plain checks = %v", empty)
	}

	types := x.call("GET", v1+"/check-types", nil).want(http.StatusOK).items()
	seen := map[string]bool{}
	for _, ty := range types {
		seen[ty["type"].(string)] = true
	}
	for _, want := range []string{"http", "tcp", "systemd", "docker", "command", "heartbeat", "prometheus"} {
		if !seen[want] {
			t.Errorf("check type %s missing from %v", want, types)
		}
	}
}

func TestAlertIngestAndList(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	sigs := x.collect(events.TypeSignal)

	payload := `{"service":"web","alert":"HighLatency","status":"firing","severity":"warning","message":"p99 above 1s"}`
	r := x.call("POST", v1+"/hooks/alerts/generic", payload, bearer(x.ingest)).want(http.StatusAccepted)
	if r.obj()["signals"] != float64(1) {
		t.Fatalf("ingest = %s", r)
	}
	sigs.until("alert signal", func(e events.Event) bool { return e.Service == "web" })
	// A repeat of a firing alert refreshes it silently.
	r = x.call("POST", v1+"/hooks/alerts/generic", payload, bearer(x.ingest)).want(http.StatusAccepted)
	if r.obj()["signals"] != float64(0) {
		t.Fatalf("repeat = %s", r)
	}

	alerts := x.call("GET", v1+"/alerts?service=web", nil).want(http.StatusOK).items()
	if len(alerts) != 1 || alerts[0]["name"] != "HighLatency" || alerts[0]["severity"] != "warning" || alerts[0]["service"] != "web" {
		t.Fatalf("alerts = %v", alerts)
	}
	if n := len(x.call("GET", v1+"/alerts", nil).items()); n != 1 {
		t.Fatalf("all alerts = %d", n)
	}
	if n := len(x.call("GET", v1+"/alerts?service=db", nil).items()); n != 0 {
		t.Fatalf("db alerts = %d", n)
	}
	if got := x.call("GET", v1+"/services/web", nil).obj()["alerts"].([]any); len(got) != 1 {
		t.Fatalf("service alerts = %v", got)
	}

	// Resolving removes it from the firing list.
	x.call("POST", v1+"/hooks/alerts/generic", `{"service":"web","alert":"HighLatency","status":"resolved"}`, bearer(x.ingest)).want(http.StatusAccepted)
	if n := len(x.call("GET", v1+"/alerts", nil).items()); n != 0 {
		t.Fatalf("alerts after resolve = %d", n)
	}

	x.call("POST", v1+"/hooks/alerts/nagios", payload, bearer(x.ingest)).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/hooks/alerts/generic", `{not json`, bearer(x.ingest)).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/hooks/alerts/alertmanager", `{"version":"4","alerts":[`, bearer(x.ingest)).wantError(http.StatusBadRequest, "bad_request")
	x.anon("POST", v1+"/hooks/alerts/generic", payload).wantError(http.StatusUnauthorized, "unauthenticated")
	big := make([]byte, 1<<20+1)
	x.call("POST", v1+"/hooks/alerts/generic", big, bearer(x.ingest)).wantError(http.StatusRequestEntityTooLarge, "bad_request")
}
