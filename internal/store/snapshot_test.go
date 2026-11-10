package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestSnapshot(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('alice', 'h', 1, 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "copy.db")
	if err := db.Snapshot(ctx, dest); err != nil {
		t.Fatal(err)
	}
	// Changes after the snapshot are not in it.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('bob', 'h', 1, 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cp, err := sql.Open("sqlite", "file:"+dest+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cp.Close() }()
	var n int
	if err := cp.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("copy holds %d users, %v", n, err)
	}
	// The original keeps working, and an existing file is never overwritten.
	if err := db.Snapshot(ctx, dest); err == nil {
		t.Error("an existing file was overwritten")
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}
}
