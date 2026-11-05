package resource

import (
	"strings"
	"time"
)

// HistoryQuery selects the stored snapshots of an observed resource: raw
// samples or one-minute points, optionally limited to a time range and
// thinned to a step.
//
// Without From, To and Step (an unranged query) the result is oldest first
// and Cursor is the ID after which to continue. With any of them the result
// is newest first and Cursor is the ID before which to continue, so the
// recent end of a range arrives first and a client that stops early lacks
// the oldest part, not the newest. Both orders follow the IDs, which grow
// with observation time.
type HistoryQuery struct {
	// Minute selects one-minute points instead of raw samples.
	Minute bool
	// From is inclusive and To exclusive, compared with the observation time
	// (the minute of a point); zero is unbounded.
	From, To time.Time
	// Step keeps only the newest snapshot of each step-long interval aligned
	// to the Unix epoch; zero keeps all. It lets a long range fit in a page.
	Step time.Duration
	// Cursor is the ID of the last snapshot of the previous page; zero starts.
	Cursor int64
	// Limit is the number of snapshots to return.
	Limit int
}

// Ranged reports whether the query reads a time range, which selects the
// newest-first order.
func (q HistoryQuery) Ranged() bool {
	return !q.From.IsZero() || !q.To.IsZero() || q.Step > 0
}

// SQL returns the statement and arguments that read the query's snapshots
// of one resource. It selects id, body and, for minute points, the sum and
// count of valid CPU samples (zero for raw samples). Both observation
// tables are read through their (resource, time, id) indexes.
func (q HistoryQuery) SQL(resourceID string) (string, []any) {
	table, at, columns := "resource_snapshots", "observed_at", "id,body,0,0"
	if q.Minute {
		table, at, columns = "resource_points", "bucket_at", "id,body,cpu_sum,cpu_count"
	}
	var window strings.Builder
	var args []any
	if !q.From.IsZero() {
		window.WriteString(" AND " + at + ">=?")
		args = append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		window.WriteString(" AND " + at + "<?")
		args = append(args, q.To.UnixMilli())
	}

	var sb strings.Builder
	sb.WriteString("SELECT " + columns + " FROM " + table + " WHERE resource_id=?")
	all := []any{resourceID}
	if q.Step > 0 {
		sb.WriteString(" AND id IN (SELECT MAX(id) FROM " + table + " WHERE resource_id=?" + window.String() + " GROUP BY " + at + "/?)")
		all = append(all, resourceID)
		all = append(all, args...)
		all = append(all, max(q.Step.Milliseconds(), 1))
	} else {
		sb.WriteString(window.String())
		all = append(all, args...)
	}
	if q.Ranged() {
		if q.Cursor > 0 {
			sb.WriteString(" AND id<?")
			all = append(all, q.Cursor)
		}
		sb.WriteString(" ORDER BY id DESC")
	} else {
		sb.WriteString(" AND id>? ORDER BY id")
		all = append(all, q.Cursor)
	}
	sb.WriteString(" LIMIT ?")
	all = append(all, q.Limit)
	return sb.String(), all
}
