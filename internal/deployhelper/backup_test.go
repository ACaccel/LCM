package deployhelper

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// installTool writes a test program as a trusted tool of the host and
// returns its real path.
func (e *env) installTool(name, body string) string {
	e.t.Helper()
	e.mkdir("/usr/local/bin", 0o755)
	e.writeReal(e.real("/usr/local/bin/"+name), "#!/bin/sh\n"+body, 0o755)
	return e.real("/usr/local/bin/" + name)
}

// uninstallTools removes the programs installed with installTool, which
// e.tools lists.
func (e *env) uninstallTools() {
	for _, p := range e.tools {
		_ = os.Remove(p)
	}
	e.tools = nil
}

// worker runs the backup worker in place of systemd-run, over the staging
// directories the unit would bind.
func (e *env) worker(c execx.Cmd) (string, int) {
	var in, out string
	restore := strings.HasPrefix(arg(c, "--unit="), "lcm-restore-")
	for _, a := range c.Argv {
		if v, ok := strings.CutPrefix(a, "--property=BindReadOnlyPaths="); ok {
			in = v
		}
		if v, ok := strings.CutPrefix(a, "--property=BindPaths="); ok {
			out = v
		}
	}
	run := func() (string, int) {
		var buf bytes.Buffer
		host := WorkerHost{
			DataDir:    func(s string) string { return e.real("/var/lib/" + s) },
			ReleaseDir: func(s string) string { return e.real("/opt/" + s + "/current") },
			ToolPath:   e.real,
		}
		if err := RunWorker(context.Background(), host, e.real(in), e.real(out), &buf); err != nil {
			return buf.String() + "error: " + err.Error() + "\n", 1
		}
		return buf.String(), 0
	}
	mode := "backup"
	if restore {
		mode = "restore"
	}
	if e.workerWrap != nil {
		return e.workerWrap(mode, run)
	}
	return run()
}

func (e *env) sqliteDB(p string, rows ...string) {
	e.t.Helper()
	e.sqliteDBMode(p, "", rows...)
}

// sqliteDBMode writes the rows to a database in the given journal mode.
func (e *env) sqliteDBMode(p, journal string, rows ...string) {
	e.t.Helper()
	e.mkdirReal(filepath.Dir(e.real(p)))
	db, err := sql.Open("sqlite", "file:"+e.real(p))
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if journal != "" {
		if _, err := db.Exec("PRAGMA journal_mode=" + journal); err != nil {
			e.t.Fatal(err)
		}
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS t (v TEXT)"); err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM t"); err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec("INSERT INTO t VALUES (?)", r); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) sqliteRows(real string) []string {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+real+"?mode=ro")
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rs, err := db.Query("SELECT v FROM t ORDER BY v")
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	var out []string
	for rs.Next() {
		var v string
		if err := rs.Scan(&v); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// seedData fills the web service's data directory.
func (e *env) seedData() {
	e.write("/var/lib/web/media/a.txt", "alpha", 0o644)
	e.write("/var/lib/web/media/sub/b.bin", "bravo", 0o755)
	e.mkdir("/var/lib/web/media/empty", 0o755)
	if err := os.Symlink("a.txt", e.real("/var/lib/web/media/link")); err != nil {
		e.t.Fatal(err)
	}
	e.sqliteDB("/var/lib/web/app.sqlite", "one", "two")
}

var fileSources = []BackupSource{{Type: SourceFiles, Path: "media"}, {Type: SourceSQLite, Path: "app.sqlite"}}

func backupReq(sources ...BackupSource) Request {
	return Request{Op: OpBackup, Service: "web", Sources: sources, TimeoutSec: 600}
}

func (e *env) readTar(p string) map[string]string {
	e.t.Helper()
	f, err := os.Open(e.real(p)) //nolint:gosec // a file of the test's own tree
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			e.t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[fmt.Sprintf("%s|%o", h.Name, h.Mode)] = string(b)
	}
}

func TestBackupFilesAndSQLite(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.deploy(commit1)
	resp := e.ok(backupReq(fileSources...))
	b := resp.Backup
	if b == nil || !backupPattern.MatchString(b.BackupID) || b.Skipped != 1 || b.Files != 2 || b.CreatedAt != e.clock.Now().UnixMilli() || b.ReleaseID == "" {
		t.Fatalf("backup result %+v", b)
	}
	set := "/var/backups/lcm/web/" + b.BackupID
	raw := e.read(set + "/manifest.json")
	sum := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(sum[:]) != b.ManifestSHA256 {
		t.Errorf("manifest checksum %s, reply %s", hex.EncodeToString(sum[:]), b.ManifestSHA256)
	}
	var m setManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m.validate("web", b.BackupID) != nil {
		t.Fatalf("manifest %v %v: %s", err, m.validate("web", b.BackupID), raw)
	}
	if m.Kind != KindBackup || m.Mode != ModeOnline || m.Skipped != 1 || m.Size != b.Size || len(m.Files) != 2 {
		t.Errorf("manifest %+v", m)
	}
	for _, f := range m.Files {
		data := e.read(set + "/" + f.Name)
		s := sha256.Sum256([]byte(data))
		if int64(len(data)) != f.Size || hex.EncodeToString(s[:]) != f.SHA256 {
			t.Errorf("%s does not match its manifest entry", f.Name)
		}
		if fi, err := os.Stat(e.real(set + "/" + f.Name)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v %v", f.Name, fi, err)
		}
	}
	if fi, err := os.Stat(e.real(set)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("set mode %v %v", fi, err)
	}
	entries := e.readTar(set + "/000-files.tar")
	for _, want := range []string{"media/|755", "media/a.txt|644", "media/sub/|755", "media/sub/b.bin|755", "media/empty/|755"} {
		if _, ok := entries[want]; !ok {
			t.Errorf("archive lacks %s: %v", want, entries)
		}
	}
	if entries["media/a.txt|644"] != "alpha" || len(entries) != 5 {
		t.Errorf("archive %v", entries)
	}
	if rows := e.sqliteRows(e.real(set + "/001-sqlite.db")); !slices.Equal(rows, []string{"one", "two"}) {
		t.Errorf("sqlite copy %v", rows)
	}
	// Nothing is left behind, and the directory holds only the set.
	names, _ := os.ReadDir(e.real("/var/backups/lcm/web"))
	if len(names) != 1 {
		t.Errorf("backup directory: %v", names)
	}

	argv := e.runner.Calls()
	var worker []string
	for _, c := range argv {
		if len(c.Argv) > 1 && strings.HasPrefix(arg(c, "--unit="), "lcm-backup-web-") {
			worker = c.Argv
		}
	}
	stage := "/var/backups/lcm/web/.stage-"
	for _, want := range []string{
		"--uid=" + fmt.Sprint(os.Getuid()), "--property=ProtectSystem=strict", "--property=NoNewPrivileges=yes", "--property=PrivateDevices=yes",
		"--property=SocketBindDeny=any", "--property=EnvironmentFile=/etc/web/env", "--property=RuntimeMaxSec=600", "--property=MemoryMax=2G",
		"--property=TemporaryFileSystem=/var/backups/lcm:ro", "--property=LimitFSIZE=34359738368", "--property=UMask=0077",
		"--property=InaccessiblePaths=-/var/lib/lcm -/etc/lcm -/var/lib/lcm-deploy -/var/lib/lcm-machine-power",
	} {
		if !slices.Contains(worker, want) {
			t.Errorf("worker argv lacks %s: %v", want, worker)
		}
	}
	// The data directory is writable for the worker's account so that a
	// read-only SQLite connection can create the -shm of a WAL database.
	if !slices.Contains(worker, "--property=ReadWritePaths=/var/lib/web") {
		t.Errorf("the data directory is not a read-write path: %v", worker)
	}
	if !slices.ContainsFunc(worker, func(a string) bool {
		return strings.HasPrefix(a, "--property=BindReadOnlyPaths="+stage) && strings.HasSuffix(a, "/in")
	}) ||
		!slices.ContainsFunc(worker, func(a string) bool {
			return strings.HasPrefix(a, "--property=ReadWritePaths="+stage) && strings.HasSuffix(a, "/out")
		}) {
		t.Errorf("staging mounts: %v", worker)
	}
	if n := len(worker); n < 4 || worker[n-4] != "/usr/local/libexec/lcm-deploy-helper" || worker[n-3] != "backup-worker" {
		t.Errorf("worker command %v", worker)
	}
}

// A WAL database that was closed cleanly has no -wal or -shm file. The
// worker's read-only connection must create the -shm, which needs a
// writable directory: the sandbox grants one.
func TestBackupOfACleanWALDatabase(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.sqliteDBMode("/var/lib/web/wal.sqlite", "WAL", "x", "y")
	for _, suffix := range []string{"-wal", "-shm"} {
		if e.exists("/var/lib/web/wal.sqlite" + suffix) {
			t.Fatalf("a cleanly closed database left its %s file", suffix)
		}
	}
	for _, mode := range []string{ModeOnline, ModeStopped} {
		r := backupReq(BackupSource{Type: SourceSQLite, Path: "wal.sqlite"})
		r.Mode = mode
		resp := e.ok(r)
		if rows := e.sqliteRows(e.real("/var/backups/lcm/web/" + resp.Backup.BackupID + "/000-sqlite.db")); !slices.Equal(rows, []string{"x", "y"}) {
			t.Errorf("%s: copy %v", mode, rows)
		}
	}

	// Without a writable directory the same copy fails, which is what the
	// sandbox's read-write path prevents. The read-only connection leaves the
	// files it created; removing them gives the clean state again.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(e.real("/var/lib/web/wal.sqlite" + suffix))
	}
	if err := os.Chmod(e.real("/var/lib/web"), 0o555); err != nil { //nolint:gosec // a read-only directory, to show the failure
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(e.real("/var/lib/web"), 0o755) }() //nolint:gosec // restored for the cleanup
	e.refused(backupReq(BackupSource{Type: SourceSQLite, Path: "wal.sqlite"}), CodeFailed, "the backup worker exited with status 1")
}

// The leftovers of an interrupted operation are swept by the next changing
// one, whether or not it finishes, not only by prune.
func TestChangingOperationsSweepLeftovers(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	leftovers := func() {
		e.mkdir("/var/backups/lcm/web/.stage-deadbeefb/out", 0o700)
		e.write("/var/backups/lcm/web/.stage-deadbeefb/out/big", "x", 0o600)
		e.mkdir("/var/backups/lcm/web/.tmp-deadbeefb", 0o700)
		e.mkdir("/var/backups/lcm/web/.del-20200101T000000Z-00000000", 0o700)
		// mkdir loosens the chain above; the backup directory is root's alone.
		for _, d := range []string{"/var/backups/lcm", "/var/backups/lcm/web"} {
			if err := os.Chmod(e.real(d), 0o700); err != nil { //nolint:gosec // a directory of the test host
				t.Fatal(err)
			}
		}
	}
	left := func() []string {
		var out []string
		names, _ := os.ReadDir(e.real("/var/backups/lcm/web"))
		for _, n := range names {
			if strings.HasPrefix(n.Name(), ".") {
				out = append(out, n.Name())
			}
		}
		return out
	}
	leftovers()
	a := e.ok(backupReq(fileSources...)).Backup
	if l := left(); len(l) != 0 {
		t.Errorf("a backup left %v", l)
	}
	leftovers()
	// Even a refused restore clears them (an unknown set, after the sweep).
	e.refused(e.restoreReq("20200101T000000Z-00000000"), CodeNotFound, "there is no backup")
	if l := left(); len(l) != 0 {
		t.Errorf("a restore left %v", l)
	}
	leftovers()
	e.ok(e.restoreReq(a.BackupID))
	if l := left(); len(l) != 0 {
		t.Errorf("a restore left %v", l)
	}
	// Listing changes nothing.
	leftovers()
	e.ok(Request{Op: OpBackups, Service: "web"})
	if l := left(); len(l) != 3 {
		t.Errorf("a listing removed %v", l)
	}
}

func TestBackupStoppedModeStopsAndStarts(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.deploy(commit1)
	e.active = true
	r := backupReq(fileSources...)
	r.Mode = ModeStopped
	resp := e.ok(r)
	calls := e.systemctlCalls()
	stop, start := slices.Index(calls, "stop web.service"), slices.Index(calls, "start web.service")
	if stop < 0 || start < stop || !e.active {
		t.Errorf("systemctl calls %v, active %v", calls, e.active)
	}
	if info := e.call(Request{Op: OpBackups, Service: "web"}); len(info.Backups) != 1 || info.Backups[0].Mode != ModeStopped || info.Backups[0].BackupID != resp.Backup.BackupID {
		t.Errorf("listing %+v", info.Backups)
	}

	// The service starts again after a failed backup too.
	e.workerWrap = func(string, func() (string, int)) (string, int) { return "boom\n", 1 }
	r.Sources = []BackupSource{{Type: SourceFiles, Path: "media"}}
	failed := e.refused(r, CodeFailed, "the backup worker exited with status 1")
	if !e.active || failed.Log != "boom\n" {
		t.Errorf("active %v log %q", e.active, failed.Log)
	}
	// An inactive service is stopped again to be sure, and left stopped.
	e.active, e.workerWrap = false, nil
	before := len(e.systemctlCalls())
	e.ok(r)
	for _, c := range e.systemctlCalls()[before:] {
		if strings.HasPrefix(c, "start web") {
			t.Errorf("unexpected %s", c)
		}
	}
	if e.active {
		t.Error("an inactive service was started")
	}
}

// A unit that waits to restart after a crash is not "active", but it would
// start the service again in the middle of a stopped backup.
func TestStoppedBackupStopsACrashLoopingUnit(t *testing.T) {
	for _, state := range []string{"activating", "reloading"} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			e.seedData()
			e.deploy(commit1)
			e.activeState = state
			r := backupReq(fileSources...)
			r.Mode = ModeStopped
			before := len(e.systemctlCalls())
			e.workerWrap = func(_ string, run func() (string, int)) (string, int) {
				if e.activeState != "" || e.active {
					e.t.Error("the unit was not stopped while the worker ran")
				}
				return run()
			}
			e.ok(r)
			calls := e.systemctlCalls()[before:]
			stop, start := slices.Index(calls, "stop web.service"), slices.Index(calls, "start web.service")
			if stop < 0 || start < stop || !e.active {
				t.Errorf("systemctl calls %v, active %v", calls, e.active)
			}
		})
	}
	t.Run("failed units stay stopped", func(t *testing.T) {
		e := newEnv(t)
		e.seedData()
		e.activeState = "failed"
		r := backupReq(fileSources...)
		r.Mode = ModeStopped
		e.ok(r)
		if slices.Contains(e.systemctlCalls(), "start web.service") {
			t.Error("a failed unit was started")
		}
	})
}

func TestStoppedBackupRefusesAUnitThatStaysUp(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.deploy(commit1)
	e.activeState, e.stopStuck = "activating", true
	r := backupReq(fileSources...)
	r.Mode = ModeStopped
	workers := 0
	e.workerWrap = func(_ string, run func() (string, int)) (string, int) { workers++; return run() }
	e.refused(r, CodeRefused, "after being stopped")
	if workers != 0 {
		t.Error("the worker ran against a unit that was not stopped")
	}
	if names, _ := os.ReadDir(e.real("/var/backups/lcm/web")); len(names) != 0 {
		t.Errorf("left %v", names)
	}
	// It was started again, as it had been running.
	if calls := e.systemctlCalls(); calls[len(calls)-1] != "start web.service" {
		t.Errorf("systemctl calls %v", calls)
	}
}

func TestBackupRefusals(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.deploy(commit1)
	e.refused(Request{Op: OpBackup, Service: "api", Sources: fileSources, TimeoutSec: 60}, CodeRefused, "api is not in the services")
	e.refused(Request{Op: OpBackup, Service: "web", Sources: []BackupSource{{Type: SourceFiles, Path: "nope"}}, TimeoutSec: 60, Runtime: "python3"}, CodeRefused, "the runtime python3 is not granted")
	// A missing path fails the backup and leaves nothing.
	e.refused(backupReq(BackupSource{Type: SourceFiles, Path: "nope"}), CodeFailed, "the backup worker exited with status 1")
	e.refused(backupReq(BackupSource{Type: SourceSQLite, Path: "media/a.txt"}), CodeFailed, "the backup worker exited with status 1")
	if names, _ := os.ReadDir(e.real("/var/backups/lcm/web")); len(names) != 0 {
		t.Errorf("a failed backup left %v", names)
	}
	// An unusable backup directory.
	if err := os.Chmod(e.real("/var/backups/lcm"), 0o755); err != nil { //nolint:gosec // a directory that others may enter, to be refused
		t.Fatal(err)
	}
	e.refused(backupReq(fileSources...), CodeRefused, "must be accessible by root only")
	if err := os.Chmod(e.real("/var/backups/lcm"), 0o700); err != nil { //nolint:gosec // a directory of the test host
		t.Fatal(err)
	}
	e.cfg.Backup = nil
	e.refused(backupReq(fileSources...), CodeRefused, "backup.dir is not set")
	e.refused(Request{Op: OpBackups, Service: "web"}, CodeRefused, "backup.dir is not set")
	e.cfg.Backup = nil
	e.cfg.Components.Deploy = false
	e.refused(backupReq(fileSources...), CodeRefused, "components.deploy is false")
}

// A worker belongs to the service account, so everything it leaves is
// hostile input.
func TestBackupDoesNotTrustTheWorkersOutput(t *testing.T) {
	secret := "/etc/secret"
	for name, tamper := range map[string]struct {
		do   func(e *env, out string)
		want string
	}{
		"a link in place of an artifact": {func(e *env, out string) {
			_ = os.Remove(out + "/000-files.tar")
			if err := os.Symlink(e.real(secret), out+"/000-files.tar"); err != nil {
				e.t.Fatal(err)
			}
		}, "is not a regular file"},
		"a missing artifact": {func(e *env, out string) { _ = os.Remove(out + "/001-sqlite.db") }, "did not produce 001-sqlite.db"},
		"a missing result":   {func(e *env, out string) { _ = os.Remove(out + "/result.json") }, "left no result"},
		"a malformed result": {func(e *env, out string) {
			e.writeReal(out+"/result.json", `{"version":1,"sources":[{"absent":true},{}],"extra":1}`, 0o600)
		}, "result is not valid"},
		"an absent source of a real backup": {func(e *env, out string) {
			e.writeReal(out+"/result.json", `{"version":1,"sources":[{"absent":true},{}]}`, 0o600)
		}, "reports files media as absent"},
		"an extra file": {func(e *env, out string) {
			e.writeReal(out+"/leftover", "x", 0o600)
		}, "unexpected entry \"leftover\""},
		"a subdirectory": {func(e *env, out string) {
			e.mkdirReal(out + "/sub")
			e.writeReal(out+"/sub/x", "x", 0o600)
		}, "unexpected entry \"sub\""},
		// A directory the account made read-only cannot be emptied until root
		// takes it over; the cleanup must still remove the staging directory.
		"a read-only subdirectory": {func(e *env, out string) {
			e.mkdirReal(out + "/sub/deeper")
			e.writeReal(out+"/sub/deeper/x", "x", 0o600)
			for _, d := range []string{out + "/sub/deeper", out + "/sub"} {
				if err := os.Chmod(d, 0o555); err != nil { //nolint:gosec // a directory that cannot be emptied, to be taken over
					e.t.Fatal(err)
				}
			}
		}, "unexpected entry \"sub\""},
		"a directory in place of an artifact": {func(e *env, out string) {
			_ = os.Remove(out + "/001-sqlite.db")
			e.mkdirReal(out + "/001-sqlite.db")
		}, "is not a regular file"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.seedData()
			e.write(secret, "root secret", 0o600)
			e.workerWrap = func(_ string, run func() (string, int)) (string, int) {
				out, code := run()
				// The staging output is the only <stage>/out of the call.
				matches, _ := filepath.Glob(e.real("/var/backups/lcm/web/.stage-*/out"))
				if len(matches) != 1 {
					e.t.Fatalf("staging directories %v", matches)
				}
				tamper.do(e, matches[0])
				return out, code
			}
			e.refused(backupReq(fileSources...), CodeFailed, tamper.want)
			if names, _ := os.ReadDir(e.real("/var/backups/lcm/web")); len(names) != 0 {
				t.Errorf("left %v", names)
			}
		})
	}
}

// stuckFS cannot remove staging leftovers while stuck is set, like a tree
// whose directories the service account keeps changing.
type stuckFS struct {
	hostsetup.OS
	stuck *bool
}

func (f stuckFS) RemoveAll(name string) error {
	if _, err := f.OS.Lstat(name); err != nil {
		return nil
	}
	if *f.stuck && (strings.Contains(name, "/.stage-") || strings.Contains(name, "/.del-stuck-")) {
		return fmt.Errorf("remove %s: directory not empty", name)
	}
	return f.OS.RemoveAll(name)
}

// A staging tree that cannot be removed is moved aside, so that it blocks no
// later operation, and the sweep skips what it cannot remove.
func TestStuckStagingBlocksNothing(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	stuck := true
	e.h.FS = stuckFS{OS: hostsetup.OS{Root: e.root}, stuck: &stuck}
	hold := func() []string {
		var out []string
		names, _ := os.ReadDir(e.real("/var/backups/lcm/web"))
		for _, n := range names {
			out = append(out, n.Name())
		}
		return out
	}
	// A failed backup leaves its staging directory, now under another name.
	e.workerWrap = func(string, func() (string, int)) (string, int) { return "boom\n", 1 }
	e.refused(backupReq(fileSources...), CodeFailed, "the backup worker exited")
	left := hold()
	if len(left) != 1 || !strings.HasPrefix(left[0], ".del-stuck-") {
		t.Fatalf("left %v", left)
	}
	// The next operations go on, and say what they skipped.
	e.workerWrap = nil
	resp := e.ok(backupReq(fileSources...))
	if !strings.Contains(resp.Log, "left "+left[0]+" in place") {
		t.Errorf("log %q", resp.Log)
	}
	if got := e.ok(Request{Op: OpPrune, Service: "web", Keep: &Keep{Daily: 5, Weekly: 1}}); !strings.Contains(got.Log, "in place") {
		t.Errorf("prune log %q", got.Log)
	}
	e.ok(e.restoreReq(resp.Backup.BackupID))
	// Once the tree can be removed the next change clears it.
	stuck = false
	e.ok(backupReq(fileSources...))
	for _, n := range hold() {
		if strings.HasPrefix(n, ".") {
			t.Errorf("left %v", hold())
		}
	}
}

// recordingFS notes the order in which the helper secures the staging
// output and opens it for reading.
type recordingFS struct {
	hostsetup.OS
	events *[]string
}

func (r recordingFS) Chown(name string, want hostsetup.Info, uid, gid int) error {
	if strings.HasSuffix(name, "/out") {
		*r.events = append(*r.events, fmt.Sprintf("chown %d:%d", uid, gid))
	}
	return r.OS.Chown(name, want, uid, gid)
}

func (r recordingFS) Chmod(name string, want hostsetup.Info, perm fs.FileMode) error {
	if strings.HasSuffix(name, "/out") {
		*r.events = append(*r.events, fmt.Sprintf("chmod %04o", perm))
	}
	return r.OS.Chmod(name, want, perm)
}

func (r recordingFS) OpenTree(root string) (hostsetup.Tree, error) {
	if strings.HasSuffix(root, "/out") {
		*r.events = append(*r.events, "read")
	}
	return r.OS.OpenTree(root)
}

// The service account can keep a descriptor of the staging output after the
// worker ends, so root takes the directory over before it reads anything.
func TestBackupSecuresTheOutputBeforeReadingIt(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	var events []string
	e.h.FS = recordingFS{OS: hostsetup.OS{Root: e.root}, events: &events}
	e.ok(backupReq(fileSources...))
	// The directory is taken before anything reads it; the measurement and the
	// copy read it after, and the cleanup takes the staging directory once more.
	if len(events) < 3 || events[0] != fmt.Sprintf("chown %d:%d", e.h.RootUID, e.h.RootGID) || events[1] != "chmod 0700" || events[2] != "read" {
		t.Errorf("events %v", events)
	}
}

func TestBackupHashesWhatItCopies(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	resp := e.ok(backupReq(BackupSource{Type: SourceFiles, Path: "media"}))
	var m setManifest
	if err := json.Unmarshal([]byte(e.read("/var/backups/lcm/web/"+resp.Backup.BackupID+"/manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	data := e.read("/var/backups/lcm/web/" + resp.Backup.BackupID + "/000-files.tar")
	s := sha256.Sum256([]byte(data))
	if m.Files[0].SHA256 != hex.EncodeToString(s[:]) {
		t.Error("the manifest does not hold the checksum of the stored copy")
	}
}

func TestBackupLimits(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.h.Limits = Limits{BackupBytes: 100}
	e.write("/var/lib/web/media/big.bin", strings.Repeat("x", 4096), 0o644)
	// The worker is told the limit, and the helper checks it again.
	e.refused(backupReq(BackupSource{Type: SourceFiles, Path: "media"}), CodeFailed, "the backup worker exited")
	e.h.Limits = Limits{}
	e.h.Free = func(string) (uint64, uint64, error) { return 1 << 20, 1 << 30, nil }
	e.refused(backupReq(BackupSource{Type: SourceFiles, Path: "media"}), CodeRefused, "not enough room for a backup")
	e.h.Free = func(string) (uint64, uint64, error) { return 1 << 40, 10, nil }
	e.refused(backupReq(BackupSource{Type: SourceFiles, Path: "media"}), CodeRefused, "inodes free")
}

// Root's own copy of the artifacts needs room too, beyond what a running
// backup is allowed to leave.
func TestBackupChecksRoomBeforeCollecting(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	lim := Limits{}.withDefaults()
	e.h.Free = func(p string) (uint64, uint64, error) {
		if p == "/var/backups/lcm/web" {
			return lim.StopFreeBytes, 1 << 30, nil // nothing beyond the reserve
		}
		return 1 << 40, 1 << 30, nil
	}
	e.refused(backupReq(fileSources...), CodeRefused, "not enough room to store the backup")
	if names, _ := os.ReadDir(e.real("/var/backups/lcm/web")); len(names) != 0 {
		t.Errorf("left %v", names)
	}
	e.h.Free = func(p string) (uint64, uint64, error) {
		if p == "/var/backups/lcm/web" {
			return lim.StopFreeBytes + 1<<20, lim.StopFreeInodes + 3, nil
		}
		return 1 << 40, 1 << 30, nil
	}
	e.ok(backupReq(fileSources...))
	e.h.Free = func(p string) (uint64, uint64, error) {
		if p == "/var/backups/lcm/web" {
			return lim.StopFreeBytes + 1<<20, lim.StopFreeInodes, nil // no inodes for the files
		}
		return 1 << 40, 1 << 30, nil
	}
	e.refused(backupReq(fileSources...), CodeRefused, "inodes free")
}

func TestBackupToolsAndCommand(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.deploy(commit1)
	bin := t.TempDir()
	argsFile, confFile := filepath.Join(bin, "args"), filepath.Join(bin, "conf")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	script := func(name, body string) string {
		return e.installTool(name, body)
	}
	e.tools = map[string]string{
		"pg_dump":   script("pg_dump", `echo "$@" >> `+argsFile+"\nfor a in \"$@\"; do case $a in --file=*) echo dump > ${a#--file=};; esac; done\n"),
		"mongodump": script("mongodump", `echo "$@" >> `+argsFile+"\nfor a in \"$@\"; do case $a in --archive=*) echo archive > ${a#--archive=};; --config=*) cat ${a#--config=} >> "+confFile+"; stat -c %a ${a#--config=} >> "+confFile+";; esac; done\n"),
	}
	// A release program whose standard output is the dump.
	e.write("/opt/web/current/bin/dump", "#!/bin/sh\necho command-output \"$1\"\n", 0o755)
	sources := []BackupSource{
		{Type: SourcePostgres, Database: "app"},
		{Type: SourceMongoDB, Database: "app", Users: true},
		{Type: SourceCommand, Argv: []string{"bin/dump", "--all"}},
	}
	t.Setenv("MONGODB_URI", "mongodb://localhost/?x=1")
	resp := e.ok(backupReq(sources...))
	set := "/var/backups/lcm/web/" + resp.Backup.BackupID
	if resp.Backup.Files != 5 {
		t.Fatalf("result %+v", resp.Backup)
	}
	for name, want := range map[string]string{
		"000-postgres.dump":         "dump\n",
		"001-mongodb.archive":       "archive\n",
		"001-mongodb-users.archive": "archive\n",
		"001-mongodb-roles.archive": "archive\n",
		"002-command.out":           "command-output --all\n",
	} {
		if got := e.read(set + "/" + name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	args, _ := os.ReadFile(argsFile) //nolint:gosec // a file of the test's own tree
	for _, want := range []string{
		"--format=custom --no-password", "--dbname=app",
		"--gzip --db=app --config=", "--gzip --db=admin --collection=system.users --config=", "--collection=system.roles",
	} {
		if !strings.Contains(string(args), want) {
			t.Errorf("tool arguments lack %q:\n%s", want, args)
		}
	}

	// The connection string, which holds a password, is not in the arguments
	// of the tools: it is in a private file that is gone afterwards.
	if strings.Contains(string(args), "localhost") || strings.Contains(string(args), "--uri") {
		t.Errorf("the connection string is in the arguments:\n%s", args)
	}
	conf, _ := os.ReadFile(confFile) //nolint:gosec // a file of the test's own tree
	if want := "uri: \"mongodb://localhost/?x=1\"\n600\n"; !strings.HasPrefix(string(conf), want) {
		t.Errorf("config file seen by the tool: %q", conf)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("temporary files remain: %v", left)
	}

	// A missing tool is a clear refusal of the backup, and the program of a
	// command source must be an executable of the release.
	e.uninstallTools()
	e.refused(backupReq(sources[0]), CodeRefused, "pg_dump is not installed")
	e.write("/opt/web/current/bin/plain", "x", 0o644)
	if resp := e.call(backupReq(BackupSource{Type: SourceCommand, Argv: []string{"bin/plain"}})); !strings.Contains(resp.Log, "not an executable file of the release") {
		t.Errorf("log %q", resp.Log)
	}
}

// The tools run as the service's account, so the helper picks them as root
// and refuses any that someone other than root could have replaced.
func TestBackupToolsMustBeTrusted(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	src := backupReq(BackupSource{Type: SourcePostgres, Database: "app"})
	good := "for a in \"$@\"; do case $a in --file=*) echo dump > ${a#--file=};; esac; done\n"
	setMode := func(p string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(e.real(p), mode); err != nil { //nolint:gosec // loosening a test host path to be refused
			t.Fatal(err)
		}
	}

	e.installTool("pg_dump", good)
	setMode("/usr/local/bin/pg_dump", 0o775)
	e.refused(src, CodeRefused, "pg_dump cannot be trusted")
	setMode("/usr/local/bin/pg_dump", 0o755)
	setMode("/usr/local/bin", 0o777)
	e.refused(src, CodeRefused, "is not root's alone")
	setMode("/usr/local/bin", 0o755)
	setMode("/usr/local/bin/pg_dump", 0o644)
	e.refused(src, CodeRefused, "is not executable")
	setMode("/usr/local/bin/pg_dump", 0o755)
	e.ok(src)

	// A link is fine when everything it leads through is root's, and the
	// worker runs the link, not its target (pg_wrapper dispatches on its
	// name). One to a file others can write is not.
	e.mkdir("/usr/share/wrapper", 0o755)
	e.mkdir("/usr/bin", 0o755)
	e.write("/usr/share/wrapper/pg_wrapper", "#!/bin/sh\n"+good, 0o755)
	if err := os.Remove(e.real("/usr/local/bin/pg_dump")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../share/wrapper/pg_wrapper", e.real("/usr/bin/pg_dump")); err != nil {
		t.Fatal(err)
	}
	e.ok(src)
	setMode("/usr/share/wrapper/pg_wrapper", 0o777)
	e.refused(src, CodeRefused, "pg_dump cannot be trusted")

	// The restore needs its own tools, and checks them before it stops the
	// service.
	setMode("/usr/share/wrapper/pg_wrapper", 0o755)
	a := e.ok(src).Backup
	e.active = true
	before := len(e.systemctlCalls())
	e.refused(e.restoreReq(a.BackupID), CodeRefused, "pg_restore is not installed")
	if calls := e.systemctlCalls()[before:]; len(calls) != 0 || !e.active {
		t.Errorf("the service was touched: %v", calls)
	}
}

func TestListAndRetention(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	src := BackupSource{Type: SourceFiles, Path: "media"}
	var ids []string
	// One backup a day for 10 days, and two on the last one.
	for d := 0; d < 10; d++ {
		ids = append(ids, e.ok(backupReq(src)).Backup.BackupID)
		e.clock.Advance(24 * time.Hour)
	}
	e.clock.Advance(-time.Hour)
	ids = append(ids, e.ok(backupReq(src)).Backup.BackupID)
	list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups
	if len(list) != 11 || list[0].BackupID != ids[10] || list[10].BackupID != ids[0] || list[0].Sources[0].Path != "media" {
		t.Fatalf("listing %+v", list)
	}

}

func TestRetentionRules(t *testing.T) {
	day := func(s string) int64 {
		t, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			panic(err)
		}
		return t.UnixMilli()
	}
	set := func(id, at, kind string) BackupInfo {
		return BackupInfo{BackupID: id, Kind: kind, CreatedAt: day(at)}
	}
	now := time.UnixMilli(day("2026-11-10 12:00"))
	list := []BackupInfo{ // newest first
		set("b10b", "2026-11-10 04:00", KindBackup), set("b10a", "2026-11-10 01:00", KindBackup),
		set("pre-restore-new", "2026-11-09 12:00", KindPreRestore),
		set("b09", "2026-11-09 04:00", KindBackup), set("b08", "2026-11-08 04:00", KindBackup),
		set("b07", "2026-11-07 04:00", KindBackup), set("b06", "2026-11-06 04:00", KindBackup),
		set("b03", "2026-11-03 04:00", KindBackup), set("pre-restore-old", "2026-11-02 12:00", KindPreRestore),
		set("b02", "2026-11-02 04:00", KindBackup),
		set("b01", "2026-11-01 04:00", KindBackup), set("b-oct", "2026-10-28 04:00", KindBackup),
		{BackupID: "broken", Kind: KindBackup, CreatedAt: day("2026-10-01 00:00"), Problem: "the manifest is not valid"},
	}
	// The newest of the last 3 days and of the last 2 weeks (ISO weeks 46
	// and 45); everything else of kind backup goes.
	doomed := retention(list, Keep{Daily: 3, Weekly: 2}, now)
	// 2026-11-10 is in ISO week 46; week 45 holds 11-02 to 11-08, whose
	// newest set is b08; the days kept are 10, 09 and 08.
	want := []string{"b10a", "b07", "b06", "b03", "pre-restore-old", "b02", "b01", "b-oct"}
	if !slices.Equal(doomed, want) {
		t.Errorf("doomed %v, want %v", doomed, want)
	}
	// The newest set and unreadable sets are never removed, and a keep of
	// one day keeps at least the newest.
	doomed = retention(list, Keep{Daily: 1}, now)
	if slices.Contains(doomed, "b10b") || slices.Contains(doomed, "broken") || slices.Contains(doomed, "pre-restore-new") {
		t.Errorf("doomed %v", doomed)
	}
	// A pre-restore set younger than 7 days stays whatever the keep says;
	// an older one goes unless it is the newest set.
	if !slices.Contains(doomed, "pre-restore-old") {
		t.Errorf("doomed %v", doomed)
	}
	only := []BackupInfo{set("pre-restore-x", "2026-10-01 00:00", KindPreRestore)}
	if d := retention(only, Keep{Daily: 1}, now); len(d) != 0 {
		t.Errorf("the newest set was removed: %v", d)
	}
}

func TestPrune(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	src := BackupSource{Type: SourceFiles, Path: "media"}
	var ids []string
	for d := 0; d < 6; d++ {
		ids = append(ids, e.ok(backupReq(src)).Backup.BackupID)
		e.clock.Advance(24 * time.Hour)
	}
	// Leftovers of an interrupted backup are removed by prune, and a set
	// with a broken manifest is kept and listed with its problem.
	e.mkdir("/var/backups/lcm/web/.stage-deadbeefb/out", 0o700)
	e.mkdir("/var/backups/lcm/web/.tmp-deadbeefb", 0o700)
	broken := "20200101T000000Z-00000000"
	e.mkdir("/var/backups/lcm/web/"+broken, 0o700)
	e.write("/var/backups/lcm/web/"+broken+"/manifest.json", "{", 0o600)
	for _, d := range []string{"/var/backups/lcm", "/var/backups/lcm/web"} {
		if err := os.Chmod(e.real(d), 0o700); err != nil { //nolint:gosec // a directory of the test host
			t.Fatal(err)
		}
	}

	resp := e.ok(Request{Op: OpPrune, Service: "web", Keep: &Keep{Daily: 2, Weekly: 0}})
	if want := []string{ids[3], ids[2], ids[1], ids[0]}; resp.Prune == nil || !slices.Equal(resp.Prune.Removed, want) || resp.Prune.Kept != 3 {
		t.Fatalf("prune %+v", resp.Prune)
	}
	names, _ := os.ReadDir(e.real("/var/backups/lcm/web"))
	var left []string
	for _, n := range names {
		left = append(left, n.Name())
	}
	if want := []string{broken, ids[4], ids[5]}; !slices.Equal(left, want) {
		t.Errorf("left %v, want %v", left, want)
	}
	list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups
	if len(list) != 3 || list[2].BackupID != broken || !strings.Contains(list[2].Problem, "manifest is not valid") {
		t.Errorf("listing %+v", list)
	}
	e.refused(Request{Op: OpPrune, Service: "web", Keep: &Keep{}}, CodeRefused, "keep must keep at least one")
}
