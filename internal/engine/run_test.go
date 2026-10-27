package engine_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
)

func statuses(v engine.RunView) map[string]engine.NodeStatus {
	out := map[string]engine.NodeStatus{}
	for _, n := range v.Nodes {
		out[n.ID] = n.Status
	}
	return out
}

func wantStatus(t *testing.T, v engine.RunView, want map[string]engine.NodeStatus) {
	t.Helper()
	got := statuses(v)
	for id, st := range want {
		if got[id] != st {
			t.Errorf("node %s: status %s, want %s (all: %v)", id, got[id], st, got)
		}
	}
}

func output(t *testing.T, v engine.RunView, id string) any {
	t.Helper()
	n, ok := v.Node(id)
	if !ok {
		t.Fatalf("node %s not in view", id)
	}
	return n.Output
}

func TestLinear(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("linear", `
  params: { greeting: hello }
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: data.set, props: { values: { msg: "{{ params.greeting }} {{ trigger.name }}", n: "{{ trigger.n + 1 }}" } }, next: { out: [b] } }
    - { id: b, type: test.echo, props: { value: { got: "{{ nodes.a.msg }}", n: "{{ nodes.a.n }}", run: "{{ run.policy }}/{{ run.attempt }}" } } }
`))
	id := h.Start(e, "linear", map[string]any{"name": "web", "n": 1})
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	want := map[string]any{"got": "hello web", "n": int64(2), "run": "linear/1"}
	if got := output(t, v, "b"); !reflect.DeepEqual(got, want) {
		t.Fatalf("b = %#v", got)
	}
	n, _ := v.Node("b")
	if n.Attempts != 1 || n.StartedAt == nil || n.FinishedAt == nil || n.Port != "out" {
		t.Fatalf("node b = %+v", n)
	}
	// The stored view matches the in-memory one.
	stored, err := e.Run(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(statuses(stored), statuses(v)) || !reflect.DeepEqual(output(t, stored, "b"), want) {
		t.Fatalf("stored view differs: %+v", stored)
	}
}

func TestIfElseRejoinRunsJoinOnce(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("rejoin", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [check] } }
    - { id: check, type: flow.if, props: { condition: "trigger.down" }, next: { "true": [restart], "false": [log] } }
    - { id: restart, type: test.echo, props: { value: restarted }, next: { out: [join] } }
    - { id: log, type: test.echo, props: { value: logged }, next: { out: [after_log] } }
    - { id: after_log, type: test.echo, next: { out: [join] } }
    - { id: join, type: flow.merge, props: { value: "{{ nodes.restart ?? nodes.log }}" } }
`))
	for _, down := range []bool{true, false} {
		id := h.Start(e, "rejoin", map[string]any{"down": down})
		v := h.Wait(e, id)
		if v.Status != engine.RunSucceeded {
			t.Fatalf("status = %s (%s)", v.Status, v.Error)
		}
		if down {
			wantStatus(t, v, map[string]engine.NodeStatus{"restart": engine.NodeCompleted, "log": engine.NodeDead, "after_log": engine.NodeDead, "join": engine.NodeCompleted})
			if output(t, v, "join") != "restarted" {
				t.Fatalf("join = %v", output(t, v, "join"))
			}
		} else {
			wantStatus(t, v, map[string]engine.NodeStatus{"restart": engine.NodeDead, "log": engine.NodeCompleted, "join": engine.NodeCompleted})
			if output(t, v, "join") != "logged" {
				t.Fatalf("join = %v", output(t, v, "join"))
			}
		}
	}
	finished := 0
	for _, ev := range h.Events.Events() {
		if ev.Kind == engine.EventNodeFinished && ev.NodeID == "join" {
			finished++
		}
	}
	if finished != 2 {
		t.Fatalf("join finished %d times over two runs", finished)
	}
}

func TestSwitchFanOutFanIn(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("switch", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [sw] } }
    - id: sw
      type: flow.switch
      props: { cases: [{ port: low, when: "trigger.v < 10" }, { port: high, when: "trigger.v < 100" }] }
      next: { low: [l], high: [h1, h2, h3], default: [d] }
    - { id: l, type: test.echo, next: { out: [all] } }
    - { id: h1, type: test.echo, props: { value: 1 }, next: { out: [all] } }
    - { id: h2, type: test.echo, props: { value: 2 }, next: { out: [all] } }
    - { id: h3, type: test.echo, props: { value: 3 }, next: { out: [all] } }
    - { id: d, type: test.echo, next: { out: [all] } }
    - { id: all, type: test.echo, props: { value: "{{ [nodes.h1, nodes.h2, nodes.h3] }}" } }
`))
	v := h.Wait(e, h.Start(e, "switch", map[string]any{"v": 50}))
	wantStatus(t, v, map[string]engine.NodeStatus{
		"l": engine.NodeDead, "d": engine.NodeDead, "h1": engine.NodeCompleted, "h2": engine.NodeCompleted,
		"h3": engine.NodeCompleted, "all": engine.NodeCompleted,
	})
	if n, _ := v.Node("sw"); n.Port != "high" {
		t.Fatalf("switch port = %s", n.Port)
	}
	if got := output(t, v, "all"); !reflect.DeepEqual(got, []any{int64(1), int64(2), int64(3)}) {
		t.Fatalf("all = %#v", got)
	}
	v = h.Wait(e, h.Start(e, "switch", map[string]any{"v": 500}))
	wantStatus(t, v, map[string]engine.NodeStatus{"d": engine.NodeCompleted, "h1": engine.NodeDead, "all": engine.NodeCompleted})
}

func TestJoinAnyAndAllDead(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("join", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [fast, slow, cond] } }
    - { id: fast, type: test.echo, props: { value: fast }, next: { out: [first] } }
    - { id: slow, type: test.echo, props: { gate: slow-gate }, next: { out: [first] } }
    - { id: first, type: test.echo, props: { value: "{{ nodes.fast }}" }, joinMode: any }
    - { id: cond, type: flow.if, props: { condition: "false" }, next: { "true": [x, y] } }
    - { id: x, type: test.echo, next: { out: [z] } }
    - { id: y, type: test.echo, next: { out: [z] } }
    - { id: z, type: test.echo }
`))
	id := h.Start(e, "join", nil)
	h.Events.WaitFor(t, "first finished", enginetest.Node(engine.EventNodeFinished, id, "first"))
	v, err := e.Run(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, v, map[string]engine.NodeStatus{"first": engine.NodeCompleted, "slow": engine.NodeRunning})
	if v.Status != engine.RunRunning {
		t.Fatalf("run status = %s", v.Status)
	}
	h.Release("slow-gate")
	v = h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s", v.Status)
	}
	wantStatus(t, v, map[string]engine.NodeStatus{"x": engine.NodeDead, "y": engine.NodeDead, "z": engine.NodeDead, "first": engine.NodeCompleted})
	if h.Calls("first") != 1 {
		t.Fatalf("joinMode any ran %d times", h.Calls("first"))
	}
}

func TestErrorPortRouting(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("errport", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [act] } }
    - { id: act, type: test.fail, props: { message: kaput }, next: { out: [ok], error: [alert] } }
    - { id: ok, type: test.echo }
    - { id: alert, type: test.echo, props: { value: "{{ nodes.act.message }}|{{ nodes.act.attempts }}" } }
`))
	v := h.Wait(e, h.Start(e, "errport", nil))
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	wantStatus(t, v, map[string]engine.NodeStatus{"act": engine.NodeCompleted, "ok": engine.NodeDead, "alert": engine.NodeCompleted})
	n, _ := v.Node("act")
	if n.Port != "error" || !strings.Contains(n.Error, "kaput") {
		t.Fatalf("act = %+v", n)
	}
	if got := output(t, v, "alert"); got != "kaput (attempt 1): boom|1" {
		t.Fatalf("alert = %v", got)
	}
}

func TestUnhandledErrorCancelsSiblings(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("unhandled", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [blocker, gate_then_fail] } }
    - { id: blocker, type: test.read, props: { gate: never }, next: { out: [after] } }
    - { id: gate_then_fail, type: test.echo, props: { gate: go }, next: { out: [failer] } }
    - { id: failer, type: test.fail, props: { permanent: true } }
    - { id: after, type: test.echo }
`))
	id := h.Start(e, "unhandled", nil)
	h.Entered("never")
	h.Entered("go")
	h.Release("go")
	v := h.Wait(e, id)
	if v.Status != engine.RunFailed || !strings.Contains(v.Error, "node failer failed") {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	wantStatus(t, v, map[string]engine.NodeStatus{"failer": engine.NodeFailed, "blocker": engine.NodeFailed, "after": engine.NodePending})
	if n, _ := v.Node("blocker"); !strings.Contains(n.Error, "node failer failed") {
		t.Fatalf("blocker error = %q", n.Error)
	}
}

func TestPanicFailsNode(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("panic", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [p] } }
    - { id: p, type: test.panic, retry: { max: 3 } }
`))
	v := h.Wait(e, h.Start(e, "panic", nil))
	if v.Status != engine.RunFailed || !strings.Contains(v.Error, "panicked") {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if n, _ := v.Node("p"); n.Attempts != 1 {
		t.Fatalf("panic retried: %d attempts", n.Attempts)
	}
}

func TestRetriesWithFakeClock(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("retry", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [flaky] } }
    - { id: flaky, type: test.fail, props: { times: 2 }, retry: { max: 3, backoff: 2s, maxBackoff: 3s } }
`))
	id := h.Start(e, "retry", nil)
	for attempt := 1; attempt <= 2; attempt++ {
		ev := h.Events.WaitFor(t, "retrying", func(ev engine.Event) bool {
			return ev.Kind == engine.EventNodeRetrying && ev.RunID == id && ev.Attempt == attempt
		})
		// Backoff doubles (2s, then capped at 3s) with equal jitter.
		base := 2 * time.Second
		if attempt == 2 {
			base = 3 * time.Second
		}
		delay := ev.RetryAt.Sub(h.Clock.Now())
		if delay < base/2 || delay > base {
			t.Fatalf("attempt %d delay %s, want [%s, %s]", attempt, delay, base/2, base)
		}
		v, _ := e.Run(context.Background(), id)
		wantStatus(t, v, map[string]engine.NodeStatus{"flaky": engine.NodeRetrying})
		h.WaitTimers(1)
		h.Clock.Advance(base)
	}
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if n, _ := v.Node("flaky"); n.Attempts != 3 || n.Error != "" {
		t.Fatalf("flaky = %+v", n)
	}
	if h.Calls("flaky") != 3 {
		t.Fatalf("calls = %d", h.Calls("flaky"))
	}
}

func TestRetryExhaustedAndPermanent(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("exhaust", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a, b] } }
    - { id: a, type: test.fail, retry: { max: 2 }, next: { error: [ea] } }
    - { id: b, type: test.fail, props: { permanent: true }, retry: { max: 5 }, next: { error: [eb] } }
    - { id: ea, type: test.echo, props: { value: "{{ nodes.a.attempts }}" } }
    - { id: eb, type: test.echo, props: { value: "{{ nodes.b.attempts }}" } }
`))
	v := h.Wait(e, h.Start(e, "exhaust", nil))
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if output(t, v, "ea") != int64(3) || output(t, v, "eb") != int64(1) {
		t.Fatalf("attempts a=%v b=%v", output(t, v, "ea"), output(t, v, "eb"))
	}
}

func TestNodeTimeout(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("ntimeout", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [slow] } }
    - { id: slow, type: test.read, timeout: 5s, props: { gate: forever }, retry: { max: 1 }, next: { error: [handle] } }
    - { id: handle, type: test.echo, props: { value: "{{ nodes.slow.message }}" } }
`))
	id := h.Start(e, "ntimeout", nil)
	for range 2 {
		h.Entered("forever")
		h.WaitTimers(1)
		h.Clock.Advance(5 * time.Second)
	}
	v := h.Wait(e, id)
	msg, _ := output(t, v, "handle").(string)
	if v.Status != engine.RunSucceeded || !strings.HasPrefix(msg, "timed out after 5s") {
		t.Fatalf("status = %s, handle = %q", v.Status, msg)
	}
	if n, _ := v.Node("slow"); n.Attempts != 2 {
		t.Fatalf("attempts = %d", n.Attempts)
	}
}

func TestRunTimeout(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("rtimeout", `
  timeout: 1m
  nodes:
    - { id: start, type: trigger.manual, next: { out: [slow] } }
    - { id: slow, type: test.read, props: { gate: forever-run } }
`))
	id := h.Start(e, "rtimeout", nil)
	h.Entered("forever-run")
	h.Clock.Advance(time.Minute)
	v := h.Wait(e, id)
	if v.Status != engine.RunFailed || !strings.Contains(v.Error, "run timed out after 1m0s") {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
}

func TestCancel(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("cancel", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [slow] } }
    - { id: slow, type: test.read, props: { gate: cancel-gate }, next: { out: [never] } }
    - { id: never, type: test.echo }
`))
	id := h.Start(e, "cancel", nil)
	h.Entered("cancel-gate")
	if err := e.Cancel(context.Background(), id); err == nil {
		t.Fatal("cancel without actor accepted")
	}
	if err := e.Cancel(h.Ctx, id); err != nil {
		t.Fatal(err)
	}
	v := h.Wait(e, id)
	if v.Status != engine.RunCancelled || !strings.Contains(v.Error, "cancelled by cli") {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	wantStatus(t, v, map[string]engine.NodeStatus{"never": engine.NodePending})
	if h.Calls("never") != 0 {
		t.Fatal("downstream ran after cancel")
	}
	if err := e.Cancel(h.Ctx, id); !errors.Is(err, engine.ErrRunNotActive) {
		t.Fatalf("second cancel: %v", err)
	}
	if err := e.Cancel(h.Ctx, "nope"); !errors.Is(err, engine.ErrRunNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
}

func TestStartErrors(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("p", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.echo }
`))
	ctx := context.Background()
	if _, err := e.Start(ctx, "ghost", "start", nil, engine.ModeLive); !errors.Is(err, engine.ErrPolicyNotDeployed) {
		t.Fatalf("ghost: %v", err)
	}
	if _, err := e.Start(ctx, "p", "a", nil, engine.ModeLive); !errors.Is(err, engine.ErrUnknownTrigger) {
		t.Fatalf("non-trigger: %v", err)
	}
	if _, err := e.Start(ctx, "p", "start", nil, "fast"); !errors.Is(err, engine.ErrInvalidMode) {
		t.Fatalf("mode: %v", err)
	}
	if _, err := e.Start(ctx, "p", "start", strings.Repeat("x", engine.MaxValueBytes), engine.ModeLive); !errors.Is(err, engine.ErrValueTooLarge) {
		t.Fatalf("large: %v", err)
	}
	if _, err := e.Start(ctx, "p", "start", func() {}, engine.ModeLive); err == nil {
		t.Fatal("non-JSON event accepted")
	}
	if err := e.SetPaused(h.Ctx, "p", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, "p", "start", nil, engine.ModeLive); !errors.Is(err, engine.ErrPolicyPaused) {
		t.Fatalf("paused: %v", err)
	}
	if err := e.SetPaused(h.Ctx, "p", false); err != nil {
		t.Fatal(err)
	}
	h.Wait(e, h.Start(e, "p", nil))
	if err := e.SetPaused(h.Ctx, "ghost", true); !errors.Is(err, engine.ErrPolicyNotDeployed) {
		t.Fatalf("pause ghost: %v", err)
	}
}

func TestLargeOutputTruncated(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("big", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [big] } }
    - { id: big, type: test.echo, props: { value: "{{ repeat('x', 300000) }}" }, next: { out: [after] } }
    - { id: after, type: test.echo, props: { value: "{{ nodes.big['$truncated'] }}" } }
`))
	v := h.Wait(e, h.Start(e, "big", nil))
	if output(t, v, "after") != true {
		t.Fatalf("after = %v", output(t, v, "after"))
	}
}
