package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/node"
)

// waitRun waits for the run to reach a terminal status event.
func (x *site) waitRun(c *collector, id string) map[string]any {
	x.t.Helper()
	c.until("run "+id+" to finish", func(e events.Event) bool {
		ev, ok := e.Data.(engine.Event)
		return ok && ev.RunID == id && ev.Kind == engine.EventRunFinished
	})
	return x.call("GET", v1+"/runs/"+id, nil).want(http.StatusOK).obj()
}

func TestPolicyDeploymentAndVersions(t *testing.T) {
	x := newSite(t)

	r := x.putPolicy("echo", manualPolicyNodes)
	if r.Head.Get("ETag") != `"1"` || r.Head.Get("Location") != v1+"/policies/echo" {
		t.Fatalf("create headers = %v", r.Head)
	}
	created := r.obj()
	if created["version"] != float64(1) || created["deployed"] != true || created["paused"] != false ||
		created["name"] != "echo" || created["lastRun"] != nil {
		t.Fatalf("created = %v", created)
	}
	trig := created["triggers"].([]any)
	if len(trig) != 1 || trig[0].(map[string]any)["id"] != "start" || trig[0].(map[string]any)["type"] != "trigger.manual" {
		t.Fatalf("triggers = %v", trig)
	}
	if created["document"].(map[string]any)["metadata"].(map[string]any)["name"] != "echo" || created["updatedBy"] == nil {
		t.Fatalf("document = %v", created["document"])
	}

	// Updating needs the current version; a stale one reports the real one.
	next := policyDoc("echo", manualPolicyNodes+"    - { id: extra, type: data.set, props: { values: { a: 1 } } }\n")
	x.call("PUT", v1+"/policies/echo", next, yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/policies/echo", next, yamlBody(), ifMatch("*")).wantError(http.StatusBadRequest, "bad_request")
	d := x.call("PUT", v1+"/policies/echo", next, yamlBody(), ifMatch("7")).wantError(http.StatusConflict, "version_conflict")
	if d["currentVersion"] != float64(1) || d["expectedVersion"] != float64(7) {
		t.Fatalf("conflict details = %v", d)
	}
	x.call("PUT", v1+"/policies/ghost", policyDoc("ghost", manualPolicyNodes), yamlBody(), ifMatch("1")).wantError(http.StatusNotFound, "not_found")
	x.call("PUT", v1+"/policies/other", next, yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	r = x.call("PUT", v1+"/policies/echo", next, yamlBody(), ifMatch(`"1"`)).want(http.StatusOK)
	if r.obj()["version"] != float64(2) || r.Head.Get("ETag") != `"2"` {
		t.Fatalf("update = %s", r)
	}
	// An identical document is not a new version.
	if v := x.call("PUT", v1+"/policies/echo", next, yamlBody(), ifMatch("2")).want(http.StatusOK).obj()["version"]; v != float64(2) {
		t.Fatalf("identical deploy version = %v", v)
	}

	// Compile errors carry document paths and keep the old version.
	broken := policyDoc("echo", "    - { id: start, type: trigger.manual, next: { out: [x] } }\n    - { id: x, type: no.such.type }\n")
	d = x.call("PUT", v1+"/policies/echo", broken, yamlBody(), ifMatch("2")).wantError(http.StatusUnprocessableEntity, "validation_failed")
	if p := d["issues"].([]any)[0].(map[string]any)["path"].(string); !strings.HasPrefix(p, "spec.nodes[1]") {
		t.Fatalf("issue path = %q", p)
	}
	x.call("PUT", v1+"/policies/echo", "kind: Service", yamlBody(), ifMatch("2")).wantError(http.StatusUnprocessableEntity, "validation_failed")

	vs := x.call("GET", v1+"/policies/echo/versions", nil).want(http.StatusOK).items()
	if len(vs) != 2 || vs[0]["version"] != float64(2) {
		t.Fatalf("versions = %v", vs)
	}
	v1doc := x.call("GET", v1+"/policies/echo/versions/1", nil).want(http.StatusOK).obj()
	if n := len(v1doc["document"].(map[string]any)["spec"].(map[string]any)["nodes"].([]any)); n != 2 {
		t.Fatalf("version 1 has %d nodes", n)
	}
	x.call("GET", v1+"/policies/echo/versions/9", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/policies/ghost/versions", nil).wantError(http.StatusNotFound, "not_found")

	list := x.call("GET", v1+"/policies", nil).want(http.StatusOK).items()
	if len(list) != 1 || list[0]["deployedVersion"] != float64(2) || list[0]["latestVersion"] != float64(2) {
		t.Fatalf("list = %v", list)
	}

	// Warnings from the compiler are part of the detail.
	warnDoc := policyDoc("lonely", manualPolicyNodes+"    - { id: orphan, type: data.set, props: { values: { a: 1 } } }\n")
	x.call("PUT", v1+"/policies/lonely", warnDoc, yamlBody()).want(http.StatusCreated)
	detail := x.call("GET", v1+"/policies/lonely", nil).want(http.StatusOK).obj()
	if len(detail["warnings"].([]any)) == 0 {
		t.Fatalf("expected warnings: %v", detail)
	}

	// Pause, resume, undeploy.
	if p := x.call("POST", v1+"/policies/echo/pause", nil).want(http.StatusOK).obj()["paused"]; p != true {
		t.Fatalf("pause = %v", p)
	}
	x.call("POST", v1+"/policies/echo/runs", map[string]string{"mode": "dry"}).wantError(http.StatusConflict, "conflict")
	if p := x.call("GET", v1+"/policies/echo", nil).obj()["paused"]; p != true {
		t.Fatalf("paused flag = %v", p)
	}
	x.call("POST", v1+"/policies/echo/resume", nil).want(http.StatusOK)
	x.call("DELETE", v1+"/policies/echo/deployment", nil).want(http.StatusNoContent)
	x.call("DELETE", v1+"/policies/echo/deployment", nil).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/policies/echo/pause", nil).wantError(http.StatusNotFound, "not_found")
	und := x.call("GET", v1+"/policies/echo", nil).want(http.StatusOK).obj()
	if und["deployed"] != false || und["deployedVersion"] != nil || und["version"] != float64(2) {
		t.Fatalf("undeployed policy = %v", und)
	}
	x.call("POST", v1+"/policies/echo/runs", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/policies/ghost", nil).wantError(http.StatusNotFound, "not_found")
	// Deploying again resurrects the stored version.
	x.call("PUT", v1+"/policies/echo", next, yamlBody(), ifMatch("2")).want(http.StatusOK)
	if x.call("GET", v1+"/policies/echo", nil).obj()["deployed"] != true {
		t.Fatal("policy not deployed again")
	}
}

func TestManualRunsAndTrace(t *testing.T) {
	x := newSite(t)
	x.putPolicy("echo", manualPolicyNodes)
	runs := x.collect(events.TypeRun)

	r := x.call("POST", v1+"/policies/echo/runs", map[string]any{"mode": "dry", "event": map[string]string{"name": "web"}}).want(http.StatusAccepted)
	body := r.obj()
	id := body["runId"].(string)
	if body["trigger"] != "start" || body["mode"] != "dry" || r.Head.Get("Location") != v1+"/runs/"+id {
		t.Fatalf("start = %v, headers %v", body, r.Head)
	}
	view := x.waitRun(runs, id)
	if view["status"] != "succeeded" || view["mode"] != "dry" || view["policy"] != "echo" || view["event"].(map[string]any)["name"] != "web" {
		t.Fatalf("run = %v", view)
	}
	nodes := view["nodes"].([]any)
	if len(nodes) != 2 || nodes[1].(map[string]any)["output"].(map[string]any)["got"] != "web" {
		t.Fatalf("trace = %v", nodes)
	}

	// A live run without a body uses defaults (live mode, the only manual trigger).
	id2 := x.call("POST", v1+"/policies/echo/runs", map[string]any{"event": map[string]string{"name": "db"}}).want(http.StatusAccepted).obj()["runId"].(string)
	if v := x.waitRun(runs, id2); v["mode"] != "live" || v["status"] != "succeeded" {
		t.Fatalf("live run = %v", v)
	}

	// Run list, filters and paging (newest first).
	all := x.call("GET", v1+"/runs", nil).want(http.StatusOK).items()
	if len(all) != 2 || all[0]["id"] != id2 || all[1]["id"] != id {
		t.Fatalf("runs = %v", all)
	}
	if _, has := all[0]["nodes"]; has {
		t.Error("run list items carry the trace")
	}
	page := x.call("GET", v1+"/runs?limit=1", nil).want(http.StatusOK).obj()
	cursor := page["nextCursor"].(string)
	if n := len(x.call("GET", v1+"/runs?limit=1&cursor="+cursor, nil).items()); n != 1 || len(page["items"].([]any)) != 1 {
		t.Fatalf("paging broken: %v", page)
	}
	if n := len(x.call("GET", v1+"/runs?policy=echo&status=succeeded,failed", nil).items()); n != 2 {
		t.Fatalf("filtered runs = %d", n)
	}
	if n := len(x.call("GET", v1+"/runs?policy=other", nil).items()); n != 0 {
		t.Fatalf("other policy runs = %d", n)
	}
	x.call("GET", v1+"/runs?status=bogus", nil).wantError(http.StatusBadRequest, "bad_request")
	if lr := x.call("GET", v1+"/policies/echo", nil).obj()["lastRun"].(map[string]any); lr["id"] != id2 || lr["status"] != "succeeded" {
		t.Fatalf("lastRun = %v", lr)
	}

	x.call("GET", v1+"/runs/ghost", nil).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/runs/ghost/cancel", nil).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/runs/"+id+"/cancel", nil).wantError(http.StatusConflict, "conflict")

	// Bad starts.
	x.call("POST", v1+"/policies/ghost/runs", nil).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/policies/echo/runs", map[string]string{"mode": "fast"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/policies/echo/runs", map[string]string{"trigger": "echo"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/policies/echo/runs", map[string]string{"trigger": "nope"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/policies/echo/runs", `{"bogus":1}`).wantError(http.StatusBadRequest, "bad_request")

	// Two manual triggers need an explicit choice.
	two := "    - { id: a, type: trigger.manual }\n    - { id: b, type: trigger.manual }\n"
	x.putPolicy("two", two)
	x.call("POST", v1+"/policies/two/runs", nil).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/policies/two/runs", map[string]string{"trigger": "b"}).want(http.StatusAccepted)
}

// approvalProps configures the approval node the test registers.
type approvalProps struct {
	Note string `json:"note"`
}

// registerApproval adds a node that parks until it is resumed by key, as an
// approval node would.
func registerApproval(x *site) {
	node.Register(x.app.Registry, node.Spec[approvalProps]{
		Type: "test.approve", Category: "test",
		Run: func(_ context.Context, rc *node.RunContext, _ approvalProps) (node.Result, error) {
			if rc.Resumed != nil {
				return node.Result{Output: map[string]any{"approvedWith": rc.Resumed.Payload}}, nil
			}
			return node.Result{Suspend: &node.Suspend{Key: "approve-1", Output: map[string]any{"waiting": true}}}, nil
		},
	})
}

const waitingPolicy = `    - { id: start, type: trigger.manual, next: { out: [pause] } }
    - { id: pause, type: test.approve }
`

func TestCancelResumeAndDropped(t *testing.T) {
	x := newSite(t)
	registerApproval(x)
	x.putPolicy("waiter", waitingPolicy)
	runs := x.collect(events.TypeRun, events.TypeNode)

	id := x.call("POST", v1+"/policies/waiter/runs", nil).want(http.StatusAccepted).obj()["runId"].(string)
	runs.until("node to suspend", func(e events.Event) bool {
		ev, ok := e.Data.(engine.Event)
		return ok && ev.RunID == id && ev.Kind == engine.EventNodeSuspended
	})
	view := x.call("GET", v1+"/runs/"+id, nil).want(http.StatusOK).obj()
	if view["status"] != "suspended" {
		t.Fatalf("run = %v", view)
	}
	var key string
	for _, n := range view["nodes"].([]any) {
		if m := n.(map[string]any); m["id"] == "pause" {
			key, _ = m["suspendKey"].(string)
		}
	}
	if key == "" {
		t.Fatalf("no suspend key in %v", view["nodes"])
	}

	nodePath := v1 + "/runs/" + id + "/nodes/pause/resume"
	x.call("POST", nodePath, map[string]any{"payload": 1}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", nodePath, map[string]any{"key": "wrong"}).wantError(http.StatusConflict, "conflict")
	x.call("POST", v1+"/runs/"+id+"/nodes/start/resume", map[string]any{"key": key}).wantError(http.StatusConflict, "conflict")
	x.call("POST", v1+"/runs/ghost/nodes/pause/resume", map[string]any{"key": key}).wantError(http.StatusNotFound, "not_found")
	x.call("POST", nodePath, map[string]any{"key": key, "payload": map[string]any{"by": "test"}, "extra": 1}).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", nodePath, map[string]any{"key": key, "payload": map[string]any{"by": "test"}}).want(http.StatusAccepted)
	v := x.waitRun(runs, id)
	if v["status"] != "succeeded" {
		t.Fatalf("resumed run = %v", v)
	}
	for _, n := range v["nodes"].([]any) {
		if m := n.(map[string]any); m["id"] == "pause" && m["output"].(map[string]any)["approvedWith"].(map[string]any)["by"] != "test" {
			t.Fatalf("resume payload not delivered: %v", m)
		}
	}

	// A second run is cancelled instead.
	id2 := x.call("POST", v1+"/policies/waiter/runs", nil).want(http.StatusAccepted).obj()["runId"].(string)
	runs.until("second node to suspend", func(e events.Event) bool {
		ev, ok := e.Data.(engine.Event)
		return ok && ev.RunID == id2 && ev.Kind == engine.EventNodeSuspended
	})
	x.call("POST", v1+"/runs/"+id2+"/cancel", nil).want(http.StatusAccepted)
	if v := x.waitRun(runs, id2); v["status"] != "cancelled" {
		t.Fatalf("cancelled run = %v", v)
	}

	// In drop mode a second start while one is active is stored as dropped.
	dropDoc := "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: dropper }\nspec:\n  concurrency: { mode: drop }\n  nodes:\n" + waitingPolicy
	x.call("PUT", v1+"/policies/dropper", dropDoc, yamlBody()).want(http.StatusCreated)
	first := x.call("POST", v1+"/policies/dropper/runs", nil).want(http.StatusAccepted).obj()["runId"].(string)
	d := x.call("POST", v1+"/policies/dropper/runs", nil).wantError(http.StatusConflict, "conflict")
	if d["runId"] == "" || d["runId"] == first || d["status"] != "dropped" {
		t.Fatalf("dropped details = %v", d)
	}
	if st := x.call("GET", v1+"/runs/"+d["runId"].(string), nil).want(http.StatusOK).obj()["status"]; st != "dropped" {
		t.Fatalf("dropped run status = %v", st)
	}
}

func TestPolicyState(t *testing.T) {
	x := newSite(t)
	x.putPolicy("echo", manualPolicyNodes)
	if _, err := x.app.State.Set(x.ctx, "echo", "cooldown", map[string]any{"until": "later"}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.app.State.Set(x.ctx, "echo", "count", 3); err != nil {
		t.Fatal(err)
	}

	st := x.call("GET", v1+"/policies/echo/state", nil).want(http.StatusOK).items()
	if len(st) != 2 || st[0]["key"] != "cooldown" || st[0]["version"] == nil || st[1]["value"] != float64(3) {
		t.Fatalf("state = %v", st)
	}
	x.call("GET", v1+"/policies/ghost/state", nil).wantError(http.StatusNotFound, "not_found")

	x.call("DELETE", v1+"/policies/echo/state/count", nil).want(http.StatusNoContent)
	x.call("DELETE", v1+"/policies/echo/state/count", nil).wantError(http.StatusNotFound, "not_found")
	if n := len(x.call("GET", v1+"/policies/echo/state", nil).items()); n != 1 {
		t.Fatalf("state keys after delete = %d", n)
	}
	var found bool
	for _, a := range x.call("GET", v1+"/audit", nil).items() {
		found = found || (a["action"] == "policy.state.delete" && a["target"] == "policy:echo")
	}
	if !found {
		t.Fatal("state delete was not audited")
	}
}
