package flow_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/nodes/flow"
	"github.com/ACaccel/lcm/internal/secret"
)

func policy(name, nodes string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  nodes:\n" + nodes
}

func setup(t *testing.T, srv *httptest.Server) (*enginetest.Harness, *engine.Engine) {
	t.Helper()
	h := enginetest.New(t)
	deps := flow.Deps{Secrets: missingSecrets{}}
	if srv != nil {
		deps.Client = srv.Client()
	}
	flow.Register(h.Registry, deps)
	return h, h.Open()
}

type missingSecrets struct{}

func (missingSecrets) Get(name string) (secret.Value, error) {
	return secret.Value{}, fmt.Errorf("%w: %q", secret.ErrNotFound, name)
}

// waitSuspended waits until the node is parked with its resume timer armed
// for the given time, so that advancing the clock cannot miss the timer.
func waitSuspended(t *testing.T, h *enginetest.Harness, at time.Time, runID, nodeID string) {
	t.Helper()
	h.Events.WaitFor(t, "node suspended", func(e engine.Event) bool {
		return e.Kind == engine.EventNodeSuspended && e.RunID == runID && e.NodeID == nodeID && e.RetryAt != nil && e.RetryAt.Equal(at)
	})
	h.WaitTimers(1)
}

func output(t *testing.T, v engine.RunView, id string) map[string]any {
	t.Helper()
	n, ok := v.Node(id)
	if !ok {
		t.Fatalf("no node %s", id)
	}
	m, _ := n.Output.(map[string]any)
	return m
}

func TestFlowWaitDuration(t *testing.T) {
	h, e := setup(t, nil)
	h.Deploy(e, policy("w", `
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - { id: wait, type: flow.wait, props: { duration: 10m }, next: { out: [after] } }
    - { id: after, type: test.echo, props: { value: "{{ nodes.wait }}" } }
`))
	id := h.Start(e, "w", nil)
	waitSuspended(t, h, enginetest.Epoch.Add(10*time.Minute), id, "wait")
	h.Clock.Advance(9 * time.Minute)
	if v, _ := e.Run(context.Background(), id); v.Status != engine.RunSuspended {
		t.Fatalf("status = %s", v.Status)
	}
	h.Clock.Advance(time.Minute)
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if got := output(t, v, "wait")["waitedUntil"]; got != "2026-01-01T00:10:00Z" {
		t.Fatalf("waitedUntil = %v", got)
	}
}

func TestFlowWaitUntilTimeAndPast(t *testing.T) {
	h, e := setup(t, nil)
	h.Deploy(e, policy("w", `
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - { id: wait, type: flow.wait, props: { until: "{{ trigger.at }}" } }
`))
	id := h.Start(e, "w", map[string]any{"at": "2026-01-01T03:00:00+02:00"}) // 01:00 UTC
	waitSuspended(t, h, enginetest.Epoch.Add(time.Hour), id, "wait")
	h.Clock.Advance(time.Hour)
	if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s", v.Status)
	}

	id = h.Start(e, "w", map[string]any{"at": "2025-12-31T00:00:00Z"})
	if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
		t.Fatalf("past time: status = %s", v.Status)
	}
}

func TestFlowWaitDryRunDoesNotWait(t *testing.T) {
	h, e := setup(t, nil)
	h.Deploy(e, policy("w", `
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - { id: wait, type: flow.wait, props: { duration: 24h } }
`))
	id, err := e.Start(context.Background(), "w", "start", nil, engine.ModeDry)
	if err != nil {
		t.Fatal(err)
	}
	if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s", v.Status)
	}
}

func TestFlowWaitValidation(t *testing.T) {
	h, e := setup(t, nil)
	_ = h
	for _, props := range []string{`{}`, `{ duration: 1m, until: "2026-01-01T00:00:00Z" }`, `{ until: tomorrow }`} {
		_, err := e.Validate(context.Background(), []byte(policy("v", fmt.Sprintf(`
    - { id: start, type: trigger.manual, next: { out: [w] } }
    - { id: w, type: flow.wait, props: %s }
`, props))))
		if err == nil {
			t.Errorf("props %s accepted", props)
		}
	}
}

// healthServer answers 503 for the first n requests, then 200 with a JSON
// body carrying version.
func healthServer(t *testing.T, failures int32, version string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= failures {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","version":"`+version+`"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const untilPolicy = `
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - id: wait
      type: wait.until
      props:
        interval: 30s
        timeout: 2m
        probe: { url: "{{ trigger.url }}", timeout: 5s }
        condition: "probe.body.version == inputs.want"
        inputs: { want: "{{ trigger.version }}" }
      next: { out: [ok], timeout: [late] }
    - { id: ok, type: test.echo, props: { value: "{{ nodes.wait }}" } }
    - { id: late, type: test.echo, props: { value: timed-out } }
`

func TestWaitUntilPollsUntilConditionHolds(t *testing.T) {
	srv, calls := healthServer(t, 2, "v2")
	h, e := setup(t, srv)
	h.Deploy(e, policy("health", untilPolicy))
	id := h.Start(e, "health", map[string]any{"url": srv.URL, "version": "v2"})

	for i := 1; i <= 2; i++ {
		waitSuspended(t, h, enginetest.Epoch.Add(time.Duration(i)*30*time.Second), id, "wait")
		h.Clock.Advance(30 * time.Second)
	}
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	o := output(t, v, "wait")
	if o["satisfied"] != true || o["polls"] != int64(3) || o["elapsedMs"] != int64(60_000) || calls.Load() != 3 {
		t.Fatalf("output = %v (calls %d)", o, calls.Load())
	}
	if n, _ := v.Node("late"); n.Status == engine.NodeCompleted {
		t.Fatal("timeout branch ran")
	}
	// The failed probes did not fail the node.
	if n, _ := v.Node("wait"); n.Port != "out" {
		t.Fatalf("port = %s", n.Port)
	}
}

func TestWaitUntilTimesOut(t *testing.T) {
	srv, _ := healthServer(t, 1000, "v2")
	h, e := setup(t, srv)
	h.Deploy(e, policy("health", untilPolicy))
	id := h.Start(e, "health", map[string]any{"url": srv.URL, "version": "v2"})
	for _, at := range []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second, 120 * time.Second} {
		waitSuspended(t, h, enginetest.Epoch.Add(at), id, "wait")
		h.Clock.Advance(30 * time.Second)
		if at == 120*time.Second {
			break
		}
	}
	v := h.Wait(e, id)
	n, _ := v.Node("wait")
	if v.Status != engine.RunSucceeded || n.Port != "timeout" {
		t.Fatalf("status=%s port=%s (%s)", v.Status, n.Port, v.Error)
	}
	if o := output(t, v, "wait"); o["satisfied"] != false || o["polls"] != int64(5) {
		t.Fatalf("output = %v", o)
	}
	if late, _ := v.Node("late"); late.Status != engine.NodeCompleted {
		t.Fatalf("timeout branch status = %s", late.Status)
	}
}

func TestWaitUntilSurvivesRestart(t *testing.T) {
	srv, _ := healthServer(t, 1, "v2")
	h, e := setup(t, srv)
	h.Deploy(e, policy("health", untilPolicy))
	id := h.Start(e, "health", map[string]any{"url": srv.URL, "version": "v2"})
	waitSuspended(t, h, enginetest.Epoch.Add(30*time.Second), id, "wait")
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.WaitTimers(1)
	h.Clock.Advance(30 * time.Second)
	v := h.Wait(e2, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	// The poll count and start time came from the stored suspension.
	if o := output(t, v, "wait"); o["polls"] != int64(2) || o["elapsedMs"] != int64(30_000) {
		t.Fatalf("output = %v", o)
	}
}

func TestWaitUntilConditionOnlyAndDryRun(t *testing.T) {
	h, e := setup(t, nil)
	h.Deploy(e, policy("cond", `
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - id: wait
      type: wait.until
      props: { interval: 1s, timeout: 5s, condition: "inputs.count >= 3", inputs: { count: "{{ trigger.count }}" } }
      next: { out: [ok], timeout: [late] }
    - { id: ok, type: test.echo, props: { value: ok } }
    - { id: late, type: test.echo, props: { value: late } }
`))
	id := h.Start(e, "cond", map[string]any{"count": 5})
	if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s", v.Status)
	}
	if v, _ := e.Run(context.Background(), id); func() bool { n, _ := v.Node("ok"); return n.Status != engine.NodeCompleted }() {
		t.Fatal("true condition did not continue")
	}

	// A dry run checks once and continues instead of waiting out the timeout.
	dry, err := e.Start(context.Background(), "cond", "start", map[string]any{"count": 0}, engine.ModeDry)
	if err != nil {
		t.Fatal(err)
	}
	v := h.Wait(e, dry)
	if n, _ := v.Node("wait"); v.Status != engine.RunSucceeded || n.Port != "out" || output(t, v, "wait")["dry"] != true {
		t.Fatalf("dry run: status=%s node=%+v", v.Status, n)
	}
}

func TestWaitUntilConditionErrorsAreTolerated(t *testing.T) {
	// The probe body is not JSON, so probe.body.version is a nil dereference.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "starting") }))
	defer srv.Close()
	h, e := setup(t, srv)
	h.Deploy(e, policy("p", `
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - id: wait
      type: wait.until
      props: { interval: 1s, timeout: 1s, probe: { url: "{{ trigger.url }}" }, condition: "probe.body.version == 'x'" }
      next: { timeout: [late] }
    - { id: late, type: test.echo, props: { value: late } }
`))
	id := h.Start(e, "p", map[string]any{"url": srv.URL})
	waitSuspended(t, h, enginetest.Epoch.Add(time.Second), id, "wait")
	h.Clock.Advance(time.Second)
	v := h.Wait(e, id)
	o := output(t, v, "wait")
	if n, _ := v.Node("wait"); v.Status != engine.RunSucceeded || n.Port != "timeout" || o["conditionError"] == nil {
		t.Fatalf("status=%s port=%s output=%v", v.Status, n.Port, o)
	}
}

func TestWaitUntilNetworkFailureIsNotNodeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	h, e := setup(t, nil)
	h.Deploy(e, policy("p", `
    - { id: start, type: trigger.manual, next: { out: [wait] } }
    - id: wait
      type: wait.until
      props: { interval: 1s, timeout: 1s, probe: { url: "{{ trigger.url }}", timeout: 1s } }
      next: { timeout: [late] }
    - { id: late, type: test.echo, props: { value: late } }
`))
	id := h.Start(e, "p", map[string]any{"url": url})
	waitSuspended(t, h, enginetest.Epoch.Add(time.Second), id, "wait")
	h.Clock.Advance(time.Second)
	v := h.Wait(e, id)
	probe, _ := output(t, v, "wait")["probe"].(map[string]any)
	if n, _ := v.Node("wait"); v.Status != engine.RunSucceeded || n.Port != "timeout" || probe["ok"] != false || probe["error"] == nil {
		t.Fatalf("status=%s port=%s probe=%v", v.Status, n.Port, probe)
	}
}

func TestWaitUntilValidation(t *testing.T) {
	_, e := setup(t, nil)
	cases := map[string]string{
		`{}`:                                                "probe or a condition",
		`{ condition: "probe.ok" }`:                         "needs a probe",
		`{ condition: "trigger.x == 1" }`:                   "inputs",
		`{ condition: "nodes.a.b == 1" }`:                   "",
		`{ condition: "inputs.x ==" }`:                      "condition",
		`{ condition: "true", interval: 100ms }`:            "interval",
		`{ condition: "true", interval: 1m, timeout: 30s }`: "timeout",
		`{ probe: { url: "ftp://x" } }`:                     "probe",
		`{ probe: { url: "http://x", method: PUT } }`:       "",
	}
	for props, want := range cases {
		_, err := e.Validate(context.Background(), []byte(policy("v", fmt.Sprintf(`
    - { id: start, type: trigger.manual, next: { out: [w] } }
    - { id: w, type: wait.until, props: %s }
`, props))))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("props %s: err = %v, want %q", props, err, want)
		}
	}
}
