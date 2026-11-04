package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
)

// A service and the local machine are resources side by side: both are
// listed by the resource store and monitored, and the machine's combined
// health (native health from its observations) is a health.changed signal
// that starts a policy filtered on the machine type, which sees the machine
// through the resource expression variable.
func TestServiceAndMachineShareHealthSignals(t *testing.T) {
	probe := &machineProbe{}
	h := newHarness(t, func(o *testOpts) { o.machineObserver = probe })
	h.putService(checkedService)
	h.putService("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: workstation, labels: { site: home } }\nspec: { type: machine, adapter: local }\n")
	h.deploy(`
apiVersion: lcm/v2
kind: Policy
metadata: { name: machine-healthy }
spec:
  nodes:
    - id: t
      type: trigger.signal
      props: { kinds: [health.changed], types: [machine], statuses: [healthy] }
      next: { out: [seen] }
    - id: seen
      type: data.set
      props:
        values:
          name: "{{ trigger.resource }}"
          type: "{{ trigger.resourceType }}"
          state: "{{ resource[trigger.resource].state }}"
          phase: "{{ resource[trigger.resource].phase }}"
          service: "{{ resource.cache.type }}"
`)
	h.syncTriggers()
	if err := h.app.SyncMonitor(h.ctx); err != nil {
		t.Fatal(err)
	}

	recs, err := h.app.Resources.List(h.ctx, resource.Filter{})
	if err != nil || len(recs) != 2 || recs[0].Type != model.ResourceService || recs[1].Type != model.ResourceMachine {
		t.Fatalf("resources = %+v, %v", recs, err)
	}
	if _, ok := h.app.Monitor.Resource("cache"); !ok {
		t.Fatal("service checks are not monitored")
	}

	// CPU use needs two samples; the second makes the machine healthy.
	for range 2 {
		h.clk.Advance(30 * time.Second)
		if _, err := h.app.Machines.Observe(h.ctx, "workstation"); err != nil {
			t.Fatal(err)
		}
	}
	view := h.waitRun("machine-healthy", map[string]bool{})
	if view.Status != engine.RunSucceeded {
		t.Fatalf("run = %s: %+v", view.Status, view)
	}
	n, _ := view.Node("seen")
	b, _ := json.Marshal(n.Output)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["name"] != "workstation" || out["type"] != "machine" || out["state"] != "online" || out["phase"] != "managed" || out["service"] != "service" {
		t.Fatalf("policy saw %v", out)
	}
	if st, ok := h.app.Monitor.Resource("workstation"); !ok || st.Type != model.ResourceMachine || st.Native == nil {
		t.Fatalf("machine health = %+v", st)
	}
}
