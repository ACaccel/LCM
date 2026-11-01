// Package maintenance coordinates local service writes with durable machine maintenance.
package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/ACaccel/lcm/internal/store"
)

// ErrActive rejects writes while maintenance is active or another write is in flight.
var ErrActive = errors.New("maintenance: local machine maintenance or a conflicting write is active")

type leaseKey struct{}
type lease struct {
	gate   *Gate
	active bool
	mu     sync.Mutex
}

// Gate holds read leases for the complete lifetime of service writes.
// The database row survives restarts; the mutex closes the check/execute race.
type Gate struct {
	db *store.DB
	mu sync.RWMutex
}

func New(db *store.DB) *Gate { return &Gate{db: db} }

// Acquire permits a write and returns a context for nested component calls.
// The returned release must run after the external side effect has finished.
func (g *Gate) Acquire(ctx context.Context) (context.Context, func(), error) {
	if g == nil {
		return ctx, func() {}, nil
	}
	if l, ok := ctx.Value(leaseKey{}).(*lease); ok && l.gate == g {
		l.mu.Lock()
		active := l.active
		l.mu.Unlock()
		if active {
			return ctx, func() {}, nil
		}
	}
	if !g.mu.TryRLock() {
		return ctx, nil, ErrActive
	}
	var exists int
	err := g.db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance WHERE host='local'`).Scan(&exists)
	if err != nil || exists != 0 {
		g.mu.RUnlock()
		if err != nil {
			return ctx, nil, err
		}
		return ctx, nil, ErrActive
	}
	l := &lease{gate: g, active: true}
	var once sync.Once
	release := func() { once.Do(func() { l.mu.Lock(); l.active = false; l.mu.Unlock(); g.mu.RUnlock() }) }
	return context.WithValue(ctx, leaseKey{}, l), release, nil
}

// Exclusive performs the maintenance transition only when all writers have left.
// It never waits for a deployment or backup while holding other machine locks.
func (g *Gate) Exclusive(ctx context.Context, fn func(*sql.Tx) error) error {
	if !g.mu.TryLock() {
		return ErrActive
	}
	defer g.mu.Unlock()
	return g.db.Write(ctx, fn)
}
