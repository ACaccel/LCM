package machine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

const testDoc = "apiVersion: lcm/v1\nkind: Machine\nmetadata: {name: workstation}\nspec: {monitoring: {units: [app.service], journal: true, thresholds: {consecutive: 2}}}"

type fakeObserver struct {
	snapshot Snapshot
	batch    JournalBatch
	err      error
	hook     func()
}

func (f *fakeObserver) Observe(context.Context, model.MachineMonitoring) (Snapshot, error) {
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
	if err = e.m.SetPhase(e.ctx, "workstation", "unmanaged", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		t.Fatal(err)
	}
	archives, err := e.m.Archives(e.ctx)
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
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", "reboot", "retention", 1)
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
	if err = e.m.Prune(e.ctx); err != nil {
		t.Fatal(err)
	}
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

func TestControlEventsEnforceCapacityWithoutPolling(t *testing.T) {
	e := newEnv(t)
	if _, err := e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		t.Fatal(err)
	}
	r, err := e.m.Get(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	body, err := encode(Event{MachineID: r.ID, Source: "poll", Kind: "machine.synthetic", Severity: "info", OccurredAt: e.clk.Now(), IngestedAt: e.clk.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO machine_events(id,machine_id,source,source_key,occurred_at,ingested_at,kind,body) VALUES(200002,?,'poll','synthetic',0,0,'machine.synthetic',?)`, r.ID, body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = e.m.SetPhase(e.ctx, "workstation", "retired", 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = e.db.Reader().QueryRow(`SELECT COUNT(*) FROM machine_events WHERE id<=3`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old events retained without a poll: %d %v", count, err)
	}
	events, err := e.m.Events(e.ctx, "workstation", 0, 200)
	if err != nil || len(events) != 2 || events[1].Kind != "machine.retired" {
		t.Fatalf("retained events %+v %v", events, err)
	}
}
func (f *fakeObserver) ReadJournal(context.Context, model.MachineMonitoring, string, time.Time) (JournalBatch, error) {
	return f.batch, nil
}

type testEnv struct {
	m      *Manager
	cfg    Config
	db     *store.DB
	ctx    context.Context
	clk    *clock.Fake
	probe  *fakeObserver
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
	e.cfg = Config{DB: db, Audit: audit.New(db, audit.WithClock(clk.Now)), Clock: clk, Gate: maintenance.New(db), Observer: e.probe, OnEvent: func(ev Event) { e.events = append(e.events, ev) }}
	var err error
	e.m, err = New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
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
	if _, err := e.m.Put(context.Background(), []byte(testDoc), 0); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("missing actor: %v", err)
	}
	res, err := e.m.Put(e.ctx, []byte(testDoc), 0)
	if err != nil || res.Version != 1 {
		t.Fatalf("create: %+v %v", res, err)
	}
	res, err = e.m.Put(e.ctx, []byte(testDoc), 1)
	if err != nil || res.Changed {
		t.Fatalf("repeat: %+v %v", res, err)
	}
	if _, err = e.m.Put(e.ctx, []byte(testDoc), 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	first, _ := e.m.Get(e.ctx, "workstation")
	if err = e.m.SetPhase(e.ctx, "workstation", "retired", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.Observe(e.ctx, "workstation"); err == nil {
		t.Fatal("observed retired")
	}
	if err = e.m.SetPhase(e.ctx, "workstation", "unmanaged", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		t.Fatal(err)
	}
	second, _ := e.m.Get(e.ctx, "workstation")
	if first.ID == second.ID {
		t.Fatal("recreated identity reused")
	}
}

func TestPollingCheckpointRestartAndFreshness(t *testing.T) {
	e := newEnv(t)
	if _, err := e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		t.Fatal(err)
	}
	first := e.poll(t)
	if first.Status.Snapshot.CPUPercent != nil || first.Status.Health != "unknown" {
		t.Fatalf("first sample %+v", first.Status)
	}
	second := e.poll(t)
	if second.Status.Health != "healthy" || second.Status.Snapshot.CPUPercent == nil {
		t.Fatalf("second %+v", second.Status)
	}
	count := len(e.events)
	e.m, _ = New(e.cfg)
	e.poll(t)
	if len(e.events) != count {
		t.Fatalf("restart duplicated events: %d -> %d", count, len(e.events))
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
	count = len(e.events)
	e.m, _ = New(e.cfg)
	e.poll(t)
	if len(e.events) > count+1 {
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
	if _, err := e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		t.Fatal(err)
	}
	e.poll(t)
	count := len(e.events)
	err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(e.ctx, `CREATE TRIGGER reject_snapshot BEFORE INSERT ON machine_snapshots BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
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
	if cursor != "cursor-a" || len(e.events) != count {
		t.Fatalf("published failed transaction: %q %d", cursor, len(e.events))
	}
}

func TestChangedIdentityFailsClosedAndOptionalCoverage(t *testing.T) {
	e := newEnv(t)
	if _, err := e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		t.Fatal(err)
	}
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
