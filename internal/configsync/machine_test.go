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
	"github.com/ACaccel/lcm/internal/resource"
)

const machineDoc = "apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: workstation}\nspec: {type: machine, adapter: local}\n"

// machineDocWith returns machineDoc with a config.
func machineDocWith(config string) string {
	return strings.Replace(machineDoc, "adapter: local}", "adapter: local, config: "+config+"}", 1)
}

type syntheticObserver struct{}

func (syntheticObserver) Observe(context.Context, model.MachineConfig) (machine.Snapshot, error) {
	return machine.Snapshot{DeviceHash: "synthetic-device", BootID: "synthetic-boot", Init: "systemd", CPUTotal: 100, CPUIdle: 90, CPUCount: 1, MemoryTotal: 100, MemoryAvailable: 90}, nil
}
func (syntheticObserver) ReadJournal(context.Context, model.MachineConfig, string, time.Time) (machine.JournalBatch, error) {
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
	e.res, err = resource.New(resource.Config{DB: e.db, Audit: e.audit, Clock: e.clk, Gate: e.gate})
	if err != nil {
		t.Fatal(err)
	}
	e.machines, err = machine.New(machine.Config{DB: e.db, Audit: e.audit, Clock: e.clk, Gate: e.gate, Resources: e.res, Observer: syntheticObserver{}})
	if err != nil {
		t.Fatal(err)
	}
	e.res.RegisterHooks(model.ResourceMachine, e.machines.Hooks())
	e.cat, err = catalog.New(catalog.Config{DB: e.db, Audit: e.audit, Bus: e.bus, Clock: e.clk, Gate: e.gate, Resources: e.res})
	if err != nil {
		t.Fatal(err)
	}
	e.sync, err = configsync.New(configsync.Config{DB: e.db, Audit: e.audit, Resources: e.res, Catalog: e.cat, Modules: e.mods, Engine: e.eng, Bus: e.bus, Clock: e.clk, Gate: e.gate, Runner: execx.New(), Dir: filepath.Join(t.TempDir(), "sources")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.sync.Close)
	return e
}

func TestMachineRoundTripAndObservationDoesNotDrift(t *testing.T) {
	e := newMachineEnv(t)
	r := newRepo(t)
	r.commit("initial", map[string]string{"machine.yaml": machineDoc, "service.yaml": serviceYAML("web", "")})
	e.addSource("infra", r, ".")
	rep := e.syncNow("infra")
	wantAction(t, rep.Items, "Resource/workstation", configsync.ActionCreate)
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
	if own := e.owner("resource", "workstation"); own.Drifted {
		t.Fatalf("observation caused drift: %+v", own)
	}
	exported, err := e.sync.Export(e.ctx, configsync.Selector{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "type: machine") || strings.Contains(string(exported), "synthetic-device") || strings.Contains(string(exported), "\nstatus:") {
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
	vs, err := e.res.Versions(e.ctx, "web")
	if err != nil || len(vs) != 1 {
		t.Fatalf("service churn: %v %v", vs, err)
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
	local := machineDocWith("{interval: 40s}")
	if _, err := e.res.Put(e.ctx, []byte(local), 1); err != nil {
		t.Fatal(err)
	}
	wantAction(t, e.apply(configsync.Options{}, machineDoc), "Resource/workstation", configsync.ActionSkipped)
	pending := machineDocWith("{interval: 50s}")
	r.commit("remote edit", map[string]string{"machine.yaml": pending})
	rep := e.syncNow("infra")
	result := wantAction(t, rep.Items, "Resource/workstation", configsync.ActionSkipped)
	if !result.Conflict || !result.Drifted {
		t.Fatalf("missing conflict: %+v", result)
	}
	sigs := alerts(sub)
	if len(sigs) != 1 || sigs[0].Kind != "resource.config.conflict" || sigs[0].Resource != "workstation" || sigs[0].ResourceType != model.ResourceMachine {
		t.Fatalf("machine conflict signals: %+v", sigs)
	}
	tl, err := e.cat.Timeline(e.ctx, catalog.TimelineQuery{Resource: "workstation", Kinds: []string{configsync.KindConflict}})
	if err != nil || len(tl.Entries) != 1 {
		t.Fatalf("machine conflict timeline: %+v %v", tl, err)
	}
	own, err := e.sync.Resolve(e.ctx, model.KindResource, "workstation", false)
	if err != nil || !own.Drifted || !own.Acknowledged || own.Conflict {
		t.Fatalf("resolve current: %+v %v", own, err)
	}
	own, err = e.sync.Resolve(e.ctx, model.KindResource, "workstation", true)
	if err != nil || own.Drifted {
		t.Fatalf("resolve git: %+v %v", own, err)
	}
	before, _ := e.machines.Get(e.ctx, "workstation")
	r.commit("remove document", map[string]string{"machine.yaml": "", "notes.txt": "keep repository"})
	rep = e.syncNow("infra")
	if len(rep.Orphaned) != 1 || rep.Orphaned[0].Kind != model.KindResource {
		t.Fatalf("orphan report: %+v", rep)
	}
	after, err := e.machines.Get(e.ctx, "workstation")
	if err != nil || after.ID != before.ID || after.Version != before.Version || after.Phase != before.Phase {
		t.Fatalf("orphan changed the machine: %+v %v", after, err)
	}
	ops, err := e.machines.Operations(e.ctx, "workstation")
	if err != nil || len(ops) != 0 {
		t.Fatalf("orphan submitted power: %+v %v", ops, err)
	}
	if err := e.res.SetPhase(e.ctx, "workstation", resource.PhaseRetired, after.Version, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.res.SetPhase(e.ctx, "workstation", resource.PhaseUnmanaged, after.Version, ""); !errors.Is(err, resource.ErrPrecondition) {
		t.Fatalf("unmanaged a Git-owned machine: %v", err)
	}
	if err := e.sync.Release(e.ctx, model.KindResource, "workstation"); err != nil {
		t.Fatal(err)
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
		if _, err := e.sync.Resolve(e.ctx, model.KindResource, "workstation", useGit); !errors.Is(err, maintenance.ErrActive) {
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
		"release": func() error { return e.sync.Release(e.ctx, model.KindResource, "workstation") },
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
	s, err := configsync.New(configsync.Config{DB: e.db, Audit: e.audit, Resources: e.res, Catalog: e.cat, Modules: e.mods, Engine: e.eng, Gate: e.gate, Checks: check})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	results, err := s.Apply(e.ctx, docs("all.yaml", serviceYAML("web", ""), machineDoc), configsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantAction(t, results, "Resource/workstation", configsync.ActionCreate)
	wantAction(t, results, "Resource/web", configsync.ActionCreate)
	if !check.held {
		t.Fatal("configsync did not acquire the gate before validation")
	}
	ctx, release, err := e.gate.Acquire(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	results, err = s.Apply(ctx, docs("all.yaml", strings.Replace(serviceYAML("web", ""), "web-status", "web-status-updated", 1), machineDocWith("{interval: 40s}")), configsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantAction(t, results, "Resource/workstation", configsync.ActionUpdate)
	wantAction(t, results, "Resource/web", configsync.ActionUpdate)
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
	s, err := configsync.New(configsync.Config{DB: e.db, Audit: e.audit, Resources: e.res, Catalog: e.cat, Modules: e.mods, Engine: e.eng, Gate: e.gate, Runner: runner, Dir: filepath.Join(t.TempDir(), "sources")})
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
