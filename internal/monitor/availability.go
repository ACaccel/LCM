package monitor

import (
	"context"
	"fmt"
	"time"
)

// Availability is how often the checks of one resource passed in each of a
// run of equal buckets, oldest first.
type Availability struct {
	Buckets []AvailabilityBucket
	// Count and Failures add up the buckets.
	Count, Failures int64
}

// AvailabilityBucket counts the results of all checks of a resource in one
// bucket; a bucket without results has Count 0.
type AvailabilityBucket struct {
	Count, Failures int64
}

// AvailabilityAll returns the availability of every resource that has check
// results, in n buckets of width step that end at the current rollup minute.
// It answers from the one-minute rollups with a single query, so a page
// that shows many resources does not read one series per check. step must
// be a whole number of minutes.
func (m *Monitor) AvailabilityAll(ctx context.Context, n int, step time.Duration) (map[string]Availability, error) {
	if n <= 0 || step < rollupBucket || step%rollupBucket != 0 {
		return nil, fmt.Errorf("monitor: availability: %d buckets of %v", n, step)
	}
	to := bucketStart(m.clock.Now()) + rollupBucket.Milliseconds()
	from := to - int64(n)*step.Milliseconds()
	rows, err := m.db.Reader().QueryContext(ctx,
		`SELECT c.resource, (r.bucket_ms - ?1) / ?2 AS idx, SUM(r.count), SUM(r.failures)
         FROM monitor_rollups r JOIN monitor_checks c ON c.check_id = r.check_id
         WHERE r.bucket_ms >= ?1 AND r.bucket_ms < ?3
         GROUP BY c.resource, idx`,
		from, step.Milliseconds(), to)
	if err != nil {
		return nil, fmt.Errorf("monitor: availability: %w", err)
	}
	out := map[string]Availability{}
	for rows.Next() {
		var (
			name                     string
			idx, count, failureCount int64
		)
		if err := rows.Scan(&name, &idx, &count, &failureCount); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("monitor: availability: %w", err)
		}
		if idx < 0 || idx >= int64(n) {
			continue
		}
		a, ok := out[name]
		if !ok {
			a.Buckets = make([]AvailabilityBucket, n)
		}
		a.Buckets[idx] = AvailabilityBucket{Count: count, Failures: failureCount}
		a.Count += count
		a.Failures += failureCount
		out[name] = a
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("monitor: availability: %w", err)
	}
	return out, nil
}
