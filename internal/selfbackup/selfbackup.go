// Package selfbackup backs up LCM's own state: a consistent copy of the
// SQLite database and the secret files, written by the server itself into a
// private directory of its data directory. It needs no privileges and does
// not use the deploy helper, so it also works on hosts without the deploy
// component. Each backup is recorded in the same table as the typed backups
// of services, under the resource name "lcm".
//
// Layout of <dir>/<id>/ (mode 0700, id is the UTC time and eight hex
// digits): lcm.db, secrets/<name> (mode 0600) and manifest.json with the
// size and SHA-256 of every file. A backup is written to <id>.tmp and
// renamed when complete. There is no restore operation: a running server
// cannot replace its own database; docs/operations.md describes the manual
// procedure.
package selfbackup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/store"
)

// Resource is the name the backups are recorded under.
const Resource = "lcm"

const (
	manifestName = "manifest.json"
	dbName       = "lcm.db"
	secretsName  = "secrets"
	tmpSuffix    = ".tmp"
	// maxSecretSize and maxSecrets bound what is copied of the secrets
	// directory; real secrets are far smaller (see package secret).
	maxSecretSize = 64 << 10
	maxSecrets    = 256
)

var idPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

// Recorder stores the backup records; *catalog.Catalog implements it.
type Recorder interface {
	AddBackup(ctx context.Context, r catalog.BackupRecord) (int64, error)
	MarkBackupsPruned(ctx context.Context, name string, ids []string) error
	LastSelfBackup(ctx context.Context, name string) (time.Time, error)
}

var _ Recorder = (*catalog.Catalog)(nil)

// Config configures a Backuper.
type Config struct {
	DB *store.DB
	// Dir is the backup directory, created with mode 0700.
	Dir string
	// SecretsDir holds the secret files to copy; it may not exist.
	SecretsDir string
	// Keep is how many UTC days keep their newest backup.
	Keep int
	// MinInterval is the shortest time between two backups; Run does
	// nothing sooner after the last successful one.
	MinInterval time.Duration
	Recorder    Recorder
	Audit       *audit.Log
	Clock       clock.Clock
	Log         *slog.Logger
}

// Backuper makes the backups.
type Backuper struct {
	cfg Config
}

// New validates cfg and returns a Backuper.
func New(cfg Config) (*Backuper, error) {
	switch {
	case cfg.DB == nil || cfg.Recorder == nil || cfg.Audit == nil:
		return nil, errors.New("selfbackup: DB, Recorder and Audit are required")
	case !filepath.IsAbs(cfg.Dir) || filepath.Clean(cfg.Dir) != cfg.Dir:
		return nil, fmt.Errorf("selfbackup: Dir %q must be a clean absolute path", cfg.Dir)
	case cfg.Keep < 1:
		return nil, errors.New("selfbackup: Keep must be at least 1")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Backuper{cfg: cfg}, nil
}

type manifestFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Version   int            `json:"version"`
	BackupID  string         `json:"backupId"`
	CreatedAt int64          `json:"createdAt"`
	Schema    int            `json:"schema"`
	Files     []manifestFile `json:"files"`
}

// Result describes a backup that was made.
type Result struct {
	BackupID       string
	Size           int64
	ManifestSHA256 string
	Pruned         []string
}

// ErrTooSoon is returned by Run when the last backup is younger than
// MinInterval.
var ErrTooSoon = errors.New("selfbackup: the last backup is recent enough")

// Run makes a backup unless one was made within MinInterval, records it and
// applies the retention. A failed backup is recorded as failed and
// audited; it never affects the server.
func (b *Backuper) Run(ctx context.Context) (Result, error) {
	ctx = withSystemActor(ctx)
	now := b.cfg.Clock.Now()
	if b.cfg.MinInterval > 0 {
		last, err := b.cfg.Recorder.LastSelfBackup(ctx, Resource)
		if err != nil {
			return Result{}, err
		}
		if !last.IsZero() && now.Sub(last) < b.cfg.MinInterval {
			return Result{}, ErrTooSoon
		}
	}
	res, err := b.backup(ctx, now)
	outcome, details := audit.OutcomeOK, map[string]any{"backupId": res.BackupID, "size": res.Size}
	if err != nil {
		outcome, details["error"] = audit.OutcomeError, err.Error()
		rec := catalog.BackupRecord{Resource: Resource, Kind: catalog.BackupKindSelf, StartedAt: now, Outcome: catalog.OutcomeFailed,
			Actor: systemActor, Error: err.Error()}
		if _, rerr := b.cfg.Recorder.AddBackup(ctx, rec); rerr != nil {
			b.cfg.Log.Error("record failed self backup failed", "error", rerr)
		}
	}
	if aerr := b.cfg.Audit.Record(ctx, "lcm.backup", Resource, outcome, details); aerr != nil {
		b.cfg.Log.Error("audit self backup failed", "error", aerr)
	}
	if err != nil {
		return res, err
	}
	res.Pruned, err = b.prune(ctx)
	if err != nil {
		b.cfg.Log.Warn("prune self backups failed", "error", err)
	}
	return res, nil
}

var systemActor = auth.Actor{Kind: auth.KindSystem, ID: "lcm", Name: "lcm"}

func withSystemActor(ctx context.Context) context.Context {
	if _, ok := auth.ActorFrom(ctx); ok {
		return ctx
	}
	return auth.WithActor(ctx, systemActor)
}

// ensureDir creates the backup directory with mode 0700, or checks that it
// is one: no link, ours, and closed to everybody else.
func (b *Backuper) ensureDir() error {
	if err := os.MkdirAll(b.cfg.Dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(b.cfg.Dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a directory", b.cfg.Dir)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(b.cfg.Dir, 0o700); err != nil { //nolint:gosec // a private directory needs the owner's execute bit
			return err
		}
	}
	return nil
}

func (b *Backuper) backup(ctx context.Context, now time.Time) (res Result, err error) {
	if err := b.ensureDir(); err != nil {
		return res, fmt.Errorf("backup directory: %w", err)
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:]) // crypto/rand does not fail on supported platforms
	id := now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(rnd[:])
	tmp := filepath.Join(b.cfg.Dir, id+tmpSuffix)
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return res, err
	}
	m := manifest{Version: 1, BackupID: id, CreatedAt: now.UnixMilli()}
	if m.Schema, err = store.SchemaVersion(); err != nil {
		return res, err
	}
	// VACUUM INTO writes a consistent snapshot while the server runs.
	dst := filepath.Join(tmp, dbName)
	if err := b.cfg.DB.Snapshot(ctx, dst); err != nil {
		return res, fmt.Errorf("copy the database: %w", err)
	}
	if err := checkCopy(dst); err != nil {
		return res, err
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return res, err
	}
	f, err := fileEntry(tmp, dbName)
	if err != nil {
		return res, err
	}
	m.Files = append(m.Files, f)
	secrets, err := b.copySecrets(tmp)
	if err != nil {
		return res, fmt.Errorf("copy the secrets: %w", err)
	}
	m.Files = append(m.Files, secrets...)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return res, err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(tmp, manifestName), raw, 0o600); err != nil {
		return res, err
	}
	if err := os.Rename(tmp, filepath.Join(b.cfg.Dir, id)); err != nil {
		return res, err
	}
	sum := sha256.Sum256(raw)
	res = Result{BackupID: id, ManifestSHA256: hex.EncodeToString(sum[:])}
	for _, f := range m.Files {
		res.Size += f.Size
	}
	rec := catalog.BackupRecord{
		Resource: Resource, Kind: catalog.BackupKindSelf, BackupID: id, StartedAt: now, Outcome: catalog.OutcomeSucceeded,
		Size: res.Size, ManifestSHA256: res.ManifestSHA256, Actor: systemActor,
		Sources: []model.BackupSource{{Type: model.BackupSQLite, Path: dbName}, {Type: model.BackupFiles, Path: secretsName}},
	}
	if _, err := b.cfg.Recorder.AddBackup(ctx, rec); err != nil {
		return res, fmt.Errorf("the backup %s was made, but recording it failed: %w", id, err)
	}
	return res, nil
}

// checkCopy opens the copy and runs the quick integrity check.
func checkCopy(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var verdict string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&verdict); err != nil {
		return fmt.Errorf("check the copy: %w", err)
	}
	if verdict != "ok" {
		return fmt.Errorf("the copy failed its integrity check: %s", verdict)
	}
	return nil
}

// fileEntry describes the file rel of dir.
func fileEntry(dir, rel string) (manifestFile, error) {
	f, err := os.Open(filepath.Join(dir, rel)) //nolint:gosec // a file this package just wrote
	if err != nil {
		return manifestFile{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return manifestFile{}, err
	}
	return manifestFile{Name: rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// copySecrets copies the regular files of the secrets directory that have
// valid secret names. The directory is read through a root, so a link in it
// never leads out; other entries are left out.
func (b *Backuper) copySecrets(tmp string) ([]manifestFile, error) {
	if b.cfg.SecretsDir == "" {
		return nil, nil
	}
	root, err := os.OpenRoot(b.cfg.SecretsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	d, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	out := filepath.Join(tmp, secretsName)
	if err := os.Mkdir(out, 0o700); err != nil {
		return nil, err
	}
	var files []manifestFile
	for _, name := range names {
		if !secret.ValidName(name) {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSecretSize {
			continue
		}
		if len(files) >= maxSecrets {
			return nil, fmt.Errorf("more than %d secrets", maxSecrets)
		}
		src, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		dst, err := os.OpenFile(filepath.Join(out, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the name passed secret.ValidName
		if err != nil {
			_ = src.Close()
			return nil, err
		}
		_, err = io.Copy(dst, io.LimitReader(src, maxSecretSize))
		_ = src.Close()
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		f, err := fileEntry(tmp, filepath.Join(secretsName, name))
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// prune keeps the newest backup of each of the last Keep days, and always
// the newest. Directories that are not backups are left alone, apart from
// the leftovers of backups that did not finish.
func (b *Backuper) prune(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(b.cfg.Dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && idPattern.MatchString(name):
			ids = append(ids, name)
		case e.IsDir() && strings.HasSuffix(name, tmpSuffix) && idPattern.MatchString(strings.TrimSuffix(name, tmpSuffix)):
			if err := os.RemoveAll(filepath.Join(b.cfg.Dir, name)); err != nil {
				return nil, err
			}
		}
	}
	slices.Sort(ids)
	slices.Reverse(ids) // newest first: the names start with the UTC time
	keep, days := map[string]bool{}, map[string]bool{}
	for i, id := range ids {
		day := id[:8]
		if i == 0 || (!days[day] && len(days) < b.cfg.Keep) {
			keep[id] = true
		}
		if keep[id] {
			days[day] = true
		}
	}
	var removed []string
	for _, id := range ids {
		if keep[id] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(b.cfg.Dir, id)); err != nil {
			return removed, err
		}
		removed = append(removed, id)
	}
	if err := b.cfg.Recorder.MarkBackupsPruned(ctx, Resource, removed); err != nil {
		return removed, err
	}
	return removed, nil
}
