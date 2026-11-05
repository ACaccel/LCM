package monitor

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// seedRollup stores a check and its one-minute rollups: at maps a minute
// offset from the epoch to {count, failures}.
func (h *harness) seedRollup(resource, check string, at map[int][2]int64) {
	h.t.Helper()
	id := checkID(resource, check)
	err := h.db.Write(h.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO monitor_checks (check_id, resource, name, type, critical, interval_ms, status, since_ms) VALUES (?, ?, ?, 'fake', 1, 10000, 'up', 0)`,
			id, resource, check); err != nil {
			return err
		}
		for minute, cf := range at {
			if _, err := tx.ExecContext(context.Background(),
				`INSERT INTO monitor_rollups (check_id, bucket_ms, count, failures) VALUES (?, ?, ?, ?)`,
				id, epoch.Add(time.Duration(minute)*time.Minute).UnixMilli(), cf[0], cf[1]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func TestAvailabilityAll(t *testing.T) {
	h := newHarness(t)
	// Two checks of "api" share bucket 1 (minutes 60 to 119); "db" has one
	// result in bucket 3.
	h.seedRollup("api", "web", map[int][2]int64{65: {6, 1}, 70: {6, 0}})
	h.seedRollup("api", "tcp", map[int][2]int64{65: {6, 2}})
	h.seedRollup("db", "tcp", map[int][2]int64{200: {1, 1}})
	// Older than the 4h window that ends at epoch+4h.
	h.seedRollup("old", "web", map[int][2]int64{-1: {5, 0}})
	h.clk.Advance(4*time.Hour - 30*time.Second)

	got, err := h.m.AvailabilityAll(h.ctx, 4, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("resources = %v", got)
	}
	api := got["api"]
	want := []AvailabilityBucket{{}, {Count: 18, Failures: 3}, {}, {}}
	if len(api.Buckets) != 4 || api.Count != 18 || api.Failures != 3 {
		t.Fatalf("api = %+v", api)
	}
	for i := range want {
		if api.Buckets[i] != want[i] {
			t.Errorf("api bucket %d = %+v, want %+v", i, api.Buckets[i], want[i])
		}
	}
	if db := got["db"]; db.Buckets[3] != (AvailabilityBucket{Count: 1, Failures: 1}) {
		t.Errorf("db = %+v", db)
	}

	if _, err := h.m.AvailabilityAll(h.ctx, 0, time.Hour); err == nil {
		t.Error("zero buckets accepted")
	}
	if _, err := h.m.AvailabilityAll(h.ctx, 4, 90*time.Second); err == nil {
		t.Error("a step that is not whole minutes accepted")
	}
}
