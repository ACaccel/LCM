package stateful_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/nodes/stateful"
	"github.com/ACaccel/lcm/internal/state"
)

func policy(name, nodes string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  concurrency: { mode: parallel, limit: 10 }\n  nodes:\n" + nodes
}

type env struct {
	h        *enginetest.Harness
	e        *engine.Engine
	state    *state.Store
	locks    *lock.Manager
	releaser *stateful.Releaser
}

func setup(t *testing.T) env {
	t.Helper()
	h := enginetest.New(t)
	st := state.New(h.DB, state.WithClock(h.Clock.Now))
	lm := lock.New(h.DB, lock.WithClock(h.Clock))
	stateful.Register(h.Registry, stateful.Deps{State: st, Locks: lm})
	rel := stateful.NewReleaser(lm, h.Clock, nil)
	e := h.Open(func(c *engine.Config) {
		c.Vars = append(c.Vars, st.Var())
		c.Observers = append(c.Observers, rel)
	})
	return env{h: h, e: e, state: st, locks: lm, releaser: rel}
}

func out(t *testing.T, v engine.RunView, id string) map[string]any {
	t.Helper()
	n, ok := v.Node(id)
	if !ok {
		t.Fatalf("no node %s", id)
	}
	m, _ := n.Output.(map[string]any)
	return m
}

func status(v engine.RunView, id string) engine.NodeStatus {
	n, _ := v.Node(id)
	return n.Status
}

func TestStateGetSetAndExpressionVariable(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("counter", `
    - { id: start, type: trigger.manual, next: { out: [read] } }
    - { id: read, type: state.get, props: { key: count, default: 0 }, next: { out: [write] } }
    - { id: write, type: state.set, props: { key: count, value: "{{ nodes.read.value + 1 }}" }, next: { out: [seen] } }
    - { id: seen, type: test.echo, props: { value: "{{ state.count }}" } }
`))
	for want := int64(1); want <= 3; want++ {
		id := x.h.Start(x.e, "counter", nil)
		v := x.h.Wait(x.e, id)
		if v.Status != engine.RunSucceeded {
			t.Fatalf("status = %s (%s)", v.Status, v.Error)
		}
		if got := out(t, v, "read"); got["value"] != want-1 {
			t.Fatalf("read = %v, want %d", got, want-1)
		}
		if n, _ := v.Node("seen"); n.Output != want {
			t.Fatalf("state variable = %v, want %d", n.Output, want)
		}
	}
	if e, _ := x.state.Get(x.h.Ctx, "counter", "count"); e.Value != int64(3) {
		t.Fatalf("stored = %+v", e)
	}
	if e, _ := x.state.Get(x.h.Ctx, "other", "count"); e.Found() {
		t.Fatal("state is shared between policies")
	}
}

func TestStateSetCompareAndSet(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("cas", `
    - { id: start, type: trigger.manual, next: { out: [first] } }
    - { id: first, type: state.set, props: { key: leader, value: a, expectVersion: 0 }, next: { out: [second] } }
    - { id: second, type: state.set, props: { key: leader, value: b, expectVersion: 0 }, next: { out: [ok2], conflict: [lost] } }
    - { id: ok2, type: test.echo, props: { value: ok2 } }
    - { id: lost, type: test.echo, props: { value: "{{ nodes.second }}" } }
`))
	id := x.h.Start(x.e, "cas", nil)
	v := x.h.Wait(x.e, id)
	if v.Status != engine.RunSucceeded || status(v, "lost") != engine.NodeCompleted || status(v, "ok2") == engine.NodeCompleted {
		t.Fatalf("status=%s lost=%s ok2=%s", v.Status, status(v, "lost"), status(v, "ok2"))
	}
	cur, _ := v.Node("lost")
	conflict := cur.Output.(map[string]any)
	if conflict["found"] != true || conflict["value"] != "a" {
		t.Fatalf("conflict output = %v", conflict)
	}
	if e, _ := x.state.Get(x.h.Ctx, "cas", "leader"); e.Value != "a" {
		t.Fatalf("losing writer overwrote the value: %+v", e)
	}
}

func TestStateSetExpectValue(t *testing.T) {
	x := setup(t)
	_, _ = x.state.Set(x.h.Ctx, "ev", "phase", "building")
	x.h.Deploy(x.e, policy("ev", `
    - { id: start, type: trigger.manual, next: { out: [adv] } }
    - { id: adv, type: state.set, props: { key: phase, value: "{{ trigger.to }}", expect: "{{ trigger.from }}" }, next: { out: [done], conflict: [nope] } }
    - { id: done, type: test.echo, props: { value: done } }
    - { id: nope, type: test.echo, props: { value: nope } }
`))
	run := func(from, to string) engine.RunView {
		return x.h.Wait(x.e, x.h.Start(x.e, "ev", map[string]any{"from": from, "to": to}))
	}
	if v := run("testing", "deployed"); status(v, "nope") != engine.NodeCompleted {
		t.Fatal("mismatching expect did not conflict")
	}
	if v := run("building", "testing"); status(v, "done") != engine.NodeCompleted {
		t.Fatal("matching expect did not apply")
	}
	if e, _ := x.state.Get(x.h.Ctx, "ev", "phase"); e.Value != "testing" {
		t.Fatalf("phase = %v", e.Value)
	}
}

func TestStateSetLimitsFailPermanently(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("big", `
    - { id: start, type: trigger.manual, next: { out: [w] } }
    - { id: w, type: state.set, props: { key: k, value: "{{ trigger.blob }}" }, retry: { max: 3, backoff: 1ms } }
`))
	v := x.h.Wait(x.e, x.h.Start(x.e, "big", map[string]any{"blob": strings.Repeat("x", state.MaxValueBytes)}))
	if n, _ := v.Node("w"); v.Status != engine.RunFailed || n.Attempts != 1 {
		t.Fatalf("status=%s attempts=%d", v.Status, n.Attempts)
	}
}

func TestStateNodesDryRun(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("dry", `
    - { id: start, type: trigger.manual, next: { out: [w] } }
    - { id: w, type: state.set, props: { key: k, value: v }, next: { out: [r] } }
    - { id: r, type: state.get, props: { key: k } }
`))
	id, _ := x.e.Start(x.h.Ctx, "dry", "start", nil, engine.ModeDry)
	v := x.h.Wait(x.e, id)
	if e, _ := x.state.Get(x.h.Ctx, "dry", "k"); e.Found() || out(t, v, "r")["found"] != false {
		t.Fatal("dry run wrote state")
	}
}

const lockPolicy = `
    - { id: start, type: trigger.manual, next: { out: [take] } }
    - id: take
      type: lock
      props: { name: "{{ trigger.lock }}", ttl: "{{ trigger.ttl }}", ifBusy: "{{ trigger.ifBusy }}", waitTimeout: 1m, pollInterval: 1s }
      next: { out: [work], busy: [gaveup] }
    - { id: work, type: test.echo, props: { value: worked, gate: "{{ trigger.gate }}" } }
    - { id: gaveup, type: test.echo, props: { value: "{{ nodes.take }}" } }
`

func (x env) startLock(name, gate, ifBusy, ttl string) string {
	return x.h.Start(x.e, "locked", map[string]any{"lock": name, "gate": gate, "ifBusy": ifBusy, "ttl": ttl})
}

func (x env) holder(t *testing.T, name string) (lock.Info, bool) {
	t.Helper()
	i, ok, err := x.locks.Holder(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return i, ok
}

func TestLockHandoffWhenHolderRunEnds(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("locked", lockPolicy))

	a := x.startLock("deploy", "a", "wait", "1h")
	x.h.Entered("a")
	if i, ok := x.holder(t, "deploy"); !ok || i.Owner != "run:"+a {
		t.Fatalf("holder = %+v ok=%v", i, ok)
	}
	b := x.startLock("deploy", "", "wait", "1h")
	x.h.Events.WaitFor(t, "b waiting", enginetest.Node(engine.EventNodeSuspended, b, "take"))
	x.h.WaitTimers(1)
	x.h.Clock.Advance(time.Second) // b polls: still busy
	x.h.Events.WaitFor(t, "b waiting again", func(e engine.Event) bool {
		return e.Kind == engine.EventNodeSuspended && e.RunID == b && e.RetryAt != nil && e.RetryAt.Equal(enginetest.Epoch.Add(2*time.Second))
	})
	x.h.WaitTimers(1)
	if v, _ := x.e.Run(context.Background(), b); status(v, "work") == engine.NodeCompleted {
		t.Fatal("second run entered the critical section")
	}

	// A ends; the releaser frees its lock without an unlock node.
	x.h.Release("a")
	if v := x.h.Wait(x.e, a); v.Status != engine.RunSucceeded {
		t.Fatalf("a: %s", v.Status)
	}
	deadline := time.After(enginetest.Timeout)
	for {
		if _, held := x.holder(t, "deploy"); !held {
			break
		}
		select {
		case <-deadline:
			t.Fatal("lock was not released when the run finished")
		case <-time.After(time.Millisecond):
		}
	}
	x.h.Clock.Advance(time.Second)
	v := x.h.Wait(x.e, b)
	if v.Status != engine.RunSucceeded || status(v, "work") != engine.NodeCompleted {
		t.Fatalf("b: status=%s work=%s", v.Status, status(v, "work"))
	}
}

func TestLockIfBusySkip(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("locked", lockPolicy))
	a := x.startLock("only-one", "a", "skip", "1h")
	x.h.Entered("a")

	b := x.startLock("only-one", "", "skip", "1h")
	v := x.h.Wait(x.e, b)
	if v.Status != engine.RunSucceeded || status(v, "gaveup") != engine.NodeCompleted || status(v, "work") == engine.NodeCompleted {
		t.Fatalf("status=%s gaveup=%s work=%s", v.Status, status(v, "gaveup"), status(v, "work"))
	}
	if got := out(t, v, "gaveup"); got["heldBy"] != "run:"+a {
		t.Fatalf("busy output = %v", got)
	}
	x.h.Release("a")
	x.h.Wait(x.e, a)
}

func TestLockWaitTimeoutTakesBusyPort(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("locked", lockPolicy))
	_ = x.startLock("slow", "a", "wait", "1h")
	x.h.Entered("a")
	b := x.startLock("slow", "", "wait", "1h")
	// pollInterval 1s, waitTimeout 1m: step through the whole wait.
	for i := 1; i <= 60; i++ {
		at := enginetest.Epoch.Add(time.Duration(i) * time.Second)
		x.h.Events.WaitFor(t, "poll", func(e engine.Event) bool {
			return e.Kind == engine.EventNodeSuspended && e.RunID == b && e.RetryAt != nil && e.RetryAt.Equal(at)
		})
		x.h.WaitTimers(1)
		x.h.Clock.Advance(time.Second)
	}
	v := x.h.Wait(x.e, b)
	if v.Status != engine.RunSucceeded || status(v, "gaveup") != engine.NodeCompleted {
		t.Fatalf("status=%s gaveup=%s", v.Status, status(v, "gaveup"))
	}
}

func TestLockExpiresByTTLWhileHolderStillRuns(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("locked", lockPolicy))
	a := x.startLock("ttl", "a", "wait", "10m")
	x.h.Entered("a")
	if i, _ := x.holder(t, "ttl"); !i.ExpiresAt.Equal(enginetest.Epoch.Add(10 * time.Minute)) {
		t.Fatalf("expiry = %v", i.ExpiresAt)
	}
	x.h.Clock.Advance(10 * time.Minute)
	if _, held := x.holder(t, "ttl"); held {
		t.Fatal("lock outlived its ttl")
	}
	b := x.startLock("ttl", "b", "skip", "10m")
	x.h.Entered("b")
	if i, ok := x.holder(t, "ttl"); !ok || i.Owner != "run:"+b {
		t.Fatalf("takeover failed: %+v %v", i, ok)
	}
	x.h.Release("a")
	x.h.Wait(x.e, a)
	// a's end must not free b's lock.
	if i, ok := x.holder(t, "ttl"); !ok || i.Owner != "run:"+b {
		t.Fatalf("a's release freed b's lock: %+v %v", i, ok)
	}
	x.h.Release("b")
	x.h.Wait(x.e, b)
}

func TestLockReleasedWhenRunFails(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("failing", `
    - { id: start, type: trigger.manual, next: { out: [take] } }
    - { id: take, type: lock, props: { name: crash, ttl: 1h }, next: { out: [boom] } }
    - { id: boom, type: test.fail, props: { permanent: true } }
`))
	id := x.h.Start(x.e, "failing", nil)
	if v := x.h.Wait(x.e, id); v.Status != engine.RunFailed {
		t.Fatalf("status = %s", v.Status)
	}
	deadline := time.After(enginetest.Timeout)
	for {
		if _, held := x.holder(t, "crash"); !held {
			return
		}
		select {
		case <-deadline:
			t.Fatal("lock of a failed run was not released")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestUnlockReleasesEarly(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("early", `
    - { id: start, type: trigger.manual, next: { out: [take] } }
    - { id: take, type: lock, props: { name: section, ttl: 1h }, next: { out: [drop] } }
    - { id: drop, type: unlock, props: { name: section }, next: { out: [after] } }
    - { id: after, type: test.echo, props: { value: after, gate: tail } }
`))
	id := x.h.Start(x.e, "early", nil)
	x.h.Entered("tail")
	if _, held := x.holder(t, "section"); held {
		t.Fatal("lock still held after unlock")
	}
	x.h.Release("tail")
	v := x.h.Wait(x.e, id)
	if got := out(t, v, "drop"); got["released"] != true {
		t.Fatalf("unlock output = %v", got)
	}
}

func TestLockDryRunDoesNotAcquire(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("locked", lockPolicy))
	id, _ := x.e.Start(x.h.Ctx, "locked", "start", map[string]any{"lock": "dry", "gate": "", "ifBusy": "wait", "ttl": "1h"}, engine.ModeDry)
	x.h.Wait(x.e, id)
	if _, held := x.holder(t, "dry"); held {
		t.Fatal("dry run took a lock")
	}
}

func TestReleaserSweepFreesLocksOfEndedRuns(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("locked", lockPolicy))
	live := x.startLock("live", "a", "wait", "1h")
	x.h.Entered("a")

	// A run that is gone (for example one lost with its database) and a
	// lock owned by someone else.
	if _, err := x.locks.TryAcquire(context.Background(), "orphan", "run:01HNOSUCHRUN", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := x.locks.TryAcquire(context.Background(), "svc", "service:api", time.Hour); err != nil {
		t.Fatal(err)
	}
	n, err := x.releaser.Sweep(context.Background(), x.e)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	if _, held := x.holder(t, "orphan"); held {
		t.Fatal("orphaned lock survived the sweep")
	}
	if i, held := x.holder(t, "live"); !held || i.Owner != "run:"+live {
		t.Fatal("sweep released the lock of an active run")
	}
	if _, held := x.holder(t, "svc"); !held {
		t.Fatal("sweep released a lock that is not a run lock")
	}
	x.h.Release("a")
	x.h.Wait(x.e, live)
}

func TestLockValidation(t *testing.T) {
	x := setup(t)
	for props, want := range map[string]string{
		`{ name: "bad name" }`:           "name",
		`{ name: l, ttl: 0s }`:           "ttl",
		`{ name: l, ttl: 999h }`:         "ttl",
		`{ name: l, pollInterval: 1ms }`: "pollInterval",
		`{ name: l, ifBusy: maybe }`:     "one of",
	} {
		_, err := x.e.Validate(x.h.Ctx, []byte(policy("v", "    - { id: start, type: trigger.manual, next: { out: [l] } }\n    - { id: l, type: lock, props: "+props+" }\n")))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", props, err, want)
		}
	}
}
