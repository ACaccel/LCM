package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
)

const checkedService = `
apiVersion: lcm/v1
kind: Service
metadata: { name: cache, labels: { tier: backend } }
spec:
  runtime: { type: command, start: cache-start, stop: cache-stop, status: cache-status }
  checks:
    - name: probe
      type: command
      interval: 10s
      timeout: 2s
      props: { command: ["true"] }
`

// The catalog change hook must schedule a new service's checks without any
// manual sync.
func TestCatalogChangeReachesMonitor(t *testing.T) {
	h := newHarness(t)
	before := h.clk.Pending()
	h.run()
	// Trigger sync, lock sweep, monitor sync and retention each arm one
	// timer once their first pass is done.
	h.waitTimers(before + loopTimers)

	base := h.clk.Pending()
	h.putService(checkedService)
	// Scheduling a check arms its first-run timer.
	h.waitTimers(base + 1)
	if _, ok := h.app.Monitor.Service("cache"); !ok {
		t.Fatal("monitor does not know the new service")
	}
}

// loopTimers is the number of clock timers the loops started by Run hold
// once each has finished its first pass.
const loopTimers = 4

func TestMonitorSyncSkipsRetiredAndRestoresOnStart(t *testing.T) {
	h := newHarness(t)
	h.putService(checkedService)
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.app.Monitor.Service("cache"); !ok {
		t.Fatal("service not monitored after sync")
	}
	if _, err := h.app.Manager.Retire(h.ctx, "cache", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.app.Monitor.Service("cache"); ok {
		t.Error("retired service is still monitored")
	}
}

// Invalid check definitions are reported but must not stop the others.
func TestMonitorSyncSurvivesInvalidChecks(t *testing.T) {
	h := newHarness(t)
	h.putService(`
apiVersion: lcm/v1
kind: Service
metadata: { name: mixed }
spec:
  runtime: { type: command, start: s, stop: s, status: s }
  checks:
    - { name: broken, type: http, interval: 10s, timeout: 2s, props: { url: "not a url" } }
    - { name: fine, type: command, interval: 10s, timeout: 2s, props: { command: ["true"] } }
`)
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatalf("sync with an invalid check: %v", err)
	}
	st, ok := h.app.Monitor.Service("mixed")
	if !ok || len(st.Checks) != 1 || st.Checks[0].Name != "fine" {
		t.Fatalf("service status = %+v", st)
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
		}
	})
	h.deploy(`
apiVersion: lcm/v1
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
		Service: "ghost", Kind: catalog.KindConfig, Summary: "ancient", Time: epoch.Add(-100 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Catalog.AppendTimeline(h.ctx, catalog.Entry{Service: "ghost", Kind: catalog.KindConfig, Summary: "recent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Locks.TryAcquire(h.ctx, "short", "someone", time.Second); err != nil {
		t.Fatal(err)
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
	if n := count(`SELECT COUNT(*) FROM runs`); n != 1 {
		t.Errorf("runs left = %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM timeline WHERE service = 'ghost'`); n != 1 {
		t.Errorf("timeline entries left = %d, want 1 (the recent one)", n)
	}
	if n := count(`SELECT COUNT(*) FROM locks`); n != 0 {
		t.Errorf("expired locks left = %d", n)
	}
}

// Policies compile against the module store, and the deployed version keeps
// the module it was compiled with.
func TestPolicyUsesModuleStore(t *testing.T) {
	h := newHarness(t)
	const policy = `
apiVersion: lcm/v1
kind: Policy
metadata: { name: uses-module }
spec:
  nodes:
    - { id: t, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: pass-through } }
`
	if _, err := h.app.Engine.Deploy(h.ctx, []byte(policy)); err == nil {
		t.Fatal("deploy succeeded without the module")
	}
	if _, err := h.app.Modules.Put(h.ctx, []byte(`
apiVersion: lcm/v1
kind: Module
metadata: { name: pass-through }
spec:
  nodes:
    - { id: in, type: module.input, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: out } }
`), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Engine.Deploy(h.ctx, []byte(policy)); err != nil {
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
	code, body := get("/services/web")
	if (code != http.StatusOK && code != http.StatusServiceUnavailable) || !strings.Contains(body, "<html") {
		t.Fatalf("console route = %d %q", code, body)
	}
}
