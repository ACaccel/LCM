package app_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/trigger"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// waitTimeout only guards against hangs; no assertion depends on it.
const waitTimeout = 20 * time.Second

// harness is one App on a temp data dir with a fake clock and command runner.
type harness struct {
	t      *testing.T
	dir    string
	clk    *clock.Fake
	runner *execx.Fake
	events *enginetest.Recorder
	app    *app.App
	// ctx carries the CLI actor, as for operator-driven calls.
	ctx context.Context
}

type testOpts struct {
	dir       string
	clk       *clock.Fake
	runner    *execx.Fake
	git       trigger.GitRunner
	configure func(*config.Config)
}

func newHarness(t *testing.T, mod ...func(*testOpts)) *harness {
	t.Helper()
	o := testOpts{dir: filepath.Join(t.TempDir(), "data"), clk: clock.NewFake(epoch), runner: &execx.Fake{}}
	for _, f := range mod {
		f(&o)
	}
	h := &harness{t: t, dir: o.dir, clk: o.clk, runner: o.runner, events: enginetest.NewRecorder(),
		ctx: auth.WithActor(context.Background(), auth.CLI)}
	h.app = h.open(o)
	return h
}

func (h *harness) open(o testOpts) *app.App {
	h.t.Helper()
	cfg := config.Default()
	cfg.DataDir = o.dir
	cfg.Listen = "127.0.0.1:0"
	// Binaries are irrelevant: every command goes through the fake.
	cfg.Systemctl, cfg.Docker, cfg.Git = "systemctl", "docker", "git"
	cfg.SystemctlPrivilege = config.NoPrivilege
	if o.configure != nil {
		o.configure(&cfg)
	}
	// Keep-alives would leave connection goroutines behind for goleak.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	a, err := app.New(h.ctx, cfg,
		app.WithClock(o.clk), app.WithRunner(o.runner), app.WithHTTPClient(client),
		app.WithLogger(testLogger(h.t)), app.WithObservers(h.events), app.WithGitRunner(o.git))
	if err != nil {
		h.t.Fatalf("app.New: %v", err)
	}
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			h.t.Errorf("Close: %v", err)
		}
		client.CloseIdleConnections()
	})
	return a
}

// reopen closes the current App and starts a new one on the same data dir
// and the same fake command runner, as a process restart would.
func (h *harness) reopen(clk *clock.Fake) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.app.Close(ctx); err != nil {
		h.t.Fatalf("Close before reopen: %v", err)
	}
	h.clk = clk
	h.events = enginetest.NewRecorder()
	h.app = h.open(testOpts{dir: h.dir, clk: clk, runner: h.runner})
}

// run starts App.Run and returns a function that stops it and returns its error.
func (h *harness) run() (stop func() error) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.app.Run(ctx) }()
	var once sync.Once
	var result error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case result = <-done:
			case <-time.After(waitTimeout):
				h.t.Error("Run did not return after cancel")
			}
		})
		return result
	}
	h.t.Cleanup(func() { _ = stop() })
	return stop
}

func (h *harness) waitTimers(n int) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.clk.WaitForTimers(ctx, n); err != nil {
		h.t.Fatalf("waiting for %d clock timers (have %d): %v", n, h.clk.Pending(), err)
	}
}

// advance moves the clock and waits until the monitor has stored and
// published everything the step triggered.
func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	h.clk.Advance(d)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.app.Monitor.Flush(ctx); err != nil {
		h.t.Fatalf("monitor flush: %v", err)
	}
}

// waitRun waits for the next finished run of policy and returns its view.
func (h *harness) waitRun(policy string, seen map[string]bool) engine.RunView {
	h.t.Helper()
	ev := h.events.WaitFor(h.t, "finished run of "+policy, func(e engine.Event) bool {
		return e.Kind == engine.EventRunFinished && e.Policy == policy && !seen[e.RunID]
	})
	seen[ev.RunID] = true
	view, err := h.app.Engine.Run(h.ctx, ev.RunID)
	if err != nil {
		h.t.Fatal(err)
	}
	return view
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

var _ io.Writer = testWriter{}
