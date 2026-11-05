package api_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/security"
)

func securitySite(t *testing.T) *site {
	x := newSite(t, func(c *config.Config) { c.SecurityEnabled = true; c.SecurityMachineID = "machine-a" })
	if err := x.app.DB.Write(x.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(x.ctx, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES('machine-a','workstation','machine','local','managed',1,0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return x
}
func TestSecurityConsoleFiltersHandlingAndVersion(t *testing.T) {
	x := securitySite(t)
	e := security.Event{EventID: "http-event", MachineID: "machine-a", Source: "aide", Fingerprint: "integrity", Status: "firing", Severity: "warning", Summary: "Synthetic integrity signal", ObservedAt: x.clk.Now()}
	receipt, err := x.app.Security.Ingest(x.ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	path := v1 + "/security/incidents/" + receipt.IncidentID
	detail := x.call("GET", path, nil).want(200)
	if detail.Head.Get("ETag") != `"0"` {
		t.Fatal("missing initial ETag")
	}
	u := map[string]any{"action": "acknowledge", "reason": "Reviewed by operator"}
	x.call("PUT", path+"/handling", u).wantError(400, "bad_request")
	x.call("PUT", path+"/handling", u, ifMatch(`"0"`), bearer(x.ingest)).wantError(403, "forbidden")
	x.call("PUT", path+"/handling", u, ifMatch(`"0"`), header("Origin", "https://other.example"), header("Sec-Fetch-Site", "cross-site")).wantError(403, "cross_origin")
	x.call("PUT", path+"/handling", u, ifMatch(`"0"`)).want(200)
	x.call("PUT", path+"/handling", u, ifMatch(`"0"`)).wantError(409, "version_conflict")
	detail = x.call("GET", path, nil).want(200)
	incident := detail.obj()["incident"].(map[string]any)
	if incident["state"] != "firing" || !incident["handling"].(map[string]any)["acknowledged"].(bool) {
		t.Fatalf("handling changed detection: %s", detail.Body)
	}
	if len(detail.obj()["history"].([]any)) != 1 {
		t.Fatal("handling history missing")
	}
	rows := x.call("GET", v1+"/security/incidents?source=aide&severity=warning&state=firing&machineId=machine-a&service=__unassigned__&limit=1", nil).want(200).items()
	if len(rows) != 1 {
		t.Fatalf("filter: %+v", rows)
	}
	if rows := x.call("GET", v1+"/security/incidents?source=nginx", nil).want(200).items(); len(rows) != 0 {
		t.Fatal("filter ignored")
	}
	x.call("GET", v1+"/security/incidents?source=unknown", nil).wantError(422, "validation_failed")
	x.call("GET", v1+"/security/incidents?state=firing&state=resolved", nil).wantError(400, "bad_request")
	x.call("GET", v1+"/security/incidents?from=bad", nil).wantError(400, "bad_request")
	x.call("GET", v1+"/security/incidents/missing", nil).wantError(404, "not_found")
}
func TestSecuritySettingsPersistenceOriginAndSecretExclusion(t *testing.T) {
	x := securitySite(t)
	u := security.SettingsUpdate{NotificationsEnabled: false, CooldownSeconds: 120, MinimumSeverity: "critical", Reason: "Quiet maintenance"}
	x.call("PUT", v1+"/security/settings", u, ifMatch(`"0"`)).want(200)
	x.call("PUT", v1+"/security/settings", u, ifMatch(`"0"`)).wantError(409, "version_conflict")
	settings := x.call("GET", v1+"/security/settings", nil).want(200)
	if strings.Contains(settings.String(), "token") || strings.Contains(settings.String(), "URL") || strings.Contains(settings.String(), "konata") {
		t.Fatal("settings expose private relay configuration")
	}
	x.call("PUT", v1+"/security/settings", map[string]any{"notificationsEnabled": true, "cooldownSeconds": 1, "minimumSeverity": "critical", "reason": "Check"}, ifMatch(`"1"`)).wantError(422, "validation_failed")
	x.call("POST", v1+"/security/test-notification", security.TestRequest{RequestID: "test", Reason: "Acceptance"}).wantError(503, "unavailable")
	x.call("GET", v1+"/security/tests/missing", nil).wantError(404, "not_found")
}
func TestSecurityTrafficSeriesHTTPFiltersAndGaps(t *testing.T) {
	x := securitySite(t)
	observation := security.Traffic{MachineID: "machine-a", Category: "api", WindowStart: x.clk.Now().Add(-time.Minute), WindowEnd: x.clk.Now(), Requests: 2, RequestTimeSumMs: 100, RequestTimeMaxMs: 80}
	if _, err := x.app.Security.IngestTraffic(x.ctx, observation); err != nil {
		t.Fatal(err)
	}
	r := x.call("GET", v1+"/security/traffic/series?category=api&bucketSeconds=60", nil).want(200)
	if len(r.items()) != 1 || r.items()[0]["averageMs"] != float64(50) {
		t.Fatalf("aggregate: %s", r.Body)
	}
	x.call("GET", v1+"/security/traffic/series?category=unknown", nil).wantError(422, "validation_failed")
	x.call("GET", v1+"/security/traffic/series?bucketSeconds=1", nil).wantError(422, "validation_failed")
	x.call("GET", v1+"/security/traffic?category=api&service=__unassigned__", nil).want(200)
}

func TestSecurityHostStatusWithoutMachineIsNotAnOutage(t *testing.T) {
	x := newSite(t)
	body := x.call("GET", v1+"/security/host", nil).want(200).obj()
	if body["configured"] != false || body["available"] != false || !strings.Contains(body["reason"].(string), "LCM_SECURITY_MACHINE_ID") {
		t.Fatalf("status = %v", body)
	}
	for _, c := range body["capabilities"].([]any) {
		if c.(map[string]any)["enabled"] != false {
			t.Fatalf("capability enabled without a host: %v", c)
		}
	}
	// The write paths still refuse.
	x.call("GET", v1+"/security/host/operations", nil).wantError(503, "unavailable")
}
