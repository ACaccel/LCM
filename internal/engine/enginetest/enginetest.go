// Package enginetest provides a test harness for the engine: a real SQLite
// store, a fake clock, an event recorder and controllable test node types.
package enginetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/core"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

// Timeout bounds every wait in the harness. It only guards against hangs;
// no test logic depends on it.
const Timeout = 20 * time.Second

// Epoch is the fake clock's start time.
var Epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Harness owns the resources of one test.
type Harness struct {
	T        testing.TB
	DB       *store.DB
	Clock    *clock.Fake
	Registry *node.Registry
	Modules  engine.StaticModules
	Events   *Recorder
	// Ctx carries the CLI actor.
	Ctx context.Context

	mu      sync.Mutex
	gates   map[string]chan struct{}
	entered map[string]chan struct{}
	calls   map[string]int
}

// New creates a harness with the core and test node types registered.
func New(t testing.TB) *Harness {
	t.Helper()
	h := &Harness{
		T: t, DB: storetest.Open(t), Clock: clock.NewFake(Epoch), Registry: node.NewRegistry(),
		Modules: engine.StaticModules{}, Events: NewRecorder(),
		Ctx:   auth.WithActor(context.Background(), auth.CLI),
		gates: map[string]chan struct{}{}, entered: map[string]chan struct{}{}, calls: map[string]int{},
	}
	core.Register(h.Registry)
	h.registerTestNodes()
	return h
}

// Open starts an engine on the harness store. The engine is closed when
// the test ends unless the test closes it first.
func (h *Harness) Open(modify ...func(*engine.Config)) *engine.Engine {
	h.T.Helper()
	cfg := engine.Config{
		DB: h.DB, Registry: h.Registry, Modules: h.Modules, Workers: 4, Clock: h.Clock,
		Observers: []engine.Observer{h.Events},
	}
	for _, m := range modify {
		m(&cfg)
	}
	e, err := engine.New(cfg)
	if err != nil {
		h.T.Fatalf("engine.New: %v", err)
	}
	h.T.Cleanup(func() {
		h.ReleaseAll()
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		if err := e.Close(ctx); err != nil {
			h.T.Errorf("engine close: %v", err)
		}
	})
	return e
}

// AddModule registers a module document for module resolution.
func (h *Harness) AddModule(doc string) {
	h.T.Helper()
	m, err := model.DecodeModule([]byte(doc))
	if err != nil {
		h.T.Fatalf("decode module: %v", err)
	}
	h.Modules[m.Metadata.Name] = m
}

// Deploy deploys doc and fails the test on error.
func (h *Harness) Deploy(e *engine.Engine, doc string) int {
	h.T.Helper()
	v, err := e.Deploy(h.Ctx, []byte(doc))
	if err != nil {
		h.T.Fatalf("deploy: %v", err)
	}
	return v
}

// Start starts a run at trigger "start" in live mode.
func (h *Harness) Start(e *engine.Engine, policy string, event any) string {
	h.T.Helper()
	id, err := e.Start(context.Background(), policy, "start", event, engine.ModeLive)
	if err != nil {
		h.T.Fatalf("start %s: %v", policy, err)
	}
	return id
}

// Wait waits for a run to finish.
func (h *Harness) Wait(e *engine.Engine, runID string) engine.RunView {
	h.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	v, err := e.Wait(ctx, runID)
	if err != nil {
		h.T.Fatalf("wait %s: %v", runID, err)
	}
	return v
}

// WaitTimers waits until the fake clock has at least n pending timers.
func (h *Harness) WaitTimers(n int) {
	h.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	if err := h.Clock.WaitForTimers(ctx, n); err != nil {
		h.T.Fatalf("waiting for %d timers (have %d): %v", n, h.Clock.Pending(), err)
	}
}

// Release opens a gate; test.block and test.write nodes waiting on it
// return.
func (h *Harness) Release(gate string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := h.gateLocked(gate)
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// ReleaseAll opens every gate.
func (h *Harness) ReleaseAll() {
	h.mu.Lock()
	names := make([]string, 0, len(h.gates))
	for g := range h.gates {
		names = append(names, g)
	}
	h.mu.Unlock()
	for _, g := range names {
		h.Release(g)
	}
}

func (h *Harness) gateLocked(name string) chan struct{} {
	ch, ok := h.gates[name]
	if !ok {
		ch = make(chan struct{})
		h.gates[name] = ch
	}
	return ch
}

func (h *Harness) enteredLocked(name string) chan struct{} {
	ch, ok := h.entered[name]
	if !ok {
		ch = make(chan struct{}, 1024)
		h.entered[name] = ch
	}
	return ch
}

// Entered waits until a node has started waiting on gate.
func (h *Harness) Entered(gate string) {
	h.T.Helper()
	h.mu.Lock()
	ch := h.enteredLocked(gate)
	h.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(Timeout):
		h.T.Fatalf("no node entered gate %q", gate)
	}
}

// Calls returns how often a node id was executed across all runs.
func (h *Harness) Calls(nodeID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[nodeID]
}

func (h *Harness) count(nodeID string) {
	h.mu.Lock()
	h.calls[nodeID]++
	h.mu.Unlock()
}

func (h *Harness) block(ctx context.Context, gate string) error {
	if gate == "" {
		return nil
	}
	h.mu.Lock()
	ch := h.gateLocked(gate)
	h.enteredLocked(gate) <- struct{}{}
	h.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EchoProps configures test.echo and test.read.
type EchoProps struct {
	Value any    `json:"value"`
	Gate  string `json:"gate"`
}

// FailProps configures test.fail: the first Times attempts fail (all
// attempts when Times is negative).
type FailProps struct {
	Times     int    `json:"times" default:"-1"`
	Permanent bool   `json:"permanent"`
	Message   string `json:"message" default:"boom"`
}

// PortProps configures test.port.
type PortProps struct {
	Port string `json:"port" lcm:"required"`
}

// SuspendProps configures test.suspend.
type SuspendProps struct {
	Key      string        `json:"key"`
	ResumeIn node.Duration `json:"resumeIn"`
}

// ErrBoom is returned by test.fail.
var ErrBoom = errors.New("boom")

func (h *Harness) registerTestNodes() {
	echo := func(ctx context.Context, rc *node.RunContext, p EchoProps) (node.Result, error) {
		h.count(rc.NodeID)
		if err := h.block(ctx, p.Gate); err != nil {
			return node.Result{}, err
		}
		return node.Result{Output: p.Value}, nil
	}
	node.Register(h.Registry, node.Spec[EchoProps]{Type: "test.echo", Category: "test", Run: echo})
	node.Register(h.Registry, node.Spec[EchoProps]{Type: "test.read", Category: "test", Effect: node.EffectRead, Run: echo})
	node.Register(h.Registry, node.Spec[EchoProps]{Type: "test.write", Category: "test", Effect: node.EffectWrite, Run: echo})
	node.Register(h.Registry, node.Spec[EchoProps]{
		Type: "test.writedry", Category: "test", Effect: node.EffectWrite, Run: echo,
		DryRun: func(_ context.Context, rc *node.RunContext, p EchoProps) (node.Result, error) {
			return node.Result{Output: map[string]any{"dry": p.Value, "key": rc.IdempotencyKey}}, nil
		},
	})
	node.Register(h.Registry, node.Spec[FailProps]{
		Type: "test.fail", Category: "test",
		Run: func(_ context.Context, rc *node.RunContext, p FailProps) (node.Result, error) {
			h.count(rc.NodeID)
			if p.Times < 0 || rc.Attempt <= p.Times {
				err := fmt.Errorf("%s (attempt %d): %w", p.Message, rc.Attempt, ErrBoom)
				if p.Permanent {
					err = node.Permanent(err)
				}
				return node.Result{}, err
			}
			return node.Result{Output: map[string]any{"attempt": rc.Attempt}}, nil
		},
	})
	node.Register(h.Registry, node.Spec[PortProps]{
		Type: "test.port", Category: "test", Ports: []string{"a", "b", "c"},
		Run: func(_ context.Context, rc *node.RunContext, p PortProps) (node.Result, error) {
			h.count(rc.NodeID)
			return node.Result{Port: p.Port, Output: p.Port}, nil
		},
	})
	node.Register(h.Registry, node.Spec[SuspendProps]{
		Type: "test.suspend", Category: "test",
		Run: func(_ context.Context, rc *node.RunContext, p SuspendProps) (node.Result, error) {
			h.count(rc.NodeID)
			if rc.Resumed == nil {
				s := &node.Suspend{Key: p.Key, Output: map[string]any{"waiting": p.Key}}
				if p.ResumeIn > 0 {
					at := rc.Now().Add(p.ResumeIn.D())
					s.ResumeAt = &at
				}
				return node.Result{Suspend: s}, nil
			}
			return node.Result{Output: map[string]any{
				"payload": rc.Resumed.Payload, "timer": rc.Resumed.Timer, "saved": rc.Resumed.Output,
				"attempt": rc.Attempt,
			}}, nil
		},
	})
	node.Register(h.Registry, node.Spec[struct{}]{
		Type: "test.panic", Category: "test",
		Run: func(context.Context, *node.RunContext, struct{}) (node.Result, error) {
			panic("test panic")
		},
	})
}

// Recorder is an Observer that keeps every event.
type Recorder struct {
	mu     sync.Mutex
	events []engine.Event
	notify chan struct{}
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{notify: make(chan struct{})} }

// Observe implements engine.Observer.
func (r *Recorder) Observe(e engine.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	close(r.notify)
	r.notify = make(chan struct{})
	r.mu.Unlock()
}

// Events returns a copy of the recorded events.
func (r *Recorder) Events() []engine.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.Event(nil), r.events...)
}

// WaitFor blocks until an event matching pred has been recorded and
// returns it.
func (r *Recorder) WaitFor(t testing.TB, what string, pred func(engine.Event) bool) engine.Event {
	t.Helper()
	deadline := time.After(Timeout)
	for {
		r.mu.Lock()
		for _, e := range r.events {
			if pred(e) {
				r.mu.Unlock()
				return e
			}
		}
		ch := r.notify
		r.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// Node matches events of a kind for one node of a run.
func Node(kind engine.EventKind, runID, nodeID string) func(engine.Event) bool {
	return func(e engine.Event) bool { return e.Kind == kind && e.RunID == runID && e.NodeID == nodeID }
}

// RunEvent matches events of a kind for a run.
func RunEvent(kind engine.EventKind, runID string) func(engine.Event) bool {
	return func(e engine.Event) bool { return e.Kind == kind && e.RunID == runID }
}
