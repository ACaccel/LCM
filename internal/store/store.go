// Package store owns the SQLite database: connection setup, schema
// migrations and the transactional write helper.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ACaccel/lcm/internal/execx"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// DB wraps two handles on the same SQLite file. SQLite allows a single
// writer at a time, so writes go through a one-connection pool that
// serializes them in-process, while a separate pool serves concurrent reads
// (WAL mode keeps readers from blocking the writer).
type DB struct {
	w    *sql.DB
	r    *sql.DB
	path string
}

// ErrSchemaMismatch means an existing database was migrated by another
// version of LCM than this binary; OpenExisting leaves it untouched.
var ErrSchemaMismatch = errors.New("store: database schema does not match this binary")

// Option configures the data migrations of Open and OpenExisting.
type Option func(*migrateEnv)

// WithGit sets the git program that data migrations read repository
// configuration with; default "git".
func WithGit(binary string) Option { return func(e *migrateEnv) { e.git = binary } }

// WithWorkDir sets LCM's build directory, which holds one checkout per
// service; default the work directory beside the database file.
func WithWorkDir(dir string) Option { return func(e *migrateEnv) { e.workDir = dir } }

// migrateEnv is what data migrations may use besides the transaction.
type migrateEnv struct {
	git     string
	workDir string
	runner  execx.Runner
}

func newMigrateEnv(dbPath string, opts []Option) (migrateEnv, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return migrateEnv{}, fmt.Errorf("store: resolve path: %w", err)
	}
	e := migrateEnv{git: "git", workDir: filepath.Join(filepath.Dir(abs), "work")}
	for _, o := range opts {
		o(&e)
	}
	if !filepath.IsAbs(e.workDir) {
		return migrateEnv{}, fmt.Errorf("store: work dir %q must be absolute", e.workDir)
	}
	e.workDir = filepath.Clean(e.workDir)
	if e.runner == nil {
		e.runner = execx.New()
	}
	return e, nil
}

// Open opens (creating if necessary) the database at path with mode 0600 and
// applies pending migrations.
func Open(ctx context.Context, path string, opts ...Option) (*DB, error) {
	env, err := newMigrateEnv(path, opts)
	if err != nil {
		return nil, err
	}
	db, err := open(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db.w, env); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// OpenExisting opens the database for a command that runs beside the
// server, which owns migrations. A database without any table, including a
// file that does not exist yet, is a fresh install and is created and
// migrated as Open does. Any other database must carry exactly the
// migrations of this binary; otherwise OpenExisting returns an error
// matching ErrSchemaMismatch and changes nothing.
func OpenExisting(ctx context.Context, path string, opts ...Option) (*DB, error) {
	env, err := newMigrateEnv(path, opts)
	if err != nil {
		return nil, err
	}
	db, err := open(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := db.checkSchema(ctx, env); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) checkSchema(ctx context.Context, env migrateEnv) error {
	var tables int
	if err := d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return fmt.Errorf("store: read schema: %w", err)
	}
	if tables == 0 {
		return migrate(ctx, d.w, env)
	}
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	want := ms[len(ms)-1].version
	var applied, newest int
	err = d.r.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&applied, &newest)
	if err != nil {
		return fmt.Errorf("%w: no LCM schema found: %w", ErrSchemaMismatch, err)
	}
	switch {
	case newest > want:
		return fmt.Errorf("%w: the database is at schema %d and this lcm binary supports %d; use the lcm binary of the server that runs on it",
			ErrSchemaMismatch, newest, want)
	case newest < want || applied != len(ms):
		return fmt.Errorf("%w: the database is at schema %d and this lcm binary expects %d; upgrade the server first (lcm serve migrates at startup), then run this command again",
			ErrSchemaMismatch, newest, want)
	}
	return nil
}

func open(ctx context.Context, path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve path: %w", err)
	}
	if err := touch(abs); err != nil {
		return nil, err
	}

	w, err := sql.Open("sqlite", dsn(abs, false))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)

	r, err := sql.Open("sqlite", dsn(abs, true))
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	r.SetMaxOpenConns(max(2, min(runtime.NumCPU(), 8)))

	db := &DB{w: w, r: r, path: abs}
	if err := db.connect(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) connect(ctx context.Context) error {
	// Switching to WAL needs the writer; do it before readers connect.
	if err := d.w.PingContext(ctx); err != nil {
		return fmt.Errorf("store: connect: %w", err)
	}
	if err := d.r.PingContext(ctx); err != nil {
		return fmt.Errorf("store: connect reader: %w", err)
	}
	return nil
}

// Close closes both handles.
func (d *DB) Close() error {
	return errors.Join(d.w.Close(), d.r.Close())
}

// Ping verifies that the database answers queries.
func (d *DB) Ping(ctx context.Context) error {
	return d.r.PingContext(ctx)
}

// Snapshot writes a consistent copy of the database to dest, a file that
// must not exist, while the database stays in use. It reads through its own
// read-only connection, since VACUUM INTO is refused on the reader pool
// (query_only) and cannot run inside a write transaction.
func (d *DB) Snapshot(ctx context.Context, dest string) error {
	u := url.URL{Scheme: "file", Path: d.path, RawQuery: url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(30000)"}}.Encode()}
	c, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fmt.Errorf("store: open snapshot connection: %w", err)
	}
	defer func() { _ = c.Close() }()
	c.SetMaxOpenConns(1)
	if _, err := c.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return fmt.Errorf("store: snapshot: %w", err)
	}
	return nil
}

// Reader returns the pool for read-only queries. Statements that modify data
// are rejected by the connection (query_only).
func (d *DB) Reader() *sql.DB {
	return d.r
}

// Write runs fn in a transaction on the single writer connection. The
// transaction commits when fn returns nil and rolls back otherwise.
func (d *DB) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// touch creates the database file with owner-only permissions before SQLite
// does, since SQLite would otherwise apply the process umask. The -wal and
// -shm companions inherit the permissions of the main file.
func touch(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path comes from validated configuration
	if err != nil {
		return fmt.Errorf("store: create database file: %w", err)
	}
	return f.Close()
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		// Take the write lock at BEGIN so a transaction never fails midway
		// with SQLITE_BUSY while upgrading from a read lock.
		q.Set("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}
