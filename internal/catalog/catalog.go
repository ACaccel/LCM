// Package catalog is the registry of managed services: versioned service
// documents, the lifecycle state machine, deployment history and the
// per-service timeline, plus the Manager that performs lifecycle actions
// through the runtime adapters.
//
// # Documents
//
// Every save of a changed service document adds a version. Saves carry the
// version the caller based the change on; a mismatch is ErrConflict, so two
// editors cannot silently overwrite each other. Restoring an old version
// saves it as a new one. Versions survive deleting the service.
//
// # Lifecycle
//
// A service is in exactly one State. The transitions table in state.go is
// the only authority on allowed changes, and every change is written with
// its timeline entry in one transaction before a lifecycle signal is
// published. Health is not stored here; the monitor owns it.
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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
)

// Errors returned by the catalog.
var (
	ErrNotFound = errors.New("catalog: not found")
	ErrExists   = errors.New("catalog: service already exists")
	// ErrConflict means the stored version differs from the expected one.
	ErrConflict = errors.New("catalog: version conflict")
)

// Config configures a Catalog.
type Config struct {
	DB    *store.DB
	Audit *audit.Log
	// Bus receives lifecycle signals; optional.
	Bus *signal.Bus
	// Clock defaults to the real clock.
	Clock clock.Clock
	Log   *slog.Logger
}

// Catalog stores services and their history.
type Catalog struct {
	db    *store.DB
	audit *audit.Log
	bus   *signal.Bus
	clk   clock.Clock
	log   *slog.Logger
}

// New returns a Catalog.
func New(cfg Config) (*Catalog, error) {
	if cfg.DB == nil || cfg.Audit == nil {
		return nil, errors.New("catalog: DB and Audit are required")
	}
	c := &Catalog{db: cfg.DB, audit: cfg.Audit, bus: cfg.Bus, clk: cfg.Clock, log: cfg.Log}
	if c.clk == nil {
		c.clk = clock.Real()
	}
	if c.log == nil {
		c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return c, nil
}

// Record is a service as stored.
type Record struct {
	Name       string
	Host       string
	Version    int
	State      State
	StateSince time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Spec       *model.Service
}

// VersionInfo describes one stored version of a service document.
type VersionInfo struct {
	Version      int
	CreatedAt    time.Time
	CreatedBy    auth.Actor
	RestoredFrom int
}

// Host is a machine services run on.
type Host struct {
	Name    string
	Kind    string
	Address string
}

// PutResult is the outcome of saving a document.
type PutResult struct {
	Version int
	// Created is true for the first version of a service.
	Created bool
	// Changed is false when the document equals the current version and
	// nothing was written.
	Changed bool
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

// Put saves a service document given as YAML or JSON. expected is the
// version the change is based on: 0 creates the service, a positive value
// updates it and must equal the current version.
func (c *Catalog) Put(ctx context.Context, data []byte, expected int) (PutResult, error) {
	svc, err := model.DecodeService(data)
	if err != nil {
		return PutResult{}, err
	}
	return c.put(ctx, svc, expected, 0)
}

func (c *Catalog) put(ctx context.Context, svc *model.Service, expected, restoredFrom int) (PutResult, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return PutResult{}, err
	}
	if expected < 0 {
		return PutResult{}, errors.New("catalog: expected version must not be negative")
	}
	doc, err := canonical(svc)
	if err != nil {
		return PutResult{}, err
	}
	name := svc.Metadata.Name
	now := c.now().UnixMilli()

	var res PutResult
	err = c.db.Write(ctx, func(tx *sql.Tx) error {
		var hostKind string
		switch err := tx.QueryRowContext(ctx, `SELECT kind FROM hosts WHERE name = ?`, svc.Spec.EffectiveHost()).Scan(&hostKind); {
		case errors.Is(err, sql.ErrNoRows):
			return model.Issues{{Path: "spec.host", Message: fmt.Sprintf("unknown host %q", svc.Spec.EffectiveHost())}}
		case err != nil:
			return err
		}

		var cur int
		var curDoc string
		found := true
		err := tx.QueryRowContext(ctx,
			`SELECT s.version, v.document FROM services s
			 JOIN service_versions v ON v.name = s.name AND v.version = s.version
			 WHERE s.name = ?`, name).Scan(&cur, &curDoc)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			found = false
		case err != nil:
			return err
		}

		switch {
		case expected == 0 && found:
			return fmt.Errorf("%w: %s", ErrExists, name)
		case expected > 0 && !found:
			return fmt.Errorf("%w: service %s", ErrNotFound, name)
		case expected > 0 && cur != expected:
			return fmt.Errorf("%w: %s is at version %d, expected %d", ErrConflict, name, cur, expected)
		}
		if found && curDoc == doc {
			res = PutResult{Version: cur}
			return nil
		}

		var next int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version), 0) + 1 FROM service_versions WHERE name = ?`, name).Scan(&next); err != nil {
			return err
		}
		var from any
		if restoredFrom > 0 {
			from = restoredFrom
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO service_versions (name, version, document, restored_from, created_at, created_by_kind, created_by_id, created_by_name)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			name, next, doc, from, now, string(actor.Kind), actor.ID, actor.Name); err != nil {
			return err
		}
		if found {
			_, err = tx.ExecContext(ctx,
				`UPDATE services SET host = ?, version = ?, updated_at = ? WHERE name = ?`,
				svc.Spec.EffectiveHost(), next, now, name)
		} else {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO services (name, host, version, state, state_since, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				name, svc.Spec.EffectiveHost(), next, string(StateRegistered), now, now, now)
		}
		if err != nil {
			return err
		}
		summary := fmt.Sprintf("configuration version %d saved by %s", next, actor.Name)
		if restoredFrom > 0 {
			summary = fmt.Sprintf("configuration version %d restored from version %d by %s", next, restoredFrom, actor.Name)
		}
		if err := insertTimeline(ctx, tx, Entry{
			Service: name, Time: time.UnixMilli(now), Kind: KindConfig, Summary: summary,
			Data: map[string]any{"version": next, "restoredFrom": restoredFrom, "actor": actor.Name},
		}); err != nil {
			return err
		}
		res = PutResult{Version: next, Created: !found, Changed: true}
		return nil
	})
	if err != nil {
		return PutResult{}, err
	}

	if res.Changed {
		action := "service.update"
		switch {
		case restoredFrom > 0:
			action = "service.restore"
		case res.Created:
			action = "service.create"
		}
		details := map[string]any{"version": res.Version}
		if restoredFrom > 0 {
			details["restoredFrom"] = restoredFrom
		}
		if err := c.audit.Record(ctx, action, name, audit.OutcomeOK, details); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Restore saves an old version of the document as a new version. expected
// is the current version, as for Put.
func (c *Catalog) Restore(ctx context.Context, name string, version, expected int) (PutResult, error) {
	if expected <= 0 {
		return PutResult{}, errors.New("catalog: restore needs the current version as the expected version")
	}
	svc, _, err := c.GetVersion(ctx, name, version)
	if err != nil {
		return PutResult{}, err
	}
	return c.put(ctx, svc, expected, version)
}

// Delete removes a service from the catalog. Its versions, deployments and
// timeline stay. Services must be out of any running action; running ones
// are retired first so that deleting never orphans a live process.
func (c *Catalog) Delete(ctx context.Context, name string) error {
	actor, err := requireActor(ctx)
	if err != nil {
		return err
	}
	now := c.now()
	err = c.db.Write(ctx, func(tx *sql.Tx) error {
		var state State
		switch err := tx.QueryRowContext(ctx, `SELECT state FROM services WHERE name = ?`, name).Scan(&state); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: service %s", ErrNotFound, name)
		case err != nil:
			return err
		}
		if state != StateRegistered && state != StateStopped && state != StateFailed && state != StateRetired {
			return &StateError{Service: name, Action: "delete", State: state}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM services WHERE name = ?`, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM action_completions WHERE service = ?`, name); err != nil {
			return err
		}
		return insertTimeline(ctx, tx, Entry{
			Service: name, Time: now, Kind: KindConfig,
			Summary: "service deleted by " + actor.Name, Data: map[string]any{"deleted": true},
		})
	})
	if err != nil {
		return err
	}
	return c.audit.Record(ctx, "service.delete", name, audit.OutcomeOK, nil)
}

// Get returns a service.
func (c *Catalog) Get(ctx context.Context, name string) (*Record, error) {
	recs, err := c.query(ctx, `WHERE s.name = ?`, name)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("%w: service %s", ErrNotFound, name)
	}
	return &recs[0], nil
}

// List returns every service ordered by name.
func (c *Catalog) List(ctx context.Context) ([]Record, error) {
	return c.query(ctx, ``)
}

func (c *Catalog) query(ctx context.Context, where string, args ...any) ([]Record, error) {
	rows, err := c.db.Reader().QueryContext(ctx,
		`SELECT s.name, s.host, s.version, s.state, s.state_since, s.created_at, s.updated_at, v.document
		 FROM services s JOIN service_versions v ON v.name = s.name AND v.version = s.version `+
			where+` ORDER BY s.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: query services: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		var (
			r                       Record
			state                   string
			since, created, updated int64
			doc                     string
		)
		if err := rows.Scan(&r.Name, &r.Host, &r.Version, &state, &since, &created, &updated, &doc); err != nil {
			return nil, fmt.Errorf("catalog: scan service: %w", err)
		}
		r.State = State(state)
		r.StateSince = time.UnixMilli(since).UTC()
		r.CreatedAt = time.UnixMilli(created).UTC()
		r.UpdatedAt = time.UnixMilli(updated).UTC()
		if r.Spec, err = model.DecodeService([]byte(doc)); err != nil {
			return nil, fmt.Errorf("catalog: stored document of %s is invalid: %w", r.Name, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: query services: %w", err)
	}
	return out, nil
}

// Versions lists the stored versions of a service, newest first.
func (c *Catalog) Versions(ctx context.Context, name string) ([]VersionInfo, error) {
	rows, err := c.db.Reader().QueryContext(ctx,
		`SELECT version, created_at, created_by_kind, created_by_id, created_by_name, restored_from
		 FROM service_versions WHERE name = ? ORDER BY version DESC`, name)
	if err != nil {
		return nil, fmt.Errorf("catalog: list versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []VersionInfo
	for rows.Next() {
		var (
			v        VersionInfo
			ms       int64
			kind     string
			restored sql.NullInt64
		)
		if err := rows.Scan(&v.Version, &ms, &kind, &v.CreatedBy.ID, &v.CreatedBy.Name, &restored); err != nil {
			return nil, fmt.Errorf("catalog: scan version: %w", err)
		}
		v.CreatedAt = time.UnixMilli(ms).UTC()
		v.CreatedBy.Kind = auth.Kind(kind)
		v.RestoredFrom = int(restored.Int64)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: list versions: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: service %s", ErrNotFound, name)
	}
	return out, nil
}

// GetVersion returns one stored version of a service document.
func (c *Catalog) GetVersion(ctx context.Context, name string, version int) (*model.Service, VersionInfo, error) {
	var (
		doc  string
		v    = VersionInfo{Version: version}
		ms   int64
		kind string
		rest sql.NullInt64
	)
	err := c.db.Reader().QueryRowContext(ctx,
		`SELECT document, created_at, created_by_kind, created_by_id, created_by_name, restored_from
		 FROM service_versions WHERE name = ? AND version = ?`, name, version).
		Scan(&doc, &ms, &kind, &v.CreatedBy.ID, &v.CreatedBy.Name, &rest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, v, fmt.Errorf("%w: service %s version %d", ErrNotFound, name, version)
	}
	if err != nil {
		return nil, v, fmt.Errorf("catalog: get version: %w", err)
	}
	v.CreatedAt = time.UnixMilli(ms).UTC()
	v.CreatedBy.Kind = auth.Kind(kind)
	v.RestoredFrom = int(rest.Int64)
	svc, err := model.DecodeService([]byte(doc))
	if err != nil {
		return nil, v, fmt.Errorf("catalog: stored document of %s version %d is invalid: %w", name, version, err)
	}
	return svc, v, nil
}

// Hosts lists the known hosts. Only the local host exists for now; the
// table is ready for SSH hosts.
func (c *Catalog) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := c.db.Reader().QueryContext(ctx, `SELECT name, kind, address FROM hosts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("catalog: list hosts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.Name, &h.Kind, &h.Address); err != nil {
			return nil, fmt.Errorf("catalog: scan host: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// canonical returns the indented JSON form of a document. Go orders map
// keys, so equal documents have equal text and stored versions diff well.
func canonical(svc *model.Service) (string, error) {
	b, err := model.Encode(svc)
	if err != nil {
		return "", err
	}
	var buf []byte
	buf, err = json.MarshalIndent(json.RawMessage(b), "", "  ")
	if err != nil {
		return "", fmt.Errorf("catalog: format document: %w", err)
	}
	return string(buf), nil
}
