// Package catalog is the service resource type: the lifecycle state
// machine of service resources, deployment history and the per-resource
// timeline, plus the Manager that performs lifecycle actions through the
// runtime adapters.
//
// # Documents and phases
//
// Service documents, versions and management phases live in the resource
// store (internal/resource). The catalog takes part in saves and phase
// changes through the hooks returned by Manager.Hooks, and implements the
// service side of resource.Type through Manager.Type.
//
// # Lifecycle
//
// A service resource is in exactly one State. The transitions table in
// state.go is the only authority on allowed changes, and every change is
// written with its timeline entry in one transaction before a lifecycle
// signal is published. Health is not stored here; the monitor owns it.
// Retirement is a management phase, not a state: a retired service keeps
// its last state and accepts no actions until it is resumed.
//
// # Consistency
//
// Lifecycle actions hold a named lock (Locker) for their whole duration and
// are additionally tracked in memory, so the same action cannot overlap in
// one process even when the Locker is shared with other holders. After a
// crash, Reconcile compares the stored state with what the runtime reports
// and repairs states that cannot be true any more.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
)

// ErrNotFound is returned for a name that is not an active service resource.
var ErrNotFound = errors.New("catalog: not found")

// Config configures a Catalog.
type Config struct {
	Gate  *maintenance.Gate
	DB    *store.DB
	Audit *audit.Log
	// Resources holds the service documents and phases; required.
	Resources *resource.Store
	// Bus receives lifecycle signals; optional.
	Bus *signal.Bus
	// Clock defaults to the real clock.
	Clock clock.Clock
	Log   *slog.Logger
	// OnTimeline, when set, is called after a timeline entry was stored by
	// a state change, an action or AppendTimeline. Entries written together
	// with a state change have ID 0. Entries the resource hooks write
	// inside a document save or phase change are not reported: the store's
	// own change notification covers them. It runs on the caller's
	// goroutine and must not block or call back into the Catalog.
	OnTimeline func(Entry)
}

// Catalog stores the lifecycle state and history of service resources.
type Catalog struct {
	gate  *maintenance.Gate
	db    *store.DB
	audit *audit.Log
	res   *resource.Store
	bus   *signal.Bus
	clk   clock.Clock
	log   *slog.Logger
	onTL  func(Entry)
}

// New returns a Catalog.
func New(cfg Config) (*Catalog, error) {
	if cfg.DB == nil || cfg.Audit == nil || cfg.Resources == nil {
		return nil, errors.New("catalog: DB, Audit and Resources are required")
	}
	c := &Catalog{gate: cfg.Gate, db: cfg.DB, audit: cfg.Audit, res: cfg.Resources, bus: cfg.Bus, clk: cfg.Clock, log: cfg.Log, onTL: cfg.OnTimeline}
	if c.clk == nil {
		c.clk = clock.Real()
	}
	if c.log == nil {
		c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return c, nil
}

// Record is a service resource with its lifecycle state.
type Record struct {
	resource.Record
	State      State
	StateSince time.Time
	// Config is the service config of the current document.
	Config *model.ServiceConfig
}

// Runtime returns the runtime the adapters consume. A managed service is
// known to the deploy helper by its resource name.
func (r *Record) Runtime() model.Runtime {
	rt := r.Config.Runtime(r.Adapter)
	if rt.Type == model.RuntimeManaged {
		rt.Service = r.Name
	}
	return rt
}

var systemActor = auth.Actor{Kind: auth.KindSystem, ID: "lcm", Name: "lcm"}

// withActor returns ctx with an actor, defaulting to the system actor for
// internal work such as reconciliation.
func withActor(ctx context.Context) context.Context {
	if _, ok := auth.ActorFrom(ctx); ok {
		return ctx
	}
	return auth.WithActor(ctx, systemActor)
}

func requireActor(ctx context.Context) (auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return auth.Actor{}, audit.ErrNoActor
	}
	return a, nil
}

func (c *Catalog) now() time.Time { return c.clk.Now() }

// sleep waits d on the catalog's clock and reports false when ctx ended
// first.
func (c *Catalog) sleep(ctx context.Context, d time.Duration) bool {
	done := make(chan struct{})
	t := c.clk.AfterFunc(d, func() { close(done) })
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Catalog) timelineAdded(e Entry) {
	if c.onTL != nil {
		c.onTL(e)
	}
}

// Get returns a managed or retired service resource by name.
func (c *Catalog) Get(ctx context.Context, name string) (*Record, error) {
	rec, err := c.res.Get(ctx, name)
	switch {
	case errors.Is(err, resource.ErrNotFound):
		return nil, fmt.Errorf("%w: service %s", ErrNotFound, name)
	case err != nil:
		return nil, err
	case rec.Type != model.ResourceService || rec.Phase == resource.PhaseUnmanaged:
		return nil, fmt.Errorf("%w: service %s", ErrNotFound, name)
	}
	states, err := c.states(ctx, `WHERE resource_id = ?`, rec.ID)
	if err != nil {
		return nil, err
	}
	return c.record(rec, states)
}

// List returns every managed and retired service resource ordered by name.
// Callers that act on services filter on Phase.
func (c *Catalog) List(ctx context.Context) ([]Record, error) {
	recs, err := c.res.List(ctx, resource.Filter{Type: model.ResourceService, IncludeRetired: true})
	if err != nil {
		return nil, err
	}
	states, err := c.states(ctx, ``)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(recs))
	for _, rec := range recs {
		r, err := c.record(rec, states)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, nil
}

type stateRow struct {
	state State
	since time.Time
}

func (c *Catalog) states(ctx context.Context, where string, args ...any) (map[string]stateRow, error) {
	rows, err := c.db.Reader().QueryContext(ctx, `SELECT resource_id, state, state_since FROM service_states `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: query states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]stateRow{}
	for rows.Next() {
		var (
			id, state string
			since     int64
		)
		if err := rows.Scan(&id, &state, &since); err != nil {
			return nil, fmt.Errorf("catalog: scan state: %w", err)
		}
		out[id] = stateRow{state: State(state), since: time.UnixMilli(since).UTC()}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: query states: %w", err)
	}
	return out, nil
}

func (c *Catalog) record(rec resource.Record, states map[string]stateRow) (*Record, error) {
	cfg, ok := rec.Document.Service()
	if !ok {
		return nil, fmt.Errorf("catalog: %s has no service config", rec.Name)
	}
	st, ok := states[rec.ID]
	if !ok {
		return nil, fmt.Errorf("catalog: service %s has no lifecycle state", rec.Name)
	}
	return &Record{Record: rec, State: st.state, StateSince: st.since, Config: cfg}, nil
}

// stateOf reads the lifecycle state of a resource inside a transaction.
func stateOf(ctx context.Context, tx *sql.Tx, id string) (State, error) {
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM service_states WHERE resource_id = ?`, id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: lifecycle state of %s", ErrNotFound, id)
		}
		return "", err
	}
	return State(state), nil
}
