package catalog_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

var (
	alice = auth.Actor{Kind: auth.KindUser, ID: "1", Name: "alice"}
	bob   = auth.Actor{Kind: auth.KindToken, ID: "7", Name: "ci-token"}
)

// fakeAdapter is a controllable runtime. It never blocks unless a hook says so.
type fakeAdapter struct {
	mu    sync.Mutex
	state rt.State
	calls []string

	statusErr, startErr, stopErr, restartErr error
	// onRestart, when set, runs inside Restart before it returns.
	onRestart func()
}

func (a *fakeAdapter) Status(context.Context, *model.Service) (rt.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.statusErr != nil {
		return rt.Status{State: rt.StateUnknown}, a.statusErr
	}
	return rt.Status{State: a.state}, nil
}

func (a *fakeAdapter) record(call string) { a.calls = append(a.calls, call) }

func (a *fakeAdapter) Start(context.Context, *model.Service) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("start")
	if a.startErr != nil {
		return a.startErr
	}
	a.state = rt.StateActive
	return nil
}

func (a *fakeAdapter) Stop(context.Context, *model.Service) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("stop")
	if a.stopErr != nil {
		return a.stopErr
	}
	a.state = rt.StateInactive
	return nil
}

func (a *fakeAdapter) Restart(context.Context, *model.Service) error {
	a.mu.Lock()
	hook := a.onRestart
	a.record("restart")
	a.mu.Unlock()
	if hook != nil {
		hook()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.restartErr != nil {
		return a.restartErr
	}
	a.state = rt.StateActive
	return nil
}

func (a *fakeAdapter) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *fakeAdapter) set(f func(*fakeAdapter)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a)
}

// fakeRuntimes hands out one adapter per service name.
type fakeRuntimes struct {
	mu       sync.Mutex
	adapters map[string]*fakeAdapter
}

func (f *fakeRuntimes) For(svc *model.Service) (rt.Adapter, error) {
	return f.adapter(svc.Metadata.Name), nil
}

func (f *fakeRuntimes) adapter(name string) *fakeAdapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.adapters[name]
	if !ok {
		a = &fakeAdapter{state: rt.StateInactive}
		f.adapters[name] = a
	}
	return a
}

// fakeLocker is an in-memory named lock.
type fakeLocker struct {
	mu      sync.Mutex
	held    map[string]string
	owners  []string
	ttls    []time.Duration
	acquire int
}

func newFakeLocker() *fakeLocker { return &fakeLocker{held: map[string]string{}} }

func (l *fakeLocker) Acquire(_ context.Context, name, owner string, ttl time.Duration) (func(context.Context) error, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if by, ok := l.held[name]; ok {
		return nil, fmt.Errorf("%w: %s held by %s", catalog.ErrLockHeld, name, by)
	}
	l.held[name] = owner
	l.owners = append(l.owners, owner)
	l.ttls = append(l.ttls, ttl)
	l.acquire++
	return func(context.Context) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.held, name)
		return nil
	}, nil
}

func (l *fakeLocker) hold(name, owner string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held[name] = owner
}

func (l *fakeLocker) isHeld(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.held[name]
	return ok
}

// countingRunner counts commands passed to the wrapped runner.
type countingRunner struct {
	inner execx.Runner
	mu    sync.Mutex
	cmds  []string
}

func (c *countingRunner) Run(ctx context.Context, cmd execx.Cmd) (execx.Result, error) {
	c.mu.Lock()
	c.cmds = append(c.cmds, cmd.Display())
	c.mu.Unlock()
	return c.inner.Run(ctx, cmd)
}

func (c *countingRunner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cmds)
}

type env struct {
	t       *testing.T
	db      *store.DB
	clk     *clock.Fake
	bus     *signal.Bus
	audit   *audit.Log
	cat     *catalog.Catalog
	runtime *fakeRuntimes
	locker  *fakeLocker
	runner  *countingRunner
	mgr     *catalog.Manager
	ctx     context.Context
}

type envOption func(*env, *catalog.ManagerConfig)

func withRunner(r execx.Runner) envOption {
	return func(e *env, cfg *catalog.ManagerConfig) { e.runner.inner = r; cfg.Runner = e.runner }
}

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	e := &env{t: t, clk: clock.NewFake(epoch), runtime: &fakeRuntimes{adapters: map[string]*fakeAdapter{}}, locker: newFakeLocker()}
	e.db = storetest.Open(t)
	e.bus = signal.NewBus(testLogger(t), e.clk.Now)
	e.audit = audit.New(e.db, audit.WithClock(e.clk.Now))
	var err error
	e.cat, err = catalog.New(catalog.Config{DB: e.db, Audit: e.audit, Bus: e.bus, Clock: e.clk, Log: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	e.runner = &countingRunner{inner: &execx.Fake{}}
	cfg := catalog.ManagerConfig{
		Catalog: e.cat, Runtimes: e.runtime, Runner: e.runner, Locker: e.locker, Log: testLogger(t),
	}
	for _, o := range opts {
		o(e, &cfg)
	}
	e.mgr, err = catalog.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.ctx = auth.WithActor(context.Background(), alice)
	return e
}

// put saves a service document and fails the test on error.
func (e *env) put(doc string, expected int) catalog.PutResult {
	e.t.Helper()
	res, err := e.cat.Put(e.ctx, []byte(doc), expected)
	if err != nil {
		e.t.Fatalf("put: %v", err)
	}
	return res
}

func (e *env) state(name string) catalog.State {
	e.t.Helper()
	rec, err := e.cat.Get(context.Background(), name)
	if err != nil {
		e.t.Fatalf("get %s: %v", name, err)
	}
	return rec.State
}

// setState forces a state through legal transitions from registered.
func (e *env) setState(name string, path ...catalog.State) {
	e.t.Helper()
	cur := e.state(name)
	for _, to := range path {
		if err := e.cat.Transition(context.Background(), name, cur, to, catalog.TransitionInfo{Action: "test"}); err != nil {
			e.t.Fatalf("transition %s -> %s: %v", cur, to, err)
		}
		cur = to
	}
}

func svcDoc(name string, extra string) string {
	return "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: " + name + ", labels: { tier: web } }\nspec:\n" +
		"  runtime: { type: systemd, unit: " + name + ".service }\n" + extra
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
