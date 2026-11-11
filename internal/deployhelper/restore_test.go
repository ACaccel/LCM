package deployhelper

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// dataState reads the parts of the web service's data the tests change.
func (e *env) dataState() string {
	var parts []string
	_ = filepath.WalkDir(e.real("/var/lib/web"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a vanished entry is simply absent
		}
		rel := strings.TrimPrefix(p, e.real("/var/lib/web")+"/")
		if strings.HasSuffix(rel, ".sqlite") {
			parts = append(parts, rel+"="+strings.Join(e.sqliteRows(p), ","))
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			parts = append(parts, rel+"->link")
			return nil
		}
		b, _ := os.ReadFile(p) //nolint:gosec // a file of the test's own tree
		parts = append(parts, rel+"="+string(b))
		return nil
	})
	return strings.Join(parts, "; ")
}

func lastIndex(list []string, s string) int {
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] == s {
			return i
		}
	}
	return -1
}

func (e *env) restoreReq(id string) Request {
	return Request{Op: OpRestore, Service: "web", BackupID: id}
}

func (e *env) stageDirs() []string {
	var out []string
	names, _ := os.ReadDir(e.real("/var/backups/lcm/web"))
	for _, n := range names {
		if strings.HasPrefix(n.Name(), ".") {
			out = append(out, n.Name())
		}
	}
	return out
}

func TestRestoreAndUndo(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.deploy(commit1)
	original := e.dataState()
	a := e.ok(backupReq(fileSources...)).Backup
	e.clock.Advance(time.Hour)

	// The data changes, and a stale journal of the old database is left.
	e.write("/var/lib/web/media/a.txt", "changed", 0o644)
	e.write("/var/lib/web/media/new.txt", "new", 0o644)
	if err := os.RemoveAll(e.real("/var/lib/web/media/sub")); err != nil {
		t.Fatal(err)
	}
	e.sqliteDB("/var/lib/web/app.sqlite", "three")
	e.write("/var/lib/web/app.sqlite-wal", "stale", 0o644)
	changed := e.dataState()
	e.active = true

	r := e.restoreReq(a.BackupID)
	r.ManifestSHA256 = a.ManifestSHA256
	resp := e.ok(r)
	rs := resp.Restore
	if rs == nil || rs.BackupID != a.BackupID || !strings.HasPrefix(rs.PreRestoreID, "pre-restore-") || !rs.Started || len(rs.Skipped) != 0 ||
		!slices.Equal(rs.Restored, []string{"files media", "sqlite app.sqlite"}) {
		t.Fatalf("restore result %+v", rs)
	}
	// The link was not stored, so it does not come back.
	if got, want := e.dataState(), strings.ReplaceAll(original, "; media/link->link", ""); got != want {
		t.Errorf("after restore:\n got %s\nwant %s", got, want)
	}
	if e.exists("/var/lib/web/app.sqlite-wal") || e.exists("/var/lib/web/media.lcm-new") || e.exists("/var/lib/web/app.sqlite.lcm-new") {
		t.Error("journal or temporary files remain")
	}
	if fi, err := os.Stat(e.real("/var/lib/web/media/sub/b.bin")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode of a restored file: %v %v", fi, err)
	}
	calls := e.systemctlCalls()
	stop, start := lastIndex(calls, "stop web.service"), lastIndex(calls, "start web.service")
	if stop < 0 || start < stop || !e.active {
		t.Errorf("systemctl calls %v", calls)
	}
	if dirs := e.stageDirs(); len(dirs) != 0 {
		t.Errorf("staging left: %v", dirs)
	}

	// The pre-restore set holds what the restore replaced.
	list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups
	if len(list) != 2 || list[0].BackupID != rs.PreRestoreID || list[0].Kind != KindPreRestore || list[0].RestoredFrom != a.BackupID || list[1].BackupID != a.BackupID {
		t.Fatalf("listing %+v", list)
	}

	// Undo: restoring the pre-restore set brings the changes back.
	undo := e.ok(e.restoreReq(rs.PreRestoreID))
	// Neither the link nor the stale journal was part of what the
	// pre-restore set holds.
	wantChanged := strings.ReplaceAll(strings.ReplaceAll(changed, "; media/link->link", ""), "; app.sqlite-wal=stale", "")
	if got := e.dataState(); got != wantChanged {
		t.Errorf("after undo:\n got %s\nwant %s", got, wantChanged)
	}
	if undo.Restore.PreRestoreID == "" || undo.Restore.PreRestoreID == rs.PreRestoreID {
		t.Errorf("undo result %+v", undo.Restore)
	}
}

func TestRestoreOfAMissingPathRemovesIt(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	a := e.ok(backupReq(fileSources...)).Backup
	// The data directory has no media when the restore starts.
	if err := os.RemoveAll(e.real("/var/lib/web/media")); err != nil {
		t.Fatal(err)
	}
	resp := e.ok(e.restoreReq(a.BackupID))
	if !e.exists("/var/lib/web/media/a.txt") {
		t.Fatal("media was not restored")
	}
	var pre setManifest
	if err := json.Unmarshal([]byte(e.read("/var/backups/lcm/web/"+resp.Restore.PreRestoreID+"/manifest.json")), &pre); err != nil || !pre.Sources[0].Absent || pre.Sources[1].Absent {
		t.Fatalf("pre-restore manifest %+v %v", pre, err)
	}
	// Undoing the restore removes what did not exist.
	e.ok(e.restoreReq(resp.Restore.PreRestoreID))
	if e.exists("/var/lib/web/media") || !e.exists("/var/lib/web/app.sqlite") {
		t.Errorf("after undo: %s", e.dataState())
	}
}

func TestRestoreVerifiesBeforeChangingAnything(t *testing.T) {
	setup := func() (*env, *BackupResult, string) {
		e := newEnv(t)
		e.seedData()
		e.deploy(commit1)
		a := e.ok(backupReq(fileSources...)).Backup
		e.write("/var/lib/web/media/a.txt", "changed", 0o644)
		e.active = true
		return e, a, "/var/backups/lcm/web/" + a.BackupID
	}
	untouched := func(e *env, before string, callsBefore int) {
		t.Helper()
		if got := e.dataState(); got != before {
			t.Errorf("data changed:\n got %s\nwant %s", got, before)
		}
		for _, c := range e.systemctlCalls()[callsBefore:] {
			if strings.HasPrefix(c, "stop web") {
				t.Errorf("the service was stopped: %s", c)
			}
		}
		if dirs := e.stageDirs(); len(dirs) != 0 {
			t.Errorf("staging left: %v", dirs)
		}
		list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups
		for _, s := range list {
			if s.Kind == KindPreRestore {
				t.Errorf("a pre-restore set exists: %s", s.BackupID)
			}
		}
	}
	for name, tc := range map[string]struct {
		damage func(e *env, set string)
		want   string
		code   Code
	}{
		"a flipped byte": {func(e *env, set string) {
			b := []byte(e.read(set + "/000-files.tar"))
			b[len(b)/2] ^= 0xff
			e.writeReal(e.real(set+"/000-files.tar"), string(b), 0o600)
		}, "does not match its manifest: it is corrupt or was modified", CodeFailed},
		"a truncated artifact": {func(e *env, set string) {
			e.writeReal(e.real(set+"/001-sqlite.db"), "short", 0o600)
		}, "does not match its manifest (size 5", CodeFailed},
		"a missing artifact": {func(e *env, set string) { _ = os.Remove(e.real(set + "/001-sqlite.db")) }, "001-sqlite.db", CodeFailed},
		"an artifact that is a link": {func(e *env, set string) {
			_ = os.Remove(e.real(set + "/001-sqlite.db"))
			if err := os.Symlink(e.real("/var/lib/web/app.sqlite"), e.real(set+"/001-sqlite.db")); err != nil {
				e.t.Fatal(err)
			}
		}, "symbolic link", CodeFailed},
		"a manifest of another set": {func(e *env, set string) {
			e.writeReal(e.real(set+"/manifest.json"), strings.Replace(e.read(set+"/manifest.json"), `"service": "web"`, `"service": "api"`, 1), 0o600)
		}, "not this set's", CodeFailed},
		"an unknown manifest key": {func(e *env, set string) {
			e.writeReal(e.real(set+"/manifest.json"), strings.Replace(e.read(set+"/manifest.json"), `"version": 1,`, `"version": 1, "extra": 1,`, 1), 0o600)
		}, "the manifest is not valid", CodeFailed},
		"a manifest others can write": {func(e *env, set string) {
			if err := os.Chmod(e.real(set+"/manifest.json"), 0o666); err != nil { //nolint:gosec // a writable manifest, to be refused
				e.t.Fatal(err)
			}
		}, "is not root's alone", CodeFailed},
		"an artifact listed twice": {func(e *env, set string) {
			var m setManifest
			_ = json.Unmarshal([]byte(e.read(set+"/manifest.json")), &m)
			m.Files = append(m.Files, m.Files[0])
			raw, _ := json.Marshal(m)
			e.writeReal(e.real(set+"/manifest.json"), string(raw), 0o600)
		}, "lists", CodeFailed},
	} {
		t.Run(name, func(t *testing.T) {
			e, a, set := setup()
			tc.damage(e, set)
			before, calls := e.dataState(), len(e.systemctlCalls())
			e.refused(e.restoreReq(a.BackupID), tc.code, tc.want)
			untouched(e, before, calls)
		})
	}

	t.Run("a checksum that LCM did not record", func(t *testing.T) {
		e, a, _ := setup()
		before, calls := e.dataState(), len(e.systemctlCalls())
		r := e.restoreReq(a.BackupID)
		r.ManifestSHA256 = strings.Repeat("0", 64)
		e.refused(r, CodeRefused, "does not match the checksum LCM recorded")
		untouched(e, before, calls)
	})
	t.Run("an unknown set", func(t *testing.T) {
		e, _, _ := setup()
		before, calls := e.dataState(), len(e.systemctlCalls())
		e.refused(e.restoreReq("20200101T000000Z-00000000"), CodeNotFound, "there is no backup")
		e.refused(e.restoreReq("pre-restore-20200101T000000Z-00000000"), CodeNotFound, "there is no backup")
		untouched(e, before, calls)
	})
	t.Run("a set of command output", func(t *testing.T) {
		e, _, _ := setup()
		e.write("/opt/web/current/bin/dump", "#!/bin/sh\necho x\n", 0o755)
		c := e.ok(backupReq(BackupSource{Type: SourceCommand, Argv: []string{"bin/dump"}})).Backup
		before, calls := e.dataState(), len(e.systemctlCalls())
		e.refused(e.restoreReq(c.BackupID), CodeRefused, "only command output")
		untouched(e, before, calls)
	})
}

// A unit waiting to restart after a crash is stopped for the pre-restore
// copy, the restore and the undo like an active one, and started again.
func TestRestoreStopsAUnitThatIsRestarting(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	e.deploy(commit1)
	a := e.ok(backupReq(fileSources...)).Backup
	e.write("/var/lib/web/media/a.txt", "changed", 0o644)
	e.activeState = "activating"
	calls := len(e.systemctlCalls())
	e.workerWrap = func(mode string, run func() (string, int)) (string, int) {
		if e.activeState != "" || e.active {
			t.Errorf("the unit was not stopped for the %s worker", mode)
		}
		return run()
	}
	resp := e.ok(e.restoreReq(a.BackupID))
	if !resp.Restore.Started || !e.active {
		t.Errorf("restore %+v active %v", resp.Restore, e.active)
	}
	got := e.systemctlCalls()[calls:]
	if stop, start := slices.Index(got, "stop web.service"), slices.Index(got, "start web.service"); stop < 0 || start < stop {
		t.Errorf("systemctl calls %v", got)
	}

	// A unit that stays up refuses the restore before anything changed.
	e.activeState, e.stopStuck = "activating", true
	before := e.dataState()
	e.workerWrap = nil
	e.refused(e.restoreReq(a.BackupID), CodeRefused, "after being stopped")
	if e.dataState() != before || len(e.stageDirs()) != 0 {
		t.Errorf("data %s, staging %v", e.dataState(), e.stageDirs())
	}
	list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups
	if len(list) != 2 { // the backup and the pre-restore copy of the first restore
		t.Errorf("listing %+v", list)
	}
}

// The pre-restore copy and the staging of an undo come on top of the set
// being restored, and are checked before anything is replaced.
func TestRestoreReservesRoomForTheCopyAndTheUndo(t *testing.T) {
	setup := func() (*env, string, uint64) {
		e := newEnv(t)
		e.seedData()
		e.deploy(commit1)
		a := e.ok(backupReq(fileSources...)).Backup
		e.active = true
		return e, a.BackupID, uint64(a.Size) //nolint:gosec // a size of a test backup
	}
	lim := Limits{}.withDefaults()
	t.Run("the current data is larger than the set", func(t *testing.T) {
		e, id, size := setup()
		e.write("/var/lib/web/media/big.bin", strings.Repeat("x", 1<<20), 0o644)
		before := e.dataState()
		// Room for the staged set twice, but not for the copy of the current
		// data and its undo.
		e.h.Free = func(p string) (uint64, uint64, error) {
			if p == "/var/backups/lcm/web" {
				return lim.MinFreeBytes + 2*size + 1<<10, 1 << 30, nil
			}
			return 1 << 40, 1 << 30, nil
		}
		e.refused(e.restoreReq(id), CodeRefused, "not enough room for the restore")
		if e.dataState() != before || len(e.stageDirs()) != 0 {
			t.Errorf("data %s, staging %v", e.dataState(), e.stageDirs())
		}
		e.h.Free = nil
		e.ok(e.restoreReq(id))
	})
	t.Run("no room for an undo after the copy was made", func(t *testing.T) {
		e, id, _ := setup()
		before := e.dataState()
		calls := 0
		e.h.Free = func(p string) (uint64, uint64, error) {
			if p != "/var/backups/lcm/web" {
				return 1 << 40, 1 << 30, nil
			}
			if calls++; calls >= 3 { // the check after the pre-restore copy
				return lim.StopFreeBytes, 1 << 30, nil
			}
			return 1 << 40, 1 << 30, nil
		}
		resp := e.refused(e.restoreReq(id), CodeRefused, "no room to undo")
		if resp.Restore == nil || resp.Restore.PreRestoreID == "" || e.dataState() != before || !e.active {
			t.Errorf("reply %+v data %s active %v", resp, e.dataState(), e.active)
		}
		if len(e.stageDirs()) != 0 {
			t.Errorf("staging left: %v", e.stageDirs())
		}
	})
}

// The data directory is the service's, so sizing what a restore replaces
// must not follow its links out of it, as root.
func TestRestoreSizingStaysInsideTheDataDirectory(t *testing.T) {
	e := newEnv(t)
	e.write("/var/lib/web/real/sub/f", strings.Repeat("x", 5000), 0o644)
	e.write("/srv/secret/x/big", strings.Repeat("y", 100000), 0o644)
	if err := os.Symlink(e.real("/srv/secret"), e.real("/var/lib/web/a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", e.real("/var/lib/web/inner")); err != nil {
		t.Fatal(err)
	}
	size := func(path string) uint64 {
		t.Helper()
		m := setManifest{Sources: []manifestSource{{BackupSource: BackupSource{Type: SourceFiles, Path: path}}}}
		n, err := e.h.currentSize(backupEnv{svc: "web"}, m, []int{0}, Limits{}.withDefaults())
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return n
	}
	if n := size("real"); n < 5000 || n > 20000 {
		t.Errorf("real: %d", n)
	}
	// Through a link to a directory outside, to one inside, and as the last
	// component: nothing is measured, and nothing tells whether the target exists.
	for _, p := range []string{"a/x", "a/x/big", "a/nope", "a", "inner/sub", "inner"} {
		if n := size(p); n != 0 {
			t.Errorf("%s: %d bytes through a link", p, n)
		}
	}
	if n := size("missing/x"); n != 0 {
		t.Errorf("missing: %d", n)
	}
}

func TestFailedRestoreIsUndone(t *testing.T) {
	setup := func() (*env, string, string) {
		e := newEnv(t)
		e.seedData()
		e.deploy(commit1)
		a := e.ok(backupReq(fileSources...)).Backup
		e.write("/var/lib/web/media/a.txt", "changed", 0o644)
		e.sqliteDB("/var/lib/web/app.sqlite", "three")
		e.active = true
		return e, a.BackupID, strings.ReplaceAll(e.dataState(), "; media/link->link", "")
	}
	t.Run("the replacement fails after it changed data", func(t *testing.T) {
		e, id, before := setup()
		restores := 0
		e.workerWrap = func(mode string, run func() (string, int)) (string, int) {
			if mode != "restore" {
				return run()
			}
			// The first restore replaces the data and then reports a failure;
			// the undo is a restore too and works.
			if restores++; restores == 1 {
				run()
				return "disk full\n", 1
			}
			return run()
		}
		resp := e.refused(e.restoreReq(id), CodeFailed, "the restore failed")
		if !strings.Contains(resp.Message, "the previous data was put back") || resp.Restore == nil || resp.Restore.PreRestoreID == "" {
			t.Errorf("reply %+v", resp)
		}
		if got := e.dataState(); got != before {
			t.Errorf("data after the undo:\n got %s\nwant %s", got, before)
		}
		if !e.active {
			t.Error("the service was not started again")
		}
		if dirs := e.stageDirs(); len(dirs) != 0 {
			t.Errorf("staging left: %v", dirs)
		}
	})
	t.Run("the undo fails too", func(t *testing.T) {
		e, id, _ := setup()
		e.workerWrap = func(mode string, run func() (string, int)) (string, int) {
			if mode == "restore" {
				return "no space\n", 1
			}
			return run()
		}
		resp := e.refused(e.restoreReq(id), CodeFailed, "putting the previous data back from pre-restore-")
		// The data is half restored, so the service stays stopped.
		if !strings.Contains(resp.Message, "failed too") || !strings.Contains(resp.Message, "left stopped") ||
			resp.Restore == nil || !resp.Restore.LeftStopped || e.active {
			t.Errorf("reply %+v active %v", resp, e.active)
		}
		if calls := e.systemctlCalls(); calls[len(calls)-1] == "start web.service" {
			t.Errorf("systemctl calls %v", calls)
		}
		// The pre-restore set stays for the administrator.
		if !e.exists("/var/backups/lcm/web/" + resp.Restore.PreRestoreID + "/manifest.json") {
			t.Error("the pre-restore set is gone")
		}
	})
	t.Run("the copy of the current data fails", func(t *testing.T) {
		e, id, before := setup()
		e.workerWrap = func(mode string, run func() (string, int)) (string, int) {
			if mode == "backup" {
				return "cannot read\n", 1
			}
			return run()
		}
		resp := e.refused(e.restoreReq(id), CodeFailed, "nothing was restored")
		if got := strings.ReplaceAll(e.dataState(), "; media/link->link", ""); resp.Restore != nil || got != before || !e.active {
			t.Errorf("reply %+v data %s active %v", resp, got, e.active)
		}
		if dirs := e.stageDirs(); len(dirs) != 0 {
			t.Errorf("staging left: %v", dirs)
		}
	})
}

func TestRestoreRunsTheDatabaseTools(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	script := func(name, body string) string {
		return e.installTool(name, body)
	}
	record := `echo "$(basename $0) $@" >> ` + argsFile + "\n"
	e.tools = map[string]string{
		"pg_dump":      script("pg_dump", record+"for a in \"$@\"; do case $a in --file=*) echo dump > ${a#--file=};; esac; done\n"),
		"pg_restore":   script("pg_restore", record),
		"mongodump":    script("mongodump", record+"for a in \"$@\"; do case $a in --archive=*) echo archive > ${a#--archive=};; esac; done\n"),
		"mongorestore": script("mongorestore", record),
		"bsondump":     script("bsondump", record+`echo '{"_id":"admin.admin","user":"admin","db":"admin"}'`+"\n"),
	}
	t.Setenv("MONGODB_URI", "mongodb://admin:secret@localhost/")
	sources := []BackupSource{{Type: SourcePostgres, Database: "app"}, {Type: SourceMongoDB, Database: "app", Users: true}}
	a := e.ok(backupReq(sources...)).Backup
	// run restores and returns the tools it ran, in order, with their arguments.
	run := func(users bool) (Response, []string, string) {
		t.Helper()
		_ = os.Remove(argsFile)
		r := e.restoreReq(a.BackupID)
		r.RestoreUsers = users
		resp := e.ok(r)
		raw, _ := os.ReadFile(argsFile) //nolint:gosec // a file of the test's own tree
		var tools []string
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			tools = append(tools, strings.Fields(l)[0])
		}
		if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "--uri") || !strings.Contains(string(raw), "mongorestore --archive=") {
			t.Errorf("the connection string is in the arguments:\n%s", raw)
		}
		return resp, tools, string(raw)
	}

	// A plain restore puts the database back and never touches the instance's
	// accounts in admin, nor copies them first.
	resp, tools, raw := run(false)
	if !slices.Equal(resp.Restore.Restored, []string{"postgres app", "mongodb app"}) ||
		!slices.Equal(resp.Restore.Skipped, []string{"users and roles of mongodb app (restoreUsers is not set)"}) {
		t.Errorf("restore result %+v", resp.Restore)
	}
	// The pre-restore copy dumps the databases first, then the restore runs.
	if want := []string{"pg_dump", "mongodump", "pg_restore", "mongorestore"}; !slices.Equal(tools, want) {
		t.Fatalf("tools run: %v\n%s", tools, raw)
	}
	if strings.Contains(raw, "admin") {
		t.Errorf("a plain restore touches admin:\n%s", raw)
	}
	for _, want := range []string{"--clean --if-exists --no-owner --no-privileges --exit-on-error --no-password --dbname=app", "--gzip --drop --nsInclude=app.*"} {
		if !strings.Contains(raw, want) {
			t.Errorf("arguments lack %q:\n%s", want, raw)
		}
	}

	// With restoreUsers the users and roles are copied and restored too.
	resp, tools, raw = run(true)
	if len(resp.Restore.Skipped) != 0 {
		t.Errorf("skipped %v", resp.Restore.Skipped)
	}
	// The users of the set are checked first; the admin archive restores
	// users, roles and the auth schema version together.
	if want := []string{"bsondump", "pg_dump", "mongodump", "mongodump", "mongodump", "pg_restore", "mongorestore", "mongorestore"}; !slices.Equal(tools, want) {
		t.Fatalf("tools run: %v\n%s", tools, raw)
	}
	for _, want := range []string{"--nsInclude=admin.system.users --nsInclude=admin.system.roles --nsInclude=admin.system.version", "-users.bson"} {
		if !strings.Contains(raw, want) {
			t.Errorf("arguments lack %q:\n%s", want, raw)
		}
	}
}

// ---- the worker on hostile archives ----

type tarEntry struct {
	hdr  tar.Header
	data string
}

func dir(name string) tarEntry {
	return tarEntry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755}}
}

func file(name, data string) tarEntry {
	return tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data))}, data: data}
}

func buildTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, en := range entries {
		h := en.hdr
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(en.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// workerEnv runs the restore worker over an archive in a bare directory.
type workerEnv struct {
	t          *testing.T
	root       string
	in, out, d string
	maxBytes   int64
	maxEntries int
	sourcePath string
}

func newWorkerEnv(t *testing.T) *workerEnv {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("the worker refuses to run as root")
	}
	w := &workerEnv{t: t, root: t.TempDir(), maxBytes: 1 << 20, maxEntries: 1000, sourcePath: "media"}
	w.in, w.out, w.d = filepath.Join(w.root, "in"), filepath.Join(w.root, "out"), filepath.Join(w.root, "var/lib/web")
	for _, p := range []string{w.in, w.out, w.d} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *workerEnv) run(archive []byte) error {
	w.t.Helper()
	sum := sha256.Sum256(archive)
	spec := workerSpec{Version: Version, Mode: "restore", Service: "web", MaxBytes: w.maxBytes, MaxEntries: w.maxEntries,
		Sources: []specSource{{BackupSource: BackupSource{Type: SourceFiles, Path: w.sourcePath},
			Artifacts: []artifactRef{{Name: "000-files.tar", Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:])}}}}}
	raw, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(w.in, "spec.json"), raw, 0o600); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.in, "000-files.tar"), archive, 0o400); err != nil {
		w.t.Fatal(err)
	}
	host := DefaultWorkerHost()
	host.DataDir = func(string) string { return w.d }
	return RunWorker(context.Background(), host, w.in, w.out, &bytes.Buffer{})
}

func (w *workerEnv) put(rel, data string) {
	w.t.Helper()
	p := filepath.Join(w.d, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o640); err != nil { //nolint:gosec // a file of the test's own tree
		w.t.Fatal(err)
	}
}

func (w *workerEnv) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(w.d, rel)) //nolint:gosec // a file of the test's own tree
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func TestWorkerRestoreRefusesHostileArchives(t *testing.T) {
	good := []tarEntry{dir("media"), file("media/a.txt", "from the archive")}
	setuid := file("media/su", "x")
	setuid.hdr.Mode = 0o4755
	sticky := dir("media/sticky")
	sticky.hdr.Mode = 0o1777
	for name, tc := range map[string]struct {
		entries []tarEntry
		want    string
	}{
		"a parent reference":        {[]tarEntry{dir("media"), file("media/../../evil", "x")}, "not a clean relative name"},
		"an absolute name":          {[]tarEntry{dir("media"), file("/etc/evil", "x")}, "not a clean relative name"},
		"a name outside the target": {[]tarEntry{dir("media"), file("other/x", "x")}, "outside media"},
		"the target's sibling":      {[]tarEntry{dir("media"), file("media2/x", "x")}, "outside media"},
		"a dot component":           {[]tarEntry{dir("media"), file("media/./x", "x")}, "not a clean relative name"},
		"a symbolic link":           {[]tarEntry{dir("media"), {hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "media/l", Linkname: "/etc"}}}, "link, device or other special"},
		"a hard link":               {[]tarEntry{dir("media"), file("media/a", "x"), {hdr: tar.Header{Typeflag: tar.TypeLink, Name: "media/b", Linkname: "media/a"}}}, "link, device or other special"},
		"a device":                  {[]tarEntry{dir("media"), {hdr: tar.Header{Typeflag: tar.TypeChar, Name: "media/null", Devmajor: 1, Devminor: 3}}}, "link, device or other special"},
		"a pipe":                    {[]tarEntry{dir("media"), {hdr: tar.Header{Typeflag: tar.TypeFifo, Name: "media/p"}}}, "link, device or other special"},
		"a setuid file":             {[]tarEntry{dir("media"), setuid}, "special mode bits"},
		"a sticky directory":        {[]tarEntry{dir("media"), sticky}, "special mode bits"},
		"a name twice":              {[]tarEntry{dir("media"), file("media/a", "x"), file("media/a", "y")}, "appears twice"},
		"a file without its parent": {[]tarEntry{dir("media"), file("media/d/a", "x")}, "parent of /d/a is missing"},
		"a file under a file":       {[]tarEntry{file("media", "x"), file("media/a", "y")}, "is not a directory"},
		"a control character":       {[]tarEntry{dir("media"), file("media/a\x1b[31m", "x")}, "is not printable"},
		"an empty archive":          {nil, "empty"},
		"too much data":             {[]tarEntry{dir("media"), file("media/big", strings.Repeat("x", 4096))}, "more than 1024 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorkerEnv(t)
			w.put("media/keep.txt", "the current data")
			outside := filepath.Join(w.root, "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "too much data" {
				w.maxBytes = 1024
			}
			err := w.run(buildTar(t, tc.entries...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if w.read("media/keep.txt") != "the current data" {
				t.Error("the current data was touched")
			}
			if names, _ := os.ReadDir(w.d); len(names) != 1 {
				t.Errorf("the data directory holds %v", names)
			}
			if b, _ := os.ReadFile(outside); string(b) != "outside" { //nolint:gosec // a file of the test's own tree
				t.Error("a file outside the data directory changed")
			}
			if names, _ := os.ReadDir(w.root); len(names) != 4 { // in, out, outside, var
				t.Errorf("stray entries beside the data: %v", names)
			}
		})
	}

	t.Run("a good archive replaces the target", func(t *testing.T) {
		w := newWorkerEnv(t)
		w.put("media/keep.txt", "the current data")
		if err := w.run(buildTar(t, good...)); err != nil {
			t.Fatal(err)
		}
		if w.read("media/a.txt") != "from the archive" || w.read("media/keep.txt") == "the current data" {
			t.Error("the target was not replaced")
		}
		names, _ := os.ReadDir(w.d)
		if len(names) != 1 {
			t.Errorf("the data directory holds %v", names)
		}
	})
	t.Run("a link in the data directory is replaced, not followed", func(t *testing.T) {
		w := newWorkerEnv(t)
		outside := filepath.Join(w.root, "outside")
		if err := os.MkdirAll(outside, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "f"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../../outside", filepath.Join(w.d, "media")); err != nil {
			t.Fatal(err)
		}
		if err := w.run(buildTar(t, good...)); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(outside, "f")); string(b) != "outside" { //nolint:gosec // a file of the test's own tree
			t.Error("the link's target was changed")
		}
		if fi, err := os.Lstat(filepath.Join(w.d, "media")); err != nil || !fi.IsDir() {
			t.Errorf("media: %v %v", fi, err)
		}
	})
	t.Run("a link above the target is not followed", func(t *testing.T) {
		w := newWorkerEnv(t)
		w.sourcePath = "a/media"
		outside := filepath.Join(w.root, "outside")
		if err := os.MkdirAll(outside, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../../outside", filepath.Join(w.d, "a")); err != nil {
			t.Fatal(err)
		}
		if err := w.run(buildTar(t, dir("a/media"), file("a/media/x", "x"))); err == nil {
			t.Fatal("the restore followed a link out of the data directory")
		}
		if names, _ := os.ReadDir(outside); len(names) != 0 {
			t.Errorf("the outside directory holds %v", names)
		}
	})
	t.Run("a corrupt staged artifact", func(t *testing.T) {
		w := newWorkerEnv(t)
		w.put("media/keep.txt", "the current data")
		archive := buildTar(t, good...)
		if err := w.run(archive); err != nil {
			t.Fatal(err)
		}
		w.put("media/keep.txt", "the current data")
		// Another archive than the one the specification hashes.
		other := buildTar(t, dir("media"), file("media/evil", "x"))
		_ = os.Remove(filepath.Join(w.in, "000-files.tar"))
		if err := os.WriteFile(filepath.Join(w.in, "000-files.tar"), other, 0o600); err != nil {
			t.Fatal(err)
		}
		host := DefaultWorkerHost()
		host.DataDir = func(string) string { return w.d }
		err := RunWorker(context.Background(), host, w.in, w.out, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "does not match its manifest") || w.read("media/keep.txt") != "the current data" {
			t.Errorf("err = %v", err)
		}
	})
}

func TestWorkerBackupSkipsAndRefusesLinks(t *testing.T) {
	w := newWorkerEnv(t)
	w.put("media/a.txt", "a")
	if err := os.Symlink("/etc", filepath.Join(w.d, "media/escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(w.d, "media/inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("media", filepath.Join(w.d, "dirlink")); err != nil {
		t.Fatal(err)
	}
	backup := func(src BackupSource) (string, error) {
		spec := workerSpec{Version: Version, Mode: "backup", Service: "web", MaxBytes: 1 << 20, MaxEntries: 1000, Sources: []specSource{{BackupSource: src}}}
		raw, _ := json.Marshal(spec)
		if err := os.WriteFile(filepath.Join(w.in, "spec.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.RemoveAll(w.out)
		_ = os.Mkdir(w.out, 0o750)
		host := DefaultWorkerHost()
		host.DataDir = func(string) string { return w.d }
		var buf bytes.Buffer
		err := RunWorker(context.Background(), host, w.in, w.out, &buf)
		return buf.String(), err
	}
	if _, err := backup(BackupSource{Type: SourceFiles, Path: "media"}); err != nil {
		t.Fatal(err)
	}
	var res workerResult
	raw, _ := os.ReadFile(filepath.Join(w.out, "result.json")) //nolint:gosec // a file of the test's own tree
	if err := json.Unmarshal(raw, &res); err != nil || res.Sources[0].Skipped != 2 {
		t.Errorf("result %s %v", raw, err)
	}
	// A source that is itself a link is refused.
	if _, err := backup(BackupSource{Type: SourceFiles, Path: "dirlink"}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v", err)
	}
	if _, err := backup(BackupSource{Type: SourceSQLite, Path: "dirlink/a.txt"}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v", err)
	}
	// A path that leaves the data directory through a link is refused by
	// the confined root.
	if _, err := backup(BackupSource{Type: SourceFiles, Path: "media/escape/passwd"}); err == nil {
		t.Error("a path through an escaping link was read")
	}
}

func TestRunWorkerNeedsASpecification(t *testing.T) {
	w := newWorkerEnv(t)
	const tail = `"maxBytes":1,"maxEntries":1`
	for spec, want := range map[string]string{
		"{":                              "specification",
		`{"version":1,"mode":"restore"}`: "invalid service",
		`{"version":2,"mode":"backup","service":"web","sources":[{"type":"files","path":"a"}],` + tail + `}`:       "unsupported",
		`{"version":1,"mode":"backup","service":"web","sources":[{"type":"files","path":"../a"}],` + tail + `}`:    "path must be",
		`{"version":1,"mode":"backup","service":"web","sources":[{"type":"files","path":"a"}],` + tail + `,"x":1}`: "unknown field",
	} {
		if err := os.WriteFile(filepath.Join(w.in, "spec.json"), []byte(spec), 0o600); err != nil {
			t.Fatal(err)
		}
		err := RunWorker(context.Background(), DefaultWorkerHost(), w.in, w.out, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", spec, err, want)
		}
	}
}
