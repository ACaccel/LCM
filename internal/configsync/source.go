package configsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/model"
)

// Source limits and defaults.
const (
	DefaultInterval = time.Minute
	MinInterval     = 10 * time.Second
	MaxInterval     = 24 * time.Hour
	maxRepoLen      = 1024
	maxBranchLen    = 200
)

var (
	// A source name becomes a directory name, so it starts with a letter or
	// digit and has no separators.
	sourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	branchRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	// urlPassword matches credentials embedded in a URL.
	urlPassword = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://[^/@]*:[^/@]*@`)
	urlUserinfo = regexp.MustCompile(`(://)[^/@\s]+@`)
)

// Source is a registered repository location that LCM pulls documents from.
type Source struct {
	Name   string
	Repo   string
	Branch string
	// Path is a directory (or file) inside the repository, "." for all of it.
	Path     string
	Interval time.Duration
	Enabled  bool

	LastCommit string
	// LastSyncAt is zero before the first sync.
	LastSyncAt time.Time
	// LastError describes what went wrong in the last sync, empty if nothing.
	LastError string
	// Syncing: a sync of this source is running now.
	Syncing   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// normalize applies defaults and validates the user-settable fields.
func (src *Source) normalize() error {
	var is model.Issues
	bad := func(p, msg string) { is = append(is, model.Issue{Path: p, Message: msg}) }

	if !sourceNameRe.MatchString(src.Name) {
		bad("name", "must be 1-63 characters of a-z, 0-9, '.', '_' or '-', starting with a letter or digit")
	}
	switch repo := src.Repo; {
	case repo == "":
		bad("repo", "is required")
	case len(repo) > maxRepoLen:
		bad("repo", "is too long")
	case strings.HasPrefix(repo, "-"):
		bad("repo", "must not start with '-'")
	case strings.IndexFunc(repo, unicode.IsControl) >= 0:
		bad("repo", "must not contain control characters")
	case strings.Contains(repo, "::"):
		bad("repo", "remote helpers (transport::address) are not supported")
	case urlPassword.MatchString(repo):
		bad("repo", "must not embed a password; use an SSH key or a git credential helper")
	}
	if src.Branch == "" {
		src.Branch = "main"
	}
	if len(src.Branch) > maxBranchLen || !branchRe.MatchString(src.Branch) || strings.Contains(src.Branch, "..") ||
		strings.Contains(src.Branch, "//") || strings.HasSuffix(src.Branch, "/") || strings.HasSuffix(src.Branch, ".") ||
		strings.HasSuffix(src.Branch, ".lock") {
		bad("branch", "is not a valid branch name")
	}
	p := strings.Trim(strings.TrimSpace(src.Path), "/")
	switch {
	case strings.HasPrefix(src.Path, "/") && p != "":
		bad("path", "must be relative to the repository root")
	case strings.Contains(src.Path, `\`) || strings.IndexFunc(src.Path, unicode.IsControl) >= 0:
		bad("path", "contains invalid characters")
	default:
		p = path.Clean(p)
		if p == ".." || strings.HasPrefix(p, "../") {
			bad("path", "must stay inside the repository")
		}
		src.Path = p
	}
	if src.Interval == 0 {
		src.Interval = DefaultInterval
	}
	if src.Interval < MinInterval || src.Interval > MaxInterval {
		bad("interval", fmt.Sprintf("must be between %s and %s", MinInterval, MaxInterval))
	}
	return is.Err()
}

const sourceCols = `name, repo, branch, path, interval_ms, enabled, last_commit, last_sync_at, last_error, created_at, updated_at`

func scanSource(sc interface{ Scan(...any) error }) (Source, error) {
	var (
		src              Source
		interval         int64
		enabled          int
		lastSync         sql.NullInt64
		created, updated int64
	)
	if err := sc.Scan(&src.Name, &src.Repo, &src.Branch, &src.Path, &interval, &enabled, &src.LastCommit,
		&lastSync, &src.LastError, &created, &updated); err != nil {
		return Source{}, err
	}
	src.Interval, src.Enabled = time.Duration(interval)*time.Millisecond, enabled != 0
	if lastSync.Valid {
		src.LastSyncAt = time.UnixMilli(lastSync.Int64).UTC()
	}
	src.CreatedAt, src.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return src, nil
}

// GetSource returns one source.
func (s *Syncer) GetSource(ctx context.Context, name string) (Source, error) {
	src, err := scanSource(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+sourceCols+` FROM config_sources WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, fmt.Errorf("%w: source %s", ErrNotFound, name)
	}
	if err != nil {
		return Source{}, fmt.Errorf("configsync: get source: %w", err)
	}
	src.Syncing = s.syncing(name)
	return src, nil
}

// ListSources returns every source ordered by name.
func (s *Syncer) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT `+sourceCols+` FROM config_sources ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("configsync: list sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("configsync: scan source: %w", err)
		}
		src.Syncing = s.syncing(src.Name)
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("configsync: list sources: %w", err)
	}
	return out, nil
}

// CreateSource registers a source. LastCommit and the other state fields of
// src are ignored. The running sync loop picks the source up at once.
func (s *Syncer) CreateSource(ctx context.Context, src Source) (Source, error) {
	if _, ok := actorOf(ctx); !ok {
		return Source{}, audit.ErrNoActor
	}
	if err := src.normalize(); err != nil {
		return Source{}, err
	}
	now := s.now().UnixMilli()
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_sources WHERE name = ?`, src.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: %s", ErrExists, src.Name)
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO config_sources (name, repo, branch, path, interval_ms, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			src.Name, src.Repo, src.Branch, src.Path, src.Interval.Milliseconds(), b2i(src.Enabled), now, now)
		return err
	})
	if err != nil {
		return Source{}, err
	}
	s.record(ctx, "config.source.create", "source:"+src.Name, audit.OutcomeOK,
		map[string]any{"repo": redact(src.Repo), "branch": src.Branch, "path": src.Path})
	s.wake()
	return s.GetSource(ctx, src.Name)
}

// UpdateSource replaces the settings of a source. Changing where documents
// are read from forgets the last commit so the next sync reads them again.
func (s *Syncer) UpdateSource(ctx context.Context, src Source) (Source, error) {
	if _, ok := actorOf(ctx); !ok {
		return Source{}, audit.ErrNoActor
	}
	if err := src.normalize(); err != nil {
		return Source{}, err
	}
	old, err := s.GetSource(ctx, src.Name)
	if err != nil {
		return Source{}, err
	}
	moved := old.Repo != src.Repo || old.Branch != src.Branch || old.Path != src.Path
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		q := `UPDATE config_sources SET repo = ?, branch = ?, path = ?, interval_ms = ?, enabled = ?, updated_at = ?`
		if moved {
			q += `, last_commit = '', last_error = ''`
		}
		_, err := tx.ExecContext(ctx, q+` WHERE name = ?`,
			src.Repo, src.Branch, src.Path, src.Interval.Milliseconds(), b2i(src.Enabled), s.now().UnixMilli(), src.Name)
		return err
	})
	if err != nil {
		return Source{}, fmt.Errorf("configsync: update source: %w", err)
	}
	s.record(ctx, "config.source.update", "source:"+src.Name, audit.OutcomeOK,
		map[string]any{"repo": redact(src.Repo), "branch": src.Branch, "path": src.Path, "enabled": src.Enabled})
	s.wake()
	return s.GetSource(ctx, src.Name)
}

// DeleteSource removes a source and its clone. The resources it owned are
// released: they keep their content and become managed through the web UI
// and API only.
func (s *Syncer) DeleteSource(ctx context.Context, name string) error {
	if _, ok := actorOf(ctx); !ok {
		return audit.ErrNoActor
	}
	if _, err := s.GetSource(ctx, name); err != nil {
		return err
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	var released int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM config_ownership WHERE source = ?`, name)
		if err != nil {
			return err
		}
		released, _ = res.RowsAffected()
		_, err = tx.ExecContext(ctx, `DELETE FROM config_sources WHERE name = ?`, name)
		return err
	})
	if err != nil {
		return fmt.Errorf("configsync: delete source: %w", err)
	}
	if s.dir != "" {
		if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
			s.log.Warn("cannot remove the clone of a deleted source", "source", name, "error", err)
		}
	}
	s.record(ctx, "config.source.delete", "source:"+name, audit.OutcomeOK, map[string]any{"released": released})
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// redact removes credentials from text that may quote a repository URL.
func redact(s string) string { return urlUserinfo.ReplaceAllString(s, "$1") }

func (s *Syncer) dbExec(ctx context.Context, query string, args ...any) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}
