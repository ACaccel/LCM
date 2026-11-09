package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/store"
)

func TestStorageSchemaOfADatabase(t *testing.T) {
	_, _, cli := testHostRoot(t)
	want, err := store.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if r := cli("storage-schema"); r.code != 0 || strings.TrimSpace(r.stdout) != strconv.Itoa(want) {
		t.Errorf("supported schema: %+v", r)
	}
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "lcm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if r := cli("storage-schema", "--data-dir", dir); r.code != 0 || strings.TrimSpace(r.stdout) != strconv.Itoa(want) {
		t.Errorf("database schema: %+v", r)
	}
	if r := cli("storage-schema", "--data-dir", t.TempDir()); r.code != 1 {
		t.Errorf("empty data dir: %+v", r)
	}
	if r := cli("storage-schema", "extra"); r.code != 2 {
		t.Errorf("argument: %+v", r)
	}
}
