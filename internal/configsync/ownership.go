package configsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/modules"
)

// Ref names a resource.
type Ref struct {
	Kind model.Kind
	Name string
}

func (r Ref) String() string { return string(r.Kind) + "/" + r.Name }

// Ownership describes a resource that a config source manages.
type Ownership struct {
	Kind model.Kind
	Name string
	// Source, Path and Commit say where the repository document came from.
	Source string
	Path   string
	Commit string
	// AppliedVersion is the resource version LCM wrote or last found equal
	// to the repository.
	AppliedVersion int
	AppliedAt      time.Time
	// Drifted: the resource was edited (or deleted) outside the repository.
	Drifted bool
	// Missing: the owned resource no longer exists.
	Missing bool
	// Conflict: a commit changed the repository document while the resource
	// was drifted, and the sync skipped it. ConflictCommit is that commit.
	Conflict       bool
	ConflictCommit string
	// Acknowledged: the operator chose to keep the current content.
	Acknowledged bool
	// Orphaned: the repository no longer holds the document.
	Orphaned bool
}

// ownRow is a config_ownership row.
type ownRow struct {
	Ref
	Source, Path, Commit string
	AppliedVersion       int
	Document             string
	Pending, PendingPath string
	ConflictCommit       string
	Acknowledged         bool
	Orphaned             bool
	AppliedAt            time.Time
}

const ownCols = `kind, name, source, path, commit_id, applied_version, document, pending, pending_path,
	conflict_commit, acknowledged, orphaned, applied_at`

func scanOwn(sc interface{ Scan(...any) error }) (ownRow, error) {
	var (
		r         ownRow
		kind      string
		at        int64
		ack, orph int
	)
	if err := sc.Scan(&kind, &r.Name, &r.Source, &r.Path, &r.Commit, &r.AppliedVersion, &r.Document, &r.Pending,
		&r.PendingPath, &r.ConflictCommit, &ack, &orph, &at); err != nil {
		return ownRow{}, err
	}
	r.Kind, r.Acknowledged, r.Orphaned, r.AppliedAt = model.Kind(kind), ack != 0, orph != 0, time.UnixMilli(at).UTC()
	return r, nil
}

func (s *Syncer) loadOwn(ctx context.Context, ref Ref) (*ownRow, error) {
	r, err := scanOwn(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+ownCols+` FROM config_ownership WHERE kind = ? AND name = ?`, string(ref.Kind), ref.Name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("configsync: load ownership: %w", err)
	}
	return &r, nil
}

func (s *Syncer) listOwn(ctx context.Context, where string, args ...any) ([]ownRow, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+ownCols+` FROM config_ownership `+where+` ORDER BY kind, name`, args...)
	if err != nil {
		return nil, fmt.Errorf("configsync: list ownership: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ownRow
	for rows.Next() {
		r, err := scanOwn(rows)
		if err != nil {
			return nil, fmt.Errorf("configsync: scan ownership: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("configsync: list ownership: %w", err)
	}
	return out, nil
}

func (s *Syncer) saveOwn(ctx context.Context, r ownRow) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO config_ownership (`+ownCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (kind, name) DO UPDATE SET source = excluded.source, path = excluded.path,
			   commit_id = excluded.commit_id, applied_version = excluded.applied_version, document = excluded.document,
			   pending = excluded.pending, pending_path = excluded.pending_path, conflict_commit = excluded.conflict_commit,
			   acknowledged = excluded.acknowledged, orphaned = excluded.orphaned, applied_at = excluded.applied_at`,
			string(r.Kind), r.Name, r.Source, r.Path, r.Commit, r.AppliedVersion, r.Document, r.Pending, r.PendingPath,
			r.ConflictCommit, b2i(r.Acknowledged), b2i(r.Orphaned), r.AppliedAt.UnixMilli())
		return err
	})
	if err != nil {
		return fmt.Errorf("configsync: save ownership: %w", err)
	}
	return nil
}

// current is a resource as it stands now.
type current struct {
	version int
	// canon is the canonical JSON of its document.
	canon string
}

// currentOf reads a resource; a nil result means it does not exist.
func (s *Syncer) currentOf(ctx context.Context, ref Ref) (*current, error) {
	switch ref.Kind {
	case model.KindService:
		rec, err := s.cat.Get(ctx, ref.Name)
		if errors.Is(err, catalog.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		b, err := model.Encode(rec.Spec)
		return &current{version: rec.Version, canon: string(b)}, err
	case model.KindModule:
		rec, err := s.mods.Get(ctx, ref.Name)
		if errors.Is(err, modules.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		b, err := model.Encode(rec.Spec)
		return &current{version: rec.Version, canon: string(b)}, err
	case model.KindPolicy:
		pv, err := s.eng.PolicyVersion(ctx, ref.Name, 0)
		if errors.Is(err, engine.ErrPolicyNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		pol, err := model.DecodePolicy(pv.Document)
		if err != nil {
			return nil, err
		}
		b, err := model.Encode(pol)
		return &current{version: pv.Version, canon: string(b)}, err
	}
	return nil, fmt.Errorf("configsync: unsupported kind %q", ref.Kind)
}

// isDrifted applies the definition: the resource is gone, or both its
// version and its content differ from what LCM accounted for.
func isDrifted(r ownRow, cur *current) bool {
	if cur == nil {
		return true
	}
	return cur.version != r.AppliedVersion && cur.canon != r.Document
}

func (s *Syncer) view(ctx context.Context, r ownRow) (Ownership, error) {
	cur, err := s.currentOf(ctx, r.Ref)
	if err != nil {
		return Ownership{}, err
	}
	return Ownership{
		Kind: r.Kind, Name: r.Name, Source: r.Source, Path: r.Path, Commit: r.Commit,
		AppliedVersion: r.AppliedVersion, AppliedAt: r.AppliedAt,
		Drifted: isDrifted(r, cur), Missing: cur == nil,
		Conflict: r.ConflictCommit != "", ConflictCommit: r.ConflictCommit,
		Acknowledged: r.Acknowledged, Orphaned: r.Orphaned,
	}, nil
}

// Ownership lists the managed resources with their computed drift. A
// non-empty source keeps only that source's resources.
func (s *Syncer) Ownership(ctx context.Context, source string) ([]Ownership, error) {
	var (
		rows []ownRow
		err  error
	)
	if source != "" {
		rows, err = s.listOwn(ctx, `WHERE source = ?`, source)
	} else {
		rows, err = s.listOwn(ctx, ``)
	}
	if err != nil {
		return nil, err
	}
	out := make([]Ownership, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Owner returns the ownership of one resource, or nil when it is managed
// through the web UI and API only.
func (s *Syncer) Owner(ctx context.Context, kind model.Kind, name string) (*Ownership, error) {
	r, err := s.loadOwn(ctx, Ref{kind, name})
	if err != nil || r == nil {
		return nil, err
	}
	v, err := s.view(ctx, *r)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// Resolve settles a drifted or conflicting resource. With useGit the
// repository document (the pending one if a commit was skipped, otherwise
// the last applied one) is written to the resource, discarding local edits.
// Otherwise the current content is kept and the pending document, if any,
// is accepted as the reference, so the resource stays drifted until the
// repository catches up.
func (s *Syncer) Resolve(ctx context.Context, kind model.Kind, name string, useGit bool) (Ownership, error) {
	if _, ok := actorOf(ctx); !ok {
		return Ownership{}, audit.ErrNoActor
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	ref := Ref{kind, name}
	row, err := s.loadOwn(ctx, ref)
	if err != nil {
		return Ownership{}, err
	}
	if row == nil {
		return Ownership{}, fmt.Errorf("%w: %s is not managed by a config source", ErrNotFound, ref)
	}
	hadConflict := row.ConflictCommit != ""
	use := "current"
	if useGit {
		use = "git"
		if err := s.resolveGit(ctx, row); err != nil {
			s.record(ctx, "config.resolve", ref.String(), audit.OutcomeError, map[string]any{"use": use, "source": row.Source})
			return Ownership{}, err
		}
	} else {
		if row.Pending != "" {
			row.Path, row.Commit = row.PendingPath, row.ConflictCommit
			row.Document, row.Pending, row.PendingPath, row.ConflictCommit = row.Pending, "", "", ""
		}
		row.Acknowledged = true
		if err := s.saveOwn(ctx, *row); err != nil {
			return Ownership{}, err
		}
	}
	s.record(ctx, "config.resolve", ref.String(), audit.OutcomeOK, map[string]any{"use": use, "source": row.Source, "commit": row.Commit})
	if hadConflict {
		s.notifyResolved(ctx, ref, row.Source, "conflict resolved by "+use)
	}
	return s.view(ctx, *row)
}

// resolveGit writes the repository document and updates the row.
func (s *Syncer) resolveGit(ctx context.Context, row *ownRow) error {
	doc, path, commit := row.Document, row.Path, row.Commit
	if row.Pending != "" {
		doc, path, commit = row.Pending, row.PendingPath, row.ConflictCommit
	}
	it := parseItem(Document{Path: path, Data: []byte(doc)})
	if it.err != nil {
		return it.err
	}
	cur, err := s.currentOf(ctx, row.Ref)
	if err != nil {
		return err
	}
	version := 0
	if cur != nil && cur.canon == it.canon {
		version = cur.version
	} else {
		if is, _ := s.validate(ctx, it, nil); len(is) > 0 {
			return is
		}
		expected := 0
		if cur != nil {
			expected = cur.version
		}
		if version, err = s.write(ctx, it, expected); err != nil {
			return err
		}
	}
	*row = ownRow{
		Ref: row.Ref, Source: row.Source, Path: path, Commit: commit, AppliedVersion: version,
		Document: doc, Orphaned: row.Orphaned, AppliedAt: s.now(),
	}
	return s.saveOwn(ctx, *row)
}

// Release ends the ownership of a resource: it becomes managed through the
// web UI and API only, and the sync no longer touches it. Its content is
// left as it is.
func (s *Syncer) Release(ctx context.Context, kind model.Kind, name string) error {
	if _, ok := actorOf(ctx); !ok {
		return audit.ErrNoActor
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	ref := Ref{kind, name}
	row, err := s.loadOwn(ctx, ref)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("%w: %s is not managed by a config source", ErrNotFound, ref)
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM config_ownership WHERE kind = ? AND name = ?`, string(kind), name)
		return err
	}); err != nil {
		return fmt.Errorf("configsync: release: %w", err)
	}
	s.record(ctx, "config.release", ref.String(), audit.OutcomeOK, map[string]any{"source": row.Source})
	if row.ConflictCommit != "" {
		s.notifyResolved(ctx, ref, row.Source, "ownership released")
	}
	return nil
}

// shortCommit abbreviates a commit id for messages.
func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
