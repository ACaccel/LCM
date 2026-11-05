package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestOverview(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", heartbeatService))
	x.createService(serviceDoc("api", ""))
	x.createService(serviceDoc("old", ""))
	x.call("POST", v1+"/resources/old/phase", map[string]any{"phase": "retired", "version": 1}).want(http.StatusOK)
	if err := x.app.SyncMonitor(x.ctx); err != nil {
		t.Fatal(err)
	}

	// A heartbeat that never arrives turns the web service down once its
	// grace period has passed.
	x.clk.Advance(10 * time.Minute)
	if err := x.app.Monitor.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	x.call("POST", v1+"/hooks/alerts/generic", `{"service":"api","alert":"Latency","status":"firing","severity":"warning"}`, bearer(x.ingest)).want(http.StatusAccepted)

	o := x.call("GET", v1+"/overview", nil).want(http.StatusOK).obj()

	if o["firingAlerts"] != float64(1) || o["activeRuns"] != float64(0) {
		t.Errorf("headline numbers = %v", o)
	}
	types := o["types"].([]any)
	if len(types) != 1 {
		t.Fatalf("types = %v", types)
	}
	svc := types[0].(map[string]any)
	if svc["type"] != "service" || svc["total"] != float64(3) || svc["retired"] != float64(1) {
		t.Errorf("service counts = %v", svc)
	}
	health := svc["health"].(map[string]any)
	for _, h := range []string{"healthy", "degraded", "down", "unknown"} {
		if _, ok := health[h]; !ok {
			t.Errorf("health %q missing from %v", h, health)
		}
	}

	// Down comes before an alert-only entry; the retired service never shows.
	attention := o["attention"].([]any)
	var names []string
	for _, a := range attention {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	if len(names) != 2 || names[0] != "web" || names[1] != "api" {
		t.Fatalf("attention = %v, want [web api]", names)
	}
	web := attention[0].(map[string]any)
	if web["health"] != "down" || len(web["reasons"].([]any)) == 0 {
		t.Errorf("web attention = %v", web)
	}
	if api := attention[1].(map[string]any); api["firingAlerts"] != float64(1) {
		t.Errorf("api attention = %v", api)
	}

	cards := o["resources"].([]any)
	if len(cards) != 2 || cards[0].(map[string]any)["name"] != "api" || cards[1].(map[string]any)["name"] != "web" {
		t.Fatalf("resources = %v, want the two managed services by name", cards)
	}
	if c := cards[1].(map[string]any); c["hasChecks"] != true || c["health"] != "down" {
		t.Errorf("web card = %v", c)
	}
	if c := cards[0].(map[string]any); c["hasChecks"] != false || len(c["buckets"].([]any)) != 0 || c["uptime"] != nil {
		t.Errorf("api card = %v", c)
	}

	x.call("GET", v1+"/overview?since=yesterday", nil).wantError(http.StatusBadRequest, "bad_request")
	since := x.call("GET", v1+"/overview?since="+epoch.Format(time.RFC3339), nil).want(http.StatusOK).obj()["deploys"].(map[string]any)
	if since["since"] != epoch.Format(time.RFC3339) || since["count"] != float64(0) {
		t.Errorf("deploys = %v", since)
	}
}
