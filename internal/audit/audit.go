// Package audit records who did what, and when. Every state-changing action
// is written with the actor taken from the request context.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/store"
)

// Outcome is the result of an audited action.
type Outcome string

// Outcomes.
const (
	OutcomeOK     Outcome = "ok"
	OutcomeDenied Outcome = "denied"
	OutcomeError  Outcome = "error"
)

const (
	// DefaultLimit and MaxLimit bound List page sizes.
	DefaultLimit = 100
	MaxLimit     = 1000
)

// ErrNoActor is returned by Record when the context carries no actor.
var ErrNoActor = errors.New("audit: no actor in context")

// Entry is one audit log row.
type Entry struct {
	ID      int64
	Time    time.Time
	Actor   auth.Actor
	Action  string
	Target  string
	Details map[string]any
	Outcome Outcome
}

// Option customizes a Log.
type Option func(*Log)

// WithClock replaces the time source, for tests.
func WithClock(now func() time.Time) Option {
	return func(l *Log) { l.now = now }
}

// Log reads and writes the audit_log table.
type Log struct {
	db  *store.DB
	now func() time.Time
}

// New returns a Log backed by db.
func New(db *store.DB, opts ...Option) *Log {
	l := &Log{db: db, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Record appends an entry. The actor comes from ctx; details must never
// contain secrets.
func (l *Log) Record(ctx context.Context, action, target string, outcome Outcome, details map[string]any) error {
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return ErrNoActor
	}
	if !actor.Kind.Valid() {
		return fmt.Errorf("audit: invalid actor kind %q", actor.Kind)
	}
	if details == nil {
		details = map[string]any{}
	}
	blob, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("audit: encode details: %w", err)
	}
	err = l.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO audit_log (time, actor_kind, actor_id, actor_name, action, target, details, outcome)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			l.now().UnixMilli(), string(actor.Kind), actor.ID, actor.Name, action, target, string(blob), string(outcome))
		return err
	})
	if err != nil {
		return fmt.Errorf("audit: record %s: %w", action, err)
	}
	return nil
}

// List returns entries newest first. before, when positive, restricts the
// result to entries with a smaller ID, which makes it a stable cursor.
// A limit outside 1..MaxLimit is replaced by DefaultLimit or MaxLimit.
func (l *Log) List(ctx context.Context, limit int, before int64) ([]Entry, error) {
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	query := `SELECT id, time, actor_kind, actor_id, actor_name, action, target, details, outcome FROM audit_log`
	args := []any{}
	if before > 0 {
		query += ` WHERE id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := l.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := []Entry{}
	for rows.Next() {
		var (
			e       Entry
			ms      int64
			kind    string
			details string
			outcome string
		)
		if err := rows.Scan(&e.ID, &ms, &kind, &e.Actor.ID, &e.Actor.Name, &e.Action, &e.Target, &details, &outcome); err != nil {
			return nil, fmt.Errorf("audit: list: %w", err)
		}
		e.Time = time.UnixMilli(ms).UTC()
		e.Actor.Kind = auth.Kind(kind)
		e.Outcome = Outcome(outcome)
		if err := json.Unmarshal([]byte(details), &e.Details); err != nil {
			return nil, fmt.Errorf("audit: decode details of entry %d: %w", e.ID, err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	return entries, nil
}
