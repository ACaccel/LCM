package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/store"
)

const insertBackup = `INSERT INTO backups (resource, kind, backup_id, started_at, outcome, actor_kind, actor_id, actor_name) VALUES (?, ?, ?, 1, ?, 'user', '1', 'alice')`

func TestBackupsMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lcm.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := store.SchemaVersion()
	if err != nil || want < 17 {
		t.Fatalf("SchemaVersion = %d, %v", want, err)
	}
	if got := migrationVersions(t, db); got[len(got)-1] != want || !contains(got, 17) {
		t.Fatalf("versions = %v", got)
	}
	exec := func(q string, args ...any) error {
		return db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		})
	}
	if err := exec(insertBackup, "web", "backup", "20261101T120000Z-0a1b2c3d", "succeeded"); err != nil {
		t.Fatal(err)
	}
	// A set exists once per resource, but other resources, restores and
	// rows without an ID repeat.
	for name, tc := range map[string]struct {
		args []any
		ok   bool
	}{
		"same set again":         {[]any{"web", "pre-restore", "20261101T120000Z-0a1b2c3d", "succeeded"}, false},
		"same ID, other service": {[]any{"api", "backup", "20261101T120000Z-0a1b2c3d", "succeeded"}, true},
		"a restore of the set":   {[]any{"web", "restore", "20261101T120000Z-0a1b2c3d", "running"}, true},
		"a second restore":       {[]any{"web", "restore", "20261101T120000Z-0a1b2c3d", "failed"}, true},
		"rows without an ID":     {[]any{"web", "backup", "", "failed"}, true},
		"again without an ID":    {[]any{"web", "backup", "", "running"}, true},
		"unknown kind":           {[]any{"web", "snapshot", "x", "succeeded"}, false},
		"unknown outcome":        {[]any{"web", "backup", "y", "done"}, false},
	} {
		err := exec(insertBackup, tc.args...)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok %v", name, err, tc.ok)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A database at schema 16 gets the table and keeps what it had.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE backups`, `DELETE FROM schema_migrations WHERE version = 17`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.Reader().QueryRow(`SELECT COUNT(*) FROM backups`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("backups after the upgrade: %d %v", n, err)
	}
	if got := migrationVersions(t, db); !contains(got, 17) {
		t.Fatalf("versions = %v", got)
	}
	var plan string
	if err := db.Reader().QueryRow(`EXPLAIN QUERY PLAN SELECT * FROM backups WHERE resource = 'web' ORDER BY id DESC LIMIT 10`).Scan(new(int), new(int), new(int), &plan); err != nil ||
		!strings.Contains(plan, "backups_resource") {
		t.Errorf("history query plan: %q %v", plan, err)
	}
}

func contains(vs []int, v int) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}
