package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// rollupBucket is the rollup resolution.
const rollupBucket = time.Minute

func bucketStart(t time.Time) int64 {
	ms := t.UnixMilli()
	return ms - ms%rollupBucket.Milliseconds()
}

// resultRec stores one result and folds it into its minute rollup.
type resultRec struct {
	checkID string
	at      time.Time
	ok      bool
	latency time.Duration
	message string
}

const (
	insertResultSQL = `INSERT INTO monitor_results (check_id, at, ok, latency_us, message) VALUES (?, ?, ?, ?, ?)`

	upsertRollupSQL = `INSERT INTO monitor_rollups (check_id, bucket_ms, count, failures, lat_sum_us, lat_min_us, lat_max_us)
VALUES (?, ?, 1, ?, ?, ?, ?)
ON CONFLICT (check_id, bucket_ms) DO UPDATE SET
    count      = count + 1,
    failures   = failures + excluded.failures,
    lat_sum_us = lat_sum_us + excluded.lat_sum_us,
    lat_min_us = CASE WHEN excluded.lat_min_us IS NULL THEN lat_min_us
                      WHEN lat_min_us IS NULL THEN excluded.lat_min_us
                      ELSE MIN(lat_min_us, excluded.lat_min_us) END,
    lat_max_us = CASE WHEN excluded.lat_max_us IS NULL THEN lat_max_us
                      WHEN lat_max_us IS NULL THEN excluded.lat_max_us
                      ELSE MAX(lat_max_us, excluded.lat_max_us) END`
)

func (r resultRec) exec(ctx context.Context, st *stmts) error {
	if err := st.exec(ctx, insertResultSQL, r.checkID, r.at.UnixMilli(), boolInt(r.ok), r.latency.Microseconds(), r.message); err != nil {
		return fmt.Errorf("insert result: %w", err)
	}
	var failures, sum int64
	var lo, hi any
	if r.ok {
		sum = r.latency.Microseconds()
		lo, hi = sum, sum
	} else {
		failures = 1
	}
	if err := st.exec(ctx, upsertRollupSQL, r.checkID, bucketStart(r.at), failures, sum, lo, hi); err != nil {
		return fmt.Errorf("update rollup: %w", err)
	}
	return nil
}

// checkRow is the persisted state of one check.
type checkRow struct {
	id          string
	service     string
	name        string
	typ         string
	critical    bool
	interval    time.Duration
	status      Status
	since       time.Time
	okRun       int
	failRun     int
	lastAt      time.Time
	lastOK      bool
	lastLatency time.Duration
	lastMessage string
	lastBeat    time.Time
}

const upsertCheckSQL = `INSERT INTO monitor_checks (check_id, service, name, type, critical, interval_ms, status, since_ms,
    consecutive_ok, consecutive_fail, last_at, last_ok, last_latency_us, last_message, last_beat_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (check_id) DO UPDATE SET
    service = excluded.service, name = excluded.name, type = excluded.type, critical = excluded.critical,
    interval_ms = excluded.interval_ms, status = excluded.status, since_ms = excluded.since_ms,
    consecutive_ok = excluded.consecutive_ok, consecutive_fail = excluded.consecutive_fail,
    last_at = excluded.last_at, last_ok = excluded.last_ok, last_latency_us = excluded.last_latency_us,
    last_message = excluded.last_message, last_beat_at = excluded.last_beat_at`

func (c checkRow) exec(ctx context.Context, st *stmts) error {
	err := st.exec(ctx, upsertCheckSQL, c.id, c.service, c.name, c.typ, boolInt(c.critical), c.interval.Milliseconds(),
		string(c.status), c.since.UnixMilli(), c.okRun, c.failRun, nullMillis(c.lastAt), boolInt(c.lastOK),
		c.lastLatency.Microseconds(), c.lastMessage, nullMillis(c.lastBeat))
	if err != nil {
		return fmt.Errorf("save check %s: %w", c.id, err)
	}
	return nil
}

// serviceRow is the persisted state of one service.
type serviceRow struct {
	service string
	health  Health
	since   time.Time
	labels  map[string]string
	at      time.Time
}

const upsertServiceSQL = `INSERT INTO monitor_services (service, health, since_ms, labels, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (service) DO UPDATE SET health = excluded.health, since_ms = excluded.since_ms,
    labels = excluded.labels, updated_at = excluded.updated_at`

func (s serviceRow) exec(ctx context.Context, st *stmts) error {
	labels, err := json.Marshal(nonNilLabels(s.labels))
	if err != nil {
		return fmt.Errorf("encode labels: %w", err)
	}
	if err := st.exec(ctx, upsertServiceSQL, s.service, string(s.health), s.since.UnixMilli(), string(labels), s.at.UnixMilli()); err != nil {
		return fmt.Errorf("save service %s: %w", s.service, err)
	}
	return nil
}

// deleteCheckRec forgets a removed check's current state. Its history
// stays until retention removes it.
type deleteCheckRec struct{ id string }

func (d deleteCheckRec) exec(ctx context.Context, st *stmts) error {
	return st.exec(ctx, `DELETE FROM monitor_checks WHERE check_id = ?`, d.id)
}

type deleteServiceRec struct{ service string }

func (d deleteServiceRec) exec(ctx context.Context, st *stmts) error {
	return st.exec(ctx, `DELETE FROM monitor_services WHERE service = ?`, d.service)
}

// loadState reads the persisted check and service state.
func loadState(ctx context.Context, db *sql.DB) (map[string]checkRow, map[string]serviceRow, error) {
	checks := map[string]checkRow{}
	rows, err := db.QueryContext(ctx, `SELECT check_id, service, name, type, critical, interval_ms, status, since_ms,
        consecutive_ok, consecutive_fail, last_at, last_ok, last_latency_us, last_message, last_beat_at FROM monitor_checks`)
	if err != nil {
		return nil, nil, fmt.Errorf("monitor: load checks: %w", err)
	}
	for rows.Next() {
		var c checkRow
		var critical, lastOK sql.NullInt64
		var intervalMs, sinceMs int64
		var status string
		var lastAt, latencyUs, beat sql.NullInt64
		if err := rows.Scan(&c.id, &c.service, &c.name, &c.typ, &critical, &intervalMs, &status, &sinceMs,
			&c.okRun, &c.failRun, &lastAt, &lastOK, &latencyUs, &c.lastMessage, &beat); err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("monitor: load checks: %w", err)
		}
		c.critical = critical.Int64 != 0
		c.interval = time.Duration(intervalMs) * time.Millisecond
		c.status = Status(status)
		c.since = fromMillis(sinceMs)
		c.lastAt = fromNullMillis(lastAt)
		c.lastOK = lastOK.Int64 != 0
		c.lastLatency = time.Duration(latencyUs.Int64) * time.Microsecond
		c.lastBeat = fromNullMillis(beat)
		checks[c.id] = c
	}
	if err := closeRows(rows); err != nil {
		return nil, nil, fmt.Errorf("monitor: load checks: %w", err)
	}

	services := map[string]serviceRow{}
	rows, err = db.QueryContext(ctx, `SELECT service, health, since_ms, labels, updated_at FROM monitor_services`)
	if err != nil {
		return nil, nil, fmt.Errorf("monitor: load services: %w", err)
	}
	for rows.Next() {
		var s serviceRow
		var health, labels string
		var sinceMs, updated int64
		if err := rows.Scan(&s.service, &health, &sinceMs, &labels, &updated); err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("monitor: load services: %w", err)
		}
		s.health = Health(health)
		s.since = fromMillis(sinceMs)
		s.at = fromMillis(updated)
		if err := json.Unmarshal([]byte(labels), &s.labels); err != nil {
			s.labels = nil
		}
		services[s.service] = s
	}
	if err := closeRows(rows); err != nil {
		return nil, nil, fmt.Errorf("monitor: load services: %w", err)
	}
	return checks, services, nil
}

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullMillis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func fromNullMillis(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return fromMillis(n.Int64)
}

func nonNilLabels(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
