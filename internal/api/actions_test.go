package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/execx"
)

func TestActionLifecycle(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", "  backup: { command: backup-web.sh }\n  jobs:\n    reindex: { command: reindex-web.sh }\n"))
	acts := x.collect(events.TypeAction)

	// A dry run returns the plan and changes nothing.
	plan := x.call("POST", v1+"/services/web/actions/start?dryRun=true", nil).want(http.StatusOK).obj()
	if plan["from"] != "registered" || plan["to"] != "running" || len(plan["steps"].([]any)) == 0 {
		t.Fatalf("plan = %v", plan)
	}
	if n := len(x.runner.Calls()); n != 0 {
		t.Fatalf("dry run executed %d commands", n)
	}

	// The real action answers at once and finishes in the background.
	id := x.startAction("web", "start", nil)
	done := x.actionDone(acts, id)
	if done["status"] != "succeeded" || done["error"] != "" || done["finishedAt"] == nil || done["actor"].(map[string]any)["name"] != "admin-token" {
		t.Fatalf("start action = %v", done)
	}
	res := done["result"].(map[string]any)
	if res["from"] != "registered" || res["to"] != "running" || res["action"] != "start" {
		t.Fatalf("result = %v", res)
	}
	if st := x.call("GET", v1+"/services/web", nil).obj()["state"]; st != "running" {
		t.Fatalf("state after start = %v", st)
	}

	// Allowed in the new state, refused in the wrong one.
	x.call("POST", v1+"/services/web/actions/start", nil).wantError(http.StatusConflict, "invalid_state")
	x.call("POST", v1+"/services/web/actions/restart?dryRun=true", nil).want(http.StatusOK)

	for _, step := range []struct {
		action string
		body   any
		to     string
	}{
		{"restart", nil, "running"},
		{"backup", nil, "running"},
		{"run-job", map[string]string{"job": "reindex"}, "running"},
		{"stop", nil, "stopped"},
	} {
		id := x.startAction("web", step.action, step.body)
		if got := x.actionDone(acts, id); got["status"] != "succeeded" || got["result"].(map[string]any)["to"] != step.to {
			t.Fatalf("%s = %v", step.action, got)
		}
	}
	if !contains(x.runner.Displays(), "reindex-web.sh") || !contains(x.runner.Displays(), "backup-web.sh") {
		t.Fatalf("commands = %v", x.runner.Displays())
	}

	// The outcome is on the timeline and in the audit log.
	tl := x.call("GET", v1+"/services/web/timeline?kinds=action,backup,job", nil).want(http.StatusOK).items()
	if len(tl) < 4 {
		t.Fatalf("timeline = %v", tl)
	}
	audits := x.call("GET", v1+"/audit?limit=100", nil).want(http.StatusOK).items()
	var restart bool
	for _, a := range audits {
		restart = restart || (a["action"] == "service.restart" && a["target"] == "web" && a["actor"].(map[string]any)["kind"] == "token")
	}
	if !restart {
		t.Fatalf("no audit entry for restart: %v", audits)
	}
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestActionErrors(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	acts := x.collect(events.TypeAction)

	x.call("POST", v1+"/services/web/actions/explode", nil).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/services/ghost/actions/start", nil).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/services/web/actions/start", `{"nope":1}`).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/services/web/actions/start?dryRun=maybe", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/services/web/actions/start", map[string]string{"version": "abc1234"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/services/web/actions/start", map[string]string{"job": "x"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/services/web/actions/run-job", nil).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/services/web/actions/run-job", map[string]string{"job": "ghost"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	// Missing configuration is a precondition, not a bad request.
	x.call("POST", v1+"/services/web/actions/backup", nil).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", v1+"/services/web/actions/deploy", nil).wantError(http.StatusConflict, "precondition_failed")
	x.createService(serviceDoc("app", "  source: { repo: /srv/app, branch: main }\n"))
	x.call("POST", v1+"/services/app/actions/deploy", map[string]string{"version": "nothex!"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/services/app/actions/rollback", nil).wantError(http.StatusConflict, "invalid_state")
	plan := x.call("POST", v1+"/services/app/actions/deploy?dryRun=true", map[string]string{"version": "abc1234"}).want(http.StatusOK).obj()
	if plan["to"] != "running" || len(plan["steps"].([]any)) < 3 {
		t.Fatalf("deploy plan = %v", plan)
	}
	x.call("GET", v1+"/actions/act_unknown", nil).wantError(http.StatusNotFound, "not_found")

	// A held lock is reported before anything starts.
	if _, err := x.app.Locks.TryAcquire(x.ctx, "service:web", "someone-else", time.Hour); err != nil {
		t.Fatal(err)
	}
	d := x.call("POST", v1+"/services/web/actions/start", nil).wantError(http.StatusConflict, "lock_held")
	if d["owner"] != "someone-else" || d["lock"] != "service:web" {
		t.Fatalf("lock details = %v", d)
	}
	x.call("POST", v1+"/services/web/actions/start?dryRun=true", nil).want(http.StatusOK)
	if _, err := x.app.Locks.Release(x.ctx, "service:web", "someone-else"); err != nil {
		t.Fatal(err)
	}

	// A failing command is reported through the tracker.
	x.runner.On(func(c execx.Cmd) bool { return strings.Contains(c.Display(), "web-start") },
		func(execx.Cmd) (execx.Result, error) {
			return execx.Result{ExitCode: 3, Output: "boom"}, &execx.ExitError{Code: 3}
		})
	id := x.startAction("web", "start", nil)
	got := x.actionDone(acts, id)
	if got["status"] != "failed" || got["error"] == "" || got["result"] == nil {
		t.Fatalf("failed action = %v", got)
	}
}
