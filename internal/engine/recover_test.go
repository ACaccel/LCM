package engine_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
)

func writeSQL(t *testing.T, h *enginetest.Harness, query string, args ...any) {
	t.Helper()
	err := h.DB.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), query, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// crash closes e with an expired context, cancelling in-flight nodes the
// way a process exit would leave them: their attempts are not recorded.
func crash(t *testing.T, e *engine.Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
}

func storedStatus(t *testing.T, h *enginetest.Harness, runID string) string {
	t.Helper()
	var s string
	if err := h.DB.Reader().QueryRow(`SELECT status FROM runs WHERE id = ?`, runID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecoverResumesReadNodes(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("rec", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [first] } }
    - { id: first, type: test.echo, props: { value: "{{ trigger.n * 2 }}" }, next: { out: [reader] } }
    - { id: reader, type: test.read, props: { gate: rec-read }, next: { out: [after] } }
    - { id: after, type: test.echo, props: { value: "{{ nodes.first }}" } }
`))
	id := h.Start(e1, "rec", map[string]any{"n": 21})
	h.Entered("rec-read")
	crash(t, e1)
	if got := storedStatus(t, h, id); got != "running" {
		t.Fatalf("stored status after crash = %s", got)
	}

	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.Entered("rec-read")
	h.Release("rec-read")
	v := h.Wait(e2, id)
	if v.Status != engine.RunSucceeded || output(t, v, "after") != int64(42) {
		t.Fatalf("status = %s, after = %v", v.Status, output(t, v, "after"))
	}
	if h.Calls("first") != 1 || h.Calls("reader") != 2 {
		t.Fatalf("calls first=%d reader=%d", h.Calls("first"), h.Calls("reader"))
	}
	// The deployment survived the restart.
	h.Wait(e2, h.Start(e2, "rec", map[string]any{"n": 1}))
}

func TestRecoverWriteNodeNotReexecuted(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("recw", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [handled, unhandled] } }
    - { id: handled, type: test.write, props: { gate: recw-1 }, next: { error: [alert] } }
    - { id: unhandled, type: test.write, props: { gate: recw-2 } }
    - { id: alert, type: test.echo, props: { value: "{{ nodes.handled.message }}" } }
`))
	id := h.Start(e1, "recw", nil)
	h.Entered("recw-1")
	h.Entered("recw-2")
	crash(t, e1)

	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := h.Wait(e2, id)
	if v.Status != engine.RunFailed || !strings.Contains(v.Error, "node unhandled failed: interrupted during side effect") {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if h.Calls("handled") != 1 || h.Calls("unhandled") != 1 {
		t.Fatalf("write nodes re-executed: %d %d", h.Calls("handled"), h.Calls("unhandled"))
	}
	n, _ := v.Node("handled")
	if n.Port != "error" || !strings.Contains(n.Error, "interrupted during side effect") {
		t.Fatalf("handled = %+v", n)
	}
	// The error port may or may not have run before the run failed; if it
	// did, it saw the interruption message.
	if out, ok := output(t, v, "alert").(string); ok && !strings.Contains(out, "interrupted") {
		t.Fatalf("alert = %q", out)
	}
}

func TestRecoverAbort(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("abort", `
  onInterrupt: abort
  nodes:
    - { id: start, type: trigger.manual, next: { out: [reader] } }
    - { id: reader, type: test.read, props: { gate: abort-gate } }
`))
	id := h.Start(e1, "abort", nil)
	h.Entered("abort-gate")
	crash(t, e1)

	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := e2.Run(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != engine.RunInterrupted || h.Calls("reader") != 1 {
		t.Fatalf("status = %s, calls = %d", v.Status, h.Calls("reader"))
	}
	if n, _ := v.Node("start"); n.Status != engine.NodeCompleted {
		t.Fatalf("stored view of start = %+v", n)
	}
}

func TestRecoverQueuedEventsAndRetries(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("recq", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [flaky] } }
    - { id: flaky, type: test.fail, props: { times: 1 }, retry: { max: 2, backoff: 10s }, next: { out: [gated] } }
    - { id: gated, type: test.read, props: { gate: "{{ trigger.gate }}" } }
`))
	first := h.Start(e1, "recq", map[string]any{"gate": "recq-0"})
	second := h.Start(e1, "recq", map[string]any{"gate": "recq-1"})
	h.Events.WaitFor(t, "retrying", enginetest.Node(engine.EventNodeRetrying, first, "flaky"))
	crash(t, e1)
	if got := storedStatus(t, h, second); got != "queued" {
		t.Fatalf("second stored as %s", got)
	}

	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, _ := e2.Run(context.Background(), first)
	if n, _ := v.Node("flaky"); n.Status != engine.NodeRetrying || n.Attempts != 1 {
		t.Fatalf("flaky after restart = %+v", n)
	}
	h.WaitTimers(1)
	h.Clock.Advance(10 * time.Second)
	h.Entered("recq-0")
	if v, _ := e2.Run(context.Background(), second); v.Status != engine.RunQueued {
		t.Fatalf("second = %s", v.Status)
	}
	h.Release("recq-0")
	h.Wait(e2, first)
	h.Events.WaitFor(t, "second retrying", enginetest.Node(engine.EventNodeRetrying, second, "flaky"))
	h.WaitTimers(1)
	h.Clock.Advance(10 * time.Second)
	h.Release("recq-1")
	v = h.Wait(e2, second)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("second: %s (%s)", v.Status, v.Error)
	}
	if n, _ := h.Wait(e2, first).Node("flaky"); n.Attempts != 2 {
		t.Fatalf("first flaky attempts = %d", n.Attempts)
	}
}

func TestRecoverRunTimeoutElapsed(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("rto", `
  timeout: 1h
  nodes:
    - { id: start, type: trigger.manual, next: { out: [reader] } }
    - { id: reader, type: test.read, props: { gate: rto-gate } }
`))
	id := h.Start(e1, "rto", nil)
	h.Entered("rto-gate")
	crash(t, e1)
	h.Clock.Advance(2 * time.Hour)
	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := h.Wait(e2, id); v.Status != engine.RunFailed || !strings.Contains(v.Error, "timed out") {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
}

func TestGracefulCloseFinishesInFlight(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("graceful", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [reader] } }
    - { id: reader, type: test.read, props: { gate: graceful-gate }, next: { out: [next] } }
    - { id: next, type: test.echo }
`))
	id := h.Start(e, "graceful", nil)
	h.Entered("graceful-gate")
	done := make(chan error, 1)
	go func() { done <- e.Close(context.Background()) }()
	h.Release("graceful-gate")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(context.Background(), "graceful", "start", nil, engine.ModeLive); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("start after close: %v", err)
	}
	// reader completed and was stored; next never started and runs after
	// recovery.
	e2 := h.Open()
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := h.Wait(e2, id)
	if v.Status != engine.RunSucceeded || h.Calls("reader") != 1 || h.Calls("next") != 1 {
		t.Fatalf("status = %s, calls reader=%d next=%d", v.Status, h.Calls("reader"), h.Calls("next"))
	}
}
