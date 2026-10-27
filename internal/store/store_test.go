package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func migrationVersions(t *testing.T, db *store.DB) []int {
	t.Helper()
	rows, err := db.Reader().QueryContext(context.Background(), `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var vs []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return vs
}

func TestMigrationsApplyOnFreshDB(t *testing.T) {
	db := storetest.Open(t)
	if got := migrationVersions(t, db); len(got) == 0 || got[0] != 1 {
		t.Fatalf("versions = %v, want to start with 1", got)
	}
	for _, table := range []string{"users", "sessions", "api_tokens", "audit_log"} {
		var n int
		if err := db.Reader().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestMigrationsIdempotentOnReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lcm.db")

	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first := migrationVersions(t, db)
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('a', 'h', 1, 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db.Close() }()
	second := migrationVersions(t, db)
	if len(first) != len(second) {
		t.Fatalf("versions changed on reopen: %v -> %v", first, second)
	}
	var n int
	if err := db.Reader().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("data lost on reopen: n=%d err=%v", n, err)
	}
}

func TestFilePermissions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lcm.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Force WAL/SHM files into existence.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('a', 'h', 1, 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		st, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("stat %s: %v", suffix, err)
		}
		if got := st.Mode().Perm(); got&0o077 != 0 {
			t.Errorf("%s mode = %o, want no group/other access", path+suffix, got)
		}
	}
}

func TestWriteRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	boom := errors.New("boom")
	err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('a', 'h', 1, 1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var n int
	if err := db.Reader().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback failed: n=%d err=%v", n, err)
	}
}

func TestReaderRejectsWrites(t *testing.T) {
	db := storetest.Open(t)
	_, err := db.Reader().Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('a', 'h', 1, 1)`)
	if err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (x'00', 99, 1, 2)`)
		return err
	})
	if err == nil {
		t.Fatal("foreign key violation was accepted")
	}
}

func TestPing(t *testing.T) {
	if err := storetest.Open(t).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
