package machine

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

const testDoc = "apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: workstation, labels: {site: home}}\nspec: {type: machine, adapter: local, config: {units: [app.service], journal: true, thresholds: {consecutive: 2}}}"

type fakeObserver struct {
	snapshot Snapshot
	batch    JournalBatch
	err      error
	hook     func()
}

func (f *fakeObserver) Observe(context.Context, model.MachineConfig) (Snapshot, error) {
	if f.hook != nil {
		f.hook()
	}
	return f.snapshot, f.err
}

func TestProbeStartedBeforeMaintenanceIsNotRecoveryEvidence(t *testing.T) {
	e, _ := readyPower(t)
	e.probe.hook = func() { e.clk.Advance(time.Nanosecond); enter(t, e) }
	e.poll(t)
	if err := e.m.ExitMaintenance(DirectRequest(e.ctx), "workstation", "ready", 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("in-flight old evidence accepted: %v", err)
	}
	e.probe.hook = nil
	e.poll(t)
	if err := e.m.ExitMaintenance(DirectRequest(e.ctx), "workstation", "ready", 1); err != nil {
		t.Fatal(err)
	}
}

func TestMinuteHistoryAndArchiveIsolation(t *testing.T) {
	e, _ := readyPower(t)
	e.probe.snapshot.CPUIdle -= 80
	e.poll(t)
	points, err := e.m.HistoryResolution(e.ctx, "workstation", 0, 200, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].CPUPercent != nil || points[1].CPUPercent == nil || *points[1].CPUPercent != 50 {
		t.Fatalf("minute points %+v", points)
	}
	first, err := e.m.Get(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	e.setPhase(t, "workstation", resource.PhaseRetired, 1)
	e.setPhase(t, "workstation", resource.PhaseUnmanaged, 1)
	e.put(t, testDoc, 0)
	archives, err := e.store.Archives(e.ctx, model.ResourceMachine)
	if err != nil || len(archives) != 1 || archives[0].ID != first.ID {
		t.Fatalf("archives %+v %v", archives, err)
	}
	old, err := e.m.History(e.ctx, "@"+first.ID, 0, 200)
	if err != nil || len(old) != 3 {
		t.Fatalf("archived history %d %v", len(old), err)
	}
	current, err := e.m.History(e.ctx, "workstation", 0, 200)
	if err != nil || len(current) != 0 {
		t.Fatalf("incarnations combined: %d %v", len(current), err)
	}
}

func TestRetentionKeepsBaselineAndIndependentOperationEvidence(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "retention", 1))
	if err != nil {
		t.Fatal(err)
	}
	e.probe.snapshot.BootID = "boot-b"
	e.poll(t)
	e.poll(t)
	if err = e.m.Reconcile(e.ctx); err != nil {
		t.Fatal(err)
	}
	op, err = e.m.Operation(e.ctx, "workstation", op.ID)
	if err != nil || op.Evidence == nil {
		t.Fatalf("proof %+v %v", op, err)
	}
	e.clk.Advance(100 * 24 * time.Hour)
	e.prune(t)
	raw, err := e.m.History(e.ctx, "workstation", 0, 200)
	if err != nil || len(raw) != 1 {
		t.Fatalf("baseline %d %v", len(raw), err)
	}
	minute, err := e.m.HistoryResolution(e.ctx, "workstation", 0, 200, true)
	if err != nil || len(minute) != 0 {
		t.Fatalf("minute retention %d %v", len(minute), err)
	}
	events, err := e.m.Events(e.ctx, "workstation", 0, 200)
	if err != nil || len(events) != 0 {
		t.Fatalf("event retention %d %v", len(events), err)
	}
	proof, err := e.m.Operation(e.ctx, "workstation", op.ID)
	if err != nil || proof.Evidence == nil || proof.Receipt == nil || proof.Receipt.AcceptedAt != p.receipt.AcceptedAt {
		t.Fatalf("evidence lost %+v %v", proof, err)
	}
}

func (f *fakeObserver) ReadJournal(context.Context, model.MachineConfig, string, time.Time) (JournalBatch, error) {
	return f.batch, nil
}

type testEnv struct {
	m      *Manager
	store  *resource.Store
	native *fakeNative
	cfg    Config
	db     *store.DB
	ctx    context.Context
	clk    *clock.Fake
	probe  *fakeObserver

	mu     sync.Mutex
	events []Event
}

func newEnv(t testing.TB) *testEnv {
	t.Helper()
	db := storetest.Open(t)
	clk := clock.NewFake(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	e := &testEnv{db: db, clk: clk, ctx: auth.WithActor(context.Background(), auth.CLI), probe: &fakeObserver{}}
	e.probe.snapshot = Snapshot{DeviceHash: "device-a", BootID: "boot-a", Init: "systemd", CPUTotal: 100, CPUIdle: 10, CPUCount: 4, MemoryTotal: 100, MemoryAvailable: 90, Filesystems: []Filesystem{{Path: "/", Total: 100, Available: 90}}, Units: map[string]string{"app.service": "active"}}
	e.probe.snapshot.Coverage = map[string]Coverage{}
	for _, source := range []string{"identity", "boot", "init", "uptime", "cpu", "memory"} {
		e.probe.snapshot.Coverage[source] = Coverage{State: "available"}
	}
	e.probe.batch = JournalBatch{Cursor: "cursor-a", Coverage: Coverage{State: "available"}, Events: []Event{{Key: "journal-a", OccurredAt: clk.Now(), Kind: "machine.unit.event", Severity: "info"}}}
	gate := maintenance.New(db)
	log := audit.New(db, audit.WithClock(clk.Now))
	e.native = &fakeNative{}
	var err error
	e.store, err = resource.New(resource.Config{DB: db, Gate: gate, Audit: log, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.store.Close)
	e.cfg = Config{DB: db, Resources: e.store, Native: e.native, Audit: log, Clock: clk, Gate: gate, Observer: e.probe, OnEvent: func(ev Event) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.events = append(e.events, ev)
	}}
	e.m, err = New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Tests replace e.m to simulate a restart; the helpers that write flush
	// the store, so no delivery reads e.m while a test replaces it.
	e.store.Subscribe(func(c resource.Change) { e.m.Changed(c) })
	e.store.RegisterHooks(model.ResourceMachine, e.m.Hooks())
	return e
}

func (e *testEnv) put(t testing.TB, doc string, expected int) resource.PutResult {
	t.Helper()
	res, err := e.store.Put(e.ctx, []byte(doc), expected)
	if err != nil {
		t.Fatal(err)
	}
	e.flush(t)
	return res
}

// prune runs the observation retention with the default settings.
func (e *testEnv) prune(t testing.TB) {
	t.Helper()
	if _, err := e.store.PruneObservations(e.ctx, resource.ObservationRetention{Snapshots: 7 * 24 * time.Hour, Points: 90 * 24 * time.Hour, Events: 90 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
}

// flush waits until the store has delivered every committed change.
func (e *testEnv) flush(t testing.TB) {
	t.Helper()
	if err := e.store.Flush(e.ctx); err != nil {
		t.Fatal(err)
	}
}

// published returns the events published so far, after the store has
// delivered every committed change.
func (e *testEnv) published(t testing.TB) []Event {
	t.Helper()
	e.flush(t)
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.events)
}

func (e *testEnv) setPhase(t testing.TB, name, phase string, expected int) {
	t.Helper()
	rec, err := e.store.Get(e.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Type().ChangePhase(e.ctx, rec, phase, expected, "test"); err != nil {
		t.Fatal(err)
	}
	e.flush(t)
}

type nativeCall struct {
	resource, typ string
	labels        map[string]string
	health        monitor.Health
	reasons       []string
}

type fakeNative struct {
	calls   []nativeCall
	cleared []string
}

func (f *fakeNative) SetNative(resource, typ string, labels map[string]string, h monitor.Health, reasons []string) {
	f.calls = append(f.calls, nativeCall{resource, typ, labels, h, reasons})
}

func (f *fakeNative) Forget(resource string) { f.cleared = append(f.cleared, resource) }

func (f *fakeNative) last() nativeCall {
	if len(f.calls) == 0 {
		return nativeCall{}
	}
	return f.calls[len(f.calls)-1]
}
func (e *testEnv) poll(t testing.TB) Record {
	t.Helper()
	e.clk.Advance(30 * time.Second)
	e.probe.snapshot.CPUTotal += 100
	e.probe.snapshot.CPUIdle += 90
	r, err := e.m.Observe(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDocumentsAndIncarnation(t *testing.T) {
	e := newEnv(t)
	if _, err := e.store.Put(context.Background(), []byte(testDoc), 0); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("missing actor: %v", err)
	}
	if res := e.put(t, testDoc, 0); res.Version != 1 {
		t.Fatalf("create: %+v", res)
	}
	if res := e.put(t, testDoc, 1); res.Changed {
		t.Fatalf("repeat: %+v", res)
	}
	if _, err := e.store.Put(e.ctx, []byte(testDoc), 2); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	first, _ := e.m.Get(e.ctx, "workstation")
	e.setPhase(t, "workstation", resource.PhaseRetired, 1)
	if _, err := e.m.Observe(e.ctx, "workstation"); err == nil {
		t.Fatal("observed retired")
	}
	e.setPhase(t, "workstation", resource.PhaseUnmanaged, 1)
	e.put(t, testDoc, 0)
	second, _ := e.m.Get(e.ctx, "workstation")
	if first.ID == second.ID {
		t.Fatal("recreated identity reused")
	}
	archived, err := e.m.Get(e.ctx, "@"+first.ID)
	if err != nil || archived.Phase != resource.PhaseUnmanaged {
		t.Fatalf("archive by id: %+v %v", archived, err)
	}
	if _, err := e.m.Get(e.ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing machine: %v", err)
	}
}

func TestPollingCheckpointRestartAndFreshness(t *testing.T) {
	e := newEnv(t)
	e.put(t, testDoc, 0)
	first := e.poll(t)
	if first.Status.Snapshot.CPUPercent != nil || first.Status.Health != "unknown" {
		t.Fatalf("first sample %+v", first.Status)
	}
	second := e.poll(t)
	if second.Status.Health != "healthy" || second.Status.Snapshot.CPUPercent == nil {
		t.Fatalf("second %+v", second.Status)
	}
	count := len(e.published(t))
	e.m, _ = New(e.cfg)
	e.poll(t)
	if got := len(e.published(t)); got != count {
		t.Fatalf("restart duplicated events: %d -> %d", count, got)
	}
	e.probe.snapshot.MemoryAvailable = 1
	e.poll(t)
	bad := e.poll(t)
	if bad.Status.Health != "degraded" {
		t.Fatalf("threshold %+v", bad.Status)
	}
	e.clk.Advance(5 * time.Minute)
	stale, err := e.m.Get(e.ctx, "workstation")
	if err != nil || stale.Status.Fresh || stale.Status.Health != "unknown" {
		t.Fatalf("stale %+v %v", stale, err)
	}
	count = len(e.published(t))
	e.m, _ = New(e.cfg)
	e.poll(t)
	if len(e.published(t)) > count+1 {
		t.Fatal("stale gap created duplicate firing")
	}
	e.probe.snapshot.BootID = "boot-b"
	boot := e.poll(t)
	if boot.Status.Snapshot.CPUPercent != nil {
		t.Fatal("CPU crossed boots")
	}
	events, err := e.m.Events(e.ctx, "workstation", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	journalCount := 0
	bootCount := 0
	for _, ev := range events {
		if ev.Source == "journal" {
			journalCount++
		}
		if ev.Kind == "machine.boot.changed" {
			bootCount++
		}
	}
	if journalCount != 1 || bootCount != 1 {
		t.Fatalf("journal=%d boot=%d", journalCount, bootCount)
	}
}

func TestFailedCommitNeverPublishesOrAdvancesCursor(t *testing.T) {
	e := newEnv(t)
	e.put(t, testDoc, 0)
	e.poll(t)
	count := len(e.published(t))
	err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(e.ctx, `CREATE TRIGGER reject_snapshot BEFORE INSERT ON resource_snapshots BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.probe.batch.Cursor = "cursor-b"
	e.probe.batch.Events[0].Key = "journal-b"
	if _, err = e.m.Observe(e.ctx, "workstation"); err == nil {
		t.Fatal("commit succeeded")
	}
	var cursor string
	if err = e.db.Reader().QueryRowContext(e.ctx, `SELECT cursor FROM machine_cursors`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if got := len(e.published(t)); cursor != "cursor-a" || got != count {
		t.Fatalf("published failed transaction: %q %d", cursor, got)
	}
}

func TestChangedIdentityFailsClosedAndOptionalCoverage(t *testing.T) {
	e := newEnv(t)
	e.put(t, testDoc, 0)
	e.poll(t)
	e.poll(t)
	e.probe.snapshot.Coverage = map[string]Coverage{"psi": {State: "unknown", Reason: "unsupported"}}
	r := e.poll(t)
	if r.Status.Health != "healthy" {
		t.Fatalf("optional source affects health: %+v", r.Status)
	}
	e.probe.snapshot.DeviceHash = "replacement"
	r = e.poll(t)
	if r.Status.Identity != "changed" || r.Status.Health != "unknown" {
		t.Fatalf("identity %+v", r.Status)
	}
}
