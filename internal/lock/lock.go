// Package lock provides durable named mutual-exclusion locks. A lock has an
// owner and an expiry: if its holder dies, the lock frees itself when the
// ttl passes, and the next acquirer simply takes over the row. Holders are
// identified by owner strings that must be unique per holder (for example a
// run id), because acquiring a lock you already own succeeds and extends it,
// which keeps retried and resumed callers idempotent.
package lock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/store"
)

// Limits.
const (
	MaxTTL = 7 * 24 * time.Hour
	// DefaultPollInterval is how often Acquire retries a busy lock.
	DefaultPollInterval = 250 * time.Millisecond
)

// Errors.
var (
	ErrInvalid = errors.New("lock: invalid argument")
	ErrNotHeld = errors.New("lock: not held")
	// ErrHeld is matched (errors.Is) by every error that reports a lock
	// held by someone else, so callers can map it to their own busy error.
	ErrHeld = errors.New("lock: held by another owner")
)

// BusyError is returned when a lock is held by someone else.
type BusyError struct{ Holder Info }

// Is makes errors.Is(err, ErrHeld) true for a BusyError.
func (e *BusyError) Is(target error) bool { return target == ErrHeld }

// Error implements error.
func (e *BusyError) Error() string {
	return fmt.Sprintf("lock %q is held by %q until %s", e.Holder.Name, e.Holder.Owner, e.Holder.ExpiresAt.Format(time.RFC3339))
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)

// ValidName reports whether name can be used as a lock name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Info describes a held lock.
type Info struct {
	Name       string
	Owner      string
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

// Manager reads and writes the locks table.
type Manager struct {
	db    *store.DB
	clock clock.Clock
	audit *audit.Log
	poll  time.Duration
}

// Option customizes a Manager.
type Option func(*Manager)

// WithClock replaces the clock, for tests.
func WithClock(c clock.Clock) Option { return func(m *Manager) { m.clock = c } }

// WithAudit sets the audit log used by ForceRelease; without it ForceRelease
// is refused, because it must never go unrecorded.
func WithAudit(l *audit.Log) Option { return func(m *Manager) { m.audit = l } }

// WithPollInterval sets how often Acquire retries a busy lock.
func WithPollInterval(d time.Duration) Option { return func(m *Manager) { m.poll = d } }

// New returns a Manager backed by db.
func New(db *store.DB, opts ...Option) *Manager {
	m := &Manager{db: db, clock: clock.Real(), poll: DefaultPollInterval}
	for _, o := range opts {
		o(m)
	}
	return m
}

func check(name, owner string, ttl time.Duration) error {
	switch {
	case !ValidName(name):
		return fmt.Errorf("%w: name %q: use letters, digits and . _ : @ / - (at most 128)", ErrInvalid, name)
	case owner == "" || len(owner) > 256:
		return fmt.Errorf("%w: owner must be 1 to 256 bytes", ErrInvalid)
	case ttl <= 0 || ttl > MaxTTL:
		return fmt.Errorf("%w: ttl %s must be between 0 and %s", ErrInvalid, ttl, MaxTTL)
	}
	return nil
}

// TryAcquire takes the lock for owner for ttl, or extends it when owner
// already holds it. It returns a *BusyError when someone else holds it.
func (m *Manager) TryAcquire(ctx context.Context, name, owner string, ttl time.Duration) (Info, error) {
	if err := check(name, owner, ttl); err != nil {
		return Info{}, err
	}
	now := m.clock.Now()
	info := Info{Name: name, Owner: owner, AcquiredAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC()}
	var taken bool
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		// A single upsert makes the check and the take atomic: the update
		// only applies to a free (expired) row or one the owner already has.
		// acquired_at is kept when the owner merely extends its own lock.
		res, err := tx.ExecContext(ctx,
			`INSERT INTO locks (name, owner, acquired_at, expires_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT (name) DO UPDATE SET
			     acquired_at = CASE WHEN locks.owner = excluded.owner AND locks.expires_at > excluded.acquired_at
			                        THEN locks.acquired_at ELSE excluded.acquired_at END,
			     owner = excluded.owner,
			     expires_at = excluded.expires_at
			 WHERE locks.owner = excluded.owner OR locks.expires_at <= excluded.acquired_at`,
			name, owner, now.UnixMilli(), now.Add(ttl).UnixMilli())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		taken = n > 0
		return err
	})
	if err != nil {
		return Info{}, fmt.Errorf("lock: acquire %q: %w", name, err)
	}
	if !taken {
		holder, found, err := m.Holder(ctx, name)
		if err != nil {
			return Info{}, err
		}
		if !found {
			// Released between our attempt and the read; let the caller retry.
			return Info{}, &BusyError{Holder: Info{Name: name}}
		}
		return Info{}, &BusyError{Holder: holder}
	}
	if cur, found, err := m.Holder(ctx, name); err == nil && found && cur.Owner == owner {
		info.AcquiredAt = cur.AcquiredAt
	}
	return info, nil
}

// Acquire takes the lock without waiting and returns a release function.
// When someone else holds it, the error matches ErrHeld (and is a
// *BusyError). The release function is idempotent and does nothing if the
// lock has since expired and been taken by someone else. Its signature
// matches the Locker interface of lifecycle actions.
func (m *Manager) Acquire(ctx context.Context, name, owner string, ttl time.Duration) (func(context.Context) error, error) {
	if _, err := m.TryAcquire(ctx, name, owner, ttl); err != nil {
		return nil, err
	}
	return m.releaser(name, owner), nil
}

// AcquireWait is Acquire that waits until the lock is free, polling every
// poll interval; bound the wait with a context deadline.
func (m *Manager) AcquireWait(ctx context.Context, name, owner string, ttl time.Duration) (func(context.Context) error, error) {
	for {
		release, err := m.Acquire(ctx, name, owner, ttl)
		if err == nil {
			return release, nil
		}
		var busy *BusyError
		if !errors.As(err, &busy) {
			return nil, err
		}
		if err := m.sleep(ctx, m.poll); err != nil {
			return nil, fmt.Errorf("lock: waiting for %q held by %q: %w", name, busy.Holder.Owner, err)
		}
	}
}

func (m *Manager) releaser(name, owner string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := m.Release(ctx, name, owner)
		return err
	}
}

func (m *Manager) sleep(ctx context.Context, d time.Duration) error {
	done := make(chan struct{})
	t := m.clock.AfterFunc(d, func() { close(done) })
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees the lock if owner holds it and reports whether it did.
func (m *Manager) Release(ctx context.Context, name, owner string) (bool, error) {
	var n int64
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE name = ? AND owner = ?`, name, owner)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("lock: release %q: %w", name, err)
	}
	return n > 0, nil
}

// ReleaseOwner frees every lock held by owner and returns their names.
func (m *Manager) ReleaseOwner(ctx context.Context, owner string) ([]string, error) {
	var names []string
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		names = nil
		rows, err := tx.QueryContext(ctx, `DELETE FROM locks WHERE owner = ? RETURNING name`, owner)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			names = append(names, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("lock: release owner %q: %w", owner, err)
	}
	return names, nil
}

// ForceRelease frees a lock regardless of its owner, for operators dealing
// with a stuck holder. The action is audited with the actor in ctx; it
// fails when there is no audit log or no actor.
func (m *Manager) ForceRelease(ctx context.Context, name string) error {
	if m.audit == nil {
		return errors.New("lock: force release requires an audit log")
	}
	if _, ok := auth.ActorFrom(ctx); !ok {
		return audit.ErrNoActor
	}
	holder, found, err := m.Holder(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrNotHeld, name)
	}
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE name = ?`, name)
		return err
	})
	outcome := audit.OutcomeOK
	if err != nil {
		outcome = audit.OutcomeError
	}
	details := map[string]any{"owner": holder.Owner, "expiresAt": holder.ExpiresAt.Format(time.RFC3339)}
	if aerr := m.audit.Record(ctx, "lock.force_release", "lock:"+name, outcome, details); aerr != nil {
		err = errors.Join(err, aerr)
	}
	if err != nil {
		return fmt.Errorf("lock: force release %q: %w", name, err)
	}
	return nil
}

// Holder returns the current, unexpired holder of name.
func (m *Manager) Holder(ctx context.Context, name string) (Info, bool, error) {
	var (
		i    Info
		a, e int64
	)
	err := m.db.Reader().QueryRowContext(ctx,
		`SELECT name, owner, acquired_at, expires_at FROM locks WHERE name = ? AND expires_at > ?`, name, m.clock.Now().UnixMilli()).
		Scan(&i.Name, &i.Owner, &a, &e)
	if errors.Is(err, sql.ErrNoRows) {
		return Info{}, false, nil
	}
	if err != nil {
		return Info{}, false, fmt.Errorf("lock: holder of %q: %w", name, err)
	}
	i.AcquiredAt, i.ExpiresAt = time.UnixMilli(a).UTC(), time.UnixMilli(e).UTC()
	return i, true, nil
}

// List returns the unexpired locks ordered by name.
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	rows, err := m.db.Reader().QueryContext(ctx,
		`SELECT name, owner, acquired_at, expires_at FROM locks WHERE expires_at > ? ORDER BY name`, m.clock.Now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("lock: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Info
	for rows.Next() {
		var (
			i    Info
			a, e int64
		)
		if err := rows.Scan(&i.Name, &i.Owner, &a, &e); err != nil {
			return nil, fmt.Errorf("lock: list: %w", err)
		}
		i.AcquiredAt, i.ExpiresAt = time.UnixMilli(a).UTC(), time.UnixMilli(e).UTC()
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lock: list: %w", err)
	}
	return out, nil
}

// Held returns the unexpired locks held by owners with the given prefix.
// Sweepers use it to find locks of runs that no longer exist.
func (m *Manager) Held(ctx context.Context, ownerPrefix string) ([]Info, error) {
	all, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, i := range all {
		if strings.HasPrefix(i.Owner, ownerPrefix) {
			out = append(out, i)
		}
	}
	return out, nil
}

// PurgeExpired deletes expired rows. It is housekeeping only: expired locks
// are already treated as free.
func (m *Manager) PurgeExpired(ctx context.Context) (int64, error) {
	var n int64
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE expires_at <= ?`, m.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("lock: purge: %w", err)
	}
	return n, nil
}
