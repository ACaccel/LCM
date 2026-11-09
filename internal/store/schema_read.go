package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
)

// StoredSchemaVersion returns the newest migration applied to the database
// file at path. It opens the file read-only and never creates, migrates or
// writes anything, so it is safe next to a running controller and on a
// database that a different build owns.
func StoredSchemaVersion(ctx context.Context, path string) (int, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, fmt.Errorf("store: resolve path: %w", err)
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "query_only(1)")
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return 0, fmt.Errorf("store: open %s: %w", abs, err)
	}
	defer func() { _ = db.Close() }()
	var newest int
	err = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&newest)
	if err != nil {
		return 0, fmt.Errorf("%w: read schema_migrations of %s: %w", ErrSchemaMismatch, abs, err)
	}
	if newest <= 0 {
		return 0, errors.New("store: no migrations applied")
	}
	return newest, nil
}
