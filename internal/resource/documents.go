package resource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/store"
)

// Put saves a Resource document given as YAML or JSON. expected is the
// version the change is based on: 0 creates the resource, a positive value
// updates it and must equal the current version.
func (s *Store) Put(ctx context.Context, data []byte, expected int) (PutResult, error) {
	doc, err := model.DecodeResource(data)
	if err != nil {
		return PutResult{}, invalid(err)
	}
	return s.put(ctx, doc, expected, 0)
}

// PutDocument saves a decoded document like Put. The document is encoded
// and decoded again, so it is normalized and validated the same way.
func (s *Store) PutDocument(ctx context.Context, doc *model.Resource, expected int) (PutResult, error) {
	b, err := model.Encode(doc)
	if err != nil {
		return PutResult{}, err
	}
	return s.Put(ctx, b, expected)
}

// Restore saves an old version of a resource's document as a new version.
// expected is the current version, as for Put.
func (s *Store) Restore(ctx context.Context, ref string, version, expected int) (PutResult, error) {
	if expected <= 0 {
		return PutResult{}, precondition("restore needs the current version as the expected version")
	}
	rec, err := s.Get(ctx, ref)
	if err != nil {
		return PutResult{}, err
	}
	if rec.Phase == PhaseUnmanaged {
		return PutResult{}, precondition("archived resources are read only")
	}
	doc, _, err := s.GetVersion(ctx, ref, version)
	if err != nil {
		return PutResult{}, err
	}
	return s.put(ctx, doc, expected, version)
}

// ValidateHost runs the host rules of the document's type (HostValidator)
// for dry runs; every save runs them again, with the checks.
func (s *Store) ValidateHost(doc *model.Resource) model.Issues {
	if v, ok := s.hooksFor(doc.Spec.Type).(HostValidator); ok {
		return v.ValidateHost(doc)
	}
	return nil
}

func (s *Store) validate(doc *model.Resource) model.Issues {
	var is model.Issues
	if s.checks != nil {
		is = append(is, s.checks.ValidateChecks(doc.Spec.Checks, "spec.checks")...)
	}
	return append(is, s.ValidateHost(doc)...)
}

func (s *Store) put(ctx context.Context, doc *model.Resource, expected, restoredFrom int) (PutResult, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return PutResult{}, err
	}
	if expected < 0 {
		return PutResult{}, precondition("expected version must not be negative")
	}
	ctx, release, err := s.gate.Acquire(ctx)
	if err != nil {
		return PutResult{}, err
	}
	defer release()
	canon, err := model.Encode(doc)
	if err != nil {
		return PutResult{}, err
	}
	if is := s.validate(doc); len(is) > 0 {
		return PutResult{}, invalid(is)
	}
	name := doc.Metadata.Name
	now := s.clk.Now()

	var res PutResult
	var next Record
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, next = PutResult{}, Record{}
		cur, err := lookup(ctx, tx, name)
		found := err == nil
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		switch {
		case expected == 0 && found:
			return fmt.Errorf("%w: %s", ErrExists, name)
		case expected > 0 && !found:
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		case expected > 0 && cur.Version != expected:
			return fmt.Errorf("%w: %s is at version %d, expected %d", ErrConflict, name, cur.Version, expected)
		case found && (cur.Type != doc.Spec.Type || cur.Adapter != doc.Spec.Adapter):
			return precondition("%s is a %s resource with adapter %s; type and adapter cannot change, unmanage it and create a new resource instead",
				name, cur.Type, cur.Adapter)
		}
		unresolved, err := checkRelations(ctx, tx, name, doc.Spec.Relations)
		if err != nil {
			return err
		}
		if found && cur.raw == string(canon) {
			res = PutResult{Version: cur.Version, Unresolved: unresolved}
			return nil
		}
		next = Record{
			Name: name, Type: doc.Spec.Type, Adapter: doc.Spec.Adapter, Phase: PhaseManaged,
			Version: 1, Document: doc, CreatedAt: now, UpdatedAt: now,
		}
		var old *Record
		if found {
			old = &cur.Record
			next.ID, next.Phase, next.CreatedAt = cur.ID, cur.Phase, cur.CreatedAt
			if err := tx.QueryRowContext(ctx, `SELECT MAX(version) + 1 FROM resource_versions WHERE resource_id = ?`, cur.ID).Scan(&next.Version); err != nil {
				return err
			}
		} else {
			if err := checkSingleton(ctx, tx, doc); err != nil {
				return err
			}
			next.ID = store.NewID()
		}
		// The row of a new resource exists before the hooks run, so that
		// type tables can reference it.
		if !found {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO resources (id, name, type, adapter, phase, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				next.ID, name, next.Type, next.Adapter, next.Phase, next.Version, now.UnixMilli(), now.UnixMilli()); err != nil {
				return err
			}
		}
		if h := s.hooksFor(next.Type); h != nil {
			if err := h.BeforeSave(ctx, tx, Save{Old: old, Next: &next, RestoredFrom: restoredFrom, Actor: actor}); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO resource_versions (resource_id, version, document, restored_from, created_at, actor_kind, actor_id, actor_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			next.ID, next.Version, string(canon), restoredFrom, now.UnixMilli(), string(actor.Kind), actor.ID, actor.Name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE resources SET version = ?, updated_at = ? WHERE id = ?`, next.Version, now.UnixMilli(), next.ID); err != nil {
			return err
		}
		res = PutResult{Version: next.Version, Created: !found, Changed: true, Unresolved: unresolved}
		action := "resource.save"
		if restoredFrom > 0 {
			action = "resource.restore"
		}
		details := map[string]any{"id": next.ID, "type": next.Type, "version": res.Version, "created": res.Created}
		if restoredFrom > 0 {
			details["restoredFrom"] = restoredFrom
		}
		return s.audit.RecordTx(ctx, tx, action, name, audit.OutcomeOK, details)
	})
	if err != nil {
		return PutResult{}, err
	}
	if !res.Changed {
		return res, nil
	}
	change := ChangeUpdated
	switch {
	case restoredFrom > 0:
		change = ChangeRestored
	case res.Created:
		change = ChangeCreated
	}
	s.changed(Change{ID: next.ID, Name: name, Type: next.Type, Change: change})
	return res, nil
}

// checkSingleton refuses a second active local machine before the unique
// index would, with a message that says what to do.
func checkSingleton(ctx context.Context, tx *sql.Tx, doc *model.Resource) error {
	if doc.Spec.Type != model.ResourceMachine || doc.Spec.Adapter != model.AdapterLocal {
		return nil
	}
	var other string
	err := tx.QueryRowContext(ctx, `SELECT name FROM resources WHERE type = ? AND adapter = ? AND phase != ?`,
		model.ResourceMachine, model.AdapterLocal, PhaseUnmanaged).Scan(&other)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return fmt.Errorf("%w: the local machine is already registered as %s", ErrExists, other)
}

// Get returns a resource by the name of an active (not unmanaged) resource,
// or by "@<id>" in any phase.
func (s *Store) Get(ctx context.Context, ref string) (Record, error) {
	r, err := lookup(ctx, s.db.Reader(), ref)
	return r.Record, err
}

// List returns managed resources, and retired ones when f.IncludeRetired is
// set, ordered by name. Unmanaged resources are listed by Archives.
//
// Every filter runs in SQL, so f.After and f.Limit page over the filtered
// result: names are unique among active resources, which makes the name a
// keyset cursor.
func (s *Store) List(ctx context.Context, f Filter) ([]Record, error) {
	where, args, err := listCondition(f)
	if err != nil {
		return nil, err
	}
	rows, err := query(ctx, s.db.Reader(), where, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(rows))
	for i, r := range rows {
		out[i] = r.Record
	}
	return out, nil
}

// listCondition returns the condition, order and limit of List.
func listCondition(f Filter) (string, []any, error) {
	// The literal term matches the WHERE clause of the partial index
	// resources_active_name, so SQLite can walk it in name order and seek
	// to f.After instead of sorting every row.
	where, args := `r.phase != 'unmanaged'`, []any{}
	switch f.Phase {
	case "":
		if !f.IncludeRetired {
			where += ` AND r.phase = ?`
			args = append(args, PhaseManaged)
		}
	case PhaseManaged, PhaseRetired:
		where += ` AND r.phase = ?`
		args = append(args, f.Phase)
	default:
		return "", nil, precondition("list phase must be %s or %s; archives are listed separately", PhaseManaged, PhaseRetired)
	}
	if f.Type != "" {
		where += ` AND r.type = ?`
		args = append(args, f.Type)
	}
	if f.After != "" {
		where += ` AND r.name > ?`
		args = append(args, f.After)
	}
	for _, k := range slices.Sorted(maps.Keys(f.Labels)) {
		where += ` AND EXISTS (SELECT 1 FROM json_each(v.document, '$.metadata.labels') l WHERE l.key = ? AND l.value = ?)`
		args = append(args, k, f.Labels[k])
	}
	where += ` ORDER BY r.name`
	if f.Limit > 0 {
		where += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	return where, args, nil
}

// Archives returns unmanaged resources, of one type when typ is set,
// ordered by name and newest first.
func (s *Store) Archives(ctx context.Context, typ string) ([]Record, error) {
	where, args := `r.phase = ?`, []any{PhaseUnmanaged}
	if typ != "" {
		where += ` AND r.type = ?`
		args = append(args, typ)
	}
	rows, err := query(ctx, s.db.Reader(), where+` ORDER BY r.name, r.updated_at DESC, r.id`, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(rows))
	for i, r := range rows {
		out[i] = r.Record
	}
	return out, nil
}

// Versions lists the stored versions of a resource, newest first.
func (s *Store) Versions(ctx context.Context, ref string) ([]Version, error) {
	rec, err := s.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT version, restored_from, created_at, actor_kind, actor_id, actor_name
		 FROM resource_versions WHERE resource_id = ? ORDER BY version DESC`, rec.ID)
	if err != nil {
		return nil, fmt.Errorf("resource: list versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resource: list versions: %w", err)
	}
	return out, nil
}

// GetVersion returns one stored version of a resource's document.
func (s *Store) GetVersion(ctx context.Context, ref string, version int) (*model.Resource, Version, error) {
	rec, err := s.Get(ctx, ref)
	if err != nil {
		return nil, Version{}, err
	}
	row := s.db.Reader().QueryRowContext(ctx,
		`SELECT version, restored_from, created_at, actor_kind, actor_id, actor_name, document
		 FROM resource_versions WHERE resource_id = ? AND version = ?`, rec.ID, version)
	var doc string
	v, err := scanVersion(row, &doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Version{}, fmt.Errorf("%w: %s version %d", ErrNotFound, rec.Name, version)
	}
	if err != nil {
		return nil, Version{}, err
	}
	d, err := model.DecodeResource([]byte(doc))
	if err != nil {
		return nil, Version{}, fmt.Errorf("resource: stored document of %s version %d is invalid: %w", rec.Name, version, err)
	}
	return d, v, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanVersion(row scanner, extra ...any) (Version, error) {
	var (
		v    Version
		at   int64
		kind string
	)
	dest := append([]any{&v.Version, &v.RestoredFrom, &at, &kind, &v.Actor.ID, &v.Actor.Name}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Version{}, err
	}
	v.CreatedAt = time.UnixMilli(at).UTC()
	v.Actor.Kind = auth.Kind(kind)
	return v, nil
}

// stored is a record with the stored text of its current document.
type stored struct {
	Record
	raw string
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// lookup finds a resource by active name or "@<id>".
func lookup(ctx context.Context, q querier, ref string) (stored, error) {
	where, args := `r.name = ? AND r.phase != ?`, []any{ref, PhaseUnmanaged}
	if id, ok := strings.CutPrefix(ref, "@"); ok {
		where, args = `r.id = ?`, []any{id}
	}
	rows, err := query(ctx, q, where, args...)
	if err != nil {
		return stored{}, err
	}
	if len(rows) == 0 {
		return stored{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	return rows[0], nil
}

// selectRecords reads records with their current documents; the caller
// appends the condition.
const selectRecords = `SELECT r.id, r.name, r.type, r.adapter, r.phase, r.version, r.created_at, r.updated_at, v.document
	FROM resources r JOIN resource_versions v ON v.resource_id = r.id AND v.version = r.version
	WHERE `

func query(ctx context.Context, q querier, where string, args ...any) ([]stored, error) {
	rows, err := q.QueryContext(ctx, selectRecords+where, args...)
	if err != nil {
		return nil, fmt.Errorf("resource: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []stored
	for rows.Next() {
		var (
			r                stored
			created, updated int64
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.Adapter, &r.Phase, &r.Version, &created, &updated, &r.raw); err != nil {
			return nil, fmt.Errorf("resource: scan: %w", err)
		}
		r.CreatedAt = time.UnixMilli(created).UTC()
		r.UpdatedAt = time.UnixMilli(updated).UTC()
		if r.Document, err = model.DecodeResource([]byte(r.raw)); err != nil {
			return nil, fmt.Errorf("resource: stored document of %s is invalid: %w", r.Name, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resource: query: %w", err)
	}
	return out, nil
}
