package catalog_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/execx"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

func TestStartStopRestart(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	ad := e.runtime.adapter("app")
	ctx := e.ctx

	res, err := e.mgr.Start(ctx, "app", catalog.ActionOptions{})
	if err != nil || res.From != catalog.StateRegistered || res.To != catalog.StateRunning || res.Action != "start" {
		t.Fatalf("start = %+v %v", res, err)
	}
	if _, err := e.mgr.Start(ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("start while running: %v", err)
	}

	if res, err = e.mgr.Restart(ctx, "app", catalog.ActionOptions{}); err != nil || res.To != catalog.StateRunning {
		t.Fatalf("restart = %+v %v", res, err)
	}
	if res, err = e.mgr.Stop(ctx, "app", catalog.ActionOptions{}); err != nil || res.To != catalog.StateStopped {
		t.Fatalf("stop = %+v %v", res, err)
	}
	if _, err := e.mgr.Stop(ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("stop while stopped: %v", err)
	}
	// A stopped service is not active, so restart starts it.
	if res, err = e.mgr.Restart(ctx, "app", catalog.ActionOptions{}); err != nil || res.From != catalog.StateStopped || res.To != catalog.StateRunning {
		t.Fatalf("restart stopped = %+v %v", res, err)
	}
	if got, want := ad.Calls(), []string{"start", "restart", "stop", "start"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("adapter calls = %v, want %v", got, want)
	}
	if e.locker.isHeld(catalog.LockName("app")) {
		t.Fatal("lock not released")
	}
}

func TestFailedActionLeavesFailedUnlessStillRunning(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		from    []catalog.State
		act     func(*env) (catalog.ActionResult, error)
		prepare func(*fakeAdapter)
		want    catalog.State
	}{
		{"start fails, not running", nil,
			func(e *env) (catalog.ActionResult, error) { return e.mgr.Start(e.ctx, "app", catalog.ActionOptions{}) },
			func(a *fakeAdapter) { a.startErr = boom }, catalog.StateFailed},
		{"start fails, but is running", nil,
			func(e *env) (catalog.ActionResult, error) { return e.mgr.Start(e.ctx, "app", catalog.ActionOptions{}) },
			func(a *fakeAdapter) { a.startErr = boom; a.state = rt.StateActive }, catalog.StateRunning},
		{"stop fails, still running", []catalog.State{catalog.StateRunning},
			func(e *env) (catalog.ActionResult, error) { return e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{}) },
			func(a *fakeAdapter) { a.stopErr = boom; a.state = rt.StateActive }, catalog.StateRunning},
		{"stop fails, process gone", []catalog.State{catalog.StateRunning},
			func(e *env) (catalog.ActionResult, error) { return e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{}) },
			func(a *fakeAdapter) { a.stopErr = boom; a.state = rt.StateFailed }, catalog.StateFailed},
		{"restart fails, status unavailable", []catalog.State{catalog.StateRunning},
			func(e *env) (catalog.ActionResult, error) {
				return e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{})
			},
			func(a *fakeAdapter) { a.restartErr = boom; a.state = rt.StateActive; a.statusErr = boom }, catalog.StateFailed},
		{"restart fails, still running", []catalog.State{catalog.StateRunning},
			func(e *env) (catalog.ActionResult, error) {
				return e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{})
			},
			func(a *fakeAdapter) { a.restartErr = boom; a.state = rt.StateActive }, catalog.StateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.put(svcDoc("app", ""), 0)
			e.setState("app", tt.from...)
			tt.prepare(e.runtime.adapter("app"))
			res, err := tt.act(e)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
			if e.state("app") != tt.want || res.To != tt.want {
				t.Fatalf("state = %s (result %s), want %s", e.state("app"), res.To, tt.want)
			}
			if e.locker.isHeld(catalog.LockName("app")) {
				t.Fatal("lock not released after failure")
			}
		})
	}
}

func TestRefusesForbiddenStates(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", "  backup: { command: x }\n"), 0)
	e.setState("app", catalog.StateRetired)
	ad := e.runtime.adapter("app")
	for name, act := range map[string]func() (catalog.ActionResult, error){
		"start":    func() (catalog.ActionResult, error) { return e.mgr.Start(e.ctx, "app", catalog.ActionOptions{}) },
		"stop":     func() (catalog.ActionResult, error) { return e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{}) },
		"restart":  func() (catalog.ActionResult, error) { return e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{}) },
		"deploy":   func() (catalog.ActionResult, error) { return e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}) },
		"rollback": func() (catalog.ActionResult, error) { return e.mgr.Rollback(e.ctx, "app", catalog.ActionOptions{}) },
		"backup":   func() (catalog.ActionResult, error) { return e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{}) },
		"retire":   func() (catalog.ActionResult, error) { return e.mgr.Retire(e.ctx, "app", catalog.ActionOptions{}) },
	} {
		_, err := act()
		var se *catalog.StateError
		if !errors.As(err, &se) || se.State != catalog.StateRetired || !errors.Is(err, catalog.ErrPrecondition) && !errors.Is(err, catalog.ErrInvalidTransition) {
			t.Errorf("%s on retired: %v", name, err)
		}
	}
	if len(ad.Calls()) != 0 || len(e.runner.cmds) != 0 {
		t.Fatalf("a refused action reached the runtime: %v %v", ad.Calls(), e.runner.cmds)
	}
	if _, err := e.mgr.Start(e.ctx, "ghost", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("unknown service: %v", err)
	}
	if e.locker.isHeld(catalog.LockName("app")) || e.locker.isHeld(catalog.LockName("ghost")) {
		t.Fatal("lock leaked by a refused action")
	}
}

func TestActionNeedsActor(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	if _, err := e.mgr.Start(context.Background(), "app", catalog.ActionOptions{}); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("err = %v", err)
	}
}

func TestLockHeldElsewhere(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.locker.hold(catalog.LockName("app"), "run-9")
	_, err := e.mgr.Start(e.ctx, "app", catalog.ActionOptions{})
	if !errors.Is(err, catalog.ErrLockHeld) {
		t.Fatalf("err = %v", err)
	}
	if e.state("app") != catalog.StateRegistered || len(e.runtime.adapter("app").Calls()) != 0 {
		t.Fatal("action ran without the lock")
	}
	// The foreign holder keeps its lock; a refused action must not release it.
	if !e.locker.isHeld(catalog.LockName("app")) {
		t.Fatal("foreign lock was released")
	}
	// The busy mark is cleared, so the action works once the lock is free.
	e.locker.mu.Lock()
	delete(e.locker.held, catalog.LockName("app"))
	e.locker.mu.Unlock()
	if _, err := e.mgr.Start(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestLockOwnerIsRunIDOrActor(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	if _, err := e.mgr.Start(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{RunID: "01RUN"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.locker.owners, []string{"1", "01RUN"}) {
		t.Fatalf("owners = %v", e.locker.owners)
	}
	if e.locker.ttls[0] <= 0 {
		t.Fatalf("ttl = %v", e.locker.ttls[0])
	}
}

// TestConcurrentActionsOnOneService holds an action open and checks that a
// second action and Reconcile leave the service alone.
func TestConcurrentActionsOnOneService(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.setState("app", catalog.StateRunning)
	ad := e.runtime.adapter("app")
	ad.set(func(a *fakeAdapter) { a.state = rt.StateActive })

	entered, release := make(chan struct{}), make(chan struct{})
	ad.set(func(a *fakeAdapter) {
		a.onRestart = func() {
			close(entered)
			<-release
		}
	})
	var wg sync.WaitGroup
	wg.Add(1)
	var firstErr error
	go func() {
		defer wg.Done()
		_, firstErr = e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{})
	}()
	<-entered

	// Same process, even with a Locker that would allow it.
	e.locker.mu.Lock()
	delete(e.locker.held, catalog.LockName("app"))
	e.locker.mu.Unlock()
	if _, err := e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrLockHeld) {
		t.Fatalf("overlapping action: %v", err)
	}

	// Reconcile must not touch a service with an action in flight, even if
	// the runtime looks dead mid-restart.
	ad.set(func(a *fakeAdapter) { a.state = rt.StateInactive })
	changes, err := e.mgr.Reconcile(e.ctx)
	if err != nil || len(changes) != 0 {
		t.Fatalf("reconcile during action = %+v %v", changes, err)
	}
	close(release)
	wg.Wait()
	if firstErr != nil || e.state("app") != catalog.StateRunning {
		t.Fatalf("first action: %v, state %s", firstErr, e.state("app"))
	}
}

func TestIdempotentActions(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	ad := e.runtime.adapter("app")
	opts := catalog.ActionOptions{RunID: "r1", IdempotencyKey: "r1/restart"}

	first, err := e.mgr.Start(e.ctx, "app", opts)
	if err != nil || first.Replayed {
		t.Fatalf("first = %+v %v", first, err)
	}
	// A retry of the same node returns the stored result even though the
	// service is now running and a real start would be refused.
	again, err := e.mgr.Start(e.ctx, "app", opts)
	if err != nil || !again.Replayed || again.To != catalog.StateRunning || again.From != catalog.StateRegistered {
		t.Fatalf("replay = %+v %v", again, err)
	}
	if got := ad.Calls(); len(got) != 1 {
		t.Fatalf("adapter calls = %v", got)
	}
	// A different key is a new request.
	if _, err := e.mgr.Start(e.ctx, "app", catalog.ActionOptions{IdempotencyKey: "r2/start"}); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("new key: %v", err)
	}
	// Failures are not remembered: the retry must really run.
	ad.set(func(a *fakeAdapter) { a.stopErr = errors.New("busy") })
	key := catalog.ActionOptions{IdempotencyKey: "r3/stop"}
	if _, err := e.mgr.Stop(e.ctx, "app", key); err == nil {
		t.Fatal("want failure")
	}
	ad.set(func(a *fakeAdapter) { a.stopErr = nil })
	if res, err := e.mgr.Stop(e.ctx, "app", key); err != nil || res.Replayed || res.To != catalog.StateStopped {
		t.Fatalf("retry after failure = %+v %v", res, err)
	}
}

func TestBackupAndJobs(t *testing.T) {
	fake := (&execx.Fake{}).
		Reply("pg_dump", "dumped 3 tables\n", 0).
		Reply("reindex", "index error\n", 2)
	e := newEnv(t, withRunner(fake))
	e.put(svcDoc("db", `
  source: { repo: /srv/db, branch: main }
  backup: { command: "pg_dump db > /backup/db.sql", timeout: 5m }
  jobs:
    reindex: { command: "app reindex", timeout: 2m }
`), 0)
	e.setState("db", catalog.StateRunning)

	res, err := e.mgr.Backup(e.ctx, "db", catalog.ActionOptions{RunID: "run1"})
	if err != nil || res.Output != "dumped 3 tables\n" || res.From != catalog.StateRunning || res.To != catalog.StateRunning {
		t.Fatalf("backup = %+v %v", res, err)
	}
	call := fake.Calls()[0]
	if call.Shell != "pg_dump db > /backup/db.sql" || call.Dir != "/srv/db" || call.Timeout.Minutes() != 5 {
		t.Fatalf("backup command = %+v", call)
	}

	if _, err := e.mgr.RunJob(e.ctx, "db", catalog.ActionOptions{Job: "reindex", RunID: "run1"}); execx.ExitCode(err) != 2 {
		t.Fatalf("failing job: %v", err)
	}
	if _, err := e.mgr.RunJob(e.ctx, "db", catalog.ActionOptions{Job: "nope"}); !errors.Is(err, catalog.ErrUnknownJob) || !errors.Is(err, catalog.ErrPrecondition) {
		t.Fatalf("unknown job: %v", err)
	}
	if e.state("db") != catalog.StateRunning {
		t.Fatalf("jobs must not change state, got %s", e.state("db"))
	}

	e.put(svcDoc("nobackup", ""), 0)
	if _, err := e.mgr.Backup(e.ctx, "nobackup", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrNoBackup) {
		t.Fatalf("no backup: %v", err)
	}

	page, _ := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Service: "db", Kinds: []string{catalog.KindBackup, catalog.KindJob}})
	if len(page.Entries) != 3 {
		t.Fatalf("timeline = %+v", page.Entries)
	}
	job, backup := page.Entries[1], page.Entries[2]
	if job.Kind != catalog.KindJob || job.Data["outcome"] != "failed" || job.Data["job"] != "reindex" || job.Ref != "run1" ||
		!strings.Contains(job.Summary, "reindex failed") {
		t.Fatalf("job entry = %+v", job)
	}
	if backup.Kind != catalog.KindBackup || backup.Data["outcome"] != "succeeded" || backup.Data["output"] != "dumped 3 tables\n" {
		t.Fatalf("backup entry = %+v", backup)
	}
}

func TestIdempotencyKeysSeparateJobs(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("db", "  jobs:\n    a: { command: x }\n    b: { command: y }\n"), 0)
	key := "r/node"
	if _, err := e.mgr.RunJob(e.ctx, "db", catalog.ActionOptions{Job: "a", IdempotencyKey: key}); err != nil {
		t.Fatal(err)
	}
	res, err := e.mgr.RunJob(e.ctx, "db", catalog.ActionOptions{Job: "b", IdempotencyKey: key})
	if err != nil || res.Replayed {
		t.Fatalf("job b must not replay job a: %+v %v", res, err)
	}
	if res, _ := e.mgr.RunJob(e.ctx, "db", catalog.ActionOptions{Job: "a", IdempotencyKey: key}); !res.Replayed {
		t.Fatal("job a should replay")
	}
	if got := len(e.runner.cmds); got != 2 {
		t.Fatalf("commands run = %d", got)
	}
}

func TestRetire(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.setState("app", catalog.StateRunning)
	ad := e.runtime.adapter("app")

	res, err := e.mgr.Retire(e.ctx, "app", catalog.ActionOptions{})
	if err != nil || res.To != catalog.StateRetired {
		t.Fatalf("retire = %+v %v", res, err)
	}
	if !reflect.DeepEqual(ad.Calls(), []string{"stop"}) {
		t.Fatalf("running service must be stopped: %v", ad.Calls())
	}

	// Retiring a stopped service does not stop it again.
	e.put(svcDoc("old", ""), 0)
	e.setState("old", catalog.StateStopped)
	if _, err := e.mgr.Retire(e.ctx, "old", catalog.ActionOptions{}); err != nil || len(e.runtime.adapter("old").Calls()) != 0 {
		t.Fatalf("retire stopped: %v %v", err, e.runtime.adapter("old").Calls())
	}

	// A failing stop keeps the service from being retired.
	e.put(svcDoc("stuck", ""), 0)
	e.setState("stuck", catalog.StateRunning)
	sa := e.runtime.adapter("stuck")
	sa.set(func(a *fakeAdapter) { a.stopErr = errors.New("busy"); a.state = rt.StateActive })
	if _, err := e.mgr.Retire(e.ctx, "stuck", catalog.ActionOptions{}); err == nil || e.state("stuck") != catalog.StateRunning {
		t.Fatalf("failed retire: %v state %s", err, e.state("stuck"))
	}

	// External services cannot be stopped by LCM; retiring still works.
	e.put(svcDoc("ext", ""), 0)
	e.setState("ext", catalog.StateRunning)
	e.runtime.adapter("ext").set(func(a *fakeAdapter) { a.stopErr = rt.ErrUnsupported })
	if _, err := e.mgr.Retire(e.ctx, "ext", catalog.ActionOptions{}); err != nil || e.state("ext") != catalog.StateRetired {
		t.Fatalf("retire external: %v state %s", err, e.state("ext"))
	}
}

func TestActionsAreAuditedAndOnTimeline(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	if _, err := e.mgr.Start(e.ctx, "app", catalog.ActionOptions{RunID: "run7"}); err != nil {
		t.Fatal(err)
	}
	e.runtime.adapter("app").set(func(a *fakeAdapter) { a.stopErr = errors.New("nope"); a.state = rt.StateActive })
	_, _ = e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{})
	_, _ = e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}) // no source: refused

	entries, _ := e.audit.List(context.Background(), 0, 0)
	var got []string
	for i := len(entries) - 1; i >= 0; i-- {
		got = append(got, entries[i].Action+"/"+string(entries[i].Outcome))
	}
	want := "service.create/ok service.start/ok service.stop/error service.deploy/error"
	if strings.Join(got, " ") != want {
		t.Fatalf("audit = %v", got)
	}
	start := entries[2]
	if start.Actor != alice || start.Details["from"] != "registered" || start.Details["to"] != "running" || start.Details["runId"] != "run7" {
		t.Fatalf("start audit = %+v", start)
	}
	for _, en := range entries {
		for k, v := range en.Details {
			if s, ok := v.(string); ok && strings.Contains(s, "nope") {
				t.Fatalf("error text leaked into audit detail %s", k)
			}
		}
	}

	page, _ := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Service: "app", Kinds: []string{catalog.KindAction}})
	if len(page.Entries) != 2 || page.Entries[0].Data["outcome"] != "failed" || page.Entries[1].Summary != "start succeeded" || page.Entries[1].Ref != "run7" {
		t.Fatalf("action entries = %+v", page.Entries)
	}
}

func TestPlan(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", `
  source: { repo: /srv/app, branch: main, build: "make" }
  backup: { command: "tar c /srv/app" }
  jobs: { reindex: { command: "app reindex" } }
`), 0)
	e.setState("app", catalog.StateRunning)
	ctx := context.Background()

	p, err := e.mgr.Plan(ctx, "app", catalog.ActionDeploy, catalog.ActionOptions{})
	if err != nil || p.From != catalog.StateRunning || p.To != catalog.StateRunning {
		t.Fatalf("deploy plan = %+v %v", p, err)
	}
	joined := strings.Join(p.Steps, "\n")
	for _, want := range []string{"lock service:app", "set state updating", "git fetch --prune origin", "head of origin/main", "build: make", "restart systemd unit app.service"} {
		if !strings.Contains(joined, want) {
			t.Errorf("deploy plan lacks %q:\n%s", want, joined)
		}
	}
	if p, err = e.mgr.Plan(ctx, "app", catalog.ActionStop, catalog.ActionOptions{}); err != nil || p.To != catalog.StateStopped {
		t.Fatalf("stop plan = %+v %v", p, err)
	}
	if p, err = e.mgr.Plan(ctx, "app", catalog.ActionRunJob, catalog.ActionOptions{Job: "reindex"}); err != nil || !strings.Contains(p.Steps[1], "app reindex") {
		t.Fatalf("job plan = %+v %v", p, err)
	}
	if _, err := e.mgr.Plan(ctx, "app", catalog.ActionRunJob, catalog.ActionOptions{Job: "x"}); !errors.Is(err, catalog.ErrUnknownJob) {
		t.Fatalf("unknown job plan: %v", err)
	}
	if _, err := e.mgr.Plan(ctx, "app", catalog.ActionRollback, catalog.ActionOptions{}); !errors.Is(err, catalog.ErrNoRollbackTarget) {
		t.Fatalf("rollback plan without history: %v", err)
	}
	if _, err := e.mgr.Plan(ctx, "app", catalog.ActionStart, catalog.ActionOptions{}); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("start plan while running: %v", err)
	}
	if _, err := e.mgr.Plan(ctx, "app", catalog.ActionDeploy, catalog.ActionOptions{Version: "--bad"}); !errors.Is(err, catalog.ErrInvalidVersion) {
		t.Fatalf("bad version plan: %v", err)
	}
	if _, err := e.mgr.Plan(ctx, "ghost", catalog.ActionStop, catalog.ActionOptions{}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("unknown service plan: %v", err)
	}
	if e.locker.acquire != 0 {
		t.Fatal("Plan must not take locks")
	}
}
