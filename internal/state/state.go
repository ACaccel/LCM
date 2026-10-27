// Package state is the durable per-policy key-value store behind the
// state.get and state.set nodes and the state expression variable. Values
// are JSON, each write gets a version that is unique across the whole
// store, and compare-and-set can match on either the version or the value.
package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/store"
)

// Limits.
const (
	// MaxValueBytes caps the JSON size of one value.
	MaxValueBytes = 64 << 10
	// MaxKeys caps the number of keys per policy.
	MaxKeys = 1000
)

// Errors.
var (
	ErrInvalidKey = errors.New("state: invalid key")
	ErrTooLarge   = errors.New("state: value too large")
	ErrTooMany    = errors.New("state: too many keys")
)

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,127}$`)

// ValidKey reports whether key can be used as a state key.
func ValidKey(key string) bool { return keyRe.MatchString(key) }

// Entry is one stored value. Version 0 means the key does not exist.
type Entry struct {
	Key       string
	Value     any
	Version   int64
	UpdatedAt time.Time
}

// Found reports whether the entry exists.
func (e Entry) Found() bool { return e.Version != 0 }

// Condition restricts a write. A nil Version and no Value impose no
// condition. Version 0 requires the key to be absent.
type Condition struct {
	Version  *int64
	HasValue bool
	Value    any
}

// Store reads and writes the policy_state table.
type Store struct {
	db  *store.DB
	now func() time.Time
}

// Option customizes a Store.
type Option func(*Store)

// WithClock replaces the time source, for tests.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// New returns a Store backed by db.
func New(db *store.DB, opts ...Option) *Store {
	s := &Store{db: db, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Get returns the entry for key; the entry is not Found when absent.
func (s *Store) Get(ctx context.Context, policy, key string) (Entry, error) {
	return getEntry(ctx, s.db.Reader(), policy, key)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getEntry(ctx context.Context, q querier, policy, key string) (Entry, error) {
	var (
		raw     string
		version int64
		updated int64
	)
	err := q.QueryRowContext(ctx,
		`SELECT value, version, updated_at FROM policy_state WHERE policy = ? AND key = ?`, policy, key).
		Scan(&raw, &version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{Key: key}, nil
	}
	if err != nil {
		return Entry{}, fmt.Errorf("state: get %q: %w", key, err)
	}
	v, err := model.DecodeJSONValue([]byte(raw))
	if err != nil {
		return Entry{}, fmt.Errorf("state: decode %q: %w", key, err)
	}
	return Entry{Key: key, Value: v, Version: version, UpdatedAt: time.UnixMilli(updated).UTC()}, nil
}

// List returns every entry of a policy ordered by key.
func (s *Store) List(ctx context.Context, policy string) ([]Entry, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT key, value, version, updated_at FROM policy_state WHERE policy = ? ORDER BY key`, policy)
	if err != nil {
		return nil, fmt.Errorf("state: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var (
			e       Entry
			raw     string
			updated int64
		)
		if err := rows.Scan(&e.Key, &raw, &e.Version, &updated); err != nil {
			return nil, fmt.Errorf("state: list: %w", err)
		}
		if e.Value, err = model.DecodeJSONValue([]byte(raw)); err != nil {
			return nil, fmt.Errorf("state: decode %q: %w", e.Key, err)
		}
		e.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: list: %w", err)
	}
	return out, nil
}

// All returns the values of a policy as a map, for expressions.
func (s *Store) All(ctx context.Context, policy string) (map[string]any, error) {
	entries, err := s.List(ctx, policy)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(entries))
	for _, e := range entries {
		out[e.Key] = e.Value
	}
	return out, nil
}

// Set stores value unconditionally and returns the new entry.
func (s *Store) Set(ctx context.Context, policy, key string, value any) (Entry, error) {
	e, _, err := s.CompareAndSet(ctx, policy, key, value, Condition{})
	return e, err
}

// CompareAndSet stores value when cond holds. It returns the new entry and
// true on success; otherwise the current entry (not Found when absent) and
// false.
func (s *Store) CompareAndSet(ctx context.Context, policy, key string, value any, cond Condition) (Entry, bool, error) {
	if !ValidKey(key) {
		return Entry{}, false, fmt.Errorf("%w %q: use letters, digits and _ . : - (at most 128)", ErrInvalidKey, key)
	}
	norm, raw, err := model.NormalizeJSON(value)
	if err != nil {
		return Entry{}, false, fmt.Errorf("state: %w", err)
	}
	if len(raw) > MaxValueBytes {
		return Entry{}, false, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(raw), MaxValueBytes)
	}
	var wantRaw []byte
	if cond.HasValue {
		if _, wantRaw, err = model.NormalizeJSON(cond.Value); err != nil {
			return Entry{}, false, fmt.Errorf("state: expected value: %w", err)
		}
	}

	var (
		result Entry
		done   bool
	)
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		result, done = Entry{}, false
		cur, err := getEntry(ctx, tx, policy, key)
		if err != nil {
			return err
		}
		if !conditionHolds(cur, cond, wantRaw) {
			result = cur
			return nil
		}
		if !cur.Found() {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM policy_state WHERE policy = ?`, policy).Scan(&n); err != nil {
				return fmt.Errorf("state: count keys: %w", err)
			}
			if n >= MaxKeys {
				return fmt.Errorf("%w: policy %q already has %d", ErrTooMany, policy, n)
			}
		}
		var version int64
		if err := tx.QueryRowContext(ctx, `UPDATE state_seq SET n = n + 1 WHERE id = 1 RETURNING n`).Scan(&version); err != nil {
			return fmt.Errorf("state: next version: %w", err)
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO policy_state (policy, key, value, version, updated_at) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (policy, key) DO UPDATE SET value = excluded.value, version = excluded.version, updated_at = excluded.updated_at`,
			policy, key, string(raw), version, now.UnixMilli()); err != nil {
			return fmt.Errorf("state: set %q: %w", key, err)
		}
		result, done = Entry{Key: key, Value: norm, Version: version, UpdatedAt: now.UTC().Truncate(time.Millisecond)}, true
		return nil
	})
	if err != nil {
		return Entry{}, false, err
	}
	return result, done, nil
}

func conditionHolds(cur Entry, cond Condition, wantRaw []byte) bool {
	if cond.Version != nil && cur.Version != *cond.Version {
		return false
	}
	if cond.HasValue {
		if !cur.Found() {
			return false
		}
		_, curRaw, err := model.NormalizeJSON(cur.Value)
		if err != nil || !bytes.Equal(curRaw, wantRaw) {
			return false
		}
	}
	return true
}

// Delete removes a key and reports whether it existed.
func (s *Store) Delete(ctx context.Context, policy, key string) (bool, error) {
	var n int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM policy_state WHERE policy = ? AND key = ?`, policy, key)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("state: delete %q: %w", key, err)
	}
	return n > 0, nil
}

// DeletePolicy removes every key of a policy and returns how many.
func (s *Store) DeletePolicy(ctx context.Context, policy string) (int64, error) {
	var n int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM policy_state WHERE policy = ?`, policy)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("state: delete policy %q: %w", policy, err)
	}
	return n, nil
}
