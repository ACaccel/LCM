// Package storetest provides a real SQLite store for tests.
package storetest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ACaccel/lcm/internal/store"
)

// Open returns a migrated store backed by a file in a per-test temporary
// directory. It is closed automatically when the test ends.
func Open(t testing.TB) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lcm.db"))
	if err != nil {
		t.Fatalf("storetest: open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("storetest: close: %v", err)
		}
	})
	return db
}

// FailInserts makes every INSERT into table fail, to show that a change
// written in the same transaction rolls back with it. The returned function
// makes inserts succeed again.
func FailInserts(t testing.TB, db *store.DB, table string) (restore func()) {
	t.Helper()
	trigger := "storetest_fail_" + table
	exec := func(q string) error {
		ctx := context.Background()
		return db.Write(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, q); return err })
	}
	if err := exec(`CREATE TRIGGER ` + trigger + ` BEFORE INSERT ON ` + table +
		` BEGIN SELECT RAISE(ABORT, 'storetest: inserts into ` + table + ` fail'); END`); err != nil {
		t.Fatalf("storetest: fail inserts into %s: %v", table, err)
	}
	return func() {
		t.Helper()
		if err := exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatalf("storetest: restore inserts into %s: %v", table, err)
		}
	}
}

// QueryPlan returns the detail column of EXPLAIN QUERY PLAN for query, one
// entry per plan step, so that a test can show which index a statement
// uses.
func QueryPlan(t testing.TB, db *store.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Reader().Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("storetest: explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("storetest: explain: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("storetest: explain: %v", err)
	}
	return plan
}
