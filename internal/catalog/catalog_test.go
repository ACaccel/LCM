package catalog_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
	"github.com/ACaccel/lcm/internal/signal"
)

func machineDoc(name string) string {
	return "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: " + name + " }\nspec: { type: machine, adapter: local }\n"
}

func TestCreateAddsLifecycleState(t *testing.T) {
	e := newEnv(t)
	res := e.put(svcDoc("app", ""), 0)
	if !res.Created || res.Version != 1 {
		t.Fatalf("put = %+v", res)
	}
	rec, err := e.cat.Get(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != catalog.StateRegistered || !rec.StateSince.Equal(epoch) || rec.Phase != resource.PhaseManaged ||
		rec.Adapter != "systemd" || rec.Runtime().Unit != "app.service" || rec.Document.Metadata.Labels["tier"] != "web" {
		t.Fatalf("record = %+v", rec)
	}
	// An update keeps the state.
	e.setState("app", catalog.StateRunning)
	e.put(svcDoc("app", "  backup: { command: x }\n"), 1)
	if rec, _ := e.cat.Get(context.Background(), "app"); rec.State != catalog.StateRunning || rec.Version != 2 || rec.Config.Backup == nil {
		t.Fatalf("after update = %+v", rec)
	}
}

func TestGetAndListOnlyServices(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("zeta", ""), 0)
	e.put(svcDoc("alpha", ""), 0)
	e.put(machineDoc("host"), 0)
	e.put(svcDoc("old", ""), 0)
	if err := e.phase(e.ctx, "old", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	recs, err := e.cat.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range recs {
		names = append(names, r.Name+":"+r.Phase)
	}
	if want := []string{"alpha:managed", "old:retired", "zeta:managed"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("list = %v, want %v", names, want)
	}
	if _, err := e.cat.Get(context.Background(), "host"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("get machine: %v", err)
	}
	if _, err := e.cat.Get(context.Background(), "ghost"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
}

func TestConfigTimelineEntries(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	if _, err := e.res.Put(auth.WithActor(context.Background(), bob), []byte(svcDoc("app", "  backup: { command: x }\n")), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.res.Restore(e.ctx, "app", 1, 2); err != nil {
		t.Fatal(err)
	}
	// An unchanged save is not an event.
	e.put(svcDoc("app", ""), 3)
	if err := e.phase(e.ctx, "app", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	page, err := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Resource: "app", Kinds: []string{catalog.KindConfig}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := len(page.Entries) - 1; i >= 0; i-- {
		got = append(got, page.Entries[i].Summary)
	}
	want := []string{
		"configuration version 1 saved by alice",
		"configuration version 2 saved by ci-token",
		"configuration version 3 restored from version 1 by alice",
		"phase managed -> retired by alice: test",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config entries = %q\nwant %q", got, want)
	}
}

func TestRetire(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.setState("app", catalog.StateRunning)
	ad := e.runtime.adapter("app")
	ad.set(func(a *fakeAdapter) { a.state = rt.StateActive })

	if err := e.phase(e.ctx, "app", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.cat.Get(context.Background(), "app")
	if rec.Phase != resource.PhaseRetired || rec.State != catalog.StateStopped || !reflect.DeepEqual(ad.Calls(), []string{"stop"}) {
		t.Fatalf("retired running service = %s/%s, calls %v", rec.Phase, rec.State, ad.Calls())
	}
	if e.locker.isHeld(catalog.LockName("app")) {
		t.Fatal("lock not released")
	}
	page, _ := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Resource: "app", Kinds: []string{catalog.KindLifecycle}, Limit: 1})
	if len(page.Entries) != 1 || page.Entries[0].Summary != "running -> stopped (retire)" {
		t.Fatalf("lifecycle entry = %+v", page.Entries)
	}

	// Retiring a stopped service does not stop it again.
	e.put(svcDoc("old", ""), 0)
	e.setState("old", catalog.StateStopped)
	if err := e.phase(e.ctx, "old", resource.PhaseRetired); err != nil || len(e.runtime.adapter("old").Calls()) != 0 {
		t.Fatalf("retire stopped: %v %v", err, e.runtime.adapter("old").Calls())
	}

	// A failing stop keeps the service from being retired.
	e.put(svcDoc("stuck", ""), 0)
	e.setState("stuck", catalog.StateRunning)
	e.runtime.adapter("stuck").set(func(a *fakeAdapter) { a.stopErr = errors.New("busy"); a.state = rt.StateActive })
	if err := e.phase(e.ctx, "stuck", resource.PhaseRetired); err == nil {
		t.Fatal("retire with a failing stop succeeded")
	}
	if rec, _ := e.cat.Get(context.Background(), "stuck"); rec.Phase != resource.PhaseManaged || rec.State != catalog.StateRunning {
		t.Fatalf("failed retire left %s/%s", rec.Phase, rec.State)
	}

	// External services cannot be stopped by LCM: retiring changes the
	// phase without touching the process or recording a stop.
	e.put(serviceDoc("ext", "external", "", ""), 0)
	e.setState("ext", catalog.StateRunning)
	if err := e.phase(e.ctx, "ext", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	if rec, _ := e.cat.Get(context.Background(), "ext"); rec.Phase != resource.PhaseRetired || rec.State != catalog.StateRunning ||
		len(e.runtime.adapter("ext").Calls()) != 0 {
		t.Fatalf("retired external = %s/%s, calls %v", rec.Phase, rec.State, e.runtime.adapter("ext").Calls())
	}

	// A transient state means an action is mid-flight.
	e.put(svcDoc("busy", ""), 0)
	e.setState("busy", catalog.StateDeploying)
	if err := e.phase(e.ctx, "busy", resource.PhaseRetired); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("retire while deploying: %v", err)
	}

	// A stale version is refused before anything is stopped.
	e.put(svcDoc("stale", ""), 0)
	e.setState("stale", catalog.StateRunning)
	rec, _ = e.cat.Get(context.Background(), "stale")
	if err := e.mgr.Type().ChangePhase(e.ctx, rec.Record, resource.PhaseRetired, 9, ""); !errors.Is(err, resource.ErrConflict) ||
		len(e.runtime.adapter("stale").Calls()) != 0 {
		t.Fatalf("stale retire: %v %v", err, e.runtime.adapter("stale").Calls())
	}
}

func TestResumeReconciles(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.setState("app", catalog.StateStopped)
	if err := e.phase(e.ctx, "app", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	// Started outside LCM while retired: reconcile leaves it alone ...
	e.runtime.adapter("app").set(func(a *fakeAdapter) { a.state = rt.StateActive })
	if changes, err := e.mgr.Reconcile(e.ctx); err != nil || len(changes) != 0 {
		t.Fatalf("reconcile retired = %+v %v", changes, err)
	}
	// ... and resuming adopts the running process.
	if err := e.phase(e.ctx, "app", resource.PhaseManaged); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.cat.Get(context.Background(), "app")
	if rec.Phase != resource.PhaseManaged || rec.State != catalog.StateRunning {
		t.Fatalf("resumed = %s/%s", rec.Phase, rec.State)
	}
	if _, err := e.mgr.Stop(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatalf("action after resume: %v", err)
	}
}

func TestUnmanagePreconditions(t *testing.T) {
	e := newEnv(t)
	e.put(serviceDoc("ext", "external", "", ""), 0)
	e.setState("ext", catalog.StateRunning)
	if err := e.phase(e.ctx, "ext", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	// Still running: LCM could not stop it, so it cannot be archived.
	if err := e.phase(e.ctx, "ext", resource.PhaseUnmanaged); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("unmanage running: %v", err)
	}

	e.put(svcDoc("app", ""), 0)
	opts := catalog.ActionOptions{IdempotencyKey: "r1/start"}
	if _, err := e.mgr.Start(e.ctx, "app", opts); err != nil {
		t.Fatal(err)
	}
	if err := e.phase(e.ctx, "app", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	if err := e.phase(e.ctx, "app", resource.PhaseUnmanaged); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cat.Get(context.Background(), "app"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("get unmanaged: %v", err)
	}
	// A new incarnation starts registered and does not replay the old
	// incarnation's completions.
	e.put(svcDoc("app", ""), 0)
	if st := e.state("app"); st != catalog.StateRegistered {
		t.Fatalf("new incarnation state = %s", st)
	}
	res, err := e.mgr.Start(e.ctx, "app", opts)
	if err != nil || res.Replayed {
		t.Fatalf("start new incarnation = %+v %v", res, err)
	}
}

func TestPhaseChangeRefusedWhileBusy(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.setState("app", catalog.StateRunning)
	e.locker.hold(catalog.LockName("app"), "run-9")
	if err := e.phase(e.ctx, "app", resource.PhaseRetired); !errors.Is(err, catalog.ErrLockHeld) {
		t.Fatalf("retire with held lock: %v", err)
	}
	e.locker.mu.Lock()
	delete(e.locker.held, catalog.LockName("app"))
	e.locker.mu.Unlock()

	// A direct store call cannot bypass an action in flight.
	ad := e.runtime.adapter("app")
	entered, release := make(chan struct{}), make(chan struct{})
	ad.set(func(a *fakeAdapter) {
		a.state = rt.StateActive
		a.onRestart = func() { close(entered); <-release }
	})
	done := make(chan error, 1)
	go func() {
		_, err := e.mgr.Restart(e.ctx, "app", catalog.ActionOptions{})
		done <- err
	}()
	<-entered
	if err := e.res.SetPhase(e.ctx, "app", resource.PhaseRetired, 1, ""); !errors.Is(err, catalog.ErrLockHeld) {
		t.Fatalf("store retire during action: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceType(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", "  backup: { command: x }\n"), 0)
	e.setState("app", catalog.StateRunning)
	typ := e.mgr.Type()
	if typ.Name() != model.ResourceService {
		t.Fatalf("name = %s", typ.Name())
	}
	rec, _ := e.res.Get(context.Background(), "app")
	st, err := typ.Status(context.Background(), rec)
	if err != nil || st.State != string(catalog.StateRunning) || !st.StateSince.Equal(epoch) {
		t.Fatalf("status = %+v %v", st, err)
	}
	got := map[string]resource.Action{}
	for _, a := range typ.Actions(context.Background(), rec) {
		got[a.Name] = a
	}
	check := func(name string, available bool, risk, kind string, policy bool) {
		t.Helper()
		a, ok := got[name]
		if !ok || a.Available != available || a.Risk != risk || a.Kind != kind || a.Policy != policy {
			t.Errorf("%s = %+v", name, a)
		}
	}
	check("stop", true, resource.RiskDisruptive, resource.KindAction, true)
	check("start", false, resource.RiskReversible, resource.KindAction, true)
	check("backup", true, resource.RiskReversible, resource.KindAction, true)
	check("deploy", false, resource.RiskDisruptive, resource.KindAction, true)
	check("retire", true, resource.RiskDisruptive, resource.KindPhase, false)
	check("resume", false, resource.RiskReversible, resource.KindPhase, false)
	check("unmanage", false, resource.RiskDestructive, resource.KindPhase, false)

	if err := e.phase(e.ctx, "app", resource.PhaseRetired); err != nil {
		t.Fatal(err)
	}
	rec, _ = e.res.Get(context.Background(), "app")
	got = map[string]resource.Action{}
	for _, a := range typ.Actions(context.Background(), rec) {
		got[a.Name] = a
	}
	if got["stop"].Available || got["stop"].Reason != catalog.ReasonNotManaged {
		t.Errorf("stop on retired = %+v", got["stop"])
	}
	check("resume", true, resource.RiskReversible, resource.KindPhase, false)
	check("unmanage", true, resource.RiskDestructive, resource.KindPhase, false)
	check("retire", false, resource.RiskDisruptive, resource.KindPhase, false)

	// Git ownership blocks unmanaging, with the reason machines and
	// routers report.
	if err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO config_ownership (kind, name, source, path, commit_id, applied_version, document, applied_at)
			VALUES ('Resource', 'app', 'repo', 'app.yaml', 'c1', 1, '{}', 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, a := range typ.Actions(context.Background(), rec) {
		if a.Name == "unmanage" && (a.Available || a.Reason != resource.ReleaseOwnershipFirst) {
			t.Errorf("unmanage of an owned service = %+v", a)
		}
	}

	if err := typ.ChangePhase(e.ctx, resource.Record{Name: "host", Type: model.ResourceMachine}, resource.PhaseRetired, 1, ""); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("change phase of a machine: %v", err)
	}
}

func TestLifecycleSignalCarriesResourceType(t *testing.T) {
	e := newEnv(t)
	sub := e.bus.Subscribe("test", 4)
	defer sub.Close()
	e.put(svcDoc("app", ""), 0)
	e.setState("app", catalog.StateRunning)
	sig := <-sub.C
	if sig.Kind != signal.KindLifecycle || sig.Resource != "app" || sig.ResourceType != model.ResourceService || sig.Labels["tier"] != "web" {
		t.Fatalf("signal = %+v", sig)
	}
}

func TestServiceStatesRowsByResourceID(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	rec, _ := e.res.Get(context.Background(), "app")
	var state string
	err := e.db.Reader().QueryRow(`SELECT state FROM service_states WHERE resource_id = ?`, rec.ID).Scan(&state)
	if err != nil || state != "registered" {
		t.Fatalf("service_states = %q %v", state, err)
	}
	// Machines get no lifecycle state.
	e.put(machineDoc("host"), 0)
	m, _ := e.res.Get(context.Background(), "host")
	if err := e.db.Reader().QueryRow(`SELECT state FROM service_states WHERE resource_id = ?`, m.ID).Scan(&state); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("machine has a lifecycle state: %v", err)
	}
}

func TestOnTimelineReportsStoredEntries(t *testing.T) {
	e := newEnv(t)
	var kinds []string
	cat, err := catalog.New(catalog.Config{
		DB: e.db, Audit: e.audit, Resources: e.res, Bus: e.bus, Clock: e.clk, Log: testLogger(t),
		OnTimeline: func(en catalog.Entry) { kinds = append(kinds, en.Kind+":"+en.Resource) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Entries written by the resource hooks are not reported.
	e.put(svcDoc("app", ""), 0)
	if err := cat.Transition(context.Background(), "app", catalog.StateRegistered, catalog.StateRunning, catalog.TransitionInfo{Action: "start"}); err != nil {
		t.Fatal(err)
	}
	if err := cat.AppendTimeline(context.Background(), catalog.Entry{Resource: "app", Kind: catalog.KindAction, Summary: "x"}); err != nil {
		t.Fatal(err)
	}
	want := "lifecycle.changed:app,action:app"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
	if _, err := catalog.New(catalog.Config{DB: e.db, Audit: e.audit}); err == nil {
		t.Fatal("catalog without resources was accepted")
	}
}
