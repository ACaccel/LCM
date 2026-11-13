package deployhelper

import (
	"strings"
	"testing"
)

func TestURIParts(t *testing.T) {
	for uri, want := range map[string][3]string{
		"mongodb://lcm-backup:p%40ss@127.0.0.1:27017/?authSource=admin": {"", "lcm-backup", "admin"},
		"mongodb://u:p@h1,h2/app":                           {"app", "u", "app"},
		"mongodb://u:p@h/app?authSource=admin":              {"app", "u", "admin"},
		"mongodb://h:27017":                                 {"", "", "admin"},
		"mongodb+srv://us%2Fer:p@cluster/?retryWrites=true": {"", "us/er", "admin"},
	} {
		db, user, auth, err := uriParts(uri)
		if err != nil || [3]string{db, user, auth} != want {
			t.Errorf("%s: %q %q %q %v, want %q", uri, db, user, auth, err, want)
		}
	}
	if _, _, _, err := uriParts("postgres://x"); err == nil {
		t.Error("a postgres URI was accepted")
	}
}

// mongodump takes the database path of the URI as --db, so a source of
// all databases with one would quietly back up that database alone, and
// one with users could not dump admin.
func TestAllRefusesAURIWithADatabase(t *testing.T) {
	e, _ := mongoToolsEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@127.0.0.1:27017/admin?authSource=admin")
	r := e.refused(backupReq(BackupSource{Type: SourceMongoDB, All: true}), CodeFailed, `MONGODB_URI names the database "admin"`)
	if !strings.Contains(r.Message, "?authSource=admin") {
		t.Errorf("message %q", r.Message)
	}
	// The hint names the database the user authenticates against.
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@127.0.0.1:27017/app?authSource=admin")
	r = e.refused(backupReq(BackupSource{Type: SourceMongoDB, All: true}), CodeFailed, `MONGODB_URI names the database "app"`)
	if !strings.Contains(r.Message, "?authSource=admin") {
		t.Errorf("message %q", r.Message)
	}
	// The users live in admin, which a path would hide.
	e.refused(backupReq(BackupSource{Type: SourceMongoDB, Database: "app", Users: true}), CodeFailed, `MONGODB_URI names the database "app"`)
	// The database form without users is unaffected.
	e.ok(backupReq(BackupSource{Type: SourceMongoDB, Database: "app"}))
}

// A connection string the sources cannot use is refused before the service
// is stopped, whether the backup is stopped-mode or the restore replaces
// anything; the worker that does the work checks it again.
func TestBadMongoURIIsRefusedBeforeStopping(t *testing.T) {
	e, argsFile := mongoToolsEnv(t)
	e.deploy(commit1)
	e.active = true
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@localhost/?authSource=admin")
	set := e.ok(backupReq(BackupSource{Type: SourceMongoDB, All: true, Users: true})).Backup
	readArgs(t, argsFile)

	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@localhost/app?authSource=admin")
	n := len(e.systemctlCalls())
	for name, src := range map[string]BackupSource{
		"all":            {Type: SourceMongoDB, All: true},
		"database users": {Type: SourceMongoDB, Database: "app", Users: true},
	} {
		req := backupReq(src)
		req.Mode = ModeStopped
		r := e.refused(req, CodeRefused, `MONGODB_URI names the database "app"`)
		if !strings.Contains(r.Message, "the service was not stopped") || !strings.Contains(r.Message, "?authSource=admin") {
			t.Errorf("%s: message %q", name, r.Message)
		}
	}
	r := e.restoreReq(set.BackupID)
	e.refused(r, CodeRefused, `MONGODB_URI names the database "app"`)
	r.RestoreUsers = true
	e.refused(r, CodeRefused, `MONGODB_URI names the database "app"`)
	if got := e.systemctlCalls()[n:]; len(got) != 0 || !e.active {
		t.Fatalf("the service was touched: %q (active %v)", got, e.active)
	}
	if raw := readArgs(t, argsFile); raw != "" {
		t.Fatalf("tools ran:\n%s", raw)
	}
	if list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups; len(list) != 1 {
		t.Fatalf("a set was made: %+v", list)
	}

	// With a good string a stopped-mode backup goes on and stops the service.
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@localhost/?authSource=admin")
	req := backupReq(BackupSource{Type: SourceMongoDB, All: true})
	req.Mode = ModeStopped
	e.ok(req)
	if got := e.systemctlCalls()[n:]; len(got) == 0 {
		t.Error("a stopped-mode backup did not stop the service")
	}
}

// A restore of users that would remove the user the backups log in as is
// refused before anything is stopped or replaced.
func TestRestoreUsersKeepsTheBackupUser(t *testing.T) {
	e, argsFile := mongoToolsEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@localhost/?authSource=admin")
	set := e.ok(backupReq(BackupSource{Type: SourceMongoDB, All: true, Users: true})).Backup
	readArgs(t, argsFile)

	// The set's users lack lcm-backup (only a user of that name in another
	// database).
	e.write("/opt/mongo/bin/bsondump", "#!/bin/sh\necho '{\"_id\":\"app.lcm-backup\",\"user\":\"lcm-backup\",\"db\":\"app\"}'\necho '{\"user\":\"bot\",\"db\":\"admin\"}'\n", 0o755)
	n := len(e.systemctlCalls())
	r := e.restoreReq(set.BackupID)
	r.RestoreUsers = true
	resp := e.refused(r, CodeRefused, "nothing was restored or changed")
	if !strings.Contains(resp.Message, "the set holds no user lcm-backup of the database admin") {
		t.Errorf("message %q", resp.Message)
	}
	if got := e.systemctlCalls()[n:]; len(got) != 0 {
		t.Fatalf("the service was touched: %q", got)
	}
	if raw := readArgs(t, argsFile); raw != "" {
		t.Fatalf("tools ran:\n%s", raw)
	}
	if list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups; len(list) != 1 {
		t.Fatalf("a pre-restore set was made: %+v", list)
	}
	// Without restoreUsers the same set restores.
	e.ok(e.restoreReq(set.BackupID))
}

// When the users cannot be restored and putting the previous users back
// fails too, the service is left stopped and the restore fails, naming the
// set to restore by hand.
func TestRestoreUsersUndoFailureLeavesTheServiceStopped(t *testing.T) {
	e, _ := mongoToolsEnv(t)
	e.deploy(commit1)
	e.active = true
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@localhost/?authSource=admin")
	set := e.ok(Request{Op: OpBackup, Service: "web", TimeoutSec: 60, Sources: []BackupSource{{Type: SourceMongoDB, All: true, Users: true}}}).Backup
	e.write("/opt/mongo/bin/mongorestore", "#!/bin/sh\nfor a in \"$@\"; do case $a in *-admin.archive) echo 'cannot restore users' >&2; exit 1;; esac; done\n", 0o755)
	r := e.restoreReq(set.BackupID)
	r.RestoreUsers = true
	resp := e.refused(r, CodeFailed, "the service was left stopped because its data may be incomplete")
	if resp.Restore == nil || !resp.Restore.LeftStopped || e.active {
		t.Fatalf("restore %+v, active %v", resp.Restore, e.active)
	}
	if !strings.Contains(resp.Message, "repair it from pre-restore-") {
		t.Errorf("message %q", resp.Message)
	}
}
