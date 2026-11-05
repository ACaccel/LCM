package app_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/signal"
)

const checkedService = `
apiVersion: lcm/v2
kind: Resource
metadata: { name: cache, labels: { tier: backend } }
spec:
  type: service
  adapter: command
  config: { start: cache-start, stop: cache-stop, status: cache-status }
  checks:
    - name: probe
      type: command
      interval: 10s
      timeout: 2s
      props: { command: ["true"] }
`

// The resource change hook must schedule a new service's checks without any
// manual sync.
func TestResourceChangeReachesMonitor(t *testing.T) {
	h := newHarness(t)
	before := h.clk.Pending()
	h.run()
	// Trigger sync, lock sweep, monitor sync, retention and reconcile each
	// arm one timer once their first pass is done.
	h.waitTimers(before + loopTimers)

	h.putService(checkedService)
	// A loop may still arm its timer after the first pass, so wait for the
	// effect rather than for a timer count.
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if _, ok := h.app.Monitor.Resource("cache"); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("monitor does not know the new service")
}

// loopTimers is the number of clock timers the loops started by Run hold
// once each has finished its first pass.
const loopTimers = 7

func TestMonitorSyncSkipsRetiredAndRestoresOnStart(t *testing.T) {
	h := newHarness(t)
	h.putService(checkedService)
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.app.Monitor.Resource("cache"); !ok {
		t.Fatal("service not monitored after sync")
	}
	rec, err := h.app.Resources.Get(h.ctx, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Manager.Type().ChangePhase(h.ctx, rec, resource.PhaseRetired, rec.Version, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.app.Monitor.Resource("cache"); ok {
		t.Error("retired service is still monitored")
	}
}

// Retiring a resource makes the monitor forget it at once and silently: its
// health must not be recomputed from the checks or native input that are
// left, and no later sync brings it back.
func TestRetireForgetsMonitorStateSilently(t *testing.T) {
	h := newHarness(t)
	h.putService(checkedService)
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.app.Monitor.SetNative("cache", model.ResourceService, nil, monitor.HealthDegraded, []string{"test"})
	if err := h.app.Monitor.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	sub := h.app.Bus.Subscribe("retire-test", 16)
	defer sub.Close()
	rec, err := h.app.Resources.Get(h.ctx, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Manager.Type().ChangePhase(h.ctx, rec, resource.PhaseRetired, rec.Version, "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.app.Monitor.Resource("cache"); ok {
		t.Fatal("retired service is still monitored before any sync")
	}
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Monitor.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case sg := <-sub.C:
			if sg.Kind == signal.KindHealth {
				t.Fatalf("retiring published %+v", sg)
			}
			continue
		default:
		}
		break
	}
	if _, ok := h.app.Monitor.Resource("cache"); ok {
		t.Fatal("sync brought the retired service back")
	}
}

// Invalid check definitions are refused when the resource is saved, so
// they never reach the monitor, and the monitored checks stay as they were.
func TestInvalidChecksNeverReachTheMonitor(t *testing.T) {
	h := newHarness(t)
	h.putService(checkedService)
	_, err := h.app.Resources.Put(h.ctx, []byte(`
apiVersion: lcm/v2
kind: Resource
metadata: { name: mixed }
spec:
  type: service
  adapter: command
  config: { start: s, stop: s, status: s }
  checks:
    - { name: broken, type: http, interval: 10s, timeout: 2s, props: { url: "not a url" } }
    - { name: fine, type: command, interval: 10s, timeout: 2s, props: { command: ["true"] } }
`), 0)
	var issues model.Issues
	if !errors.As(err, &issues) || len(issues) != 1 || !strings.HasPrefix(issues[0].Path, "spec.checks[0]") {
		t.Fatalf("save with an invalid check = %v", err)
	}
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, ok := h.app.Monitor.Resource("mixed"); ok {
		t.Error("refused resource is monitored")
	}
	if st, ok := h.app.Monitor.Resource("cache"); !ok || len(st.Checks) != 1 {
		t.Fatalf("cache status = %+v", st)
	}
}

// The first retention pass runs when the app starts and removes what the
// configured limits no longer keep.
func TestRetentionJobsPruneOldData(t *testing.T) {
	h := newHarness(t, func(o *testOpts) {
		o.configure = func(c *config.Config) {
			c.RunRetentionKeep = 1
			c.RunRetentionAge = 24 * time.Hour
			c.TimelineRetention = 90 * 24 * time.Hour
			c.VersionKeep = 1
			c.FailedLoginKeep = 1
		}
	})
	h.deploy(`
apiVersion: lcm/v2
kind: Policy
metadata: { name: tick }
spec:
  nodes:
    - { id: t, type: trigger.manual, next: { out: [d] } }
    - { id: d, type: data.set, props: { values: { ok: true } } }
`)
	seen := map[string]bool{}
	for range 3 {
		id, err := h.app.Engine.Start(h.ctx, "tick", "t", map[string]any{}, engine.ModeLive)
		if err != nil {
			t.Fatal(err)
		}
		h.waitRun("tick", seen)
		_ = id
	}
	if err := h.app.Catalog.AppendTimeline(h.ctx, catalog.Entry{
		Resource: "ghost", Kind: catalog.KindConfig, Summary: "ancient", Time: epoch.Add(-100 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Catalog.AppendTimeline(h.ctx, catalog.Entry{Resource: "ghost", Kind: catalog.KindConfig, Summary: "recent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Locks.TryAcquire(h.ctx, "short", "someone", time.Second); err != nil {
		t.Fatal(err)
	}
	for version, port := range []string{"a", "b", "c"} {
		if _, err := h.app.Modules.Put(h.ctx, []byte(`
apiVersion: lcm/v2
kind: Module
metadata: { name: kept }
spec:
  nodes:
    - { id: in, type: module.input, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: `+port+` } }
`), version); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"mallory", "eve"} {
		attempt := auth.WithActor(h.ctx, auth.Actor{Kind: auth.KindUser, Name: name})
		if err := h.app.Audit.Record(attempt, "auth.login", "", audit.OutcomeDenied, nil); err != nil {
			t.Fatal(err)
		}
	}

	h.advance(48 * time.Hour)
	base := h.clk.Pending()
	h.run()
	h.waitTimers(base + loopTimers)

	count := func(query string) int {
		var n int
		if err := h.app.DB.Reader().QueryRowContext(context.Background(), query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Other loops can register their timers before the retention pass has
	// finished, so wait for its effects rather than for a timer count alone.
	counts := func() (runs, timeline, locks, modules, logins int) {
		return count(`SELECT COUNT(*) FROM runs`),
			count(`SELECT COUNT(*) FROM timeline WHERE resource = 'ghost'`),
			count(`SELECT COUNT(*) FROM locks`),
			count(`SELECT COUNT(*) FROM module_versions WHERE name = 'kept'`),
			count(`SELECT COUNT(*) FROM audit_log WHERE action = 'auth.login' AND outcome = 'denied'`)
	}
	deadline := time.Now().Add(waitTimeout)
	runs, timeline, locks, modules, logins := counts()
	for (runs != 1 || timeline != 1 || locks != 0 || modules != 1 || logins != 1) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		runs, timeline, locks, modules, logins = counts()
	}
	if runs != 1 {
		t.Errorf("runs left = %d, want 1", runs)
	}
	if timeline != 1 {
		t.Errorf("timeline entries left = %d, want 1 (the recent one)", timeline)
	}
	if locks != 0 {
		t.Errorf("expired locks left = %d", locks)
	}
	if modules != 1 {
		t.Errorf("module versions left = %d, want 1", modules)
	}
	if logins != 1 {
		t.Errorf("failed logins left = %d, want 1", logins)
	}
}

// The reconcile loop keeps lifecycle states true after startup: a service
// started outside LCM is adopted as running, and one that stops outside LCM
// is marked failed, without any action.
func TestReconcileLoopFollowsTheRuntime(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	h := newHarness(t, func(o *testOpts) {
		o.runner.On(func(c execx.Cmd) bool { return strings.Contains(c.Display(), "app-status") }, func(execx.Cmd) (execx.Result, error) {
			if active.Load() {
				return execx.Result{}, nil
			}
			return execx.Result{ExitCode: 3}, &execx.ExitError{Code: 3}
		})
	})
	h.putService(`
apiVersion: lcm/v2
kind: Resource
metadata: { name: app }
spec:
  type: service
  adapter: command
  config: { start: app-start, stop: app-stop, status: app-status }
`)
	state := func() catalog.State {
		rec, err := h.app.Catalog.Get(h.ctx, "app")
		if err != nil {
			t.Fatal(err)
		}
		return rec.State
	}
	waitState := func(want catalog.State) {
		t.Helper()
		deadline := time.Now().Add(waitTimeout)
		for state() != want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := state(); got != want {
			t.Fatalf("state = %s, want %s", got, want)
		}
	}

	base := h.clk.Pending()
	h.run()
	h.waitTimers(base + loopTimers)
	waitState(catalog.StateRunning)

	active.Store(false)
	h.clk.Advance(time.Minute)
	waitState(catalog.StateFailed)
}

// Policies compile against the module store, and the deployed version keeps
// the module it was compiled with.
func TestPolicyUsesModuleStore(t *testing.T) {
	h := newHarness(t)
	const policy = `
apiVersion: lcm/v2
kind: Policy
metadata: { name: uses-module }
spec:
  nodes:
    - { id: t, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: pass-through } }
`
	if _, err := h.app.Engine.Deploy(h.ctx, []byte(policy), engine.AnyVersion); err == nil {
		t.Fatal("deploy succeeded without the module")
	}
	if _, err := h.app.Modules.Put(h.ctx, []byte(`
apiVersion: lcm/v2
kind: Module
metadata: { name: pass-through }
spec:
  nodes:
    - { id: in, type: module.input, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: out } }
`), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Engine.Deploy(h.ctx, []byte(policy), engine.AnyVersion); err != nil {
		t.Fatalf("deploy with module: %v", err)
	}
}

// The console and the API share one listener; neither may shadow the other.
func TestHandlerServesConsoleAndAPI(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer(h.app.Handler())
	t.Cleanup(srv.Close)

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/api/v1/health"); code != http.StatusOK || !strings.Contains(body, `"status"`) {
		t.Fatalf("health = %d %q", code, body)
	}
	if code, body := get("/api/v1/unknown"); code != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Fatalf("unknown API path = %d %q", code, body)
	}
	// Built or not, a client-side route answers with HTML, not the API's
	// JSON error.
	code, body := get("/resources/web")
	if (code != http.StatusOK && code != http.StatusServiceUnavailable) || !strings.Contains(body, "<html") {
		t.Fatalf("console route = %d %q", code, body)
	}
}
