package configsync_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
)

const machineDoc = "apiVersion: lcm/v1\nkind: Machine\nmetadata: {name: workstation}\nspec: {}\n"

type syntheticObserver struct{}

func (syntheticObserver) Observe(context.Context, model.MachineMonitoring) (machine.Snapshot, error) {
	return machine.Snapshot{DeviceHash: "synthetic-device", BootID: "synthetic-boot", Init: "systemd", CPUTotal: 100, CPUIdle: 90, CPUCount: 1, MemoryTotal: 100, MemoryAvailable: 90}, nil
}
func (syntheticObserver) ReadJournal(context.Context, model.MachineMonitoring, string, time.Time) (machine.JournalBatch, error) {
	return machine.JournalBatch{}, nil
}

type machineEnv struct {
	*env
	machines *machine.Manager
	gate     *maintenance.Gate
}

func newMachineEnv(t *testing.T) *machineEnv {
	t.Helper()
	e := &machineEnv{env: newEnv(t)}
	e.gate = maintenance.New(e.db)
	var err error
	e.machines, err = machine.New(machine.Config{DB: e.db, Audit: e.audit, Clock: e.clk, Gate: e.gate, Observer: syntheticObserver{}})
	if err != nil {
		t.Fatal(err)
	}
	e.cat, err = catalog.New(catalog.Config{DB: e.db, Audit: e.audit, Bus: e.bus, Clock: e.clk, Gate: e.gate})
	if err != nil {
		t.Fatal(err)
	}
	e.sync, err = configsync.New(configsync.Config{DB: e.db, Audit: e.audit, Catalog: e.cat, Modules: e.mods, Engine: e.eng, Bus: e.bus, Clock: e.clk, Machines: e.machines, Gate: e.gate, Runner: execx.New(), Dir: filepath.Join(t.TempDir(), "sources")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.sync.Close)
	return e
}

func TestMachineRoundTripAndObservationDoesNotDrift(t *testing.T) {
	e := newMachineEnv(t)
	for _, alias := range []string{"machine", "machines", " Machine "} {
		k, err := configsync.ParseKind(alias)
		if err != nil || k != model.KindMachine {
			t.Fatalf("ParseKind(%q): %v %v", alias, k, err)
		}
	}
	r := newRepo(t)
	r.commit("initial", map[string]string{"machine.yaml": machineDoc, "service.yaml": serviceYAML("web", "")})
	e.addSource("infra", r, ".")
	rep := e.syncNow("infra")
	wantAction(t, rep.Items, "Machine/workstation", configsync.ActionCreate)
	original, err := e.machines.Get(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		e.clk.Advance(30 * time.Second)
		if _, err := e.machines.Observe(e.ctx, "workstation"); err != nil {
			t.Fatal(err)
		}
	}
	if own := e.owner("machine", "workstation"); own.Drifted {
		t.Fatalf("observation caused drift: %+v", own)
	}
	exported, err := e.sync.Export(e.ctx, configsync.Selector{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "kind: Machine") || strings.Contains(string(exported), "synthetic-device") || strings.Contains(string(exported), "\nstatus:") {
		t.Fatalf("export leaked state: %s", exported)
	}
	results, err := e.sync.Apply(e.ctx, configsync.SplitDocuments("export.yaml", exported), configsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Action != configsync.ActionUnchanged {
			t.Fatalf("roundtrip: %+v", result)
		}
	}
	current, _ := e.machines.Get(e.ctx, "workstation")
	if current.Version != original.Version || current.ID != original.ID {
		t.Fatalf("roundtrip changed record: %+v", current)
	}
	vs, err := e.cat.Versions(e.ctx, "web")
	if err != nil || len(vs) != 1 {
		t.Fatalf("Service churn: %v %v", vs, err)
	}
}

func TestMachineDriftResolveAndOrphan(t *testing.T) {
	e := newMachineEnv(t)
	sub := e.bus.Subscribe("machine-config-test", 32)
	defer sub.Close()
	r := newRepo(t)
	r.commit("initial", map[string]string{"machine.yaml": machineDoc})
	e.addSource("infra", r, ".")
	e.syncNow("infra")
	local := strings.Replace(machineDoc, "spec: {}", "spec: {monitoring: {interval: 40s}}", 1)
	if _, err := e.machines.Put(e.ctx, []byte(local), 1); err != nil {
		t.Fatal(err)
	}
	wantAction(t, e.apply(configsync.Options{}, machineDoc), "Machine/workstation", configsync.ActionSkipped)
	pending := strings.Replace(machineDoc, "spec: {}", "spec: {monitoring: {interval: 50s}}", 1)
	r.commit("remote edit", map[string]string{"machine.yaml": pending})
	rep := e.syncNow("infra")
	result := wantAction(t, rep.Items, "Machine/workstation", configsync.ActionSkipped)
	if !result.Conflict || !result.Drifted {
		t.Fatalf("missing conflict: %+v", result)
	}
	for _, sig := range alerts(sub) {
		if sig.Kind == signal.KindAlert || sig.Service != "" {
			t.Fatalf("Machine conflict became a legacy alert: %+v", sig)
		}
	}
	own, err := e.sync.Resolve(e.ctx, model.KindMachine, "workstation", false)
	if err != nil || !own.Drifted || !own.Acknowledged || own.Conflict {
		t.Fatalf("resolve current: %+v %v", own, err)
	}
	own, err = e.sync.Resolve(e.ctx, model.KindMachine, "workstation", true)
	if err != nil || own.Drifted {
		t.Fatalf("resolve git: %+v %v", own, err)
	}
	before, _ := e.machines.Get(e.ctx, "workstation")
	r.commit("remove document", map[string]string{"machine.yaml": "", "notes.txt": "keep repository"})
	rep = e.syncNow("infra")
	if len(rep.Orphaned) != 1 || rep.Orphaned[0].Kind != model.KindMachine {
		t.Fatalf("orphan report: %+v", rep)
	}
	after, err := e.machines.Get(e.ctx, "workstation")
	if err != nil || after.ID != before.ID || after.Version != before.Version || after.Phase != before.Phase {
		t.Fatalf("orphan changed Machine: %+v %v", after, err)
	}
	ops, err := e.machines.Operations(e.ctx, "workstation")
	if err != nil || len(ops) != 0 {
		t.Fatalf("orphan submitted power: %+v %v", ops, err)
	}
	if err := e.machines.SetPhase(e.ctx, "workstation", "unmanaged", 1); err == nil {
		t.Fatal("unmanaged a Git-owned machine")
	}
	if err := e.sync.Release(e.ctx, model.KindMachine, "workstation"); err != nil {
		t.Fatal(err)
	}
}

func TestMachineRequestsFailClosedWithoutManager(t *testing.T) {
	e := newEnv(t)
	for _, dry := range []bool{false, true} {
		res := e.apply(configsync.Options{DryRun: dry}, machineDoc, serviceYAML("web", ""))
		failed := wantAction(t, res, "Machine/workstation", configsync.ActionFailed)
		if !strings.Contains(failed.Issues.Error(), configsync.ErrMachinesUnavailable.Error()) {
			t.Fatalf("failure: %+v", failed)
		}
	}
	if _, err := e.sync.Export(e.ctx, configsync.Selector{Kind: model.KindMachine}); !errors.Is(err, configsync.ErrMachinesUnavailable) {
		t.Fatalf("export: %v", err)
	}
	if _, err := e.sync.Owner(e.ctx, model.KindMachine, "workstation"); !errors.Is(err, configsync.ErrMachinesUnavailable) {
		t.Fatalf("owner: %v", err)
	}
	if _, err := e.sync.Resolve(e.ctx, model.KindMachine, "workstation", true); !errors.Is(err, configsync.ErrMachinesUnavailable) {
		t.Fatalf("resolve: %v", err)
	}
	if err := e.sync.Release(e.ctx, model.KindMachine, "workstation"); !errors.Is(err, configsync.ErrMachinesUnavailable) {
		t.Fatalf("release: %v", err)
	}
	if _, err := e.sync.Export(e.ctx, configsync.Selector{}); err != nil {
		t.Fatalf("legacy export: %v", err)
	}
}

func TestConfigWritesRespectMachineGate(t *testing.T) {
	e := newMachineEnv(t)
	r := newRepo(t)
	r.commit("initial", map[string]string{"machine.yaml": machineDoc})
	src := e.addSource("infra", r, ".")
	e.syncNow("infra")
	if _, err := e.machines.EnterMaintenance(machine.DirectRequest(e.ctx), "workstation", "synthetic maintenance", true, 1); err != nil {
		t.Fatal(err)
	}
	for _, useGit := range []bool{true, false} {
		if _, err := e.sync.Resolve(e.ctx, model.KindMachine, "workstation", useGit); !errors.Is(err, maintenance.ErrActive) {
			t.Fatalf("resolve(%v): %v", useGit, err)
		}
	}
	for label, write := range map[string]func() error{
		"apply": func() error {
			_, err := e.sync.Apply(e.ctx, docs("machine.yaml", machineDoc), configsync.Options{})
			return err
		},
		"apply service": func() error {
			_, err := e.sync.Apply(e.ctx, docs("service.yaml", serviceYAML("blocked", "")), configsync.Options{})
			return err
		},
		"release": func() error { return e.sync.Release(e.ctx, model.KindMachine, "workstation") },
		"create source": func() error {
			_, err := e.sync.CreateSource(e.ctx, configsync.Source{Name: "other", Repo: r.remote})
			return err
		},
		"update source": func() error { _, err := e.sync.UpdateSource(e.ctx, src); return err },
		"delete source": func() error { return e.sync.DeleteSource(e.ctx, src.Name) },
	} {
		if err := write(); !errors.Is(err, maintenance.ErrActive) {
			t.Errorf("%s: %v", label, err)
		}
	}
	if started, err := e.sync.RequestSync(e.ctx, "infra"); started || !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("background sync bypassed gate: started=%v err=%v", started, err)
	}
	rep, err := e.sync.SyncNow(e.ctx, "infra")
	if err != nil || !strings.Contains(rep.Error, maintenance.ErrActive.Error()) {
		t.Fatalf("sync bypassed gate: %+v %v", rep, err)
	}
	if _, err := e.sync.Apply(e.ctx, docs("machine.yaml", machineDoc), configsync.Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sync.Export(e.ctx, configsync.Selector{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sync.Ownership(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
}

type gateCheck struct {
	gate *maintenance.Gate
	held bool
}

func (c *gateCheck) ValidateChecks([]model.Check, string) model.Issues {
	c.held = errors.Is(c.gate.Exclusive(context.Background(), func(*sql.Tx) error { return nil }), maintenance.ErrActive)
	return nil
}
func TestConfigApplyHoldsAndPreservesGateLease(t *testing.T) {
	e := newMachineEnv(t)
	check := &gateCheck{gate: e.gate}
	s, err := configsync.New(configsync.Config{DB: e.db, Audit: e.audit, Catalog: e.cat, Modules: e.mods, Engine: e.eng, Machines: e.machines, Gate: e.gate, Checks: check})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	results, err := s.Apply(e.ctx, docs("all.yaml", serviceYAML("web", ""), machineDoc), configsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantAction(t, results, "Machine/workstation", configsync.ActionCreate)
	wantAction(t, results, "Service/web", configsync.ActionCreate)
	if !check.held {
		t.Fatal("configsync did not acquire the gate before validation")
	}
	ctx, release, err := e.gate.Acquire(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	results, err = s.Apply(ctx, docs("all.yaml", strings.Replace(serviceYAML("web", ""), "web-status", "web-status-updated", 1), strings.Replace(machineDoc, "spec: {}", "spec: {monitoring: {interval: 40s}}", 1)), configsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantAction(t, results, "Machine/workstation", configsync.ActionUpdate)
	wantAction(t, results, "Service/web", configsync.ActionUpdate)
	if !check.held {
		t.Fatal("configsync did not hold the gate during validation")
	}
	if err := e.gate.Exclusive(e.ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("nested apply released caller lease: %v", err)
	}
	release()
	if err := e.gate.Exclusive(e.ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("lease leaked: %v", err)
	}
}

func TestConfigSyncHoldsGateDuringGit(t *testing.T) {
	e := newMachineEnv(t)
	runner := &execx.Fake{}
	held := false
	runner.On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
		held = errors.Is(e.gate.Exclusive(e.ctx, func(*sql.Tx) error { return nil }), maintenance.ErrActive)
		return execx.Result{}, errors.New("synthetic git failure")
	})
	s, err := configsync.New(configsync.Config{DB: e.db, Audit: e.audit, Catalog: e.cat, Modules: e.mods, Engine: e.eng, Machines: e.machines, Gate: e.gate, Runner: runner, Dir: filepath.Join(t.TempDir(), "sources")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateSource(e.ctx, configsync.Source{Name: "infra", Repo: "https://example.invalid/repo.git"}); err != nil {
		t.Fatal(err)
	}
	rep, err := s.SyncNow(e.ctx, "infra")
	if err != nil || !strings.Contains(rep.Error, "synthetic git failure") || !held {
		t.Fatalf("sync did not hold gate: held=%v report=%+v err=%v", held, rep, err)
	}
	if err := e.gate.Exclusive(e.ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("sync leaked gate lease: %v", err)
	}
	s.Close()
	if started, err := s.RequestSync(e.ctx, "infra"); started || err != nil {
		t.Fatalf("closed syncer with gate: started=%v err=%v", started, err)
	}
}
