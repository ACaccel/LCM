package security

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

type fixture struct {
	m   *Manager
	cfg Config
	clk *clock.Fake
	ctx context.Context
}

func setup(t *testing.T, opts ...func(*Config)) *fixture {
	t.Helper()
	db := storetest.Open(t)
	clk := clock.NewFake(time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC))
	ctx := auth.WithActor(context.Background(), auth.CLI)
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO machines(id,name,version,created_at,updated_at) VALUES('machine-a','workstation',1,?,?)`, clk.Now().UnixMilli(), clk.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DB: db, Audit: audit.New(db, audit.WithClock(clk.Now)), Clock: clk, Enabled: true, MachineID: "machine-a"}
	for _, o := range opts {
		o(&cfg)
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{m: m, cfg: cfg, clk: clk, ctx: ctx}
}
func (f *fixture) event(id string) Event {
	return Event{EventID: id, MachineID: "machine-a", Source: "nginx", Fingerprint: "abuse.auth", Status: "firing", Severity: "warning", Summary: "Synthetic authentication traffic anomaly", ObservedAt: f.clk.Now()}
}
func (f *fixture) ingest(t *testing.T, e Event) Receipt {
	t.Helper()
	r, err := f.m.Ingest(f.ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *fixture) incidents(t *testing.T) []Incident {
	t.Helper()
	v, err := f.m.List(f.ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIncidentReplayRecoveryAndOutOfOrder(t *testing.T) {
	f := setup(t)
	e := f.event("one")
	r := f.ingest(t, e)
	if r.IncidentID == "" {
		t.Fatal("missing durable incident")
	}
	duplicate := f.ingest(t, e)
	if !duplicate.Duplicate || duplicate.IncidentID != r.IncidentID {
		t.Fatal("retry lost idempotency")
	}
	e.Summary = "Different payload"
	if _, err := f.m.Ingest(f.ctx, e); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting retry: %v", err)
	}
	f.clk.Advance(time.Minute)
	e = f.event("resolved")
	e.Status = "resolved"
	e.Severity = "info"
	f.ingest(t, e)
	if items := f.incidents(t); len(items) != 1 || items[0].State != "resolved" {
		t.Fatalf("recovery: %+v", items)
	}
	old := f.event("late")
	old.ObservedAt = old.ObservedAt.Add(-time.Second)
	if receipt := f.ingest(t, old); !receipt.Historical {
		t.Fatal("out-of-order event reopened current incident")
	}
	f.clk.Advance(time.Minute)
	again := f.ingest(t, f.event("new-episode"))
	if again.IncidentID == r.IncidentID || len(f.incidents(t)) != 2 {
		t.Fatal("new episode reused previous incident")
	}
}

func TestHistoryAndValidationNeverCreateLiveAttack(t *testing.T) {
	f := setup(t)
	e := f.event("backfill")
	e.ObservedAt = e.ObservedAt.Add(-time.Hour)
	if r := f.ingest(t, e); !r.Historical || r.IncidentID != "" {
		t.Fatalf("historical receipt: %+v", r)
	}
	for _, change := range []func(*Event){
		func(e *Event) { e.Source = "machine" }, func(e *Event) { e.Summary = "bad\nraw log" }, func(e *Event) { e.MachineID = "workstation" }, func(e *Event) { e.Service = "missing" }, func(e *Event) { e.ObservedAt = e.ObservedAt.Add(6 * time.Minute) }, func(e *Event) { e.ObservedAt = e.ObservedAt.Add(-31 * 24 * time.Hour) }, func(e *Event) { e.Summary = strings.Repeat("a", 501) },
	} {
		e = f.event("invalid")
		change(&e)
		if _, err := f.m.Ingest(f.ctx, e); err == nil {
			t.Fatalf("accepted invalid: %+v", e)
		}
	}
	if len(f.incidents(t)) != 0 {
		t.Fatal("history or invalid input opened incidents")
	}
}

func TestDeliveryRestartCoalescingCooldownAndRecovery(t *testing.T) {
	var received []Incident
	fail := true
	sender := func(_ context.Context, i Incident) error {
		received = append(received, i)
		if fail {
			return errors.New("private token must not persist")
		}
		return nil
	}
	f := setup(t, func(c *Config) { c.Send = sender })
	id := f.ingest(t, f.event("initial")).IncidentID
	if err := f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	st, err := f.m.Status(f.ctx)
	if err != nil || st.Pending != 1 || st.Failed != 1 || strings.Contains(st.LastError, "private") {
		t.Fatalf("failure status: %+v %v", st, err)
	}
	// Reconstructing the component shares only persisted SQLite state.
	f.m, err = New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(time.Minute)
	f.ingest(t, f.event("repeat"))
	fail = false
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0].ID != id || received[1].ID != id || received[1].Occurrences != 2 {
		t.Fatalf("restart changed message identity: %+v", received)
	}
	f.clk.Advance(time.Minute)
	f.ingest(t, f.event("cooldown"))
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 {
		t.Fatal("cooldown emitted duplicate notification")
	}
	f.clk.Advance(time.Minute)
	e := f.event("recovery")
	e.Status = "resolved"
	e.Severity = "info"
	f.ingest(t, e)
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(received) != 3 || received[2].ID != id || received[2].State != "resolved" {
		t.Fatalf("recovery did not update original: %+v", received)
	}
}

func TestQueueOverflowExpiryAndRetention(t *testing.T) {
	f := setup(t, func(c *Config) {
		c.Send = func(context.Context, Incident) error { return errors.New("offline") }
		c.MaxPending = 1
		c.MaxEvents = 2
		c.DeliveryTTL = time.Hour
		c.Retention = 2 * time.Hour
	})
	f.ingest(t, f.event("first"))
	e := f.event("second")
	e.Fingerprint = "other"
	f.ingest(t, e)
	st, _ := f.m.Status(f.ctx)
	if st.Pending != 1 || st.Overflow != 1 || st.Failed != 1 {
		t.Fatalf("overflow invisible: %+v", st)
	}
	f.clk.Advance(time.Hour)
	if err := f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	st, _ = f.m.Status(f.ctx)
	if st.Expired != 1 || st.Pending != 0 {
		t.Fatalf("expiry invisible: %+v", st)
	}
	e = f.event("resolve")
	e.Status = "resolved"
	f.ingest(t, e)
	var n int
	if err := f.cfg.DB.Reader().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM security_events`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("event capacity=%d %v", n, err)
	}
	f.clk.Advance(3 * time.Hour)
	if err := f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Prune(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.cfg.DB.Reader().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM security_events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("event retention=%d %v", n, err)
	}
}

func TestActiveCapacityRejectsWithoutSilentlyDropping(t *testing.T) {
	f := setup(t, func(c *Config) { c.MaxIncidents = 1 })
	f.ingest(t, f.event("one"))
	e := f.event("two")
	e.Fingerprint = "different"
	if _, err := f.m.Ingest(f.ctx, e); !errors.Is(err, ErrCapacity) {
		t.Fatalf("active capacity: %v", err)
	}
	if len(f.incidents(t)) != 1 {
		t.Fatal("active episode was dropped")
	}
}

type observations struct {
	record   machine.Record
	services []monitor.ServiceStatus
	selector string
}

func (o *observations) Get(_ context.Context, selector string) (machine.Record, error) {
	o.selector = selector
	return o.record, nil
}
func (o *observations) Services() []monitor.ServiceStatus { return o.services }
func TestNativeReconciliationStalenessAndCheckpointRestart(t *testing.T) {
	f := setup(t)
	o := &observations{record: machine.Record{ID: "machine-a", Status: machine.Status{Fresh: true, Health: "degraded", Reasons: []string{"memory"}, Snapshot: &machine.Snapshot{ID: 1, ObservedAt: f.clk.Now()}}}}
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	if o.selector != "@machine-a" {
		t.Fatalf("Machine lookup must use immutable ID selector: %q", o.selector)
	}
	first := f.incidents(t)
	if len(first) != 1 || first[0].Source != "machine" {
		t.Fatalf("native Machine attribution: %+v", first)
	}
	f.m, _ = New(f.cfg)
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	if items := f.incidents(t); items[0].Occurrences != 1 {
		t.Fatal("persisted sample replayed as fresh")
	}
	o.record.Status.Fresh = false
	f.clk.Advance(time.Minute)
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	if len(f.incidents(t)) != 1 {
		t.Fatal("single gap sample notified")
	}
	f.clk.Advance(time.Minute)
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	items := f.incidents(t)
	if len(items) != 2 || items[0].Fingerprint != "observation-gap" {
		t.Fatalf("staleness missing: %+v", items)
	}
	o.record.Status.Fresh = true
	o.record.Status.Health = "healthy"
	o.record.Status.Snapshot = &machine.Snapshot{ID: 2, ObservedAt: f.clk.Now().Add(time.Minute)}
	f.clk.Advance(time.Minute)
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	for _, i := range f.incidents(t) {
		if i.State != "resolved" {
			t.Fatalf("not recovered: %+v", i)
		}
	}
}

func TestTypedTrafficIsQueryableAndNeverChangesIncidentLifecycle(t *testing.T) {
	f := setup(t)
	v := Traffic{MachineID: "machine-a", WindowStart: f.clk.Now().Add(-time.Minute), WindowEnd: f.clk.Now(), Category: "auth", Requests: 12, Status5xx: 2, RequestTimeSumMs: 120, RequestTimeMaxMs: 20, RequestBytes: 900, ResponseBytes: 2400, DryRunHits: 3}
	receipt, err := f.m.IngestTraffic(f.ctx, v)
	if err != nil || !receipt.Accepted || receipt.Duplicate {
		t.Fatalf("traffic intake: %+v %v", receipt, err)
	}
	receipt, err = f.m.IngestTraffic(f.ctx, v)
	if err != nil || !receipt.Duplicate {
		t.Fatalf("traffic retry: %+v %v", receipt, err)
	}
	v.Requests = 13
	if _, err = f.m.IngestTraffic(f.ctx, v); !errors.Is(err, ErrConflict) {
		t.Fatalf("window mutation: %v", err)
	}
	f.m, err = New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.m.TrafficHistory(f.ctx, 0, 10)
	if err != nil || len(rows) != 1 || rows[0].Requests != 12 || rows[0].RequestTimeSumMs != 120 {
		t.Fatalf("typed readback after restart: %+v %v", rows, err)
	}
	if len(f.incidents(t)) != 0 {
		t.Fatal("normal observations opened incidents")
	}
	if st, err := f.m.Status(f.ctx); err != nil || st.Pending != 0 {
		t.Fatalf("normal observation queued notification: %+v %v", st, err)
	}
	v.Requests = -1
	v.WindowStart = v.WindowStart.Add(-time.Minute)
	if _, err = f.m.IngestTraffic(f.ctx, v); err == nil {
		t.Fatal("accepted negative counter")
	}
	f.clk.Advance(31 * 24 * time.Hour)
	if err = f.m.Prune(f.ctx); err != nil {
		t.Fatal(err)
	}
	rows, err = f.m.TrafficHistory(f.ctx, 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("traffic retention: %+v %v", rows, err)
	}
}

func TestServiceFailureAndGapRetainSeparateIdentities(t *testing.T) {
	f := setup(t)
	if err := f.cfg.DB.Write(f.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(f.ctx, `INSERT INTO service_versions(name,version,document,created_at,created_by_kind,created_by_id,created_by_name) VALUES('website',1,'{}',0,'system','test','test')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(f.ctx, `INSERT INTO services(name,host,version,state,state_since,created_at,updated_at) VALUES('website','local',1,'running',0,0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	o := &observations{record: machine.Record{ID: "machine-a", Status: machine.Status{Fresh: true, Health: "healthy", Snapshot: &machine.Snapshot{ID: 1, ObservedAt: f.clk.Now()}}}, services: []monitor.ServiceStatus{{Service: "website", Health: monitor.HealthDown, Checks: []monitor.CheckStatus{{LastResult: f.clk.Now(), Interval: time.Minute, Status: monitor.StatusDown}}}}}
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	rows := f.incidents(t)
	if len(rows) != 1 || rows[0].Source != "service" || rows[0].Service != "website" || rows[0].MachineID != "machine-a" {
		t.Fatalf("service attribution lost: %+v", rows)
	}
	// Missing checks need two distinct minute polls, not two immediate calls.
	o.services[0].Checks[0].Stale = true
	for range 2 {
		if err := f.m.Poll(f.ctx, o, o); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.incidents(t)) != 1 {
		t.Fatal("transient service gap notified")
	}
	f.clk.Advance(time.Minute)
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	if len(f.incidents(t)) != 2 {
		t.Fatal("persistent service gap missing")
	}
	f.clk.Advance(time.Minute)
	o.services[0].Health = monitor.HealthHealthy
	o.services[0].Checks[0].Stale = false
	o.services[0].Checks[0].LastResult = f.clk.Now()
	o.record.Status.Snapshot = &machine.Snapshot{ID: 2, ObservedAt: f.clk.Now()}
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	for _, i := range f.incidents(t) {
		if i.State != "resolved" {
			t.Fatalf("service recovery missing: %+v", i)
		}
	}
}

func TestNotificationDisableCancelsExistingQueueAcrossRestart(t *testing.T) {
	sends := 0
	f := setup(t, func(c *Config) { c.Send = func(context.Context, Incident) error { sends++; return nil } })
	original := f.ingest(t, f.event("before-disable")).IncidentID
	disabled := f.cfg
	disabled.Send = nil
	var err error
	f.m, err = New(disabled)
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.m.Status(f.ctx)
	if err != nil || st.Pending != 0 {
		t.Fatalf("disable did not cancel queue: %+v %v", st, err)
	}
	if items := f.incidents(t); items[0].Delivery != "disabled" {
		t.Fatalf("disable not visible: %+v", items)
	}
	f.clk.Advance(time.Minute)
	recovery := f.event("recovered-while-disabled")
	recovery.Status = "resolved"
	recovery.Severity = "info"
	f.ingest(t, recovery)
	f.m, err = New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if sends != 0 {
		t.Fatal("re-enabled delivery replayed historical recovery")
	}
	if items := f.incidents(t); len(items) != 1 || items[0].ID != original || items[0].State != "resolved" || items[0].Delivery != "disabled" {
		t.Fatalf("lost disabled history: %+v", items)
	}
	// A genuinely new episode after re-enabling may notify normally.
	f.clk.Advance(time.Minute)
	f.ingest(t, f.event("new-after-enable"))
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatalf("new episode did not notify: %d", sends)
	}
}

func TestRemovedServiceRetiresOnlyExistingMachineEpisodes(t *testing.T) {
	sends := 0
	f := setup(t, func(c *Config) {
		c.MaxIncidents = 1
		c.Send = func(context.Context, Incident) error { sends++; return nil }
	})
	if err := f.cfg.DB.Write(f.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(f.ctx, `INSERT INTO service_versions(name,version,document,created_at,created_by_kind,created_by_id,created_by_name) VALUES('website',1,'{}',0,'system','test','test')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(f.ctx, `INSERT INTO services(name,host,version,state,state_since,created_at,updated_at) VALUES('website','local',1,'running',0,0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e := f.event("service-before-removal")
	e.Service = "website"
	old := f.ingest(t, e).IncidentID
	if err := f.cfg.DB.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(f.ctx, `DELETE FROM services WHERE name='website'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.EventID = "external-fake-recovery"
	e.Status = "resolved"
	if _, err := f.m.Ingest(f.ctx, e); err == nil {
		t.Fatal("external ingest bypassed Service existence validation")
	}
	o := &observations{record: machine.Record{ID: "machine-a", Status: machine.Status{Fresh: true, Health: "healthy", Snapshot: &machine.Snapshot{ID: 1, ObservedAt: f.clk.Now()}}}}
	if err := f.m.Poll(f.ctx, o, o); err != nil {
		t.Fatal(err)
	}
	items := f.incidents(t)
	if len(items) != 1 || items[0].ID != old || items[0].State != "retired" || items[0].Delivery != "disabled" || !strings.Contains(items[0].Summary, "without asserting recovery") {
		t.Fatalf("removed scope mislabeled: %+v", items)
	}
	if err := f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if sends != 0 {
		t.Fatal("retired registration retained a notification")
	}
	// The old closed episode can make room rather than exhausting active cap.
	f.clk.Advance(time.Minute)
	f.ingest(t, f.event("new-resource-episode"))
	if items = f.incidents(t); len(items) != 1 || items[0].State != "firing" || items[0].ID == old {
		t.Fatalf("retirement did not release capacity: %+v", items)
	}
}

func TestBlockedDeliveryDoesNotLockObservationReconciliation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := setup(t, func(c *Config) {
		c.Send = func(context.Context, Incident) error { close(started); <-release; return nil }
	})
	f.ingest(t, f.event("blocked-delivery"))
	done := make(chan error, 1)
	go func() { done <- f.m.Deliver(f.ctx) }()
	<-started
	o := &observations{record: machine.Record{ID: "machine-a", Status: machine.Status{Fresh: true, Health: "healthy", Snapshot: &machine.Snapshot{ID: 1, ObservedAt: f.clk.Now()}}}}
	pollDone := make(chan error, 1)
	go func() { pollDone <- f.m.Poll(f.ctx, o, o) }()
	select {
	case err := <-pollDone:
		if err != nil {
			close(release)
			if cleanupErr := <-done; cleanupErr != nil {
				t.Errorf("delivery cleanup: %v", cleanupErr)
			}
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		close(release)
		if cleanupErr := <-done; cleanupErr != nil {
			t.Errorf("delivery cleanup: %v", cleanupErr)
		}
		t.Fatal("delivery worker blocked observation reconciliation")
	}
	st, err := f.m.Status(f.ctx)
	close(release)
	if deliverErr := <-done; deliverErr != nil {
		t.Fatal(deliverErr)
	}
	if err != nil || st.LastPollAt == nil || !st.LastPollAt.Equal(f.clk.Now()) {
		t.Fatalf("blocked relay stalled observation: %+v %v", st, err)
	}
}

func TestLateDeliveryCompletionCannotUndoScopeRetirement(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := setup(t, func(c *Config) {
		c.Send = func(context.Context, Incident) error { close(started); <-release; return nil }
	})
	if err := f.cfg.DB.Write(f.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(f.ctx, `INSERT INTO service_versions(name,version,document,created_at,created_by_kind,created_by_id,created_by_name) VALUES('website',1,'{}',0,'system','test','test')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(f.ctx, `INSERT INTO services(name,host,version,state,state_since,created_at,updated_at) VALUES('website','local',1,'running',0,0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e := f.event("service-inflight")
	e.Service = "website"
	f.ingest(t, e)
	done := make(chan error, 1)
	go func() { done <- f.m.Deliver(f.ctx) }()
	<-started
	if err := f.cfg.DB.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(f.ctx, `DELETE FROM services WHERE name='website'`)
		return err
	}); err != nil {
		close(release)
		if cleanupErr := <-done; cleanupErr != nil {
			t.Errorf("delivery cleanup: %v", cleanupErr)
		}
		t.Fatal(err)
	}
	o := &observations{record: machine.Record{ID: "machine-a", Status: machine.Status{Fresh: true, Health: "healthy", Snapshot: &machine.Snapshot{ID: 1, ObservedAt: f.clk.Now()}}}}
	err := f.m.Poll(f.ctx, o, o)
	close(release)
	if deliverErr := <-done; deliverErr != nil {
		t.Fatal(deliverErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if items := f.incidents(t); len(items) != 1 || items[0].State != "retired" || items[0].Delivery != "disabled" {
		t.Fatalf("late send undid retirement: %+v", items)
	}
	st, err := f.m.Status(f.ctx)
	if err != nil || st.Pending != 0 {
		t.Fatalf("retired send recreated queue: %+v %v", st, err)
	}
}
