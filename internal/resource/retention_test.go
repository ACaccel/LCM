package resource_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

var retention = resource.ObservationRetention{Snapshots: 7 * 24 * time.Hour, Points: 90 * 24 * time.Hour, Events: 90 * 24 * time.Hour}

// addObserved inserts a bare resource row, enough for observation rows to
// reference it.
func (h *harness) addObserved(t *testing.T, id, typ string) {
	t.Helper()
	h.exec(t, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES(?,?,?,'x','managed',1,0,0)`, id, id, typ)
}

func (h *harness) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := h.db.Write(h.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(query, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.db.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPruneObservationsByAgeKeepsBaseline(t *testing.T) {
	h := newHarness(t)
	h.addObserved(t, "machine-1", "machine")
	h.addObserved(t, "router-1", "router")
	old := h.clk.Now()
	for _, id := range []string{"machine-1", "router-1"} {
		for i := range 3 {
			at := old.Add(time.Duration(i) * time.Minute).UnixMilli()
			h.exec(t, `INSERT INTO resource_snapshots(resource_id,observed_at,body) VALUES(?,?,'{}')`, id, at)
			h.exec(t, `INSERT INTO resource_points(resource_id,bucket_at,boot_id,samples,cpu_sum,cpu_count,body) VALUES(?,?,'',1,0,0,'{}')`, id, at)
			h.exec(t, `INSERT INTO resource_events(resource_id,source,source_key,occurred_at,ingested_at,kind,body) VALUES(?,'poll',?,?,?,'k','{}')`, id, i, at, at)
		}
	}
	h.clk.Advance(30 * 24 * time.Hour)
	recent := h.clk.Now().UnixMilli()
	h.exec(t, `INSERT INTO resource_points(resource_id,bucket_at,boot_id,samples,cpu_sum,cpu_count,body) VALUES('router-1',?,'',1,0,0,'{}')`, recent)
	h.exec(t, `INSERT INTO resource_events(resource_id,source,source_key,occurred_at,ingested_at,kind,body) VALUES('router-1','poll','recent',?,?,'k','{}')`, recent, recent)

	n, err := h.store.PruneObservations(h.ctx, retention)
	if err != nil {
		t.Fatal(err)
	}
	// Snapshots: all but the baseline of each resource.
	if n != 4 {
		t.Fatalf("deleted %d rows, want 4", n)
	}
	for _, id := range []string{"machine-1", "router-1"} {
		if got := h.count(t, `SELECT COUNT(*) FROM resource_snapshots WHERE resource_id=? AND observed_at=?`, id, old.Add(2*time.Minute).UnixMilli()); got != 1 {
			t.Fatalf("%s baseline missing", id)
		}
	}

	h.clk.Advance(80 * 24 * time.Hour)
	if _, err = h.store.PruneObservations(h.ctx, retention); err != nil {
		t.Fatal(err)
	}
	if got := h.count(t, `SELECT COUNT(*) FROM resource_points`); got != 1 {
		t.Fatalf("points = %d, want the recent one", got)
	}
	if got := h.count(t, `SELECT COUNT(*) FROM resource_events`); got != 1 {
		t.Fatalf("events = %d, want the recent one", got)
	}
	if got := h.count(t, `SELECT COUNT(*) FROM resource_snapshots`); got != 2 {
		t.Fatalf("snapshots = %d, want the two baselines", got)
	}
	if _, err = h.store.PruneObservations(h.ctx, resource.ObservationRetention{}); err == nil {
		t.Fatal("zero retention accepted")
	}
}

// TestObservationQueriesUseIndexes shows the plans of the timeline read,
// the age-based event retention and the ingestion trim.
func TestObservationQueriesUseIndexes(t *testing.T) {
	db := storetest.Open(t)
	newest, args := resource.EventQuery{Newest: true, Cursor: 5, CursorAt: 1, Limit: 50}.SQL("r")
	for _, tc := range []struct {
		query string
		args  []any
		want  string
	}{
		{newest, args, "INDEX resource_events_occurred"},
		{`SELECT id FROM resource_events WHERE ingested_at<? LIMIT 10`, []any{1}, "INDEX resource_events_ingested"},
		{`SELECT MIN(id) FROM resource_events WHERE resource_id=?`, []any{"r"}, "INDEX resource_events_time"},
	} {
		plan := strings.Join(storetest.QueryPlan(t, db, tc.query, tc.args...), "\n")
		if !strings.Contains(plan, tc.want) || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s:\n%s", tc.query, plan)
		}
	}
}
