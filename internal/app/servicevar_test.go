package app_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/monitor"
)

// TestServiceVarFeedsWaitUntil shows a policy waiting for a service to turn
// healthy by reading the service variable, with no HTTP probe of its own.
func TestServiceVarFeedsWaitUntil(t *testing.T) {
	h := newHarness(t)
	// Drain real HTTP probes before virtual time can reach their timeout.
	advanceCheck := func() {
		for range int(checkInterval / time.Second) {
			h.advance(time.Second)
		}
	}
	web := newSwitchable(t)
	web.healthy.Store(false)
	h.putService(fmt.Sprintf(`
apiVersion: lcm/v1
kind: Service
metadata: { name: web }
spec:
  runtime: { type: command, start: web-start, stop: web-stop, status: web-status }
  checks:
    - { name: http, type: http, interval: %s, timeout: 2s, props: { url: %q } }
`, checkInterval, web.srv.URL))
	h.deploy(`
apiVersion: lcm/v1
kind: Policy
metadata: { name: wait-healthy }
spec:
  params: { service: web }
  nodes:
    - { id: t, type: trigger.manual, next: { out: [wait] } }
    - id: wait
      type: wait.until
      props:
        interval: 20s
        timeout: 5m
        condition: 'inputs.svc.health == "healthy" && inputs.svc.checks.http.status == "up"'
        inputs: { svc: "{{ service[params.service] }}" }
      next: { out: [done], timeout: [late] }
    - { id: done, type: data.set, props: { values: { ok: true } } }
    - { id: late, type: data.set, props: { values: { ok: false } } }
`)
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}
	advanceCheck()
	if st, ok := h.app.Monitor.Service("web"); !ok || st.Health != monitor.HealthDown {
		t.Fatalf("health before repair = %+v", st)
	}

	runID, err := h.app.Engine.Start(h.ctx, "wait-healthy", "t", map[string]any{}, engine.ModeLive)
	if err != nil {
		t.Fatal(err)
	}
	h.events.WaitFor(t, "run suspended", func(e engine.Event) bool {
		return e.Kind == engine.EventRunSuspended && e.RunID == runID
	})

	web.healthy.Store(true)
	advanceCheck()
	if st, ok := h.app.Monitor.Service("web"); !ok || st.Health != monitor.HealthHealthy {
		t.Fatalf("health after repair = %+v", st)
	}
	// The monitor must publish recovery before the policy's next poll.
	advanceCheck()

	view := h.waitRun("wait-healthy", map[string]bool{})
	if view.Status != engine.RunSucceeded {
		t.Fatalf("run = %s: %+v", view.Status, view)
	}
	if n, _ := view.Node("done"); n.Status != engine.NodeCompleted {
		t.Fatalf("done = %s, late branch taken instead", n.Status)
	}
}
