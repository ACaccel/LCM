package engine_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
)

const suspendPolicy = `
  timeout: 2h
  concurrency: { mode: parallel, limit: 5 }
  nodes:
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - { id: wait, type: test.suspend, props: { key: "{{ trigger.key }}", resumeIn: "{{ trigger.resumeIn }}" }, timeout: 1s, next: { out: [after] } }
    - { id: after, type: test.echo, props: { value: "{{ nodes.wait }}" } }
`

func TestSuspendResumeByKey(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("sus", suspendPolicy))
	id := h.Start(e, "sus", map[string]any{"key": "approve-1", "resumeIn": "0s"})
	h.Events.WaitFor(t, "run suspended", enginetest.RunEvent(engine.EventRunSuspended, id))
	v, _ := e.Run(context.Background(), id)
	n, _ := v.Node("wait")
	if v.Status != engine.RunSuspended || n.Status != engine.NodeSuspended || n.SuspendKey != "approve-1" {
		t.Fatalf("view = %s %+v", v.Status, n)
	}
	if !reflect.DeepEqual(n.Output, map[string]any{"waiting": "approve-1"}) {
		t.Fatalf("suspend output = %#v", n.Output)
	}
	// Suspended time does not count toward the node timeout (1s).
	h.Clock.Advance(time.Hour)

	if err := e.Resume(context.Background(), id, "wait", "approve-1", nil); err == nil {
		t.Fatal("resume without actor accepted")
	}
	if err := e.Resume(h.Ctx, id, "wait", "wrong", nil); !errors.Is(err, engine.ErrKeyMismatch) {
		t.Fatalf("wrong key: %v", err)
	}
	if err := e.Resume(h.Ctx, id, "after", "approve-1", nil); !errors.Is(err, engine.ErrNotSuspended) {
		t.Fatalf("not suspended: %v", err)
	}
	if err := e.Resume(h.Ctx, id, "ghost", "approve-1", nil); !errors.Is(err, engine.ErrNotSuspended) {
		t.Fatalf("unknown node: %v", err)
	}
	if err := e.Resume(h.Ctx, id, "wait", "approve-1", map[string]any{"approved": true}); err != nil {
		t.Fatal(err)
	}
	if err := e.Resume(h.Ctx, id, "wait", "approve-1", nil); err == nil {
		t.Fatal("double resume accepted")
	}
	v = h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	want := map[string]any{
		"payload": map[string]any{"approved": true}, "timer": false,
		"saved": map[string]any{"waiting": "approve-1"}, "attempt": int64(1),
	}
	if got := output(t, v, "after"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after = %#v", got)
	}
	if h.Calls("wait") != 2 {
		t.Fatalf("wait ran %d times", h.Calls("wait"))
	}
	if err := e.Resume(h.Ctx, id, "wait", "approve-1", nil); !errors.Is(err, engine.ErrRunNotActive) {
		t.Fatalf("resume finished run: %v", err)
	}
}

func TestSuspendResumeByTimer(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("sustimer", suspendPolicy))
	id := h.Start(e, "sustimer", map[string]any{"key": "", "resumeIn": "30m"})
	h.Events.WaitFor(t, "run suspended", enginetest.RunEvent(engine.EventRunSuspended, id))
	if err := e.Resume(h.Ctx, id, "wait", "", nil); !errors.Is(err, engine.ErrKeyMismatch) {
		t.Fatalf("keyless resume: %v", err)
	}
	h.WaitTimers(2) // run timeout and resume timer
	h.Clock.Advance(29 * time.Minute)
	if v, _ := e.Run(context.Background(), id); v.Status != engine.RunSuspended {
		t.Fatalf("resumed early: %s", v.Status)
	}
	h.Clock.Advance(time.Minute)
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if got := output(t, v, "after").(map[string]any); got["timer"] != true {
		t.Fatalf("after = %#v", got)
	}
}

func TestSuspendedRunTimesOut(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("sustimeout", suspendPolicy))
	id := h.Start(e, "sustimeout", map[string]any{"key": "k", "resumeIn": "0s"})
	h.Events.WaitFor(t, "run suspended", enginetest.RunEvent(engine.EventRunSuspended, id))
	h.Clock.Advance(2 * time.Hour)
	if v := h.Wait(e, id); v.Status != engine.RunFailed {
		t.Fatalf("status = %s", v.Status)
	}
}

func TestInvalidSuspend(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("badsus", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - { id: wait, type: test.suspend }
`))
	if v := h.Wait(e, h.Start(e, "badsus", nil)); v.Status != engine.RunFailed {
		t.Fatalf("status = %s", v.Status)
	}
}

func TestSuspendAcrossRestart(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("susrestart", suspendPolicy))
	byKey := h.Start(e1, "susrestart", map[string]any{"key": "k1", "resumeIn": "0s"})
	byTimer := h.Start(e1, "susrestart", map[string]any{"key": "", "resumeIn": "10m"})
	h.Events.WaitFor(t, "suspended", enginetest.RunEvent(engine.EventRunSuspended, byKey))
	h.Events.WaitFor(t, "suspended", enginetest.RunEvent(engine.EventRunSuspended, byTimer))
	if err := e1.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e2.Recover(context.Background()); !errors.Is(err, engine.ErrAlreadyRecovered) {
		t.Fatalf("second recover: %v", err)
	}
	if v, _ := e2.Run(context.Background(), byKey); v.Status != engine.RunSuspended {
		t.Fatalf("after restart: %s", v.Status)
	}
	if err := e2.Resume(h.Ctx, byKey, "wait", "k1", "yes"); err != nil {
		t.Fatal(err)
	}
	if got := output(t, h.Wait(e2, byKey), "after").(map[string]any); got["payload"] != "yes" {
		t.Fatalf("after = %#v", got)
	}
	h.Clock.Advance(10 * time.Minute)
	v := h.Wait(e2, byTimer)
	if got := output(t, v, "after").(map[string]any); got["timer"] != true || v.Status != engine.RunSucceeded {
		t.Fatalf("timer run: %s %#v", v.Status, got)
	}
}

func TestStoredResumeAppliedAfterRestart(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("susstored", suspendPolicy))
	id := h.Start(e1, "susstored", map[string]any{"key": "k", "resumeIn": "0s"})
	h.Events.WaitFor(t, "suspended", enginetest.RunEvent(engine.EventRunSuspended, id))
	if err := e1.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash right after Resume stored its payload.
	writeSQL(t, h, `UPDATE run_nodes SET status = 'resumed', resume_payload = '{"ok":1}' WHERE run_id = ? AND node_id = 'wait'`, id)

	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := h.Wait(e2, id)
	if got := output(t, v, "after").(map[string]any); !reflect.DeepEqual(got["payload"], map[string]any{"ok": int64(1)}) {
		t.Fatalf("after = %#v", got)
	}
}
