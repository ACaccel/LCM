package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestLockAdapterMapsBusyToCatalogError(t *testing.T) {
	ctx := context.Background()
	l := lockAdapter{m: lock.New(storetest.Open(t))}

	release, err := l.Acquire(ctx, "service:web", "run-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.Acquire(ctx, "service:web", "run-2", time.Hour)
	var busy *lock.BusyError
	if !errors.Is(err, catalog.ErrLockHeld) || !errors.Is(err, lock.ErrHeld) || !errors.As(err, &busy) || busy.Holder.Owner != "run-1" {
		t.Fatalf("busy lock error = %v", err)
	}

	// The same owner extends its lock, which keeps retried actions idempotent.
	if _, err := l.Acquire(ctx, "service:web", "run-1", time.Hour); err != nil {
		t.Fatalf("re-acquire by owner: %v", err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(ctx, "service:web", "run-2", time.Hour); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	// Errors other than "held" must not look like a busy service.
	_, err = l.Acquire(ctx, "bad name!", "run-1", time.Hour)
	if err == nil || errors.Is(err, catalog.ErrLockHeld) || !errors.Is(err, lock.ErrInvalid) {
		t.Fatalf("invalid name error = %v", err)
	}
}

func TestMonitorRunnerTranslatesResults(t *testing.T) {
	fake := &execx.Fake{}
	fake.Reply("is-active up", "active\n", 0).Reply("is-active down", "inactive\n", 3)
	fake.On(func(c execx.Cmd) bool { return c.Argv[0] == "/opt/bin/systemctl" && c.Argv[1] == "boom" },
		func(execx.Cmd) (execx.Result, error) { return execx.Result{ExitCode: -1}, errors.New("not started") })
	r := monitorRunner{run: fake, binaries: map[string]string{"systemctl": "/opt/bin/systemctl"}}
	ctx := context.Background()

	res, err := r.Run(ctx, monitor.Command{Name: "systemctl", Args: []string{"is-active", "up"}})
	if err != nil || res.ExitCode != 0 || res.Stdout != "active\n" {
		t.Fatalf("success: %+v, %v", res, err)
	}
	res, err = r.Run(ctx, monitor.Command{Name: "systemctl", Args: []string{"is-active", "down"}})
	if err != nil || res.ExitCode != 3 || res.Stdout != "inactive\n" || res.Stderr != "inactive\n" {
		t.Fatalf("non-zero exit must be a result, got %+v, %v", res, err)
	}
	if _, err = r.Run(ctx, monitor.Command{Name: "systemctl", Args: []string{"boom"}}); err == nil {
		t.Fatal("a command that could not run must be an error")
	}

	calls := fake.Calls()
	if calls[0].Argv[0] != "/opt/bin/systemctl" {
		t.Errorf("configured binary not used: %v", calls[0].Argv)
	}
	_, _ = r.Run(ctx, monitor.Command{Name: "other", Args: []string{"x"}})
	if last := fake.Calls()[len(fake.Calls())-1]; last.Argv[0] != "other" {
		t.Errorf("unmapped program renamed: %v", last.Argv)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Run(cancelled, monitor.Command{Name: "x"}); err == nil {
		t.Error("canceled context must be an error")
	}
}

func TestMonitorDefsSkipRetiredAndCarryLabels(t *testing.T) {
	svc := func(name string, checks ...string) *model.Service {
		s := &model.Service{}
		s.Metadata = model.Metadata{Name: name, Labels: map[string]string{"tier": "web"}}
		for _, c := range checks {
			s.Spec.Checks = append(s.Spec.Checks, model.Check{Name: c, Type: "http"})
		}
		return s
	}
	recs := []catalog.Record{
		{Name: "a", State: catalog.StateRunning, Spec: svc("a", "one", "two")},
		{Name: "b", State: catalog.StateRetired, Spec: svc("b", "one")},
		{Name: "c", State: catalog.StateStopped, Spec: svc("c")},
		{Name: "d", State: catalog.StateFailed},
	}
	defs := monitorDefs(recs)
	if len(defs) != 2 || defs[0].ID() != "a/one" || defs[1].ID() != "a/two" || defs[0].Labels["tier"] != "web" {
		t.Fatalf("defs = %+v", defs)
	}
}
