package resource

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

// lowerCeilings makes the row ceilings and the batch small enough to reach
// in a test.
func lowerCeilings(t *testing.T, ceiling, batch int64) {
	t.Helper()
	old := []int64{maxSnapshots, maxPoints, maxEvents, pruneBatch}
	maxSnapshots, maxPoints, maxEvents, pruneBatch = ceiling, ceiling, ceiling, batch
	t.Cleanup(func() { maxSnapshots, maxPoints, maxEvents, pruneBatch = old[0], old[1], old[2], old[3] })
}

type ceilingEnv struct {
	ctx   context.Context
	db    *store.DB
	store *Store
	clk   *clock.Fake
}

func newCeilingEnv(t *testing.T) *ceilingEnv {
	t.Helper()
	db := storetest.Open(t)
	clk := clock.NewFake(time.UnixMilli(1_700_000_000_000))
	s, err := New(Config{DB: db, Audit: audit.New(db), Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	e := &ceilingEnv{ctx: auth.WithActor(context.Background(), auth.CLI), db: db, store: s, clk: clk}
	for _, r := range [][2]string{{"router-1", "router"}, {"machine-1", "machine"}} {
		e.exec(t, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES(?,?,?,'x','managed',1,0,0)`, r[0], r[0], r[1])
	}
	return e
}

func (e *ceilingEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(query, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

func (e *ceilingEnv) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.db.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *ceilingEnv) insert(t *testing.T, ev Event) {
	t.Helper()
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error { return InsertEvent(e.ctx, tx, &ev) }); err != nil {
		t.Fatal(err)
	}
}

// TestJournalFloodKeepsOtherResourcesEvents shows that the event ceiling
// is per resource: a machine that floods its journal loses its own oldest
// events, never a router's, whose events are the oldest in the table.
func TestJournalFloodKeepsOtherResourcesEvents(t *testing.T) {
	lowerCeilings(t, 50, 10)
	e := newCeilingEnv(t)
	now := e.clk.Now()
	for _, key := range []string{"a", "b", "c"} {
		e.insert(t, Event{ResourceID: "router-1", Source: "control", Key: key, Kind: "router.observed", OccurredAt: now, IngestedAt: now})
	}
	e.exec(t, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO resource_events(resource_id,source,source_key,occurred_at,ingested_at,kind,body)
		SELECT 'machine-1','journal','j-'||i,?,?,'machine.journal','{}' FROM n`, 75, now.UnixMilli(), now.UnixMilli())

	// The ingestion's own trim removes one batch; the retention job
	// enforces the rest.
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error { return TrimEvents(e.ctx, tx, "machine-1") }); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM resource_events WHERE resource_id='machine-1'`); n != 65 {
		t.Fatalf("machine events after trim = %d, want 65", n)
	}
	if _, err := e.store.PruneObservations(e.ctx, ObservationRetention{Snapshots: time.Hour, Points: time.Hour, Events: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM resource_events WHERE resource_id='machine-1'`); n != 50 {
		t.Fatalf("machine events after prune = %d, want 50", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM resource_events WHERE source_key IN ('j-1','j-25','j-26')`); n != 1 {
		t.Fatalf("want only j-26 of j-1, j-25 and j-26, got %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM resource_events WHERE resource_id='router-1'`); n != 3 {
		t.Fatalf("router events = %d, want 3", n)
	}
}

// TestControlEventsEnforceCeilingWithoutPolling shows that a control event
// trims its resource at once, without waiting for a poll or the retention
// job.
func TestControlEventsEnforceCeilingWithoutPolling(t *testing.T) {
	lowerCeilings(t, 5, 10)
	e := newCeilingEnv(t)
	now := e.clk.Now()
	e.exec(t, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<5)
		INSERT INTO resource_events(resource_id,source,source_key,occurred_at,ingested_at,kind,body)
		SELECT 'machine-1','poll','p-'||i,0,0,'machine.synthetic','{}' FROM n`)
	e.insert(t, Event{ResourceID: "machine-1", Source: "control", Key: "retired", Kind: "machine.retired", OccurredAt: now, IngestedAt: now})
	if n := e.count(t, `SELECT COUNT(*) FROM resource_events WHERE resource_id='machine-1'`); n != 5 {
		t.Fatalf("events = %d, want 5", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM resource_events WHERE source_key='p-1'`); n != 0 {
		t.Fatal("oldest event kept")
	}
}

// TestSnapshotAndPointCeilingsArePerResource keeps the newest snapshots and
// points of each resource up to the ceiling, and the snapshot baseline.
func TestSnapshotAndPointCeilingsArePerResource(t *testing.T) {
	lowerCeilings(t, 4, 2)
	e := newCeilingEnv(t)
	at := e.clk.Now().UnixMilli()
	for _, id := range []string{"machine-1", "router-1"} {
		rows := 9
		if id == "router-1" {
			rows = 3
		}
		for i := range rows {
			e.exec(t, `INSERT INTO resource_snapshots(resource_id,observed_at,body) VALUES(?,?,'{}')`, id, at+int64(i))
			e.exec(t, `INSERT INTO resource_points(resource_id,bucket_at,boot_id,samples,cpu_sum,cpu_count,body) VALUES(?,?,'',1,0,0,'{}')`, id, at+int64(i))
		}
	}
	// A clock step back: the newest snapshot by ID is the oldest by time
	// and stays as the baseline.
	e.exec(t, `INSERT INTO resource_snapshots(resource_id,observed_at,body) VALUES('machine-1',?,'{"baseline":true}')`, at-1)
	n, err := e.store.PruneObservations(e.ctx, ObservationRetention{Snapshots: time.Hour, Points: time.Hour, Events: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if n != 6+5 {
		t.Fatalf("deleted %d, want 11", n)
	}
	for table, want := range map[string][2]int64{"resource_snapshots": {4, 3}, "resource_points": {4, 3}} {
		if got := e.count(t, `SELECT COUNT(*) FROM `+table+` WHERE resource_id='machine-1'`); got != want[0] {
			t.Errorf("%s machine = %d, want %d", table, got, want[0])
		}
		if got := e.count(t, `SELECT COUNT(*) FROM `+table+` WHERE resource_id='router-1'`); got != want[1] {
			t.Errorf("%s router = %d, want %d", table, got, want[1])
		}
	}
	if got := e.count(t, `SELECT COUNT(*) FROM resource_snapshots WHERE body='{"baseline":true}'`); got != 1 {
		t.Error("baseline snapshot deleted")
	}
	if got := e.count(t, `SELECT MIN(observed_at) FROM resource_snapshots WHERE resource_id='machine-1' AND body='{}'`); got != at+6 {
		t.Errorf("oldest kept snapshot at %d, want %d", got, at+6)
	}
}
