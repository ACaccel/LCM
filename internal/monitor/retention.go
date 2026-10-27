package monitor

import (
	"context"
	"database/sql"
	"fmt"
)

// defaultPruneChunk bounds the rows deleted per statement so that pruning never
// holds the single writer for long.
const defaultPruneChunk = 5000

// pruneTick is the timer callback of the prune job.
func (m *Monitor) pruneTick() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.pruneTimer = m.clock.AfterFunc(m.pruneInterval, m.pruneTick)
	m.beginLocked()
	m.mu.Unlock()
	go func() {
		defer m.end()
		if err := m.Prune(m.ctx); err != nil && m.ctx.Err() == nil {
			m.log.Error("monitor prune failed", "error", err)
		}
	}()
}

// Prune deletes raw results older than the raw retention and rollups and
// resolved alerts older than the rollup retention. It runs periodically on
// its own; calling it directly is safe.
func (m *Monitor) Prune(ctx context.Context) error {
	now := m.clock.Now()
	rawCutoff := now.Add(-m.rawRetention).UnixMilli()
	// A rollup bucket is identified by its start; keep a bucket until it
	// lies wholly outside the window.
	rollupCutoff := now.Add(-m.rollupRetention).UnixMilli()
	for _, job := range []struct {
		name, query string
		cutoff      int64
	}{
		{"results", `DELETE FROM monitor_results WHERE id IN (SELECT id FROM monitor_results WHERE at < ? LIMIT ?)`, rawCutoff},
		{"rollups", `DELETE FROM monitor_rollups WHERE (check_id, bucket_ms) IN (SELECT check_id, bucket_ms FROM monitor_rollups WHERE bucket_ms < ? LIMIT ?)`, rollupCutoff},
		{"alerts", `DELETE FROM monitor_alerts WHERE (source, fingerprint) IN (SELECT source, fingerprint FROM monitor_alerts WHERE resolved_at IS NOT NULL AND resolved_at < ? LIMIT ?)`, rollupCutoff},
	} {
		for {
			n, err := m.deleteChunk(ctx, job.query, job.cutoff)
			if err != nil {
				return fmt.Errorf("prune %s: %w", job.name, err)
			}
			if n < int64(m.pruneChunk) {
				break
			}
		}
	}
	return nil
}

func (m *Monitor) deleteChunk(ctx context.Context, query string, cutoff int64) (int64, error) {
	var n int64
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, cutoff, m.pruneChunk)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
