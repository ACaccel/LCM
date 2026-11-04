package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
)

// List returns the managed and retired machines ordered by name.
func (m *Manager) List(ctx context.Context) ([]Record, error) {
	recs, err := m.resources.List(ctx, resource.Filter{Type: model.ResourceMachine, IncludeRetired: true})
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(recs))
	for _, rec := range recs {
		r, err := m.view(ctx, rec)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Get returns a machine by the name of an active resource or by "@<id>",
// which also finds unmanaged archives.
func (m *Manager) Get(ctx context.Context, ref string) (Record, error) {
	rec, err := m.resources.Get(ctx, ref)
	if errors.Is(err, resource.ErrNotFound) {
		return Record{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	if err != nil {
		return Record{}, err
	}
	if rec.Type != model.ResourceMachine {
		return Record{}, fmt.Errorf("%w: %s is a %s resource", ErrNotFound, rec.Name, rec.Type)
	}
	return m.view(ctx, rec)
}

// view adds the stored status to a resource record.
func (m *Manager) view(ctx context.Context, rec resource.Record) (Record, error) {
	cfg, ok := rec.Document.Machine()
	if !ok {
		return Record{}, fmt.Errorf("machine: %s has no machine config", rec.Name)
	}
	r := Record{
		ID: rec.ID, Name: rec.Name, Version: rec.Version, Phase: rec.Phase,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, Document: rec.Document, Config: cfg,
	}
	var raw string
	err := m.db.Reader().QueryRowContext(ctx, `SELECT status FROM resource_status WHERE resource_id=?`, rec.ID).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		raw = "{}"
	case err != nil:
		return Record{}, err
	}
	var persisted storedStatus
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		return Record{}, err
	}
	r.Status = persisted.Status
	r.onlineSince = persisted.OnlineSince
	m.freshen(&r)
	if persisted.DocumentVersion != r.Version {
		r.Status.Fresh = false
		r.Status.Health = "unknown"
		r.Status.Reasons = []string{"configuration has not been observed"}
	}
	return r, nil
}

func (m *Manager) freshen(r *Record) {
	r.Status.Fresh = r.Status.Snapshot != nil && m.clk.Now().Sub(r.Status.Snapshot.ObservedAt) <= r.Config.StaleAfter.D() && !r.Status.Snapshot.ObservedAt.After(m.clk.Now())
	if r.Phase != resource.PhaseManaged {
		r.Status.Fresh = false
	}
	if !r.Status.Fresh {
		r.Status.Health = "unknown"
		r.Status.Reasons = []string{"observations missing or stale"}
	}
	if r.Status.Identity == "" {
		r.Status.Identity = "verification-required"
	}
	if r.Status.Journal.State == "" {
		r.Status.Journal = Coverage{State: "unknown", Reason: "not observed"}
	}
	if r.Status.Reasons == nil {
		r.Status.Reasons = []string{}
	}
}

// currentRow is the part of a machine's state that writes recheck inside
// their transaction.
type currentRow struct {
	version  int
	phase    string
	status   storedStatus
	identity string
}

// current reads the resource's version and phase with its stored status.
func current(ctx context.Context, tx *sql.Tx, id string) (currentRow, error) {
	var c currentRow
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT r.version, r.phase, COALESCE(s.status, '{}'), COALESCE(s.identity_hash, '')
		FROM resources r LEFT JOIN resource_status s ON s.resource_id = r.id WHERE r.id = ? AND r.type = ?`,
		id, model.ResourceMachine).Scan(&c.version, &c.phase, &raw, &c.identity)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal([]byte(raw), &c.status)
}

// saveStatus stores the status of a machine; the row normally exists from
// creation, but is recreated if it was lost.
func saveStatus(ctx context.Context, tx *sql.Tx, id string, status storedStatus, identity string, at time.Time) error {
	body, err := encode(status)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO resource_status (resource_id, identity_hash, status, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (resource_id) DO UPDATE SET identity_hash = excluded.identity_hash, status = excluded.status, updated_at = excluded.updated_at`,
		id, identity, body, at.UnixMilli())
	return err
}
