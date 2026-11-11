package catalog_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/message"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

const instancesConfig = `    limits: { memoryMax: 512M }
    instances:
      a: {}
      b: { start: server/b.mjs }
      c: { startArgs: [], limits: { memoryMax: 1G } }
`

// instanceSim adds instances to the simulated helper: enablement and state
// per instance, the aggregate status and the last-instance refusal.
type instanceSim struct {
	mu      sync.Mutex
	enabled map[string]bool
	active  map[string]string
	// missing lists instances that have no unit yet.
	missing map[string]bool
}

func (s *instanceSim) intercept(req deployhelper.Request) (deployhelper.Response, error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := helperReply(req)
	if req.Op.Changes() {
		resp.State = deployhelper.StateSucceeded
	}
	switch {
	case req.Op == deployhelper.OpActivate && req.Instances != nil:
		for _, in := range req.Instances {
			if _, ok := s.enabled[in.Name]; !ok {
				s.enabled[in.Name] = true
			}
			if s.enabled[in.Name] {
				s.active[in.Name] = "active"
			}
		}
		return deployhelper.Response{}, nil, false
	case req.Op == deployhelper.OpStatus:
		var list []deployhelper.InstanceStatus
		var states []string
		for _, n := range []string{"a", "b", "c"} {
			if s.missing[n] {
				list = append(list, deployhelper.InstanceStatus{Name: n, LoadState: "not-found", ActiveState: "inactive", SubState: "dead", Granted: true})
				continue
			}
			file := "disabled"
			if s.enabled[n] {
				file = "enabled"
				states = append(states, s.active[n])
			}
			list = append(list, deployhelper.InstanceStatus{Name: n, LoadState: "loaded", ActiveState: s.active[n], SubState: "running", UnitFileState: file, Granted: true})
		}
		agg := "active"
		switch {
		case slices.Contains(states, "failed"):
			agg = "failed"
		case len(states) == 0:
			agg = "inactive"
		}
		resp.Unit, resp.Instances = &deployhelper.UnitStatus{LoadState: "loaded", ActiveState: agg}, list
		return resp, nil, true
	case req.Instance != "":
		switch req.Op {
		case deployhelper.OpStop:
			others := 0
			for n, on := range s.enabled {
				if on && n != req.Instance {
					others++
				}
			}
			if others == 0 {
				resp, err := refusal(resp, deployhelper.CodeRefused, req.Instance+" is the last enabled instance of app; stop the service instead")
				return resp, err, true
			}
			s.enabled[req.Instance], s.active[req.Instance] = false, "inactive"
		default:
			s.enabled[req.Instance], s.active[req.Instance] = true, "active"
		}
		return resp, nil, true
	}
	return deployhelper.Response{}, nil, false
}

// instancesEnv is a deployed managed service with the instances a, b and
// c; onInstances counts the calls of ManagerConfig.OnInstances.
func instancesEnv(t *testing.T) (*env, *simHelper, *instanceSim, *[]string) {
	t.Helper()
	g := newGitFixture(t)
	h := newSimHelper()
	sim := &instanceSim{enabled: map[string]bool{}, active: map[string]string{}}
	h.intercept = sim.intercept
	var calls []string
	var mu sync.Mutex
	e := newEnv(t, realGit(g), withHelper(h), func(_ *env, cfg *catalog.ManagerConfig) {
		cfg.OnInstances = func(svc string) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, svc)
		}
	})
	e.put(managedDocWith(g.remote, instancesConfig), 0)
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	return e, h, sim, &calls
}

func TestInstancesDeploy(t *testing.T) {
	e, h, _, _ := instancesEnv(t)
	var act deployhelper.Request
	for _, r := range h.Requests() {
		if r.Op == deployhelper.OpActivate {
			act = r
		}
	}
	want := []deployhelper.InstanceSpec{
		{Name: "a", Start: []string{"server/main.mjs", "--quiet"}, Limits: &deployhelper.UnitLimits{MemoryMax: "512M"}},
		{Name: "b", Start: []string{"server/b.mjs", "--quiet"}, Limits: &deployhelper.UnitLimits{MemoryMax: "512M"}},
		{Name: "c", Start: []string{"server/main.mjs"}, Limits: &deployhelper.UnitLimits{MemoryMax: "1G"}},
	}
	if act.Start != nil || act.Limits != nil || len(act.Instances) != 3 {
		t.Fatalf("activate = %+v", act)
	}
	for i := range want {
		if act.Instances[i].Name != want[i].Name || !slices.Equal(act.Instances[i].Start, want[i].Start) || *act.Instances[i].Limits != *want[i].Limits {
			t.Errorf("instance %d = %+v, want %+v", i, act.Instances[i], want[i])
		}
	}
	if e.state("app") != catalog.StateRunning {
		t.Fatalf("state %s", e.state("app"))
	}

	// A single-instance service sends none of the new fields.
	_, single := backupEnv(t)
	for _, r := range single.Requests() {
		if r.Instances != nil || r.Instance != "" {
			t.Errorf("single-instance request carries instance fields: %+v", r)
		}
	}
}

func TestInstanceActions(t *testing.T) {
	e, h, _, calls := instancesEnv(t)
	n := len(h.Requests())
	res, err := e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{Instance: "a", IdempotencyKey: "k1"})
	if err != nil || res.Instance != "a" || res.To != catalog.StateRunning {
		t.Fatalf("restart a = %+v, %v", res, err)
	}
	reqs := h.Requests()[n:]
	if len(reqs) != 2 || reqs[0].Op != deployhelper.OpRestart || reqs[0].Instance != "a" || reqs[1].Op != deployhelper.OpStatus {
		t.Fatalf("requests = %+v", reqs)
	}
	// The same key for another instance is another action.
	if res, err := e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{Instance: "b", IdempotencyKey: "k1"}); err != nil || res.Replayed {
		t.Fatalf("restart b = %+v, %v", res, err)
	}
	if res, err := e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{Instance: "a", IdempotencyKey: "k1"}); err != nil || !res.Replayed {
		t.Fatalf("replay = %+v, %v", res, err)
	}

	// An instance stop leaves the service running and pauses the instance.
	if res, err := e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{Instance: "b"}); err != nil || res.To != catalog.StateRunning {
		t.Fatalf("stop b = %+v, %v", res, err)
	}
	if got := e.mgr.StoppedInstances("app"); len(got) != 1 || !got["b"] || !slices.Contains(*calls, "app") {
		t.Fatalf("stopped = %v, calls %v", got, *calls)
	}
	if _, err := e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{Instance: "a"}); err != nil {
		t.Fatal(err)
	}
	// The last enabled instance is refused before the helper is asked.
	n = len(h.Requests())
	if _, err := e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{Instance: "c"}); !errors.Is(err, catalog.ErrLastInstance) {
		t.Fatalf("stop c = %v", err)
	}
	if len(h.Requests()) != n {
		t.Fatalf("the helper was asked: %+v", h.Requests()[n:])
	}
	rec, err := e.cat.Get(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := e.mgr.Instances("app")
	for _, a := range catalog.AvailableInstanceActions(rec, nil, "c", &snap) {
		switch {
		case a.Action == catalog.ActionStop && a.Reason != catalog.ReasonLastInstance:
			t.Errorf("stop c availability = %+v", a)
		case a.Action == catalog.ActionStart && a.Reason != catalog.ReasonInstanceRunning:
			t.Errorf("start c availability = %+v", a)
		}
	}
	// A stopped instance can be started.
	for _, a := range catalog.AvailableInstanceActions(rec, nil, "a", &snap) {
		if a.Action == catalog.ActionStart && !a.Allowed {
			t.Errorf("start a availability = %+v", a)
		}
	}

	// Audit and timeline name the instance.
	entries, _ := e.audit.List(context.Background(), 0, 0)
	found := false
	for _, en := range entries {
		if en.Action == "service.stop" && en.Details["instance"] == "b" {
			found = true
		}
	}
	if !found {
		t.Error("no audit entry names instance b")
	}
	page, _ := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Resource: "app", Kinds: []string{catalog.KindAction}})
	if !slices.ContainsFunc(page.Entries, func(en catalog.Entry) bool { return en.Summary == "restart a succeeded" && en.Data["instance"] == "a" }) {
		t.Errorf("timeline = %+v", page.Entries)
	}
	if m := catalog.EntryMessage(page.Entries[0]); m.Params["instance"] == "" {
		t.Errorf("message = %+v", m)
	}

	// Refusals.
	for _, tc := range []struct {
		action func(context.Context, string, catalog.ActionOptions) (catalog.ActionResult, error)
		opts   catalog.ActionOptions
		want   error
	}{
		{e.mgr.Restart, catalog.ActionOptions{Instance: "zz"}, catalog.ErrUnknownInstance},
		{e.mgr.Backup, catalog.ActionOptions{Instance: "a"}, catalog.ErrPrecondition},
	} {
		if _, err := tc.action(e.ctx, "app", tc.opts); !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v, want %v", tc.opts, err, tc.want)
		}
	}
	if _, err := e.mgr.Plan(context.Background(), "app", catalog.ActionStop, catalog.ActionOptions{Instance: "a"}); err != nil {
		t.Fatal(err)
	}
	p, err := e.mgr.Plan(context.Background(), "app", catalog.ActionStart, catalog.ActionOptions{Instance: "a"})
	if err != nil || p.StepMessages[1].Code != "step.instanceStart" || p.To != catalog.StateRunning {
		t.Fatalf("plan = %+v, %v", p, err)
	}

	// A stopped service is started as a whole.
	if _, err := e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	var se *catalog.StateError
	if _, err := e.mgr.Start(e.ctx, "app", catalog.ActionOptions{Instance: "a"}); !errors.As(err, &se) {
		t.Fatalf("start a on a stopped service = %v", err)
	}
}

func TestInstanceRestartRecoversFailedService(t *testing.T) {
	e, _, sim, _ := instancesEnv(t)
	sim.mu.Lock()
	sim.active["b"] = "failed"
	sim.mu.Unlock()
	changes, err := e.mgr.Reconcile(e.ctx)
	if err != nil || len(changes) != 1 || e.state("app") != catalog.StateFailed {
		t.Fatalf("reconcile = %+v, %v", changes, err)
	}
	// Reconciliation observes the instances of a failed service too.
	snap, ok := e.mgr.Instances("app")
	if b, _ := snap.Instance("b"); !ok || b.State != rt.StateFailed {
		t.Fatalf("snapshot = %+v", snap)
	}
	if _, err := e.mgr.Reconcile(e.ctx); err != nil || e.state("app") != catalog.StateFailed {
		t.Fatalf("failed must stay failed: %v", err)
	}
	res, err := e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{Instance: "b"})
	if err != nil || res.From != catalog.StateFailed || res.To != catalog.StateRunning {
		t.Fatalf("restart b = %+v, %v", res, err)
	}

	// A stopped instance does not fail the service on reconciliation.
	if _, err := e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{Instance: "c"}); err != nil {
		t.Fatal(err)
	}
	if changes, err := e.mgr.Reconcile(e.ctx); err != nil || len(changes) != 0 || e.state("app") != catalog.StateRunning {
		t.Fatalf("reconcile = %+v, %v", changes, err)
	}
}

// Before the first deployment the helper has no units: a start is refused
// as not found, nothing changed, and the state stays.
func TestStartBeforeTheFirstDeployKeepsTheState(t *testing.T) {
	g := newGitFixture(t)
	h := newSimHelper()
	e := newEnv(t, realGit(g), withHelper(h))
	e.put(managedDocWith(g.remote, instancesConfig), 0)
	_, err := e.mgr.Start(e.ctx, "app", catalog.ActionOptions{})
	var he *rt.HelperError
	if !errors.As(err, &he) || he.Code != deployhelper.CodeNotFound || e.state("app") != catalog.StateRegistered {
		t.Fatalf("start = %v, state %s", err, e.state("app"))
	}
}

// An instance without a unit yet was not stopped by an operator: its
// checks keep running.
func TestInstanceWithoutUnitIsNotStopped(t *testing.T) {
	e, _, sim, _ := instancesEnv(t)
	sim.mu.Lock()
	sim.missing = map[string]bool{"c": true}
	sim.enabled["b"] = false
	sim.mu.Unlock()
	if _, err := e.mgr.Reconcile(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.mgr.StoppedInstances("app"); len(got) != 1 || !got["b"] {
		t.Fatalf("stopped = %v", got)
	}
}

func TestInstanceOnSingleService(t *testing.T) {
	e, _ := backupEnv(t)
	if _, err := e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{Instance: "a"}); !errors.Is(err, catalog.ErrUnknownInstance) {
		t.Fatalf("restart = %v", err)
	}
}

const usersBackupConfig = `    backup:
      sources:
        - { type: mongodb, all: true, users: true }
        - { type: files, path: data/ }
`

func TestRestoreUsers(t *testing.T) {
	g := newGitFixture(t)
	h := newSimHelper()
	e := newEnv(t, realGit(g), withHelper(h))
	e.put(managedDocWith(g.remote, usersBackupConfig), 0)
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	h.pruneKeeps = 100
	b, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := h.Requests()
	if bk := reqs[len(reqs)-2]; bk.Op != deployhelper.OpBackup || !bk.Sources[0].All || !bk.Sources[0].Users {
		t.Fatalf("backup request %+v", bk)
	}

	p, err := e.mgr.Plan(context.Background(), "app", catalog.ActionRestore, catalog.ActionOptions{RestoreUsers: true})
	if err != nil || !slices.ContainsFunc(p.StepMessages, func(m message.Message) bool { return m.Code == "step.restoreUsers" }) {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if !strings.Contains(p.Steps[3], "mongodb (all databases)") {
		t.Errorf("steps = %q", p.Steps)
	}
	// The plan warns that the users come from a set the service wrote.
	if !slices.ContainsFunc(p.Steps, func(s string) bool { return strings.Contains(s, "the service's own account wrote that set") }) {
		t.Errorf("steps = %q", p.Steps)
	}

	// The restored service does not come up: the undo restores the users
	// it replaced as well.
	h.restoreActive = []bool{false}
	n := len(h.Requests())
	res, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{BackupID: b.BackupID, RestoreUsers: true})
	if !errors.Is(err, catalog.ErrRestoreUndone) || !res.RestoreUsers {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	var restores []deployhelper.Request
	for _, r := range h.Requests()[n:] {
		if r.Op == deployhelper.OpRestore {
			restores = append(restores, r)
		}
	}
	if len(restores) != 2 || !restores[0].RestoreUsers || !restores[1].RestoreUsers || !strings.HasPrefix(restores[1].BackupID, "pre-restore-") {
		t.Fatalf("restores = %+v", restores)
	}
	entries, _ := e.audit.List(context.Background(), 0, 0)
	if en := entries[0]; en.Action != "service.restore" || en.Details["restoreUsers"] != true {
		t.Errorf("audit = %+v", en)
	}

	// A set without users refuses restoreUsers.
	e2, _ := backupEnv(t)
	if _, err := e2.mgr.Backup(e2.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.mgr.Restore(e2.ctx, "app", catalog.ActionOptions{RestoreUsers: true}); !errors.Is(err, catalog.ErrNoUsers) {
		t.Fatalf("restore without users = %v", err)
	}
	if _, err := e2.mgr.Plan(context.Background(), "app", catalog.ActionBackup, catalog.ActionOptions{RestoreUsers: true}); !errors.Is(err, catalog.ErrPrecondition) {
		t.Fatalf("plan backup with users = %v", err)
	}
}
