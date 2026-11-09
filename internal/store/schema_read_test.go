package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ACaccel/lcm/internal/store"
)

func TestStoredSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lcm.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	want, err := store.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	// The controller still has the database open, as during a validation.
	got, err := store.StoredSchemaVersion(ctx, path)
	if err != nil || got != want {
		t.Fatalf("StoredSchemaVersion = %d, %v; want %d", got, err, want)
	}
}

func TestStoredSchemaVersionDoesNotCreateOrMigrate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.db")
	if _, err := store.StoredSchemaVersion(ctx, missing); err == nil {
		t.Fatal("a missing database must be an error")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the missing database was created: %v", err)
	}
	foreign := filepath.Join(dir, "foreign.db")
	if err := os.WriteFile(foreign, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoredSchemaVersion(ctx, foreign); err == nil {
		t.Fatal("a file that is not an LCM database must be an error")
	}
}
