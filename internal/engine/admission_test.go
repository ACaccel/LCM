package engine_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

// insertHold parks one Start between its concurrency decision and the
// write of its run row.
type insertHold struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *insertHold) Release() { h.once.Do(func() { close(h.release) }) }

func (h *insertHold) Entered(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(enginetest.Timeout):
		t.Fatal("start did not reach the run insert")
	}
}

// holdInserts holds the run inserts of the given start calls, counted from
// 1 in the order they reach the insert.
func holdInserts(t *testing.T, calls ...int) []*insertHold {
	t.Helper()
	byCall := map[int]*insertHold{}
	var holds []*insertHold
	for _, c := range calls {
		h := &insertHold{entered: make(chan struct{}), release: make(chan struct{})}
		byCall[c] = h
		holds = append(holds, h)
	}
	var mu sync.Mutex
	n := 0
	restore := engine.SetBeforeRunInsert(func() {
		mu.Lock()
		n++
		h := byCall[n]
		mu.Unlock()
		if h != nil {
			close(h.entered)
			<-h.release
		}
	})
	t.Cleanup(func() {
		for _, h := range holds {
			h.Release()
		}
		restore()
	})
	return holds
}

type startResult struct {
	id  string
	err error
}

func startAsync(e *engine.Engine, policy string, event any) <-chan startResult {
	ch := make(chan startResult, 1)
	go func() {
		id, err := e.Start(context.Background(), policy, "start", event, engine.ModeLive)
		ch <- startResult{id, err}
	}()
	return ch
}

func await(t *testing.T, ch <-chan startResult) startResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(enginetest.Timeout):
		t.Fatal("start did not return")
		return startResult{}
	}
}

// within fails the test when f does not return in time, which is how a
// call blocked on the engine lock shows.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(enginetest.Timeout):
		t.Fatalf("%s blocked while a run was being stored", what)
	}
}

func TestStartStoresRunOutsideEngineLock(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("slow", "{ mode: queue }"))
	h.Deploy(e, gatedPolicy("other", "{ mode: queue }"))
	hold := holdInserts(t, 1)[0]

	slow := startAsync(e, "slow", map[string]any{})
	hold.Entered(t)
	within(t, "another policy's run", func() {
		id, err := e.Start(context.Background(), "other", "start", map[string]any{}, engine.ModeLive)
		if err != nil {
			t.Errorf("start other: %v", err)
			return
		}
		if v, err := e.Wait(context.Background(), id); err != nil || v.Status != engine.RunSucceeded {
			t.Errorf("other run = %s, %v", v.Status, err)
		}
		if len(e.Plans()) != 2 {
			t.Errorf("plans = %d", len(e.Plans()))
		}
	})
	hold.Release()
	res := await(t, slow)
	if res.err != nil {
		t.Fatalf("start slow: %v", res.err)
	}
	if v := h.Wait(e, res.id); v.Status != engine.RunSucceeded {
		t.Fatalf("slow run: %s", v.Status)
	}
}

func TestQueuedRunPromotedWhileStored(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("q", "{ mode: queue }"))
	first := h.Start(e, "q", map[string]any{"gate": "q0"})
	h.Entered("q0")
	hold := holdInserts(t, 1)[0]

	second := startAsync(e, "q", map[string]any{})
	hold.Entered(t)
	// The first run finishes while the second is reserved in the queue but
	// not stored: its place becomes active, and it must begin only once
	// stored.
	h.Release("q0")
	within(t, "the active run finishing", func() {
		if v := h.Wait(e, first); v.Status != engine.RunSucceeded {
			t.Errorf("first: %s", v.Status)
		}
	})
	hold.Release()
	res := await(t, second)
	if res.err != nil {
		t.Fatalf("second start: %v", res.err)
	}
	if v := h.Wait(e, res.id); v.Status != engine.RunSucceeded {
		t.Fatalf("second: %s", v.Status)
	}
	var kinds []engine.EventKind
	for _, ev := range h.Events.Events() {
		if ev.RunID == res.id && ev.NodeID == "" {
			kinds = append(kinds, ev.Kind)
		}
	}
	if len(kinds) < 2 || kinds[0] != engine.EventRunQueued || kinds[1] != engine.EventRunStarted {
		t.Fatalf("second run events %v, want queued then started", kinds)
	}
}

func TestFailedStartReleasesReservation(t *testing.T) {
	for _, mode := range []string{"{ mode: queue }", "{ mode: drop }", "{ mode: replace }", "{ mode: parallel, limit: 1 }"} {
		t.Run(mode, func(t *testing.T) {
			h := enginetest.New(t)
			e := h.Open()
			h.Deploy(e, gatedPolicy("p", mode))
			restore := storetest.FailInserts(t, h.DB, "runs")
			if id, err := e.Start(context.Background(), "p", "start", map[string]any{}, engine.ModeLive); err == nil {
				t.Fatalf("start stored run %s although inserts fail", id)
			}
			restore()
			// The failed start left no place behind: the next one runs at
			// once instead of being queued or dropped.
			id := h.Start(e, "p", map[string]any{"gate": "g"})
			h.Entered("g")
			if v, _ := e.Run(context.Background(), id); v.Status != engine.RunRunning {
				t.Fatalf("next run is %s, want running", v.Status)
			}
			h.Release("g")
			if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
				t.Fatalf("next run: %s", v.Status)
			}
		})
	}
}

func TestFailedReplaceKeepsActiveRun(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("r", "{ mode: replace }"))
	first := h.Start(e, "r", map[string]any{"gate": "r0"})
	h.Entered("r0")
	restore := storetest.FailInserts(t, h.DB, "runs")
	if _, err := e.Start(context.Background(), "r", "start", map[string]any{}, engine.ModeLive); err == nil {
		t.Fatal("start succeeded although inserts fail")
	}
	restore()
	if v, _ := e.Run(context.Background(), first); v.Status != engine.RunRunning {
		t.Fatalf("active run is %s after a failed replacing start", v.Status)
	}
	h.Release("r0")
	if v := h.Wait(e, first); v.Status != engine.RunSucceeded {
		t.Fatalf("first: %s (%s)", v.Status, v.Error)
	}
}

// A start waiting behind one whose write fails decides against the state
// without the failed run, as if the failed start had never happened.
func TestStartRacingFailedWrite(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("d", "{ mode: drop }"))
	holds := holdInserts(t, 1, 2)
	restore := storetest.FailInserts(t, h.DB, "runs")

	failing := startAsync(e, "d", map[string]any{})
	holds[0].Entered(t)
	racing := startAsync(e, "d", map[string]any{"gate": "d1"})
	holds[0].Release()
	if res := await(t, failing); res.err == nil || errors.Is(res.err, engine.ErrDropped) {
		t.Fatalf("failing start: %q %v", res.id, res.err)
	}
	holds[1].Entered(t)
	restore()
	holds[1].Release()
	res := await(t, racing)
	if res.err != nil {
		t.Fatalf("racing start: %v", res.err)
	}
	h.Entered("d1")
	h.Release("d1")
	if v := h.Wait(e, res.id); v.Status != engine.RunSucceeded {
		t.Fatalf("racing run: %s", v.Status)
	}
}

func TestUndeployCancelsRunBeingStored(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, gatedPolicy("u", "{ mode: queue }"))
	first := h.Start(e, "u", map[string]any{"gate": "u0"})
	h.Entered("u0")
	hold := holdInserts(t, 1)[0]

	queued := startAsync(e, "u", map[string]any{})
	hold.Entered(t)
	within(t, "undeploy", func() {
		if err := e.Undeploy(h.Ctx, "u"); err != nil {
			t.Errorf("undeploy: %v", err)
		}
	})
	hold.Release()
	res := await(t, queued)
	if res.err != nil {
		t.Fatalf("start: %v", res.err)
	}
	if v := h.Wait(e, res.id); v.Status != engine.RunCancelled || v.Error != "policy undeployed" {
		t.Fatalf("queued run: %s (%s)", v.Status, v.Error)
	}
	if h.Calls("work") != 1 {
		t.Fatalf("work ran %d times, want 1", h.Calls("work"))
	}
	h.Release("u0")
	if v := h.Wait(e, first); v.Status != engine.RunSucceeded {
		t.Fatalf("active run: %s", v.Status)
	}
}

// maxOverlap returns the largest number of runs of policy that were
// running at once according to the event stream.
func maxOverlap(events []engine.Event, policy string) int {
	started := map[string]bool{}
	cur, peak := 0, 0
	for _, ev := range events {
		if ev.Policy != policy || ev.NodeID != "" {
			continue
		}
		switch ev.Kind {
		case engine.EventRunStarted:
			started[ev.RunID] = true
			cur++
			peak = max(peak, cur)
		case engine.EventRunFinished:
			if started[ev.RunID] {
				cur--
			}
		}
	}
	return peak
}

func TestConcurrentStartsKeepModes(t *testing.T) {
	const n = 16
	cases := []struct {
		mode  string
		limit int
	}{
		{"{ mode: queue }", 1},
		{"{ mode: drop }", 1},
		{"{ mode: replace }", 1},
		{"{ mode: parallel, limit: 3 }", 3},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			h := enginetest.New(t)
			e := h.Open()
			h.Deploy(e, gatedPolicy("c", tc.mode))
			results := make([]<-chan startResult, n)
			for i := range results {
				results[i] = startAsync(e, "c", map[string]any{"n": i})
			}
			var ids, dropped []string
			for _, ch := range results {
				res := await(t, ch)
				switch {
				case errors.Is(res.err, engine.ErrDropped):
					dropped = append(dropped, res.id)
				case res.err != nil:
					t.Fatalf("start: %v", res.err)
				default:
					ids = append(ids, res.id)
				}
			}
			views := map[string]engine.RunView{}
			for _, id := range ids {
				views[id] = h.Wait(e, id)
			}
			newest := slices.Max(ids)
			for id, v := range views {
				switch {
				case strings.Contains(tc.mode, "replace") && id != newest:
					if v.Status != engine.RunCancelled && v.Status != engine.RunSucceeded {
						t.Errorf("run %s: %s", id, v.Status)
					}
				case v.Status != engine.RunSucceeded:
					t.Errorf("run %s: %s (%s)", id, v.Status, v.Error)
				}
			}
			if strings.Contains(tc.mode, "drop") {
				// Each start either ran or was dropped while another
				// was active; nothing was lost.
				if len(ids)+len(dropped) != n || len(ids) == 0 {
					t.Fatalf("%d ran, %d dropped", len(ids), len(dropped))
				}
			} else if len(ids) != n {
				t.Fatalf("%d of %d starts admitted", len(ids), n)
			}
			// Observer events arrive asynchronously: wait for the last one.
			h.Events.WaitFor(t, "newest run finished", enginetest.RunEvent(engine.EventRunFinished, newest))
			if e.ObserverDrops() != 0 {
				t.Fatalf("observer dropped %d events", e.ObserverDrops())
			}
			if got := maxOverlap(h.Events.Events(), "c"); got > tc.limit {
				t.Fatalf("%d runs active at once, limit %d", got, tc.limit)
			}
			if v := views[newest]; v.Status != engine.RunSucceeded {
				t.Fatalf("newest run %s: %s", newest, v.Status)
			}
		})
	}
}
