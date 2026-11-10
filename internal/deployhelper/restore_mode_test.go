package deployhelper

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRestoredDatabaseKeepsItsMode runs backup and restore through the
// helper and checks the mode of the restored database.
func TestRestoredDatabaseKeepsItsMode(t *testing.T) {
	for name, tc := range map[string]struct {
		before func(e *env)
		want   fs.FileMode
	}{
		"replaced file keeps its mode": {func(e *env) { e.chmod("/var/lib/web/app.sqlite", 0o600) }, 0o600},
		"special bits are dropped":     {func(e *env) { e.chmod("/var/lib/web/app.sqlite", 0o4750) }, 0o750},
		"removed file gets the mode recorded at backup time": {func(e *env) {
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(e.real("/var/lib/web/app.sqlite" + suffix))
			}
		}, 0o640},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.seedData()
			e.chmod("/var/lib/web/app.sqlite", 0o640)
			e.deploy(commit1)
			a := e.ok(backupReq(fileSources...)).Backup
			e.clock.Advance(time.Hour)

			tc.before(e)
			e.active = true
			r := e.restoreReq(a.BackupID)
			r.ManifestSHA256 = a.ManifestSHA256
			e.ok(r)
			fi, err := os.Stat(e.real("/var/lib/web/app.sqlite"))
			if err != nil || fi.Mode() != tc.want {
				t.Fatalf("mode = %v, %v; want %v", fi, err, tc.want)
			}
		})
	}
}

func (e *env) chmod(p string, mode fs.FileMode) {
	e.t.Helper()
	if err := os.Chmod(e.real(p), mode); err != nil {
		e.t.Fatal(err)
	}
}

// TestRestoreSQLiteModeFallbacks checks the order of the modes a restore
// uses: the file it replaces, the mode of the set, then 0640.
func TestRestoreSQLiteModeFallbacks(t *testing.T) {
	for name, tc := range map[string]struct {
		existing fs.FileMode // zero: no file to replace
		recorded fs.FileMode
		want     fs.FileMode
	}{
		"replaced file wins":       {0o600, 0o640, 0o600},
		"recorded mode":            {0, 0o660, 0o660},
		"nothing recorded":         {0, 0, 0o640},
		"recorded special bits":    {0, 0o4755, 0o640},
		"recorded mode unusable":   {0, 0o400, 0o640},
		"replaced file, no record": {0o644, 0, 0o644},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			data, in := filepath.Join(dir, "data"), filepath.Join(dir, "in")
			for _, d := range []string{data, in} {
				if err := os.Mkdir(d, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			artifact := append([]byte(sqliteHeader), make([]byte, 100)...)
			if err := os.WriteFile(filepath.Join(in, "000-sqlite.db"), artifact, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.existing != 0 {
				if err := os.WriteFile(filepath.Join(data, "app.sqlite"), []byte("old"), tc.existing); err != nil { //nolint:gosec // the mode under test
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(data, "app.sqlite"), tc.existing); err != nil {
					t.Fatal(err)
				}
			}
			dataRoot, err := os.OpenRoot(data)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dataRoot.Close() }()
			inRoot, err := os.OpenRoot(in)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = inRoot.Close() }()
			w := &worker{data: dataRoot, log: io.Discard, spec: workerSpec{MaxBytes: 1 << 20, MaxEntries: 100}}
			if err := w.restoreSQLite(inRoot, "app.sqlite", "000-sqlite.db", tc.recorded); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(filepath.Join(data, "app.sqlite"))
			if err != nil || fi.Mode() != tc.want {
				t.Fatalf("mode = %v, %v; want %v", fi, err, tc.want)
			}
		})
	}
}
