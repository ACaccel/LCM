package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
)

// Event is a stored observation or control event of an observed resource
// (machine, router) in resource_events. It is persisted before being
// announced to best-effort subscribers.
type Event struct {
	ID          int64     `json:"id"`
	ResourceID  string    `json:"resourceId"`
	Source      string    `json:"source"`
	Key         string    `json:"-"`
	Kind        string    `json:"kind"`
	Severity    string    `json:"severity"`
	OccurredAt  time.Time `json:"occurredAt"`
	IngestedAt  time.Time `json:"ingestedAt"`
	BootID      string    `json:"bootId,omitempty"`
	Unit        string    `json:"unit,omitempty"`
	Historical  bool      `json:"historical"`
	Reason      string    `json:"reason,omitempty"`
	OperationID string    `json:"operationId,omitempty"`
}

// InsertEvent stores e unless an event with the same resource, source and
// key exists, in which case it sets e.ID to 0. It sets e.ID of a new event
// and trims the resource's events after control events.
func InsertEvent(ctx context.Context, tx *sql.Tx, e *Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO resource_events(resource_id,source,source_key,occurred_at,ingested_at,kind,body) VALUES(?,?,?,?,?,?,?)`,
		e.ResourceID, e.Source, e.Key, e.OccurredAt.UnixMilli(), e.IngestedAt.UnixMilli(), e.Kind, string(body))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		e.ID = 0
		return nil
	}
	e.ID, err = res.LastInsertId()
	if err == nil && e.Source == "control" {
		err = TrimEvents(ctx, tx, e.ResourceID)
	}
	return err
}

// TrimEvents keeps the newest events of one resource up to the per-resource
// ceiling, so that a burst of one resource never evicts the events of
// another. It runs in the writer's transaction after every ingestion, so it
// first checks cheaply whether the ceiling can have been reached and
// removes at most one batch; PruneObservations enforces the rest.
func TrimEvents(ctx context.Context, tx *sql.Tx, resourceID string) error {
	var lo, hi sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT MIN(id) FROM resource_events WHERE resource_id=?),(SELECT MAX(id) FROM resource_events WHERE resource_id=?)`,
		resourceID, resourceID).Scan(&lo, &hi); err != nil {
		return err
	}
	// IDs are unique, so IDs spanning fewer than maxEvents values belong to
	// fewer than maxEvents events.
	if !hi.Valid || hi.Int64-lo.Int64 < maxEvents {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM resource_events WHERE id IN (SELECT id FROM resource_events WHERE resource_id=?
		AND id < (SELECT id FROM resource_events WHERE resource_id=? ORDER BY id DESC LIMIT 1 OFFSET ?) ORDER BY id LIMIT ?)`,
		resourceID, resourceID, maxEvents-1, pruneBatch)
	return err
}

// Actor is the wire form of the principal behind an operation.
type Actor struct {
	Kind auth.Kind `json:"kind"`
	ID   string    `json:"id"`
	Name string    `json:"name"`
}

// ActorOf converts an authenticated actor to its wire form.
func ActorOf(a auth.Actor) Actor { return Actor{Kind: a.Kind, ID: a.ID, Name: a.Name} }
