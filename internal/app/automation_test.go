package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
)

// TestAutomationOffLeavesInterruptedRunsAlone starts the app with
// LCM_AUTOMATION=off, as "lcm host upgrade" does to validate a migration:
// the run that was waiting when the process stopped is neither resumed nor
// finished, even though its wait is long over.
func TestAutomationOffLeavesInterruptedRunsAlone(t *testing.T) {
	h := newHarness(t)
	h.deploy(`
apiVersion: lcm/v2
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

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted := clock.NewFake(epoch.Add(2 * time.Hour))
	h.clk = restarted
	h.app = h.open(testOpts{dir: h.dir, clk: restarted, runner: h.runner, configure: func(c *config.Config) { c.AutomationOff = true }})
	stop := h.run()
	t.Cleanup(func() { _ = stop() })

	restarted.Advance(2 * time.Hour)
	view, err := h.app.Engine.Run(h.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != engine.RunSuspended {
		t.Fatalf("run = %s, want it left suspended", view.Status)
	}
}
