package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/signal"
)

const checkInterval = 10 * time.Second

func (h *harness) putService(doc string) {
	h.t.Helper()
	if _, err := h.app.Catalog.Put(h.ctx, []byte(doc), 0); err != nil {
		h.t.Fatalf("put service: %v", err)
	}
}

func (h *harness) deploy(doc string) {
	h.t.Helper()
	if _, err := h.app.Engine.Deploy(h.ctx, []byte(doc)); err != nil {
		h.t.Fatalf("deploy policy: %v", err)
	}
}

func (h *harness) syncTriggers() {
	h.t.Helper()
	if err := h.app.Triggers.Sync(h.ctx); err != nil {
		h.t.Fatalf("trigger sync: %v", err)
	}
}

func (h *harness) timeline(service string) []catalog.Entry {
	h.t.Helper()
	page, err := h.app.Catalog.Timeline(h.ctx, catalog.TimelineQuery{Service: service, Limit: 200})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Entries
}

// timelineHas reports whether a timeline entry of the service satisfies pred.
func (h *harness) timelineHas(service string, pred func(catalog.Entry) bool) bool {
	for _, e := range h.timeline(service) {
		if pred(e) {
			return true
		}
	}
	return false
}

func (h *harness) auditEntries() []audit.Entry {
	h.t.Helper()
	entries, err := h.app.Audit.List(h.ctx, audit.MaxLimit, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return entries
}

// switchable is a service endpoint that tests can turn healthy or failing.
type switchable struct {
	srv     *httptest.Server
	healthy atomic.Bool
}

func newSwitchable(t *testing.T) *switchable {
	t.Helper()
	s := &switchable{}
	s.healthy.Store(true)
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if s.healthy.Load() {
			_, _ = io.WriteString(w, "ok")
			return
		}
		http.Error(w, "broken", http.StatusInternalServerError)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func matchCmd(sub string) func(execx.Cmd) bool {
	return func(c execx.Cmd) bool { return strings.Contains(c.Display(), sub) }
}

func count(cmds []string, sub string) int {
	n := 0
	for _, c := range cmds {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// TestSelfHealing is the "service goes down, policy restarts it" scenario:
// a failing HTTP check turns the health down, the health signal triggers a
// policy, and the policy restarts the service through the command runtime.
func TestSelfHealing(t *testing.T) {
	h := newHarness(t)
	web := newSwitchable(t)

	// The restart command repairs the endpoint, as a real restart would.
	h.runner.On(matchCmd("web-restart"), func(execx.Cmd) (execx.Result, error) {
		web.healthy.Store(true)
		return execx.Result{}, nil
	})

	h.putService(fmt.Sprintf(`
apiVersion: lcm/v1
kind: Service
metadata: { name: web, labels: { tier: frontend } }
spec:
  runtime:
    type: command
    start: web-start
    stop: web-stop
    restart: web-restart
    status: web-status
  checks:
    - name: http
      type: http
      interval: %s
      timeout: 2s
      props: { url: %q }
`, checkInterval, web.srv.URL))
	h.deploy(`
apiVersion: lcm/v1
kind: Policy
metadata: { name: self-heal }
spec:
  nodes:
    - { id: t, type: trigger.signal, props: { kinds: [health.changed], statuses: [down] }, next: { out: [fix] } }
    - { id: fix, type: lifecycle.restart, props: { service: "{{ trigger.service }}" } }
`)

	if _, err := h.app.Manager.Start(h.ctx, "web", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	h.syncTriggers()
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}

	// The first probe runs within the start spread; the service becomes healthy.
	h.advance(checkInterval)
	if st, ok := h.app.Monitor.Service("web"); !ok || st.Health != monitor.HealthHealthy {
		t.Fatalf("health after first probe = %+v", st)
	}

	web.healthy.Store(false)
	h.advance(checkInterval)

	seen := map[string]bool{}
	view := h.waitRun("self-heal", seen)
	if view.Status != engine.RunSucceeded {
		t.Fatalf("self-heal run = %s: %+v", view.Status, view)
	}
	if n := count(h.runner.Displays(), "web-restart"); n != 1 {
		t.Fatalf("restart commands = %d in %v", n, h.runner.Displays())
	}
	rec, err := h.app.Catalog.Get(h.ctx, "web")
	if err != nil || rec.State != catalog.StateRunning {
		t.Fatalf("lifecycle state after restart = %v, %v", rec, err)
	}

	// The restart fixed the endpoint; the next probe sees it.
	h.advance(checkInterval)
	if st, _ := h.app.Monitor.Service("web"); st.Health != monitor.HealthHealthy {
		t.Fatalf("health after recovery = %s", st.Health)
	}

	// The follower writes asynchronously; closing it drains the buffer, so
	// the timeline can be read deterministically after Close.
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	h.assertSelfHealTrail(view.ID)
}

func (h *harness) assertSelfHealTrail(runID string) {
	h.t.Helper()
	// Reading needs an open store; reopen on the same data dir.
	h.reopen(clock.NewFake(epoch.Add(time.Hour)))
	var sawCheckDown, sawHealthDown, sawRestart bool
	for _, e := range h.timeline("web") {
		signalData, _ := e.Data["data"].(map[string]any)
		switch {
		case e.Kind == string(signal.KindCheck) && signalData["status"] == "down":
			sawCheckDown = true
		case e.Kind == string(signal.KindHealth) && signalData["health"] == "down":
			sawHealthDown = true
		case e.Kind == catalog.KindAction && e.Ref == runID && e.Data["action"] == "restart":
			sawRestart = true
		}
	}
	if !sawCheckDown || !sawHealthDown || !sawRestart {
		h.t.Errorf("timeline lacks entries (check down %v, health down %v, restart action %v): %+v",
			sawCheckDown, sawHealthDown, sawRestart, h.timeline("web"))
	}

	var restartAudit *audit.Entry
	for _, e := range h.auditEntries() {
		if e.Action == "service.restart" && e.Target == "web" {
			restartAudit = &e
			break
		}
	}
	switch {
	case restartAudit == nil:
		h.t.Errorf("no audit entry for the restart: %+v", h.auditEntries())
	case restartAudit.Actor.Kind != auth.KindSystem || restartAudit.Actor.ID != "run:"+runID || restartAudit.Actor.Name != "policy self-heal":
		h.t.Errorf("restart audited as %+v, want the policy run", restartAudit.Actor)
	}
}

// TestScheduledBackup is the "nightly backup, then tell someone" scenario.
func TestScheduledBackup(t *testing.T) {
	h := newHarness(t)

	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()

	h.runner.Reply("backup-db.sh", "dump written\n", 0)
	h.putService(`
apiVersion: lcm/v1
kind: Service
metadata: { name: db }
spec:
  runtime: { type: command, start: db-start, stop: db-stop, status: db-status }
  backup: { command: backup-db.sh }
`)
	h.deploy(fmt.Sprintf(`
apiVersion: lcm/v1
kind: Policy
metadata: { name: nightly-backup }
spec:
  nodes:
    - { id: t, type: trigger.cron, props: { schedule: "0 3 * * *", timezone: UTC }, next: { out: [backup] } }
    - { id: backup, type: lifecycle.backup, props: { service: db }, next: { out: [tell] } }
    - id: tell
      type: notify
      props: { target: webhook, url: %q, message: "backup of {{ nodes.backup.service }} finished" }
`, hook.URL))

	base := h.clk.Pending()
	h.syncTriggers()
	h.waitTimers(base + 1)

	// The clock starts at 12:00; the next 03:00 is 15 hours away.
	h.clk.Advance(14 * time.Hour)
	mu.Lock()
	early := len(bodies)
	mu.Unlock()
	if early != 0 || count(h.runner.Displays(), "backup-db.sh") != 0 {
		t.Fatalf("backup ran before its schedule: %v", h.runner.Displays())
	}
	h.clk.Advance(time.Hour)

	view := h.waitRun("nightly-backup", map[string]bool{})
	if view.Status != engine.RunSucceeded {
		t.Fatalf("run = %s: %+v", view.Status, view)
	}
	if n := count(h.runner.Displays(), "backup-db.sh"); n != 1 {
		t.Fatalf("backup commands = %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "backup of db finished") {
		t.Fatalf("notifications = %q", bodies)
	}
	if !h.timelineHas("db", func(e catalog.Entry) bool { return e.Kind == catalog.KindBackup && e.Ref == view.ID }) {
		t.Errorf("timeline lacks the backup: %+v", h.timeline("db"))
	}
}

// TestRecoveryResumesSuspendedRun restarts the whole app while a run waits.
func TestRecoveryResumesSuspendedRun(t *testing.T) {
	h := newHarness(t)
	h.deploy(`
apiVersion: lcm/v1
kind: Policy
metadata: { name: slow }
spec:
  nodes:
    - { id: t, type: trigger.manual, next: { out: [pause] } }
    - { id: pause, type: flow.wait, props: { duration: 1h }, next: { out: [done] } }
    - { id: done, type: data.set, props: { values: { finished: true } } }
`)
	runID, err := h.app.Engine.Start(h.ctx, "slow", "t", map[string]any{}, engine.ModeLive)
	if err != nil {
		t.Fatal(err)
	}
	h.events.WaitFor(t, "run suspended", func(e engine.Event) bool {
		return e.Kind == engine.EventRunSuspended && e.RunID == runID
	})

	// Thirty minutes pass while the process is down.
	restarted := clock.NewFake(epoch.Add(30 * time.Minute))
	h.reopen(restarted)

	// The wait timer is re-armed from the stored resume time.
	base := restarted.Pending()
	h.waitTimers(base)
	restarted.Advance(31 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	view, err := h.app.Engine.Wait(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != engine.RunSucceeded {
		t.Fatalf("recovered run = %s: %+v", view.Status, view)
	}
}

// TestServiceLockInterplay shows that policy actions and manual actions
// exclude each other through the durable service lock.
func TestServiceLockInterplay(t *testing.T) {
	h := newHarness(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once, enteredOnce sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h.runner.On(matchCmd("api-restart"), func(execx.Cmd) (execx.Result, error) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		return execx.Result{}, nil
	})
	h.putService(`
apiVersion: lcm/v1
kind: Service
metadata: { name: api }
spec:
  runtime: { type: command, start: api-start, stop: api-stop, restart: api-restart, status: api-status }
`)
	h.deploy(`
apiVersion: lcm/v1
kind: Policy
metadata: { name: bounce }
spec:
  nodes:
    - { id: t, type: trigger.manual, next: { out: [r] } }
    - { id: r, type: lifecycle.restart, props: { service: api } }
`)
	if _, err := h.app.Manager.Start(h.ctx, "api", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}

	runID, err := h.app.Engine.Start(h.ctx, "bounce", "t", map[string]any{}, engine.ModeLive)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("policy never reached the restart command")
	}

	holder, held, err := h.app.Locks.Holder(h.ctx, catalog.LockName("api"))
	if err != nil || !held || holder.Owner != runID {
		t.Fatalf("lock holder = %+v held=%v err=%v, want run %s", holder, held, err, runID)
	}
	if _, err := h.app.Manager.Restart(h.ctx, "api", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrLockHeld) {
		t.Fatalf("manual restart during a policy action: %v", err)
	}

	once.Do(func() { close(release) })
	if view := h.waitRun("bounce", map[string]bool{}); view.Status != engine.RunSucceeded {
		t.Fatalf("run = %s", view.Status)
	}
	if _, held, _ := h.app.Locks.Holder(h.ctx, catalog.LockName("api")); held {
		t.Error("service lock still held after the run")
	}

	// A holder outside the manager's process, seen through the lock table,
	// blocks a manual action with the same error and the lock's cause.
	if _, err := h.app.Locks.TryAcquire(h.ctx, catalog.LockName("api"), "other-instance", time.Hour); err != nil {
		t.Fatal(err)
	}
	_, err = h.app.Manager.Restart(h.ctx, "api", catalog.ActionOptions{})
	if !errors.Is(err, catalog.ErrLockHeld) || !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("restart while another owner holds the lock: %v", err)
	}
	if _, err := h.app.Locks.Release(h.ctx, catalog.LockName("api"), "other-instance"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Manager.Restart(h.ctx, "api", catalog.ActionOptions{}); err != nil {
		t.Fatalf("restart after the lock was released: %v", err)
	}
}
