package catalog_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

func TestReconcileAfterCrash(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	type tc struct {
		name    string
		path    []catalog.State // states to reach before the "crash"
		runtime rt.State
		status  error
		want    catalog.State
	}
	cases := []tc{
		{"deploying, runtime active", []catalog.State{catalog.StateDeploying}, rt.StateActive, nil, catalog.StateRunning},
		{"deploying, runtime inactive", []catalog.State{catalog.StateDeploying}, rt.StateInactive, nil, catalog.StateFailed},
		{"updating, runtime active", []catalog.State{catalog.StateRunning, catalog.StateUpdating}, rt.StateActive, nil, catalog.StateRunning},
		{"updating, runtime failed", []catalog.State{catalog.StateRunning, catalog.StateUpdating}, rt.StateFailed, nil, catalog.StateFailed},
		{"rolling back, status error", []catalog.State{catalog.StateRunning, catalog.StateRollingBack}, rt.StateActive, errors.New("dbus down"), catalog.StateFailed},
		{"running, died", []catalog.State{catalog.StateRunning}, rt.StateInactive, nil, catalog.StateFailed},
		{"running, runtime failed", []catalog.State{catalog.StateRunning}, rt.StateFailed, nil, catalog.StateFailed},
		{"running, alive", []catalog.State{catalog.StateRunning}, rt.StateActive, nil, catalog.StateRunning},
		{"running, status unknown", []catalog.State{catalog.StateRunning}, rt.StateUnknown, nil, catalog.StateRunning},
		{"running, status error", []catalog.State{catalog.StateRunning}, rt.StateInactive, errors.New("dbus down"), catalog.StateRunning},
		{"stopped, started elsewhere", []catalog.State{catalog.StateStopped}, rt.StateActive, nil, catalog.StateRunning},
		{"stopped, inactive", []catalog.State{catalog.StateStopped}, rt.StateInactive, nil, catalog.StateStopped},
		{"registered, adopted", nil, rt.StateActive, nil, catalog.StateRunning},
		{"registered, inactive", nil, rt.StateInactive, nil, catalog.StateRegistered},
		{"registered, broken unit", nil, rt.StateFailed, nil, catalog.StateFailed},
		{"failed stays failed", []catalog.State{catalog.StateFailed}, rt.StateActive, nil, catalog.StateFailed},
		{"retired is left alone", []catalog.State{catalog.StateRetired}, rt.StateActive, nil, catalog.StateRetired},
	}
	names := map[string]tc{}
	for i, c := range cases {
		name := "svc" + string(rune('a'+i/10)) + string(rune('0'+i%10))
		names[name] = c
		e.put(svcDoc(name, ""), 0)
		e.setState(name, c.path...)
		e.runtime.adapter(name).set(func(a *fakeAdapter) { a.state = c.runtime; a.statusErr = c.status })
	}

	// A new manager is what a restarted process would have.
	restarted, err := catalog.NewManager(catalog.ManagerConfig{Catalog: e.cat, Runtimes: e.runtime, Runner: e.runner, Locker: e.locker})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := restarted.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}

	changed := map[string]catalog.Change{}
	for _, ch := range changes {
		changed[ch.Service] = ch
	}
	var keys []string
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		c := names[name]
		if got := e.state(name); got != c.want {
			t.Errorf("%s: state = %s, want %s", c.name, got, c.want)
		}
		_, didChange := changed[name]
		if wantChange := len(c.path) > 0 && c.want != c.path[len(c.path)-1] || len(c.path) == 0 && c.want != catalog.StateRegistered; didChange != wantChange {
			t.Errorf("%s: reported change = %v, want %v", c.name, didChange, wantChange)
		}
	}

	// A correction explains itself on the timeline and in the audit log.
	ch := changed["svca0"]
	if ch.From != catalog.StateDeploying || ch.To != catalog.StateRunning || !strings.Contains(ch.Reason, "interrupted while deploying") {
		t.Fatalf("change = %+v", ch)
	}
	page, _ := e.cat.Timeline(ctx, catalog.TimelineQuery{Service: "svca0", Kinds: []string{catalog.KindLifecycle}, Limit: 1})
	if got := page.Entries[0]; got.Data["action"] != "reconcile" || !strings.Contains(got.Summary, "deploying -> running") {
		t.Fatalf("timeline = %+v", got)
	}
	entries, _ := e.audit.List(ctx, 0, 0)
	found := false
	for _, en := range entries {
		if en.Action == "service.reconcile" && en.Target == "svca0" {
			found = true
			if en.Actor.Kind != auth.KindSystem {
				t.Errorf("reconcile actor = %+v", en.Actor)
			}
		}
	}
	if !found {
		t.Fatal("reconcile not audited")
	}

	// A second pass finds nothing left to fix.
	again, err := restarted.Reconcile(ctx)
	if err != nil || len(again) != 0 {
		t.Fatalf("second reconcile = %+v %v", again, err)
	}
}

func TestReconcileInterruptsRunningDeployments(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.put(svcDoc("app", ""), 0)
	e.setState("app", catalog.StateRunning, catalog.StateUpdating)
	e.runtime.adapter("app").set(func(a *fakeAdapter) { a.state = rt.StateActive })
	id, err := e.cat.BeginDeployment(ctx, catalog.Deployment{Service: "app", Action: "deploy", Version: "abc1234", Actor: alice})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.mgr.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	deps := deployments(t, e, "app")
	if len(deps) != 1 || deps[0].ID != id || deps[0].Outcome != catalog.OutcomeInterrupted || deps[0].FinishedAt.IsZero() ||
		!strings.Contains(deps[0].Error, "restart") {
		t.Fatalf("deployment = %+v", deps)
	}
	if e.state("app") != catalog.StateRunning {
		t.Fatalf("state = %s", e.state("app"))
	}
	// An interrupted deployment is not a rollback target and not "last good".
	if _, err := e.mgr.Rollback(e.ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrNoRollbackTarget) {
		t.Fatalf("rollback = %v", err)
	}
}

func TestFinishDeploymentOnlyOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.cat.BeginDeployment(ctx, catalog.Deployment{Service: "app", Action: "deploy", Actor: alice})
	if err := e.cat.FinishDeployment(ctx, id, catalog.OutcomeSucceeded, "", "log"); err != nil {
		t.Fatal(err)
	}
	if err := e.cat.FinishDeployment(ctx, id, catalog.OutcomeFailed, "late", "other"); err != nil {
		t.Fatal(err)
	}
	if d := deployments(t, e, "app")[0]; d.Outcome != catalog.OutcomeSucceeded || d.LogExcerpt != "log" {
		t.Fatalf("a finished deployment was rewritten: %+v", d)
	}
	if err := e.cat.FinishDeployment(ctx, id, "running", "", ""); err == nil {
		t.Fatal("invalid outcome accepted")
	}
}

func TestDeploymentLogIsCapped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.cat.BeginDeployment(ctx, catalog.Deployment{Service: "app", Action: "deploy", Actor: alice})
	big := strings.Repeat("noise line\n", 5000) + "the actual failure\n"
	if err := e.cat.FinishDeployment(ctx, id, catalog.OutcomeFailed, strings.Repeat("e", 10_000), big); err != nil {
		t.Fatal(err)
	}
	d := deployments(t, e, "app")[0]
	if len(d.LogExcerpt) > 17<<10 || !strings.HasSuffix(d.LogExcerpt, "the actual failure\n") || !strings.HasPrefix(d.LogExcerpt, "... ") {
		t.Fatalf("log excerpt: len %d, %q", len(d.LogExcerpt), d.LogExcerpt[:20])
	}
	if len(d.Error) > 2100 {
		t.Fatalf("error len %d", len(d.Error))
	}
}

func TestPruneDeployments(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	finished := func(service string, n int) {
		for range n {
			id, err := e.cat.BeginDeployment(ctx, catalog.Deployment{Service: service, Action: "deploy", Actor: alice})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.cat.FinishDeployment(ctx, id, catalog.OutcomeSucceeded, "", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	finished("a", 5)
	finished("b", 2)
	// The oldest deployment of a is still running: it must survive.
	running, err := e.cat.BeginDeployment(ctx, catalog.Deployment{Service: "c", Action: "deploy", Actor: alice})
	if err != nil {
		t.Fatal(err)
	}
	finished("c", 3)

	if n, err := e.cat.PruneDeployments(ctx, 0); err != nil || n != 0 {
		t.Fatalf("keep 0 removed %d, %v", n, err)
	}
	n, err := e.cat.PruneDeployments(ctx, 2)
	if err != nil || n != 4 {
		t.Fatalf("pruned %d, %v; want 3 of a and 1 of c (its running record is spared)", n, err)
	}
	for svc, want := range map[string]int{"a": 2, "b": 2, "c": 3} {
		if got := len(deployments(t, e, svc)); got != want {
			t.Errorf("%s has %d deployments, want %d", svc, got, want)
		}
	}
	found := false
	for _, d := range deployments(t, e, "c") {
		found = found || d.ID == running
	}
	if !found {
		t.Error("the running deployment was pruned")
	}
}

func TestPruneCompletions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.cat.RecordCompletion(ctx, "app", "deploy", "k1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(48 * time.Hour)
	if err := e.cat.RecordCompletion(ctx, "app", "restart", "k2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if n, err := e.cat.PruneCompletions(ctx, 0); err != nil || n != 0 {
		t.Fatalf("no retention removed %d, %v", n, err)
	}
	if n, err := e.cat.PruneCompletions(ctx, 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, ok, _ := e.cat.CompletedAction(ctx, "app", "deploy", "k1"); ok {
		t.Error("the old completion survived")
	}
	if _, ok, _ := e.cat.CompletedAction(ctx, "app", "restart", "k2"); !ok {
		t.Error("the recent completion was pruned")
	}
}
