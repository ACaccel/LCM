package resource

import (
	"strings"
	"time"
)

// EventQuery selects the stored events of an observed resource.
//
// Oldest first (the default) follows the event IDs, which is ingestion
// order, and Cursor is the ID after which to continue. Newest first follows
// the time the event occurred, with the ID as tie-breaker, so that events
// recovered from the journal after a gap sit where they happened rather
// than where they were ingested; the cursor is the (CursorAt, Cursor) pair
// of the last event of the previous page.
type EventQuery struct {
	Newest bool
	// From is inclusive and To exclusive, compared with the time the event
	// occurred; zero is unbounded.
	From, To time.Time
	// Cursor is the ID, and CursorAt the occurrence time in Unix
	// milliseconds, of the last event of the previous page. CursorAt is only
	// read when Newest is set. Both zero starts.
	Cursor, CursorAt int64
	Limit            int
}

// SQL returns the statement and arguments that read the query's events of
// one resource: id and body.
func (q EventQuery) SQL(resourceID string) (string, []any) {
	var sb strings.Builder
	sb.WriteString("SELECT id,body FROM resource_events WHERE resource_id=?")
	args := []any{resourceID}
	if !q.From.IsZero() {
		sb.WriteString(" AND occurred_at>=?")
		args = append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		sb.WriteString(" AND occurred_at<?")
		args = append(args, q.To.UnixMilli())
	}
	switch {
	case !q.Newest:
		sb.WriteString(" AND id>? ORDER BY id")
		args = append(args, q.Cursor)
	case q.Cursor > 0:
		sb.WriteString(" AND (occurred_at<? OR (occurred_at=? AND id<?)) ORDER BY occurred_at DESC, id DESC")
		args = append(args, q.CursorAt, q.CursorAt, q.Cursor)
	default:
		sb.WriteString(" ORDER BY occurred_at DESC, id DESC")
	}
	sb.WriteString(" LIMIT ?")
	args = append(args, q.Limit)
	return sb.String(), args
}
