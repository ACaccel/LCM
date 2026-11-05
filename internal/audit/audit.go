// Package audit records who did what, and when. Every state-changing action
// is written with the actor taken from the request context.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

type detailsKey struct{}

// WithDetail returns a context whose audit entries all carry key=value in
// their details. It lets a caller attach a fact about the request, such as
// an acknowledged impact, to the entries a component records further down
// without changing every component's signature. Details passed to Record
// take precedence over context details with the same key.
func WithDetail(ctx context.Context, key string, value any) context.Context {
	prev, _ := ctx.Value(detailsKey{}).(map[string]any)
	next := make(map[string]any, len(prev)+1)
	for k, v := range prev {
		next[k] = v
	}
	next[key] = value
	return context.WithValue(ctx, detailsKey{}, next)
}

// Record appends an entry in its own transaction. Use it when nothing else
// is written; a component that changes state records the entry with
// RecordTx inside the transaction of the change. The actor comes from ctx;
// details must never contain secrets.
func (l *Log) Record(ctx context.Context, action, target string, outcome Outcome, details map[string]any) error {
	e, err := l.entry(ctx, details)
	if err != nil {
		return err
	}
	if err := l.db.Write(ctx, func(tx *sql.Tx) error { return e.insert(ctx, tx, action, target, outcome) }); err != nil {
		return fmt.Errorf("audit: record %s: %w", action, err)
	}
	return nil
}

// RecordTx appends an entry within tx, so that the entry and the change it
// describes commit or roll back together. An error must abort tx.
func (l *Log) RecordTx(ctx context.Context, tx *sql.Tx, action, target string, outcome Outcome, details map[string]any) error {
	e, err := l.entry(ctx, details)
	if err != nil {
		return err
	}
	if err := e.insert(ctx, tx, action, target, outcome); err != nil {
		return fmt.Errorf("audit: record %s: %w", action, err)
	}
	return nil
}

// RecordChange records the successful change committed by tx; it
// implements auth.Auditor, which cannot name Outcome.
func (l *Log) RecordChange(ctx context.Context, tx *sql.Tx, action, target string, details map[string]any) error {
	return l.RecordTx(ctx, tx, action, target, OutcomeOK, details)
}

// pending is an entry validated and encoded before it is written.
type pending struct {
	at      int64
	actor   auth.Actor
	details string
}

func (l *Log) entry(ctx context.Context, details map[string]any) (pending, error) {
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return pending{}, ErrNoActor
	}
	if !actor.Kind.Valid() {
		return pending{}, fmt.Errorf("audit: invalid actor kind %q", actor.Kind)
	}
	merged := map[string]any{}
	if extra, _ := ctx.Value(detailsKey{}).(map[string]any); extra != nil {
		for k, v := range extra {
			merged[k] = v
		}
	}
	for k, v := range details {
		merged[k] = v
	}
	blob, err := json.Marshal(merged)
	if err != nil {
		return pending{}, fmt.Errorf("audit: encode details: %w", err)
	}
	return pending{at: l.now().UnixMilli(), actor: actor, details: string(blob)}, nil
}

func (p pending) insert(ctx context.Context, tx *sql.Tx, action, target string, outcome Outcome) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (time, actor_kind, actor_id, actor_name, action, target, details, outcome)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.at, string(p.actor.Kind), p.actor.ID, p.actor.Name, action, target, p.details, string(outcome))
	return err
}

// Query selects audit entries. Zero fields do not filter.
type Query struct {
	// Limit is the page size; a value outside 1..MaxLimit is replaced by
	// DefaultLimit or MaxLimit.
	Limit int
	// Before, when positive, restricts the result to entries with a smaller
	// ID, which makes it a stable cursor.
	Before int64
	// From and To bound the entry time: From is inclusive, To exclusive.
	From, To time.Time
	// Actor matches the actor's name exactly.
	Actor string
	// Action matches an action exactly or as a dotted prefix, so "service"
	// selects "service.deploy" and "service.stop".
	Action string
	// Target matches the target exactly, such as a resource name.
	Target string
}

// List returns entries newest first. before, when positive, restricts the
// result to entries with a smaller ID, which makes it a stable cursor.
// A limit outside 1..MaxLimit is replaced by DefaultLimit or MaxLimit.
func (l *Log) List(ctx context.Context, limit int, before int64) ([]Entry, error) {
	return l.Search(ctx, Query{Limit: limit, Before: before})
}

// Search returns the entries matching q, newest first.
func (l *Log) Search(ctx context.Context, q Query) ([]Entry, error) {
	query, args := q.sql()
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

// sql returns the statement and arguments of the search.
func (q Query) sql() (string, []any) {
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	var (
		where []string
		args  []any
	)
	if q.Before > 0 {
		where = append(where, `id < ?`)
		args = append(args, q.Before)
	}
	if !q.From.IsZero() {
		where = append(where, `time >= ?`)
		args = append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		where = append(where, `time < ?`)
		args = append(args, q.To.UnixMilli())
	}
	if q.Actor != "" {
		where = append(where, `actor_name = ?`)
		args = append(args, q.Actor)
	}
	if q.Action != "" {
		// The range [action, action + "/") holds the action and every
		// "action." child ('/' follows '.' in byte order), so it can use
		// the action index; the exact test drops siblings such as
		// "action-x" that also fall in it.
		prefix := q.Action + "."
		where = append(where, `action >= ? AND action < ? AND (action = ? OR substr(action, 1, ?) = ?)`)
		args = append(args, q.Action, q.Action+"/", q.Action, utf8.RuneCountInString(prefix), prefix)
	}
	if q.Target != "" {
		where = append(where, `target = ?`)
		args = append(args, q.Target)
	}
	query := `SELECT id, time, actor_kind, actor_id, actor_name, action, target, details, outcome FROM audit_log`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	return query, append(args, limit)
}
