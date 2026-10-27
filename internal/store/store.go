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

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// DB wraps two handles on the same SQLite file. SQLite allows a single
// writer at a time, so writes go through a one-connection pool that
// serializes them in-process, while a separate pool serves concurrent reads
// (WAL mode keeps readers from blocking the writer).
type DB struct {
	w *sql.DB
	r *sql.DB
}

// Open opens (creating if necessary) the database at path with mode 0600 and
// applies pending migrations.
func Open(ctx context.Context, path string) (*DB, error) {
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

	db := &DB{w: w, r: r}
	if err := db.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) init(ctx context.Context) error {
	// Switching to WAL needs the writer; do it before readers connect.
	if err := d.w.PingContext(ctx); err != nil {
		return fmt.Errorf("store: connect: %w", err)
	}
	if err := d.r.PingContext(ctx); err != nil {
		return fmt.Errorf("store: connect reader: %w", err)
	}
	if err := migrate(ctx, d.w); err != nil {
		return err
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
