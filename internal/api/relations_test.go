package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/events"
)

// auditDetail returns the details of the newest audit entry with the
// action and target.
func (x *site) auditDetail(action, target string) map[string]any {
	x.t.Helper()
	for _, a := range x.call("GET", v1+"/audit?limit=200", nil).want(http.StatusOK).items() {
		if a["action"] == action && a["target"] == target {
			return a["details"].(map[string]any)
		}
	}
	x.t.Fatalf("no audit entry %s %s", action, target)
	return nil
}

func impactNames(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(map[string]any)["name"].(string))
	}
	return out
}

func TestImpactAndServiceGuard(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("db", "  relations: [{ type: dependsOn, target: ghost }]\n"))
	x.createService(serviceDoc("web", "  relations: [{ type: dependsOn, target: db }]\n"))
	x.createService(serviceDoc("tool", "  relations: [{ type: controlPath, target: web }]\n"))
	acts := x.collect(events.TypeAction)

	imp := x.call("GET", v1+"/resources/db/impact", nil).want(http.StatusOK).obj()
	if fmt.Sprint(impactNames(imp["dependents"])) != "[web]" || fmt.Sprint(impactNames(imp["controlDependents"])) != "[tool]" ||
		fmt.Sprint(imp["unresolved"]) != "[ghost]" || imp["acknowledgementRequired"] != false {
		t.Fatalf("impact = %v", imp)
	}
	if c := imp["controlDependents"].([]any)[0].(map[string]any); c["via"] != "web" || c["depth"] != float64(2) || c["relation"] != "controlPath" {
		t.Fatalf("control dependent = %v", c)
	}
	if x.call("GET", v1+"/resources/db/impact?action=stop", nil).want(http.StatusOK).obj()["acknowledgementRequired"] != true {
		t.Fatal("stop needs acknowledgement")
	}
	if x.call("GET", v1+"/resources/db/impact?action=start", nil).want(http.StatusOK).obj()["acknowledgementRequired"] != false {
		t.Fatal("start needs no acknowledgement")
	}
	x.call("GET", v1+"/resources/db/impact?action=reboot", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/resources/ghost/impact", nil).wantError(http.StatusNotFound, "not_found")

	// Starting is not guarded.
	if got := x.actionDone(acts, x.startAction("db", "start", nil)); got["status"] != "succeeded" {
		t.Fatalf("start = %v", got)
	}
	// The dry run shows the impact instead of refusing.
	plan := x.call("POST", v1+"/resources/db/actions/stop?dryRun=true", nil).want(http.StatusOK).obj()
	if pi := plan["impact"].(map[string]any); pi["acknowledgementRequired"] != true || pi["action"] != "stop" {
		t.Fatalf("plan = %v", plan)
	}
	details := x.call("POST", v1+"/resources/db/actions/stop", nil).wantError(http.StatusConflict, "impact_unacknowledged")
	if d := details["impact"].(map[string]any); details["action"] != "stop" || fmt.Sprint(impactNames(d["dependents"])) != "[web]" {
		t.Fatalf("details = %v", details)
	}
	id := x.startAction("db", "stop", map[string]any{"acknowledgeImpact": true})
	if got := x.actionDone(acts, id); got["status"] != "succeeded" {
		t.Fatalf("stop = %v", got)
	}
	ack := x.auditDetail("service.stop", "db")["acknowledgedImpact"].(map[string]any)
	if fmt.Sprint(ack["dependents"]) != "[web]" || fmt.Sprint(ack["controlDependents"]) != "[tool]" {
		t.Fatalf("audit = %v", ack)
	}

	// Retiring a service acts (it stops a running one) and is guarded too.
	x.call("POST", v1+"/resources/db/phase", map[string]any{"phase": "retired", "version": 1}).wantError(http.StatusConflict, "impact_unacknowledged")
	x.call("POST", v1+"/resources/db/phase", map[string]any{"phase": "retired", "version": 1, "acknowledgeImpact": true}).want(http.StatusOK)
	if ack := x.auditDetail("resource.phase", "db")["acknowledgedImpact"]; ack == nil {
		t.Fatal("retire audit has no acknowledged impact")
	}
	// Resuming and a leaf resource need nothing.
	x.call("POST", v1+"/resources/db/phase", map[string]any{"phase": "managed", "version": 1}).want(http.StatusOK)
	x.call("POST", v1+"/resources/tool/phase", map[string]any{"phase": "retired", "version": 1}).want(http.StatusOK)
}

func TestRouterRebootGuard(t *testing.T) {
	x, fake := routerSite(t)
	x.call("POST", v1+"/resources", routerResource, yamlBody()).want(http.StatusCreated)
	x.createService(serviceDoc("nas", "  relations: [{ type: controlPath, target: gateway }]\n"))
	x.call("POST", v1+"/resources/gateway/observe", nil).want(http.StatusOK)
	if x.call("GET", v1+"/resources/gateway/impact?action=reboot", nil).want(http.StatusOK).obj()["acknowledgementRequired"] != true {
		t.Fatal("reboot needs acknowledgement")
	}
	body := map[string]any{"action": "reboot", "idempotencyKey": "op-1"}
	details := x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).wantError(http.StatusConflict, "impact_unacknowledged")
	if fmt.Sprint(impactNames(details["impact"].(map[string]any)["controlDependents"])) != "[nas]" || fake.Reboots() != 0 {
		t.Fatalf("details = %v reboots %d", details, fake.Reboots())
	}
	body["acknowledgeImpact"] = true
	x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).want(http.StatusAccepted)
	if fake.Reboots() != 1 {
		t.Fatalf("reboots = %d", fake.Reboots())
	}
}

// Entering maintenance stops nothing, so it needs no acknowledgement even
// when the machine has dependents.
func TestMaintenanceEntryNeedsNoAcknowledgement(t *testing.T) {
	x := newSite(t)
	x.call("POST", v1+"/resources", "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: host }\nspec: { type: machine, adapter: local }\n", yamlBody()).
		want(http.StatusCreated)
	x.createService(serviceDoc("web", "  relations: [{ type: runsOn, target: host }]\n"))
	imp := x.call("GET", v1+"/resources/host/impact?action=maintenance-enter", nil).want(http.StatusOK).obj()
	if imp["acknowledgementRequired"] != false || fmt.Sprint(impactNames(imp["dependents"])) != "[web]" {
		t.Fatalf("maintenance impact = %v", imp)
	}
	if x.call("GET", v1+"/resources/host/impact?action=reboot", nil).want(http.StatusOK).obj()["acknowledgementRequired"] != true {
		t.Fatal("reboot needs acknowledgement")
	}
}

// groupDone polls a group action until it finished.
func (x *site) groupDone(id string) map[string]any {
	x.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		g := x.call("GET", v1+"/resource-groups/actions/"+id, nil).want(http.StatusOK).obj()
		if g["status"] != "running" {
			return g
		}
		time.Sleep(10 * time.Millisecond)
	}
	x.t.Fatalf("group %s did not finish", id)
	return nil
}

func stepSummary(g map[string]any) string {
	var out []string
	for _, s := range g["steps"].([]any) {
		st := s.(map[string]any)
		out = append(out, fmt.Sprint(st["resource"], ":", st["status"]))
	}
	return fmt.Sprint(out)
}

func TestGroupActions(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", "  relations: [{ type: dependsOn, target: api }]\n"))
	x.createService(serviceDoc("api", "  relations: [{ type: dependsOn, target: db }]\n"))
	x.createService(serviceDoc("db", ""))
	sel := map[string]any{"labels": map[string]string{"tier": "web"}}

	for _, bad := range []map[string]any{
		{"selector": map[string]any{"type": "machine", "labels": map[string]string{"tier": "web"}}, "action": "start"},
		{"selector": map[string]any{"labels": map[string]string{}}, "action": "start"},
		{"selector": sel, "action": "reboot"},
		{"selector": sel, "action": "start", "params": map[string]any{"job": "x"}},
	} {
		x.call("POST", v1+"/resource-groups/actions", bad).wantError(http.StatusUnprocessableEntity, "validation_failed")
	}
	x.call("POST", v1+"/resource-groups/actions", map[string]any{"selector": map[string]any{"labels": map[string]string{"tier": "none"}}, "action": "start"}).
		wantError(http.StatusConflict, "precondition_failed")

	plan := x.call("POST", v1+"/resource-groups/actions", map[string]any{"selector": sel, "action": "start", "dryRun": true}).want(http.StatusOK).obj()
	if fmt.Sprint(stepNamesOf(plan)) != "[db api web]" || plan["acknowledgementRequired"] != false || len(x.runner.Calls()) != 0 {
		t.Fatalf("plan = %v", plan)
	}

	// api fails to start: web is not run.
	x.runner.Reply("api-start", "boom", 1)
	g := x.call("POST", v1+"/resource-groups/actions", map[string]any{"selector": sel, "action": "start"}).want(http.StatusAccepted).obj()
	g = x.groupDone(g["id"].(string))
	if g["status"] != "failed" || stepSummary(g) != "[db:succeeded api:failed web:notRun]" {
		t.Fatalf("group = %v", g)
	}
	if steps := g["steps"].([]any); steps[0].(map[string]any)["actionId"] == "" || steps[1].(map[string]any)["error"] == "" {
		t.Fatalf("steps = %v", steps)
	}
	if contains(x.runner.Displays(), "web-start") {
		t.Fatalf("web was started: %v", x.runner.Displays())
	}
	if d := x.auditDetail("service.group-action", "services:tier=web"); d["failed"] != "api" || fmt.Sprint(d["notRun"]) != "[web]" {
		t.Fatalf("group audit = %v", d)
	}

	// Stop goes dependents first; db and api have dependents.
	stop := map[string]any{"selector": sel, "action": "stop"}
	plan = x.call("POST", v1+"/resource-groups/actions", map[string]any{"selector": sel, "action": "stop", "dryRun": true}).want(http.StatusOK).obj()
	if fmt.Sprint(stepNamesOf(plan)) != "[web api db]" || plan["acknowledgementRequired"] != true {
		t.Fatalf("stop plan = %v", plan)
	}
	x.call("POST", v1+"/resource-groups/actions", stop).wantError(http.StatusConflict, "impact_unacknowledged")
	x.runner.Reset()
	stop["acknowledgeImpact"] = true
	g = x.call("POST", v1+"/resource-groups/actions", stop).want(http.StatusAccepted).obj()
	if g = x.groupDone(g["id"].(string)); g["status"] != "succeeded" || stepSummary(g) != "[web:succeeded api:succeeded db:succeeded]" {
		t.Fatalf("stop group = %v", g)
	}
	var stops []string
	for _, d := range x.runner.Displays() {
		for _, n := range []string{"web", "api", "db"} {
			if strings.HasSuffix(d, " "+n+"-stop") {
				stops = append(stops, n)
			}
		}
	}
	if fmt.Sprint(stops) != "[web api db]" {
		t.Fatalf("stop commands = %v", x.runner.Displays())
	}
	if ack := x.auditDetail("service.stop", "api")["acknowledgedImpact"]; ack == nil {
		t.Fatal("api stop audit has no acknowledged impact")
	}
}

func stepNamesOf(plan map[string]any) []string {
	var out []string
	for _, s := range plan["steps"].([]any) {
		out = append(out, s.(map[string]any)["resource"].(string))
	}
	return out
}
