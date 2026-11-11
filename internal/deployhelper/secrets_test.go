package deployhelper

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
)

// workerArgv returns the systemd-run arguments of the last worker whose
// unit starts with prefix.
func workerArgv(e *env, prefix string) []string {
	var argv []string
	for _, c := range e.h.Runner.(*execx.Fake).Calls() {
		if strings.HasPrefix(arg(c, "--unit="), prefix) {
			argv = c.Argv
		}
	}
	return argv
}

// The backup credentials never reach a program of the release: a backup
// with a command source is refused while backup.env exists, before the
// service is stopped. Restores, which never run command sources, get them.
func TestBackupEnvNeverReachesCommandSources(t *testing.T) {
	e, _ := mongoToolsEnv(t)
	e.deploy(commit1)
	e.write("/opt/web/current/bin/dump", "#!/bin/sh\necho dump\n", 0o755)
	e.write("/etc/web/backup.env", "MONGODB_URI=mongodb://lcm-backup:pw@localhost/\n", 0o600)
	n := len(e.systemctlCalls())
	e.refused(Request{Op: OpBackup, Service: "web", Mode: ModeStopped, TimeoutSec: 60,
		Sources: []BackupSource{{Type: SourceMongoDB, All: true}, {Type: SourceCommand, Argv: []string{"bin/dump"}}}},
		CodeRefused, "/etc/web/backup.env exists, and a command source would run the release's own program")
	if got := e.systemctlCalls()[n:]; len(got) != 0 {
		t.Fatalf("the service was touched: %q", got)
	}
	if argv := workerArgv(e, "lcm-backup-"); argv != nil {
		t.Fatalf("a worker ran: %q", argv)
	}

	// Without backup.env the command source runs as before.
	if err := os.Remove(e.real("/etc/web/backup.env")); err != nil {
		t.Fatal(err)
	}
	e.ok(backupReq(BackupSource{Type: SourceCommand, Argv: []string{"bin/dump"}}))
	if argv := workerArgv(e, "lcm-backup-"); slices.Contains(argv, "--property=EnvironmentFile=/etc/web/backup.env") {
		t.Fatalf("backup.env given without the file: %q", argv)
	}

	// A restore loads it.
	e.write("/etc/web/backup.env", "MONGODB_URI=mongodb://lcm-backup:pw@localhost/\n", 0o600)
	set := e.ok(backupReq(BackupSource{Type: SourceMongoDB, All: true})).Backup
	e.ok(e.restoreReq(set.BackupID))
	if argv := workerArgv(e, "lcm-restore-"); !slices.Contains(argv, "--property=EnvironmentFile=/etc/web/backup.env") {
		t.Fatalf("restore worker argv %q", argv)
	}
}

// The MongoDB tools read the connection string from their private config
// file; MONGODB_URI is not in their environment, which the service's
// processes could read through /proc.
func TestMongoToolsGetNoURIInTheirEnvironment(t *testing.T) {
	e := newEnv(t)
	e.seedData()
	seen := filepath.Join(t.TempDir(), "env")
	e.mkdir("/opt/mongo/bin", 0o755)
	e.write("/opt/mongo/bin/mongodump", "#!/bin/sh\necho \"uri=${MONGODB_URI:-none}\" >> "+seen+
		"\nfor a in \"$@\"; do case $a in --archive=*) echo archive > ${a#--archive=};; --config=*) cat ${a#--config=} >> "+seen+";; esac; done\n", 0o755)
	e.cfg.Backup.Tools = map[string]string{"mongodump": "/opt/mongo/bin/mongodump"}
	t.Setenv("MONGODB_URI", "mongodb://lcm-backup:pw@localhost/")
	e.ok(backupReq(BackupSource{Type: SourceMongoDB, Database: "app"}))
	raw, _ := os.ReadFile(seen) //nolint:gosec // a file of the test's own tree
	if !strings.HasPrefix(string(raw), "uri=none\n") || !strings.Contains(string(raw), `uri: "mongodb://lcm-backup:pw@localhost/"`) {
		t.Fatalf("the tool saw:\n%s", raw)
	}
	env := []string{"PATH=/bin", "MONGODB_URI=x", "PGPASSWORD=y"}
	if got := toolEnv("pg_dump", env); !slices.Equal(got, env) {
		t.Errorf("pg_dump env = %q", got)
	}
	if got := toolEnv("mongorestore", env); !slices.Equal(got, []string{"PATH=/bin", "PGPASSWORD=y"}) {
		t.Errorf("mongorestore env = %q", got)
	}
}
