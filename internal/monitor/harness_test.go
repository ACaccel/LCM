package monitor

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const testInterval = 10 * time.Second

type fakeProps struct {
	Key string `json:"key"`
}

// harness drives a Monitor with a fake clock, scripted probes and a real
// SQLite store.
type harness struct {
	t     *testing.T
	db    *store.DB
	clk   *clock.Fake
	bus   *signal.Bus
	sub   *signal.Subscription
	reg   *Registry
	m     *Monitor
	ctx   context.Context
	cfg   Config
	mu    sync.Mutex
	res   map[string]Result
	calls map[string]int
	// gate, when set for a key, blocks that key's probe until released or
	// canceled.
	gates map[string]chan struct{}
	enter chan string
	cur   int
	peak  int
}

func newHarness(t *testing.T, mod ...func(*Config)) *harness {
	t.Helper()
	h := &harness{
		t: t, db: storetest.Open(t), clk: clock.NewFake(epoch), ctx: context.Background(),
		res: map[string]Result{}, calls: map[string]int{}, gates: map[string]chan struct{}{}, enter: make(chan string, 64),
	}
	h.bus = signal.NewBus(slog.New(slog.DiscardHandler), h.clk.Now)
	h.sub = h.bus.Subscribe("test", 256)
	h.reg = NewRegistry()
	Register(h.reg, Spec[fakeProps]{Type: "fake", Build: h.buildFake})
	Register(h.reg, Spec[heartbeatProps]{Type: "heartbeat", Build: buildHeartbeat})
	h.cfg = Config{
		DB: h.db, Bus: h.bus, Registry: h.reg, Runner: &fakeRunner{}, Clock: h.clk,
		Logger: slog.New(slog.DiscardHandler), StartSpread: -1,
	}
	for _, f := range mod {
		f(&h.cfg)
	}
	h.m = h.open()
	return h
}

func (h *harness) open() *Monitor {
	h.t.Helper()
	m, err := New(h.ctx, h.cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = m.Close() })
	return m
}

// restart closes the monitor and opens a new one on the same store.
func (h *harness) restart() {
	h.t.Helper()
	if err := h.m.Close(); err != nil {
		h.t.Fatal(err)
	}
	h.drain()
	h.m = h.open()
}

func (h *harness) buildFake(_ model.Check, p fakeProps, _ Env) (Built, error) {
	return Built{Probe: func(ctx context.Context) Result {
		h.mu.Lock()
		h.calls[p.Key]++
		gate := h.gates[p.Key]
		h.cur++
		h.peak = max(h.peak, h.cur)
		r, ok := h.res[p.Key]
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			h.cur--
			h.mu.Unlock()
		}()
		if gate != nil {
			h.enter <- p.Key
			select {
			case <-gate:
			case <-ctx.Done():
				return failure(ctx, ctx.Err())
			}
		}
		if !ok {
			return Result{OK: true, Latency: 5 * time.Millisecond, Message: "ok"}
		}
		return r
	}}, nil
}

func (h *harness) set(key string, r Result) {
	h.mu.Lock()
	h.res[key] = r
	h.mu.Unlock()
}

func (h *harness) fail(key string) { h.set(key, Result{Message: "boom"}) }
func (h *harness) pass(key string) {
	h.set(key, Result{OK: true, Latency: 5 * time.Millisecond, Message: "ok"})
}

func (h *harness) callCount(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[key]
}

func fakeDef(service, name string, mod ...func(*model.Check)) Def {
	c := model.Check{Name: name, Type: "fake", Interval: model.Duration(testInterval), Props: map[string]any{"key": service + "/" + name}}
	for _, f := range mod {
		f(&c)
	}
	return Def{Service: service, Check: c}
}

func nonCritical(c *model.Check) { f := false; c.Critical = &f }
func threshold(n int) func(*model.Check) {
	return func(c *model.Check) { c.Threshold = n }
}

func (h *harness) sync(defs ...Def) {
	h.t.Helper()
	if err := h.m.Sync(h.ctx, defs); err != nil {
		h.t.Fatalf("Sync: %v", err)
	}
}

// step advances the clock and waits until everything it triggered is
// stored and published. Callers advance at most one interval at a time so
// a check never has two firings in one step.
func (h *harness) step(d time.Duration) {
	h.t.Helper()
	h.clk.Advance(d)
	if err := h.m.Flush(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

// drain returns the signals published since the last call.
func (h *harness) drain() []signal.Signal {
	var out []signal.Signal
	for {
		select {
		case s := <-h.sub.C:
			out = append(out, s)
		default:
			return out
		}
	}
}

func kinds(sigs []signal.Signal, k signal.Kind) []signal.Signal {
	var out []signal.Signal
	for _, s := range sigs {
		if s.Kind == k {
			out = append(out, s)
		}
	}
	return out
}

func (h *harness) check(service, name string) CheckStatus {
	h.t.Helper()
	s, ok := h.m.Service(service)
	if !ok {
		h.t.Fatalf("service %s not found", service)
	}
	for _, c := range s.Checks {
		if c.Name == name {
			return c
		}
	}
	h.t.Fatalf("check %s/%s not found", service, name)
	return CheckStatus{}
}

func (h *harness) health(service string) Health {
	h.t.Helper()
	s, ok := h.m.Service(service)
	if !ok {
		h.t.Fatalf("service %s not found", service)
	}
	return s.Health
}
