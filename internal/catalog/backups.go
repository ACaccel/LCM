package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/model"
)

// Kinds of backup records.
const (
	// BackupKindBackup is a set made by the backup action.
	BackupKindBackup = "backup"
	// BackupKindPreRestore is the copy a restore takes of the data it
	// replaces.
	BackupKindPreRestore = "pre-restore"
	// BackupKindSelf is a backup of LCM's own database.
	BackupKindSelf = "self"
	// BackupKindRestore is a restore operation; BackupID is the set it
	// restored.
	BackupKindRestore = "restore"
)

// BackupRecord is one row of the backups table: a backup set (BackupID is
// the ID the deploy helper gave it), or a restore operation.
type BackupRecord struct {
	ID       int64
	Resource string
	Kind     string
	BackupID string
	// StartedAt and FinishedAt bracket the operation; FinishedAt is zero
	// while it runs.
	StartedAt  time.Time
	FinishedAt time.Time
	Outcome    string
	Size       int64
	Sources    []model.BackupSource
	// ManifestSHA256 is the checksum of the set's manifest.json; a restore
	// sends it back so that the helper can tell a replaced set.
	ManifestSHA256 string
	// Skipped counts entries of file sources that were not regular files or
	// directories and are not in the set.
	Skipped     int
	OperationID string
	Actor       auth.Actor
	RunID       string
	Error       string
	// PrunedAt is set when retention removed the set from disk.
	PrunedAt time.Time
}

// Restorable reports whether the set can be restored: a finished backup
// that retention has not removed.
func (r BackupRecord) Restorable() bool {
	return r.Outcome == OutcomeSucceeded && r.PrunedAt.IsZero() && r.BackupID != "" &&
		(r.Kind == BackupKindBackup || r.Kind == BackupKindPreRestore)
}

var backupIDRe = regexp.MustCompile(`^(?:pre-restore-)?[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

// ValidBackupID reports whether s has the form of a deploy helper backup ID.
func ValidBackupID(s string) bool { return backupIDRe.MatchString(s) }

const maxBackupError = 2 << 10

// BeginBackup records an operation as running and returns its row id.
func (c *Catalog) BeginBackup(ctx context.Context, r BackupRecord) (int64, error) {
	if r.StartedAt.IsZero() {
		r.StartedAt = c.now()
	}
	var id int64
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := insertBackup(ctx, tx, r, OutcomeRunning)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("catalog: begin backup: %w", err)
	}
	return id, nil
}

// AddBackup records a finished operation in one step.
func (c *Catalog) AddBackup(ctx context.Context, r BackupRecord) (int64, error) {
	if r.StartedAt.IsZero() {
		r.StartedAt = c.now()
	}
	if r.FinishedAt.IsZero() {
		r.FinishedAt = c.now()
	}
	var id int64
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := insertBackup(ctx, tx, r, r.Outcome)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("catalog: record backup: %w", err)
	}
	return id, nil
}

func insertBackup(ctx context.Context, tx *sql.Tx, r BackupRecord, outcome string) (sql.Result, error) {
	sources := r.Sources
	if sources == nil {
		sources = []model.BackupSource{}
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		return nil, err
	}
	var finished any
	if !r.FinishedAt.IsZero() {
		finished = r.FinishedAt.UnixMilli()
	}
	return tx.ExecContext(ctx,
		`INSERT INTO backups (resource, kind, backup_id, started_at, finished_at, outcome, size, sources, manifest_sha256, skipped,
		   operation_id, actor_kind, actor_id, actor_name, run_id, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Resource, r.Kind, r.BackupID, r.StartedAt.UnixMilli(), finished, outcome, r.Size, string(raw), r.ManifestSHA256, r.Skipped,
		r.OperationID, string(r.Actor.Kind), r.Actor.ID, r.Actor.Name, r.RunID, headCap(r.Error, maxBackupError))
}

// FinishBackup closes a running record with its outcome and, for a set that
// was made, its details. Only running records can be finished.
func (c *Catalog) FinishBackup(ctx context.Context, id int64, outcome string, r BackupRecord) error {
	switch outcome {
	case OutcomeSucceeded, OutcomeFailed, OutcomeInterrupted:
	default:
		return fmt.Errorf("catalog: invalid backup outcome %q", outcome)
	}
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE backups SET outcome = ?, finished_at = ?, backup_id = CASE WHEN ? <> '' THEN ? ELSE backup_id END,
			   size = ?, manifest_sha256 = ?, skipped = ?, error = ? WHERE id = ? AND outcome = ?`,
			outcome, c.now().UnixMilli(), r.BackupID, r.BackupID, r.Size, r.ManifestSHA256, r.Skipped, headCap(r.Error, maxBackupError), id, OutcomeRunning)
		return err
	})
	if err != nil {
		return fmt.Errorf("catalog: finish backup: %w", err)
	}
	return nil
}

// ErrBackupNotFound is returned for a backup set the catalog has no record of.
var ErrBackupNotFound = errors.New("catalog: backup not found")

const backupColumns = `id, resource, kind, backup_id, started_at, finished_at, outcome, size, sources, manifest_sha256, skipped,
	operation_id, actor_kind, actor_id, actor_name, run_id, error, pruned_at`

func scanBackup(sc interface{ Scan(...any) error }) (BackupRecord, error) {
	var (
		r        BackupRecord
		started  int64
		finished sql.NullInt64
		pruned   sql.NullInt64
		kind     string
		sources  string
	)
	if err := sc.Scan(&r.ID, &r.Resource, &r.Kind, &r.BackupID, &started, &finished, &r.Outcome, &r.Size, &sources, &r.ManifestSHA256,
		&r.Skipped, &r.OperationID, &kind, &r.Actor.ID, &r.Actor.Name, &r.RunID, &r.Error, &pruned); err != nil {
		return r, err
	}
	r.StartedAt = time.UnixMilli(started).UTC()
	if finished.Valid {
		r.FinishedAt = time.UnixMilli(finished.Int64).UTC()
	}
	if pruned.Valid {
		r.PrunedAt = time.UnixMilli(pruned.Int64).UTC()
	}
	r.Actor.Kind = auth.Kind(kind)
	if err := json.Unmarshal([]byte(sources), &r.Sources); err != nil {
		return r, fmt.Errorf("decode sources: %w", err)
	}
	return r, nil
}

// Backups lists the records of a resource newest first. before, when
// positive, returns only ids below it.
func (c *Catalog) Backups(ctx context.Context, name string, limit int, before int64) ([]BackupRecord, error) {
	return c.backups(ctx, `WHERE resource = ?`+beforeClause(before), limit, append([]any{name}, beforeArgs(before)...)...)
}

// RunningBackups lists records that have no outcome yet, across resources.
func (c *Catalog) RunningBackups(ctx context.Context) ([]BackupRecord, error) {
	return c.backups(ctx, `WHERE outcome = 'running'`, 1000)
}

func (c *Catalog) backups(ctx context.Context, where string, limit int, args ...any) ([]BackupRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := c.db.Reader().QueryContext(ctx, `SELECT `+backupColumns+` FROM backups `+where+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("catalog: query backups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BackupRecord
	for rows.Next() {
		r, err := scanBackup(rows)
		if err != nil {
			return nil, fmt.Errorf("catalog: scan backup: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: query backups: %w", err)
	}
	return out, nil
}

// Backup returns the record of a set of a resource.
func (c *Catalog) Backup(ctx context.Context, name, backupID string) (BackupRecord, error) {
	row := c.db.Reader().QueryRowContext(ctx,
		`SELECT `+backupColumns+` FROM backups WHERE resource = ? AND backup_id = ? AND kind <> 'restore'`, name, backupID)
	r, err := scanBackup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: %s of %s", ErrBackupNotFound, backupID, name)
	}
	if err != nil {
		return r, fmt.Errorf("catalog: read backup: %w", err)
	}
	return r, nil
}

// LatestBackup returns the newest restorable set made by the backup
// action.
func (c *Catalog) LatestBackup(ctx context.Context, name string) (BackupRecord, error) {
	row := c.db.Reader().QueryRowContext(ctx,
		`SELECT `+backupColumns+` FROM backups WHERE resource = ? AND kind = 'backup' AND outcome = 'succeeded' AND pruned_at IS NULL AND backup_id <> ''
		 ORDER BY id DESC LIMIT 1`, name)
	r, err := scanBackup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: %s has no backup to restore", ErrBackupNotFound, name)
	}
	if err != nil {
		return r, fmt.Errorf("catalog: read backup: %w", err)
	}
	return r, nil
}

// LastSelfBackup returns when the newest successful backup of LCM's own
// database finished, or the zero time.
func (c *Catalog) LastSelfBackup(ctx context.Context, name string) (time.Time, error) {
	var ms sql.NullInt64
	err := c.db.Reader().QueryRowContext(ctx,
		`SELECT MAX(finished_at) FROM backups WHERE resource = ? AND kind = 'self' AND outcome = 'succeeded' AND pruned_at IS NULL`, name).Scan(&ms)
	if err != nil {
		return time.Time{}, fmt.Errorf("catalog: read backups: %w", err)
	}
	if !ms.Valid {
		return time.Time{}, nil
	}
	return time.UnixMilli(ms.Int64).UTC(), nil
}

// MarkBackupsPruned records that retention removed sets from disk. Unknown
// IDs are ignored.
func (c *Catalog) MarkBackupsPruned(ctx context.Context, name string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE backups SET pruned_at = ? WHERE resource = ? AND backup_id = ? AND kind <> 'restore' AND pruned_at IS NULL`,
				c.now().UnixMilli(), name, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("catalog: mark backups pruned: %w", err)
	}
	return nil
}

// PruneBackupRows deletes finished records older than retention that no
// longer stand for a set on disk: sets retention removed, restores, and
// operations that failed or were interrupted. A succeeded set that still
// exists is kept however old. A non-positive retention keeps everything.
func (c *Catalog) PruneBackupRows(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := c.now().Add(-retention).UnixMilli()
	var n int64
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM backups WHERE outcome <> 'running' AND finished_at < ?
			   AND (pruned_at IS NOT NULL OR outcome IN ('failed', 'interrupted') OR kind = 'restore')`, cutoff)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("catalog: prune backups: %w", err)
	}
	return n, nil
}

// interruptBackups closes records that were running when the process
// stopped. The helper's ledger holds what became of the operation.
func (m *Manager) interruptBackups(ctx context.Context) error {
	recs, err := m.cat.RunningBackups(ctx)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if m.isBusy(r.Resource) {
			continue
		}
		msg := "interrupted by a restart of LCM"
		if r.OperationID != "" {
			msg += "; the deploy helper's outcome is unknown, query " + r.OperationID
		}
		if err := m.cat.FinishBackup(ctx, r.ID, OutcomeInterrupted, BackupRecord{Error: msg}); err != nil {
			return err
		}
	}
	return nil
}

// describeSources names sources for plans and results.
func describeSources(sources []model.BackupSource) string {
	parts := make([]string, len(sources))
	for i, s := range sources {
		switch s.Type {
		case model.BackupFiles, model.BackupSQLite:
			parts[i] = s.Type + " " + s.Path
		case model.BackupPostgres, model.BackupMongoDB:
			parts[i] = s.Type + " " + s.Database
			if s.All {
				parts[i] = s.Type + " (all databases)"
			}
		default:
			parts[i] = s.Type + " " + strings.Join(s.Argv, " ")
		}
	}
	return strings.Join(parts, ", ")
}
