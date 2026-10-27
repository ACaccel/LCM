// Package storetest provides a real SQLite store for tests.
package storetest

import (
	"context"
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
