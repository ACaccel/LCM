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

// maxEvents bounds resource_events independently of time-based retention.
const maxEvents = 200000

// InsertEvent stores e unless an event with the same resource, source and
// key exists, in which case it sets e.ID to 0. It sets e.ID of a new event
// and trims the table after control events.
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
		err = TrimEvents(ctx, tx)
	}
	return err
}

// TrimEvents keeps the newest maxEvents events.
func TrimEvents(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM resource_events WHERE id <= (SELECT MAX(id)-? FROM resource_events)`, maxEvents)
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
