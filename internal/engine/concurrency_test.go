package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
)

func gatedPolicy(name, concurrency string) string {
	return policy(name, `
  concurrency: `+concurrency+`
  nodes:
    - { id: start, type: trigger.manual, next: { out: [work] } }
    - { id: work, type: test.read, props: { gate: "{{ trigger.gate }}", value: "{{ trigger.n }}" } }
`)
}

func TestQueueOrder(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("q", "{ mode: queue }"))
	var ids []string
	for i := range 3 {
		ids = append(ids, h.Start(e, "q", map[string]any{"gate": fmt.Sprintf("q%d", i), "n": i}))
	}
	h.Entered("q0")
	for _, id := range ids[1:] {
		v, _ := e.Run(context.Background(), id)
		if v.Status != engine.RunQueued {
			t.Fatalf("run %s status = %s, want queued", id, v.Status)
		}
	}
	// Release out of order: the queue still starts runs in arrival order.
	h.Release("q2")
	h.Release("q1")
	h.Release("q0")
	for _, id := range ids {
		if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
			t.Fatalf("run %s: %s", id, v.Status)
		}
	}
	var started []string
	for _, ev := range h.Events.Events() {
		if ev.Kind == engine.EventRunStarted {
			started = append(started, ev.RunID)
		}
	}
	if strings.Join(started, ",") != strings.Join(ids, ",") {
		t.Fatalf("start order %v, want %v", started, ids)
	}
}

func TestDrop(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("d", "{ mode: drop }"))
	first := h.Start(e, "d", map[string]any{"gate": "d0"})
	h.Entered("d0")
	dropped, err := e.Start(context.Background(), "d", "start", map[string]any{"gate": "d1"}, engine.ModeLive)
	if !errors.Is(err, engine.ErrDropped) || dropped == "" {
		t.Fatalf("second start: %q %v", dropped, err)
	}
	v, err := e.Run(context.Background(), dropped)
	if err != nil || v.Status != engine.RunDropped {
		t.Fatalf("dropped run = %+v, %v", v.Status, err)
	}
	h.Release("d0")
	h.Wait(e, first)
	if v := h.Wait(e, h.Start(e, "d", map[string]any{})); v.Status != engine.RunSucceeded {
		t.Fatalf("after drop: %s", v.Status)
	}
}

func TestReplaceCancelsActive(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("r", "{ mode: replace }"))
	first := h.Start(e, "r", map[string]any{"gate": "r0"})
	h.Entered("r0")
	second := h.Start(e, "r", map[string]any{"gate": "r1"})
	third := h.Start(e, "r", map[string]any{"gate": "r2"})
	v := h.Wait(e, first)
	if v.Status != engine.RunCancelled || !strings.Contains(v.Error, "replaced by run") {
		t.Fatalf("first: %s (%s)", v.Status, v.Error)
	}
	v = h.Wait(e, second)
	if v.Status != engine.RunCancelled || !strings.Contains(v.Error, "replaced by run "+third) {
		t.Fatalf("second: %s (%s)", v.Status, v.Error)
	}
	h.Entered("r2")
	h.Release("r2")
	if v := h.Wait(e, third); v.Status != engine.RunSucceeded {
		t.Fatalf("third: %s", v.Status)
	}
	if h.Calls("work") != 2 {
		t.Fatalf("work ran %d times, want 2 (first and third)", h.Calls("work"))
	}
}

func TestParallelLimit(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("par", "{ mode: parallel, limit: 2 }"))
	ids := make([]string, 4)
	for i := range ids {
		ids[i] = h.Start(e, "par", map[string]any{"gate": fmt.Sprintf("p%d", i)})
	}
	h.Entered("p0")
	h.Entered("p1")
	for _, id := range ids[2:] {
		if v, _ := e.Run(context.Background(), id); v.Status != engine.RunQueued {
			t.Fatalf("run beyond limit is %s", v.Status)
		}
	}
	h.Release("p1")
	h.Wait(e, ids[1])
	h.Entered("p2")
	if v, _ := e.Run(context.Background(), ids[3]); v.Status != engine.RunQueued {
		t.Fatalf("fourth run is %s", v.Status)
	}
	for i := range ids {
		h.Release(fmt.Sprintf("p%d", i))
	}
	for _, id := range ids {
		if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
			t.Fatalf("%s: %s", id, v.Status)
		}
	}
}

func TestCancelQueuedAndUndeploy(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("cq", "{ mode: queue }"))
	first := h.Start(e, "cq", map[string]any{"gate": "cq0"})
	second := h.Start(e, "cq", map[string]any{"gate": "cq1"})
	third := h.Start(e, "cq", map[string]any{"gate": "cq2"})
	h.Entered("cq0")
	if err := e.Cancel(h.Ctx, second); err != nil {
		t.Fatal(err)
	}
	if v := h.Wait(e, second); v.Status != engine.RunCancelled {
		t.Fatalf("second: %s", v.Status)
	}
	if err := e.Undeploy(h.Ctx, "cq"); err != nil {
		t.Fatal(err)
	}
	if v := h.Wait(e, third); v.Status != engine.RunCancelled || v.Error != "policy undeployed" {
		t.Fatalf("third: %s %s", v.Status, v.Error)
	}
	if _, err := e.Start(context.Background(), "cq", "start", nil, engine.ModeLive); !errors.Is(err, engine.ErrPolicyNotDeployed) {
		t.Fatalf("start after undeploy: %v", err)
	}
	h.Release("cq0")
	if v := h.Wait(e, first); v.Status != engine.RunSucceeded {
		t.Fatalf("active run after undeploy: %s", v.Status)
	}
	if err := e.Undeploy(h.Ctx, "cq"); !errors.Is(err, engine.ErrPolicyNotDeployed) {
		t.Fatalf("second undeploy: %v", err)
	}
}
