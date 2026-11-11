package deployhelper

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
)

// mongoToolsEnv installs recording mongodump and mongorestore below
// /opt/mongo/bin, named by backup.tools, and returns the file their
// arguments are appended to.
func mongoToolsEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.seedData()
	argsFile := filepath.Join(t.TempDir(), "args")
	record := `echo "$(basename $0) $@" >> ` + argsFile + "\n"
	e.mkdir("/opt/mongo/bin", 0o755)
	e.write("/opt/mongo/bin/mongodump", "#!/bin/sh\n"+record+"for a in \"$@\"; do case $a in --archive=*) echo archive > ${a#--archive=};; esac; done\n", 0o755)
	e.write("/opt/mongo/bin/mongorestore", "#!/bin/sh\n"+record, 0o755)
	// bsondump prints the users of the set: the backup user is in it.
	e.write("/opt/mongo/bin/bsondump", "#!/bin/sh\n"+record+`echo '{"_id":"admin.lcm-backup","user":"lcm-backup","db":"admin"}'`+"\n", 0o755)
	e.cfg.Backup.Tools = map[string]string{"mongodump": "/opt/mongo/bin/mongodump", "mongorestore": "/opt/mongo/bin/mongorestore", "bsondump": "/opt/mongo/bin/bsondump"}
	return e, argsFile
}

func readArgs(t *testing.T, p string) string {
	t.Helper()
	raw, _ := os.ReadFile(p) //nolint:gosec // a file of the test's own tree
	_ = os.Remove(p)
	return string(raw)
}

func TestMongoAllDatabases(t *testing.T) {
	e, argsFile := mongoToolsEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:secret@localhost/")
	resp := e.ok(backupReq(BackupSource{Type: SourceMongoDB, All: true, Users: true}))
	raw := readArgs(t, argsFile)
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 3 || strings.Contains(lines[0], "--db=") || !strings.HasPrefix(lines[0], "mongodump --archive=") ||
		!strings.Contains(lines[1], "--gzip --db=admin") || strings.Contains(lines[1], "--collection") || !strings.Contains(lines[2], "--db=admin --collection=system.users --out=-") {
		t.Fatalf("mongodump runs:\n%s", raw)
	}
	set := "/var/backups/lcm/web/" + resp.Backup.BackupID
	for _, name := range []string{"000-mongodb.archive", "000-mongodb-admin.archive"} {
		if e.read(set+"/"+name) != "archive\n" {
			t.Errorf("%s missing", name)
		}
	}
	list := e.ok(Request{Op: OpBackups, Service: "web"}).Backups
	if len(list) != 1 || list[0].Problem != "" || !list[0].Sources[0].All {
		t.Fatalf("listing %+v", list)
	}

	// Without restoreUsers every database but the instance's own comes back
	// and the accounts stay.
	r := e.restoreReq(resp.Backup.BackupID)
	got := e.ok(r).Restore
	raw = readArgs(t, argsFile)
	if !slices.Equal(got.Restored, []string{"mongodb (all databases)"}) || len(got.Skipped) != 1 ||
		!strings.Contains(raw, "--drop --nsExclude=admin.* --nsExclude=config.* --nsExclude=local.*") || strings.Contains(raw, "system.users") {
		t.Fatalf("restore %+v\n%s", got, raw)
	}
	// With restoreUsers the users and roles come back from their own
	// artifacts.
	r = e.restoreReq(resp.Backup.BackupID)
	r.RestoreUsers = true
	e.ok(r)
	raw = readArgs(t, argsFile)
	for _, want := range []string{"--nsExclude=admin.*", "--nsInclude=admin.system.users --nsInclude=admin.system.roles --nsInclude=admin.system.version"} {
		if !strings.Contains(raw, want) {
			t.Errorf("restore with users lacks %q:\n%s", want, raw)
		}
	}

	for _, bad := range [][]BackupSource{
		{{Type: SourceMongoDB, All: true, Database: "app"}},
		{{Type: SourceMongoDB, All: true}, {Type: SourceMongoDB, Database: "app"}},
		{{Type: SourcePostgres, All: true}},
	} {
		if err := ValidateSources(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestBackupToolsFromConfig(t *testing.T) {
	e, _ := mongoToolsEnv(t)
	// A tool of the system directories is not used in place of the
	// configured one, even when the configured one cannot be trusted.
	e.installTool("mongodump", "exit 0\n")
	if err := os.Chmod(e.real("/opt/mongo/bin/mongodump"), 0o777); err != nil { //nolint:gosec // loosening a test tool to be refused
		t.Fatal(err)
	}
	e.refused(backupReq(BackupSource{Type: SourceMongoDB, All: true}), CodeRefused, "backup.tools.mongodump in /etc/lcm/host.json cannot be trusted")
	if err := os.Chmod(e.real("/opt/mongo/bin/mongodump"), 0o755); err != nil { //nolint:gosec // a test tool, executable like the host's
		t.Fatal(err)
	}
	e.ok(backupReq(BackupSource{Type: SourceMongoDB, All: true}))
}

func TestBackupEnvFile(t *testing.T) {
	e, _ := mongoToolsEnv(t)
	e.write("/etc/web/backup.env", "MONGODB_URI=mongodb://lcm-backup@localhost/\n", 0o600)
	e.ok(backupReq(BackupSource{Type: SourceMongoDB, All: true}))
	var argv []string
	for _, c := range e.h.Runner.(*execx.Fake).Calls() {
		if strings.HasPrefix(arg(c, "--unit="), "lcm-backup-") {
			argv = c.Argv
		}
	}
	i := slices.Index(argv, "--property=EnvironmentFile=/etc/web/env")
	if i < 0 || argv[i+1] != "--property=EnvironmentFile=/etc/web/backup.env" {
		t.Fatalf("worker argv %q", argv)
	}
	// Readable by the service's group, it would leak to the service.
	e.write("/etc/web/backup.env", "MONGODB_URI=x\n", 0o640)
	e.refused(backupReq(BackupSource{Type: SourceMongoDB, All: true}), CodeRefused, "/etc/web/backup.env must be readable by root only")
}
