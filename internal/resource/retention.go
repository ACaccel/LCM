package resource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Row ceilings of the observation tables, per resource, so that a burst of
// one resource (a journal flood on a machine) never shortens the history of
// another. They hold independently of the age-based retention. They are
// variables only so that tests can lower them.
var (
	maxSnapshots int64 = 200000
	maxPoints    int64 = 200000
	maxEvents    int64 = 200000
)

// pruneBatch bounds the rows one statement deletes, so that pruning never
// holds the single writer connection for long. A variable for tests.
var pruneBatch int64 = 5000

// ObservationRetention is how long the observations of observed resources
// (machines, routers) are kept.
type ObservationRetention struct {
	// Snapshots is the raw snapshot age; the latest snapshot of each
	// resource is kept regardless, as the baseline of its status.
	Snapshots time.Duration
	// Points is the minute point age.
	Points time.Duration
	// Events is the event age, compared with the time LCM stored the event.
	Events time.Duration
}

func (r ObservationRetention) validate() error {
	if r.Snapshots <= 0 || r.Points <= 0 || r.Events <= 0 {
		return errors.New("resource: observation retention must be positive")
	}
	return nil
}

// PruneObservations deletes expired snapshots, minute points and events and
// enforces the per-resource row ceilings, in bounded batches, each in its
// own write transaction. Operations are not touched: their receipts and
// completion evidence outlive the events that announced them. It returns
// how many rows it deleted.
func (s *Store) PruneObservations(ctx context.Context, r ObservationRetention) (int64, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	now := s.clk.Now()
	snapCutoff := now.Add(-r.Snapshots).UnixMilli()
	pointCutoff := now.Add(-r.Points).UnixMilli()
	eventCutoff := now.Add(-r.Events).UnixMilli()

	var total int64
	n, err := deleteBatches(ctx, s.db.Write, `DELETE FROM resource_events WHERE id IN (SELECT id FROM resource_events WHERE ingested_at<? LIMIT ?)`, eventCutoff)
	total += n
	if err != nil {
		return total, fmt.Errorf("resource: prune events: %w", err)
	}
	ids, err := s.observedIDs(ctx)
	if err != nil {
		return total, err
	}
	for _, id := range ids {
		n, err := s.pruneResource(ctx, id, snapCutoff, pointCutoff)
		total += n
		if err != nil {
			return total, fmt.Errorf("resource: prune observations of %s: %w", id, err)
		}
	}
	return total, nil
}

// observedIDs lists the resources that have any observation rows.
func (s *Store) observedIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT id FROM resources r WHERE
		EXISTS (SELECT 1 FROM resource_snapshots WHERE resource_id=r.id) OR
		EXISTS (SELECT 1 FROM resource_points WHERE resource_id=r.id) OR
		EXISTS (SELECT 1 FROM resource_events WHERE resource_id=r.id) ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("resource: list observed resources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) pruneResource(ctx context.Context, id string, snapCutoff, pointCutoff int64) (int64, error) {
	var baseline sql.NullInt64
	if err := s.db.Reader().QueryRowContext(ctx, `SELECT MAX(id) FROM resource_snapshots WHERE resource_id=?`, id).Scan(&baseline); err != nil {
		return 0, err
	}
	var total int64
	jobs := []struct {
		query string
		args  []any
	}{
		// Age, keeping the latest snapshot as the baseline.
		{`DELETE FROM resource_snapshots WHERE id IN (SELECT id FROM resource_snapshots WHERE resource_id=? AND observed_at<? AND id<>? LIMIT ?)`,
			[]any{id, snapCutoff, baseline.Int64}},
		{`DELETE FROM resource_points WHERE id IN (SELECT id FROM resource_points WHERE resource_id=? AND bucket_at<? LIMIT ?)`,
			[]any{id, pointCutoff}},
	}
	for _, job := range jobs {
		n, err := deleteBatches(ctx, s.db.Write, job.query, job.args...)
		total += n
		if err != nil {
			return total, err
		}
	}
	// Ceilings, oldest first; the snapshot baseline stays.
	caps := []struct {
		count, trim string
		args        []any
		limit       int64
	}{
		{`SELECT COUNT(*) FROM resource_snapshots WHERE resource_id=?`,
			`DELETE FROM resource_snapshots WHERE id IN (SELECT id FROM resource_snapshots WHERE resource_id=? AND id<>? ORDER BY observed_at, id LIMIT ?)`,
			[]any{id, baseline.Int64}, maxSnapshots},
		{`SELECT COUNT(*) FROM resource_points WHERE resource_id=?`,
			`DELETE FROM resource_points WHERE id IN (SELECT id FROM resource_points WHERE resource_id=? ORDER BY bucket_at, id LIMIT ?)`,
			[]any{id}, maxPoints},
		{`SELECT COUNT(*) FROM resource_events WHERE resource_id=?`,
			`DELETE FROM resource_events WHERE id IN (SELECT id FROM resource_events WHERE resource_id=? ORDER BY id LIMIT ?)`,
			[]any{id}, maxEvents},
	}
	for _, c := range caps {
		var count int64
		if err := s.db.Reader().QueryRowContext(ctx, c.count, id).Scan(&count); err != nil {
			return total, err
		}
		n, err := s.trimOldest(ctx, c.trim, c.args, count-c.limit)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// trimOldest runs trim, whose last placeholder is the batch size, until it
// has deleted excess rows.
func (s *Store) trimOldest(ctx context.Context, trim string, args []any, excess int64) (int64, error) {
	var total int64
	for excess > 0 {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		batch := min(excess, pruneBatch)
		var n int64
		err := s.db.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, trim, append(slices.Clone(args), batch)...)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		total += n
		if err != nil || n < batch {
			return total, err
		}
		excess -= n
	}
	return total, nil
}

// deleteBatches runs a DELETE whose last placeholder is the batch size
// until it deletes fewer rows than a batch, each run in its own write
// transaction.
func deleteBatches(ctx context.Context, write func(context.Context, func(*sql.Tx) error) error, query string, args ...any) (int64, error) {
	args = append(args, pruneBatch)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		err := write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, query, args...)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		total += n
		if err != nil || n < pruneBatch {
			return total, err
		}
	}
}
