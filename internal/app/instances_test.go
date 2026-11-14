package app_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/monitor"
)

// instanceHelper adds instances to the fake deploy helper: each instance's
// enablement and state, the aggregate status, and a restore after which the
// service does not come up when downAfterRestore is set.
type instanceHelper struct {
	*fakeDeployHelper
	mu               sync.Mutex
	enabled          map[string]bool
	downAfterRestore bool
	restored         bool
}

func newInstanceHelper() *instanceHelper {
	ih := &instanceHelper{fakeDeployHelper: newFakeDeployHelper(), enabled: map[string]bool{}}
	base := ih.Handle
	ih.Handle = func(req deployhelper.Request) (deployhelper.Response, error) {
		if resp, ok := ih.handle(req); ok {
			return resp, nil
		}
		return base(req)
	}
	return ih
}

func (ih *instanceHelper) handle(req deployhelper.Request) (deployhelper.Response, bool) {
	ih.mu.Lock()
	defer ih.mu.Unlock()
	resp := deployhelper.Response{Version: deployhelper.Version, Op: req.Op, OperationID: req.OperationID}
	if req.Op.Changes() {
		resp.State = deployhelper.StateSucceeded
	}
	switch {
	case req.Op == deployhelper.OpActivate:
		for _, in := range req.Instances {
			if _, seen := ih.enabled[in.Name]; !seen {
				ih.enabled[in.Name] = true
			}
		}
		return resp, false
	case req.Op == deployhelper.OpRestore:
		ih.restored = true
		return resp, false
	case req.Op == deployhelper.OpStatus && len(ih.enabled) == 0:
		state := "active"
		if ih.downAfterRestore && ih.restored {
			state = "failed"
		}
		resp.Unit = &deployhelper.UnitStatus{LoadState: "loaded", ActiveState: state, SubState: "running"}
		return resp, true
	case req.Op == deployhelper.OpStatus:
		resp.Unit = &deployhelper.UnitStatus{LoadState: "loaded", ActiveState: "active", UnitFileState: "enabled"}
		ports := map[string]int{"a": 4100, "c": 4102}
		for _, n := range []string{"a", "b", "c"} {
			st, file := "inactive", "disabled"
			if ih.enabled[n] {
				st, file = "active", "enabled"
			}
			resp.Instances = append(resp.Instances, deployhelper.InstanceStatus{Name: n, Port: ports[n], LoadState: "loaded",
				ActiveState: st, SubState: "running", UnitFileState: file, MainPID: 10, Granted: true})
		}
		return resp, true
	case req.Instance != "":
		ih.enabled[req.Instance] = req.Op != deployhelper.OpStop
		return resp, true
	}
	return resp, false
}

// apiPost sends an admin request with a JSON body and returns the status
// and decoded body.
func apiPost(t *testing.T, h *harness, srv *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	_, token, err := h.app.Auth.CreateToken(h.ctx, fmt.Sprintf("post-%d", time.Now().UnixNano()), auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestManagedInstancesThroughTheApp: a managed service with three
// instances is deployed with resolved instances, acts on one instance at a
// time, and an instance an operator stopped pauses its check, so that the
// service stays healthy and running and the auto-deploy health condition
// still passes.
func TestManagedInstancesThroughTheApp(t *testing.T) {
	helper := newInstanceHelper()
	runner := &execx.Fake{}
	runner.On(func(c execx.Cmd) bool { return slices.Contains(c.Argv, "rev-parse") }, func(execx.Cmd) (execx.Result, error) {
		return execx.Result{Output: "1111111111111111111111111111111111111111\n"}, nil
	})
	h := newHarness(t, func(o *testOpts) { o.deployHelper, o.runner = helper, runner })
	// Let each real HTTP probe finish before virtual time can reach its
	// timeout; a single ten-second jump races the asynchronous probe.
	advanceCheck := func() {
		for range int(checkInterval / time.Second) {
			h.advance(time.Second)
		}
	}
	bots := map[string]*switchable{"a": newSwitchable(t), "b": newSwitchable(t), "c": newSwitchable(t)}
	h.putService(fmt.Sprintf(`
apiVersion: lcm/v2
kind: Resource
metadata: { name: fleet }
spec:
  type: service
  adapter: managed
  config:
    source: { url: https://example.com/fleet.git, branch: main, build: npm ci && npm run build }
    artifact: dist
    runtime: node24
    start: bot.js
    limits: { memoryMax: 512M }
    instances:
      a: {}
      b: { start: archive.js, limits: { memoryMax: 1G } }
      c: { startArgs: [--quiet] }
  checks:
    - { name: a-http, instance: a, type: http, interval: %[1]s, timeout: 2s, props: { url: %[2]q } }
    - { name: b-http, instance: b, type: http, interval: %[1]s, timeout: 2s, props: { url: %[3]q } }
    - { name: c-http, instance: c, type: http, interval: %[1]s, timeout: 2s, props: { url: %[4]q } }
`, checkInterval, bots["a"].srv.URL, bots["b"].srv.URL, bots["c"].srv.URL))
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()

	if _, err := h.app.Manager.Deploy(h.ctx, "fleet", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	var act deployhelper.Request
	for _, r := range helper.Requests() {
		if r.Op == deployhelper.OpActivate {
			act = r
		}
	}
	if act.Start != nil || len(act.Instances) != 3 || !slices.Equal(act.Instances[1].Start, []string{"archive.js"}) ||
		act.Instances[1].Limits.MemoryMax != "1G" || !slices.Equal(act.Instances[2].Start, []string{"bot.js", "--quiet"}) {
		t.Fatalf("activate = %+v", act)
	}
	if _, err := h.app.Manager.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	advanceCheck()
	if st, _ := h.app.Monitor.Resource("fleet"); st.Health != monitor.HealthHealthy {
		t.Fatalf("health = %+v", st)
	}

	// One instance restarted: on the timeline and in the audit trail.
	res, err := h.app.Manager.Restart(h.ctx, "fleet", catalog.ActionOptions{Instance: "a"})
	if err != nil || res.Instance != "a" || res.To != catalog.StateRunning {
		t.Fatalf("restart a = %+v, %v", res, err)
	}
	if !h.timelineHas("fleet", func(e catalog.Entry) bool { return e.Summary == "restart a succeeded" }) {
		t.Fatal("no timeline entry for the instance restart")
	}
	audited := false
	for _, e := range h.auditEntries() {
		audited = audited || e.Action == "service.restart" && e.Details["instance"] == "a"
	}
	if !audited {
		t.Fatal("no audit entry names the instance")
	}

	// b stopped by an operator: its check is paused, the service stays
	// healthy and running.
	start := h.clk.Now().UnixMilli()
	if _, err := h.app.Manager.Stop(h.ctx, "fleet", catalog.ActionOptions{Instance: "b"}); err != nil {
		t.Fatal(err)
	}
	bots["b"].healthy.Store(false)
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	advanceCheck()
	st, _ := h.app.Monitor.Resource("fleet")
	if st.Health != monitor.HealthHealthy {
		t.Fatalf("health with b stopped = %+v", st)
	}
	for _, c := range st.Checks {
		if c.Name == "b-http" && (!c.Paused || c.Instance != "b") {
			t.Fatalf("b-http = %+v", c)
		}
	}
	// A later reconciliation, which sees b disabled, keeps the service
	// running.
	if changes, err := h.app.Manager.Reconcile(h.ctx); err != nil || len(changes) != 0 {
		t.Fatalf("reconcile = %+v, %v", changes, err)
	}
	if rec, _ := h.app.Catalog.Get(h.ctx, "fleet"); rec.State != catalog.StateRunning {
		t.Fatalf("state = %s", rec.State)
	}

	// The detail lists the instances with their state, health and actions.
	body := apiCall(t, h, srv, "GET", "/api/v1/resources/fleet")
	instances := body["service"].(map[string]any)["instances"].([]any)
	if len(instances) != 3 {
		t.Fatalf("instances = %v", instances)
	}
	b := instances[1].(map[string]any)
	if b["name"] != "b" || b["enabled"] != false || b["stopped"] != true || b["state"] != "inactive" || b["port"] != nil {
		t.Fatalf("b = %v", b)
	}
	a := instances[0].(map[string]any)
	if a["port"] != float64(4100) || a["health"] != "healthy" || a["checks"].([]any)[0] != "a-http" {
		t.Fatalf("a = %v", a)
	}
	// Stopping c, the last enabled instance after a, is still allowed; a
	// would then be the last one.
	if code, _ := apiPost(t, h, srv, "/api/v1/resources/fleet/actions/stop?dryRun=true", map[string]any{"instance": "c"}); code != http.StatusOK {
		t.Fatalf("dry run of stop c = %d", code)
	}

	// The auto-deploy template's health condition passes with b stopped:
	// the resource variable leaves the paused check out.
	h.deploy(fmt.Sprintf(`
apiVersion: lcm/v2
kind: Policy
metadata: { name: fleet-healthy }
spec:
  nodes:
    - { id: t, type: trigger.manual, next: { out: [wait] } }
    - id: wait
      type: wait.until
      props:
        interval: 10s
        timeout: 2m
        condition: 'inputs.svc.health == "healthy" && all(values(inputs.svc.checks), {!.critical || .lastResultMs > inputs.since}) && !("b-http" in inputs.svc.checks)'
        inputs: { svc: "{{ resource['fleet'] }}", since: %d }
      next: { out: [done], timeout: [late] }
    - { id: done, type: data.set, props: { values: { ok: true } } }
    - { id: late, type: data.set, props: { values: { ok: false } } }
`, start))
	if _, err := h.app.Engine.Start(h.ctx, "fleet-healthy", "t", map[string]any{}, engine.ModeLive); err != nil {
		t.Fatal(err)
	}
	view := h.waitRun("fleet-healthy", map[string]bool{})
	if n, _ := view.Node("done"); view.Status != engine.RunSucceeded || n.Status != engine.NodeCompleted {
		t.Fatalf("run = %s: %+v", view.Status, view)
	}
}

// TestRestoreUsersThroughTheApp: a restore of MongoDB users is confirmed
// with the service's name, and when the restored service does not come up
// the undo restores the users it replaced as well.
func TestRestoreUsersThroughTheApp(t *testing.T) {
	helper := newInstanceHelper()
	runner := &execx.Fake{}
	runner.On(func(c execx.Cmd) bool { return slices.Contains(c.Argv, "rev-parse") }, func(execx.Cmd) (execx.Result, error) {
		return execx.Result{Output: "1111111111111111111111111111111111111111\n"}, nil
	})
	h := newHarness(t, func(o *testOpts) { o.deployHelper, o.runner = helper, runner })
	h.putService(managedWeb + `    backup:
      sources:
        - { type: mongodb, all: true, users: true }
`)
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()
	if _, err := h.app.Manager.Deploy(h.ctx, "web", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	b, err := h.app.Manager.Backup(h.ctx, "web", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if code, _ := apiPost(t, h, srv, "/api/v1/resources/web/actions/restore", map[string]any{"restoreUsers": true, "confirm": "wrong"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("restore with a wrong confirmation = %d", code)
	}
	code, plan := apiPost(t, h, srv, "/api/v1/resources/web/actions/restore?dryRun=true", map[string]any{"restoreUsers": true, "confirm": "web"})
	if code != http.StatusOK {
		t.Fatalf("dry run = %d %v", code, plan)
	}
	var codes []string
	for _, m := range plan["stepMessages"].([]any) {
		codes = append(codes, m.(map[string]any)["code"].(string))
	}
	if !slices.Contains(codes, "step.restoreUsers") {
		t.Fatalf("plan = %v", codes)
	}

	helper.mu.Lock()
	helper.downAfterRestore = true
	helper.mu.Unlock()
	n := len(helper.Requests())
	// The API starts the same manager call in the background; calling it
	// here keeps the test synchronous.
	res, err := h.app.Manager.Restore(h.ctx, "web", catalog.ActionOptions{BackupID: b.BackupID, RestoreUsers: true})
	if !errors.Is(err, catalog.ErrRestoreUndone) || !res.RestoreUsers {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	var restores []deployhelper.Request
	for _, r := range helper.Requests()[n:] {
		if r.Op == deployhelper.OpRestore {
			restores = append(restores, r)
		}
	}
	if len(restores) != 2 || !restores[0].RestoreUsers || !restores[1].RestoreUsers || restores[0].BackupID != b.BackupID {
		t.Fatalf("restores = %+v", restores)
	}
}
