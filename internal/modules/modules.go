// Package modules stores versioned module documents and resolves them for
// the policy engine.
//
// Every save of a changed document adds a version. Saves carry the version
// the caller based the change on, so two editors cannot silently overwrite
// each other (ErrConflict). A deployed policy keeps the module versions it
// was compiled with; saving a module only affects policies deployed later.
package modules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/store"
)

// Errors returned by the store.
var (
	ErrNotFound = errors.New("modules: not found")
	ErrExists   = errors.New("modules: module already exists")
	// ErrConflict means the stored version differs from the expected one.
	ErrConflict = errors.New("modules: version conflict")
)

// Store keeps module documents in SQLite.
type Store struct {
	db    *store.DB
	audit *audit.Log
	clk   clock.Clock
}

// New returns a Store. clk defaults to the real clock.
func New(db *store.DB, log *audit.Log, clk clock.Clock) (*Store, error) {
	if db == nil || log == nil {
		return nil, errors.New("modules: DB and audit log are required")
	}
	if clk == nil {
		clk = clock.Real()
	}
	return &Store{db: db, audit: log, clk: clk}, nil
}

// VersionInfo describes one stored version.
type VersionInfo struct {
	Version   int
	CreatedAt time.Time
	CreatedBy auth.Actor
}

// Record is one version of a module with its document.
type Record struct {
	VersionInfo
	Name string
	Spec *model.Module
}

// PutResult is the outcome of saving a document.
type PutResult struct {
	Version int
	// Created is true for the first version of a module.
	Created bool
	// Changed is false when the document equals the current version and
	// nothing was written.
	Changed bool
}

// Put saves a module document given as YAML or JSON. expected is the
// version the change is based on: 0 creates the module, a positive value
// updates it and must equal the current version.
func (s *Store) Put(ctx context.Context, data []byte, expected int) (PutResult, error) {
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return PutResult{}, audit.ErrNoActor
	}
	if expected < 0 {
		return PutResult{}, errors.New("modules: expected version must not be negative")
	}
	mod, err := model.DecodeModule(data)
	if err != nil {
		return PutResult{}, err
	}
	enc, err := model.Encode(mod)
	if err != nil {
		return PutResult{}, err
	}
	// Indented canonical JSON: Go orders map keys, so equal documents have
	// equal text and stored versions diff well.
	doc, err := json.MarshalIndent(json.RawMessage(enc), "", "  ")
	if err != nil {
		return PutResult{}, fmt.Errorf("modules: format document: %w", err)
	}
	name := mod.Metadata.Name
	now := s.clk.Now().UnixMilli()

	var res PutResult
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var cur int
		var curDoc string
		found := true
		switch err := tx.QueryRowContext(ctx,
			`SELECT version, document FROM module_versions WHERE name = ? ORDER BY version DESC LIMIT 1`, name).
			Scan(&cur, &curDoc); {
		case errors.Is(err, sql.ErrNoRows):
			found = false
		case err != nil:
			return err
		}
		switch {
		case expected == 0 && found:
			return fmt.Errorf("%w: %s", ErrExists, name)
		case expected > 0 && !found:
			return fmt.Errorf("%w: module %s", ErrNotFound, name)
		case expected > 0 && cur != expected:
			return fmt.Errorf("%w: %s is at version %d, expected %d", ErrConflict, name, cur, expected)
		}
		if found && curDoc == string(doc) {
			res = PutResult{Version: cur}
			return nil
		}
		next := cur + 1
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO module_versions (name, version, document, created_at, created_by_kind, created_by_id, created_by_name)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			name, next, string(doc), now, string(actor.Kind), actor.ID, actor.Name); err != nil {
			return err
		}
		res = PutResult{Version: next, Created: !found, Changed: true}
		return nil
	})
	if err != nil {
		return PutResult{}, err
	}
	if res.Changed {
		action := "module.update"
		if res.Created {
			action = "module.create"
		}
		if err := s.audit.Record(ctx, action, "module:"+name, audit.OutcomeOK, map[string]any{"version": res.Version}); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Get returns the current version of a module.
func (s *Store) Get(ctx context.Context, name string) (*Record, error) {
	rec, err := s.get(ctx, name, 0)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// GetVersion returns one stored version of a module.
func (s *Store) GetVersion(ctx context.Context, name string, version int) (*Record, error) {
	if version <= 0 {
		return nil, fmt.Errorf("%w: module %s version %d", ErrNotFound, name, version)
	}
	return s.get(ctx, name, version)
}

// get reads the given version, or the newest when version is 0.
func (s *Store) get(ctx context.Context, name string, version int) (*Record, error) {
	query := `SELECT version, document, created_at, created_by_kind, created_by_id, created_by_name
		 FROM module_versions WHERE name = ?`
	args := []any{name}
	if version > 0 {
		query += ` AND version = ?`
		args = append(args, version)
	} else {
		query += ` ORDER BY version DESC LIMIT 1`
	}
	var (
		r    = Record{Name: name}
		doc  string
		ms   int64
		kind string
	)
	err := s.db.Reader().QueryRowContext(ctx, query, args...).
		Scan(&r.Version, &doc, &ms, &kind, &r.CreatedBy.ID, &r.CreatedBy.Name)
	if errors.Is(err, sql.ErrNoRows) {
		if version > 0 {
			return nil, fmt.Errorf("%w: module %s version %d", ErrNotFound, name, version)
		}
		return nil, fmt.Errorf("%w: module %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("modules: get %s: %w", name, err)
	}
	r.CreatedAt, r.CreatedBy.Kind = time.UnixMilli(ms).UTC(), auth.Kind(kind)
	if r.Spec, err = model.DecodeModule([]byte(doc)); err != nil {
		return nil, fmt.Errorf("modules: stored document of %s version %d is invalid: %w", name, r.Version, err)
	}
	return &r, nil
}

// Versions lists the stored versions of a module, newest first.
func (s *Store) Versions(ctx context.Context, name string) ([]VersionInfo, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT version, created_at, created_by_kind, created_by_id, created_by_name
		 FROM module_versions WHERE name = ? ORDER BY version DESC`, name)
	if err != nil {
		return nil, fmt.Errorf("modules: list versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []VersionInfo
	for rows.Next() {
		var (
			v    VersionInfo
			ms   int64
			kind string
		)
		if err := rows.Scan(&v.Version, &ms, &kind, &v.CreatedBy.ID, &v.CreatedBy.Name); err != nil {
			return nil, fmt.Errorf("modules: scan version: %w", err)
		}
		v.CreatedAt, v.CreatedBy.Kind = time.UnixMilli(ms).UTC(), auth.Kind(kind)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("modules: list versions: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: module %s", ErrNotFound, name)
	}
	return out, nil
}

// List returns the current version of every module, ordered by name.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT m.name, m.version, m.document, m.created_at, m.created_by_kind, m.created_by_id, m.created_by_name
		 FROM module_versions m
		 WHERE m.version = (SELECT MAX(version) FROM module_versions WHERE name = m.name)
		 ORDER BY m.name`)
	if err != nil {
		return nil, fmt.Errorf("modules: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		var (
			r    Record
			doc  string
			ms   int64
			kind string
		)
		if err := rows.Scan(&r.Name, &r.Version, &doc, &ms, &kind, &r.CreatedBy.ID, &r.CreatedBy.Name); err != nil {
			return nil, fmt.Errorf("modules: scan module: %w", err)
		}
		r.CreatedAt, r.CreatedBy.Kind = time.UnixMilli(ms).UTC(), auth.Kind(kind)
		if r.Spec, err = model.DecodeModule([]byte(doc)); err != nil {
			return nil, fmt.Errorf("modules: stored document of %s is invalid: %w", r.Name, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("modules: list: %w", err)
	}
	return out, nil
}

// ResolveModule returns the current version of a module. It implements
// engine.ModuleResolver.
func (s *Store) ResolveModule(ctx context.Context, name string) (*model.Module, error) {
	rec, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	return rec.Spec, nil
}
