package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"
)

// The upgrade tests run the real Upgrade against an in-memory host whose
// "systemd" and lcm binaries are modelled by upRunner: a binary is
// identified by its content, starting the new one migrates the database, and
// health reports the version of the binary that was started.

type upBuild struct {
	versionLine string
	schema      int
}

var upBuilds = map[string]upBuild{
	"old-binary":     {"lcm v1 (aaaaaaa) 2026-11-01T00:00:00Z\n", 16},
	"new-binary":     {"lcm v2 (bbbbbbb) 2026-11-05T00:00:00Z\n", 17},
	"same-binary":    {"lcm v1b (ccccccc) 2026-11-05T00:00:00Z\n", 16},
	"low-binary":     {"lcm v0 (ddddddd) 2026-11-05T00:00:00Z\n", 15},
	"older-binary":   {"lcm v1o (eeeeeee) 2026-10-01T00:00:00Z\n", 17},
	"garbage-binary": {"hello\n", 16},
}

var upDBSchema = map[string]int{"db-16": 16, "db-17": 17}

type upRunner struct {
	mu      sync.Mutex
	fs      *memFS
	calls   []string
	missing map[string]bool
	acc     map[string]Account

	active  bool
	running string
	loadOff bool
	// startedOff records, per start or restart, whether the loaded unit had
	// automation switched off.
	startedOff []bool
	// stuck keeps the unit active after a stop.
	stuck bool
	// ignoreDropIn makes daemon-reload not pick up the drop-in.
	ignoreDropIn bool
	// onStart runs after a start's effects; n counts starts from 1.
	onStart func(n int) error
	// schemaAfterStart, when set, is what "storage-schema --data-dir" reports.
	schemaAfterStart int
	hook             func(line string) error

	// What systemd reports about the unit.
	pid, restarts int
	// shows counts property reads since the last start; the main process
	// is replaced at pidChangeAt (when non-zero).
	shows, pidChangeAt int
	foreignListener    bool
	extraListener      bool
	badCgroup          bool
	churnPID           bool
	notActive          bool
	envOverride        string
}

func (r *upRunner) LookPath(name string) (string, error) {
	if r.missing[name] {
		return "", errors.New(name + " not found")
	}
	return "/usr/bin/" + name, nil
}

func (r *upRunner) called(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if matches(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (r *upRunner) Run(_ context.Context, c Command) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := c.String()
	if c.As != nil {
		line = "[" + c.As.Name + "] " + line
	}
	r.calls = append(r.calls, line)
	r.fs.mu.Lock()
	r.fs.log("run", line)
	r.fs.mu.Unlock()
	if r.hook != nil {
		if err := r.hook(line); err != nil {
			return nil, err
		}
	}
	a := c.Args
	arg := func(i int) string {
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	switch {
	case c.Path == "getent":
		acc, ok := r.acc[arg(1)]
		if !ok {
			return nil, exit(2)
		}
		return fmt.Appendf(nil, "%s:x:%d:%d::%s:/bin/sh\n", acc.Name, acc.UID, acc.GID, acc.Home), nil
	case c.Path == "visudo":
		return nil, nil
	case arg(0) == "version":
		b, ok := upBuilds[r.fs.content(c.Path)]
		if !ok {
			return []byte("not an lcm\n"), nil
		}
		if strings.HasPrefix(c.Path, "/tmp/") && (c.As == nil || c.As.Name != "nobody") {
			return nil, errors.New("the staged build must run as nobody")
		}
		if c.Path == Binary && (c.As == nil || c.As.Name != "lcm") {
			return nil, errors.New("the installed binary must run as lcm")
		}
		return []byte(b.versionLine), nil
	case arg(0) == "storage-schema":
		b, ok := upBuilds[r.fs.content(c.Path)]
		if !ok {
			return nil, exit(1)
		}
		if arg(1) == "--data-dir" {
			if b.schema < 17 {
				return nil, &ExitError{Command: c.String(), Code: 2, Stderr: "flag provided but not defined"}
			}
			if c.As == nil || c.As.Name != "lcm" {
				return nil, errors.New("only lcm may read the database")
			}
			if r.schemaAfterStart != 0 {
				return fmt.Appendf(nil, "%d\n", r.schemaAfterStart), nil
			}
			return fmt.Appendf(nil, "%d\n", upDBSchema[r.fs.content(DatabaseFile)]), nil
		}
		return fmt.Appendf(nil, "%d\n", b.schema), nil
	case c.Path == "systemctl":
		return r.systemctl(a)
	}
	return nil, fmt.Errorf("fake runner: unexpected command %s", line)
}

func (r *upRunner) systemctl(a []string) ([]byte, error) {
	switch a[0] {
	case "stop":
		if !r.stuck {
			r.active, r.running = false, ""
		}
	case "is-active":
		if !r.active {
			return nil, exit(3)
		}
	case "daemon-reload":
		r.loadOff = !r.ignoreDropIn && r.fs.get(UpgradeDropIn) != nil
	case "show":
		if len(a) > 1 && a[1] == "--property=Environment" {
			switch {
			case r.envOverride != "":
				return []byte(r.envOverride + "\n"), nil
			case r.loadOff:
				return []byte("LCM_AUTOMATION=off\n"), nil
			}
			return []byte("\n"), nil
		}
		r.shows++
		if (r.churnPID || (r.pidChangeAt != 0 && r.shows == r.pidChangeAt)) && r.active {
			r.pid++
			if r.pidChangeAt != 0 {
				r.restarts++ // systemd restarted the process
			}
			r.publish()
		}
		state, pid := "inactive", 0
		if r.active && !r.notActive {
			state, pid = "active", r.pid
		}
		return fmt.Appendf(nil, "ActiveState=%s\nMainPID=%d\nNRestarts=%d\nControlGroup=/system.slice/lcm.service\nRestartUSec=5s\n", state, pid, r.restarts), nil
	case "reset-failed":
	case "start", "restart":
		r.startedOff = append(r.startedOff, r.loadOff)
		b := upBuilds[r.fs.content(Binary)]
		r.active = true
		r.running = strings.Fields(b.versionLine)[1]
		if b.schema == 17 && r.fs.content(DatabaseFile) == "db-16" {
			r.fs.file(DatabaseFile, "db-17", 0o600, 990, 990)
			r.fs.file(DatabaseFile+"-wal", "wal-17", 0o600, 990, 990)
			r.fs.file(DatabaseFile+"-shm", "shm-17", 0o600, 990, 990)
		}
		var err error
		if r.onStart != nil {
			err = r.onStart(len(r.startedOff))
		}
		r.pid++
		r.shows = 0
		r.publish()
		return nil, err
	default:
		return nil, fmt.Errorf("fake systemctl: unexpected %v", a)
	}
	return nil, nil
}

// publish writes what /proc shows for the current main process: its cgroup,
// the socket it holds, and the listening sockets on the health port.
func (r *upRunner) publish() {
	cg := "0::/system.slice/lcm.service\n"
	if r.badCgroup {
		cg = "0::/user.slice/impostor.service\n"
	}
	r.fs.file(fmt.Sprintf("/proc/%d/cgroup", r.pid), cg, 0o444, 0, 0)
	inode := 5000 + r.pid
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   100        0 111 1 0 100 0 0 10 0\n" +
		fmt.Sprintf("   1: 0100007F:1E78 00000000:0000 0A 00000000:00000000 00:00000000 00000000   990        0 %d 1 0 100 0 0 10 0\n", inode) +
		fmt.Sprintf("   2: 0100007F:1E78 0100007F:9999 01 00000000:00000000 00:00000000 00000000   990        0 %d 1 0 100 0 0 10 0\n", 777)
	if r.extraListener {
		tcp += "   3: 00000000:1E78 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 4242 1 0 100 0 0 10 0\n"
	}
	r.fs.file("/proc/net/tcp", tcp, 0o444, 0, 0)
	r.fs.remove(fmt.Sprintf("/proc/%d/fd", r.pid))
	r.fs.dir(fmt.Sprintf("/proc/%d/fd", r.pid), 0o500, 0, 0)
	r.fs.link(fmt.Sprintf("/proc/%d/fd/0", r.pid), "/dev/null", 0, 0)
	if !r.foreignListener {
		r.fs.link(fmt.Sprintf("/proc/%d/fd/7", r.pid), fmt.Sprintf("socket:[%d]", inode), 0, 0)
	}
}

// failWriteFS fails chosen writes.
type failWriteFS struct {
	*memFS
	fail func(p string) error
}

func (f failWriteFS) WriteFile(p string, r io.Reader, perm fs.FileMode, uid, gid int) error {
	if err := f.fail(p); err != nil {
		return err
	}
	return f.memFS.WriteFile(p, r, perm, uid, gid)
}

type upTest struct {
	t  *testing.T
	th *testHost
	r  *upRunner
	fs *memFS
	// healthOverride replaces what health reports for the running version.
	healthOverride func(running string) (string, error)
	free           func(string) (uint64, error)
	cancelClock    context.CancelFunc
	// lock stands in for self-install's lock; locks counts the holders.
	lock  func() (func(), error)
	locks int
	// stamp names the backup set; the clock moves on while the upgrade waits.
	stamp string
}

func newUpTest(t *testing.T) *upTest {
	t.Helper()
	th := newTestHost(t)
	m := th.fs
	r := &upRunner{fs: m, missing: map[string]bool{}, active: true, running: "v1", pid: 100, acc: map[string]Account{
		"alice":  {Name: "alice", UID: aliceUID, GID: aliceUID, Home: "/home/alice"},
		"nobody": {Name: "nobody", UID: 65534, GID: 65534, Home: "/nonexistent"},
		"lcm":    {Name: "lcm", UID: 990, GID: 990, Home: DataDir},
	}}
	th.host.Runner = r
	th.host.OpenExecutable = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("new-binary")), nil }
	m.file(Binary, "old-binary", 0o755, 0, 0)
	m.file(Unit, "[Service]\n", 0o644, 0, 0)
	m.dir(DataDir, 0o750, 990, 990)
	m.file(DatabaseFile, "db-16", 0o600, 990, 990)
	m.dir(DataDir+"/secrets", 0o700, 990, 990)
	m.file(DataDir+"/secrets/token", "s3cret", 0o600, 990, 990)
	m.file(EnvFile, "LCM_DATA_DIR=/var/lib/lcm\nLCM_LISTEN=127.0.0.1:7800\n", 0o640, 0, 990)
	m.dir("/var/backups", 0o755, 0, 0)
	m.dir("/run/systemd/system", 0o755, 0, 0)
	r.publish()
	u := &upTest{t: t, th: th, r: r, fs: m, free: func(string) (uint64, error) { return 1 << 40, nil }}
	u.lock = func() (func(), error) {
		u.locks++
		return func() { u.locks-- }, nil
	}
	u.stamp = th.clock.Now().UTC().Format("20060102T150405Z")
	u.advanceClock()
	return u
}

func (u *upTest) health(context.Context) (string, error) {
	u.r.mu.Lock()
	defer u.r.mu.Unlock()
	if !u.r.active {
		return "", errors.New("connection refused")
	}
	if u.healthOverride != nil {
		return u.healthOverride(u.r.running)
	}
	return u.r.running, nil
}

// advanceClock moves the fake clock while the upgrade waits.
func (u *upTest) advanceClock() {
	ctx, cancel := context.WithCancel(context.Background())
	u.cancelClock = cancel
	u.t.Cleanup(cancel)
	go func() {
		for u.th.clock.WaitForTimers(ctx, 1) == nil {
			u.th.clock.Advance(time.Second)
		}
	}()
}

func (u *upTest) upgrade(o UpgradeOptions) (Result, string, error) {
	return u.upgradeCtx(context.Background(), o)
}

func (u *upTest) upgradeCtx(ctx context.Context, o UpgradeOptions) (Result, string, error) {
	u.t.Helper()
	var out bytes.Buffer
	o.Out = &out
	o.HealthVersion = u.health
	o.FreeBytes = u.free
	o.LockSelfInstall = u.lock
	res, err := Upgrade(ctx, u.th.host, o)
	return res, out.String(), err
}

// setName is the backup set the fake clock produces.
func (u *upTest) setName() string {
	return BackupRoot + "/upgrade-" + u.stamp
}

// order checks that the substrings occur in the operation log in this order.
func (u *upTest) order(want ...string) {
	u.t.Helper()
	u.fs.mu.Lock()
	ops := append([]string{}, u.fs.ops...)
	u.fs.mu.Unlock()
	i := 0
	for _, w := range want {
		found := false
		for ; i < len(ops); i++ {
			if strings.Contains(ops[i], w) {
				found, i = true, i+1
				break
			}
		}
		if !found {
			u.t.Fatalf("%q not found in order after %d operations; log:\n%s", w, i, strings.Join(ops, "\n"))
		}
	}
}

// noWrites checks that no file operation changed the host.
func (u *upTest) noWrites() {
	u.t.Helper()
	for _, w := range u.fs.writes() {
		if !strings.HasPrefix(w, "run ") {
			u.t.Fatalf("the host was changed: %q", w)
		}
	}
}

func (u *upTest) noStop() {
	u.t.Helper()
	if c := u.r.called("systemctl stop"); len(c) > 0 {
		u.t.Fatalf("LCM was stopped: %v", c)
	}
}

// restoredOld checks that the old database, binary and service are back.
func (u *upTest) restoredOld() {
	u.t.Helper()
	if got := u.fs.content(Binary); got != "old-binary" {
		u.t.Errorf("binary = %q, want the old one", got)
	}
	if got := u.fs.content(DatabaseFile); got != "db-16" {
		u.t.Errorf("database = %q, want the old one", got)
	}
	for _, s := range []string{"-wal", "-shm"} {
		if u.fs.get(DatabaseFile+s) != nil {
			u.t.Errorf("stale %s left next to the restored database", s)
		}
	}
	if info, _ := u.fs.Lstat(DatabaseFile); info.UID != 990 || info.Mode.Perm() != 0o600 {
		u.t.Errorf("restored database owner/mode = %d %v", info.UID, info.Mode.Perm())
	}
	u.r.mu.Lock()
	defer u.r.mu.Unlock()
	if !u.r.active || u.r.running != "v1" {
		u.t.Errorf("service active=%v running=%q, want the old version running", u.r.active, u.r.running)
	}
	if u.fs.get(UpgradeDropIn) != nil {
		u.t.Error("the automation drop-in was left behind")
	}
}

func TestUpgradeEqualSchemaChangesNothing(t *testing.T) {
	u := newUpTest(t)
	u.th.host.OpenExecutable = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("same-binary")), nil }
	for _, check := range []bool{true, false} {
		res, out, err := u.upgrade(UpgradeOptions{Check: check})
		if err != nil || res.Changes != 0 {
			t.Fatalf("check=%v: %+v %v\n%s", check, res, err, out)
		}
		if !strings.Contains(out, "nothing to upgrade") || !strings.Contains(out, "self-deploy") {
			t.Errorf("output = %q", out)
		}
	}
	u.noWrites()
	u.noStop()
	if c := u.r.called("systemctl"); len(c) > 0 {
		t.Errorf("systemctl was run: %v", c)
	}
}

func TestUpgradeCheckChangesNothing(t *testing.T) {
	u := newUpTest(t)
	res, out, err := u.upgrade(UpgradeOptions{Check: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Changes == 0 {
		t.Fatal("check mode must report pending steps")
	}
	for _, want := range []string{"commit aaaaaaa", "storage schema 16", "storage schema 17", "would change: stop lcm.service",
		"would change: back up", u.setName(), "would change: update " + Binary, "LCM_AUTOMATION=off", "would change: restart lcm.service"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	u.noWrites()
	u.noStop()
	if u.fs.content(Binary) != "old-binary" || u.fs.content(DatabaseFile) != "db-16" {
		t.Error("check mode changed the installation")
	}
}

func TestUpgradeHappyPath(t *testing.T) {
	u := newUpTest(t)
	res, out, err := u.upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Changes == 0 {
		t.Error("no changes counted")
	}
	set := u.setName()
	u.order(
		"run systemctl stop lcm.service",
		"run systemctl is-active",
		"write "+set+"/database/lcm.db",
		"write "+set+"/bin/lcm",
		"write "+set+"/manifest.json",
		"write "+Binary,
		"write "+UpgradeDropIn,
		"run systemctl daemon-reload",
		"run systemctl show",
		"run systemctl start lcm.service",
		"run [lcm] "+Binary+" storage-schema --data-dir "+DataDir,
		"remove "+UpgradeDropIn,
		"run systemctl daemon-reload",
		"run systemctl show",
		"run systemctl restart lcm.service",
	)
	if got := u.fs.content(Binary); got != "new-binary" {
		t.Errorf("installed binary = %q", got)
	}
	if got := u.fs.content(DatabaseFile); got != "db-17" {
		t.Errorf("database = %q", got)
	}
	if u.fs.get(UpgradeDropIn) != nil {
		t.Error("drop-in left behind")
	}
	if fmt.Sprint(u.r.startedOff) != "[true false]" {
		t.Errorf("starts with automation off = %v, want validation off then normal", u.r.startedOff)
	}
	// The old binary is kept in the set, not as lcm.prev, which self-install owns.
	if u.fs.get(Binary+".prev") != nil {
		t.Error("the upgrade touched lcm.prev")
	}
	if !strings.Contains(out, "is upgraded from") || !strings.Contains(out, set) {
		t.Errorf("output lacks the summary:\n%s", out)
	}
	if u.fs.content(set+"/bin/lcm") != "old-binary" || u.fs.content(set+"/database/lcm.db") != "db-16" {
		t.Error("the backup set lacks the old binary or database")
	}
	if u.fs.content(set+"/secrets/token") != "s3cret" || u.fs.content(set+"/etc-lcm/lcm.env") == "" {
		t.Error("the backup set lacks the secrets or /etc/lcm")
	}
	for _, d := range []string{BackupRoot, set} {
		if info, _ := u.fs.Lstat(d); info.Mode.Perm() != 0o700 || info.UID != 0 {
			t.Errorf("%s = %v uid %d, want root 0700", d, info.Mode.Perm(), info.UID)
		}
	}
	if info, _ := u.fs.Lstat(set + "/secrets/token"); info.Mode.Perm() != 0o600 || info.UID != 0 {
		t.Errorf("secret copy = %v uid %d", info.Mode.Perm(), info.UID)
	}
}

func TestUpgradeManifest(t *testing.T) {
	u := newUpTest(t)
	u.fs.file(DatabaseFile+"-wal", "wal-16", 0o600, 990, 990)
	u.fs.link(DataDir+"/secrets/alias", "token", 990, 990)
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	set := u.setName()
	var m upgradeManifest
	if err := json.Unmarshal([]byte(u.fs.content(set+"/manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	if m.From.Version != "v1" || m.From.Schema != 16 || m.To.Version != "v2" || m.To.Schema != 17 {
		t.Errorf("versions = %+v %+v", m.From, m.To)
	}
	byPath := map[string]manifestFile{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	for path, content := range map[string]string{
		"database/lcm.db": "db-16", "database/lcm.db-wal": "wal-16", "bin/lcm": "old-binary",
		"secrets/token": "s3cret", "etc-lcm/lcm.env": u.fs.content(EnvFile),
	} {
		f, ok := byPath[path]
		if !ok || f.SHA256 != sha(content) || f.Size != int64(len(content)) {
			t.Errorf("%s: manifest entry %+v, want sha %s", path, f, sha(content))
		}
	}
	if f := byPath["secrets/alias"]; f.Link != "token" {
		t.Errorf("link entry = %+v", f)
	}
	if r := byPath["database/lcm.db"].Restore; r == nil || r.Source != DatabaseFile || r.UID != 990 || r.Mode != 0o600 {
		t.Errorf("restore info = %+v", r)
	}
	if r := byPath["secrets/token"].Restore; r != nil {
		t.Errorf("secrets must not be restored automatically: %+v", r)
	}
	for i := 1; i < len(m.Files); i++ {
		if m.Files[i-1].Path >= m.Files[i].Path {
			t.Errorf("manifest not sorted at %s", m.Files[i].Path)
		}
	}
}

func TestUpgradeWithoutSecretsDirectory(t *testing.T) {
	u := newUpTest(t)
	u.fs.remove(DataDir + "/secrets")
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(u.fs.content(u.setName()+"/manifest.json"), "no secrets directory") {
		t.Error("the manifest does not say that there were no secrets")
	}
}

func TestUpgradeUsesSecretsDirectoryOfEnvFile(t *testing.T) {
	u := newUpTest(t)
	u.fs.file(EnvFile, "LCM_SECRETS_DIR=/srv/lcm-secrets\n", 0o640, 0, 990)
	u.fs.dir("/srv/lcm-secrets", 0o700, 990, 990)
	u.fs.file("/srv/lcm-secrets/key", "k", 0o600, 990, 990)
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if u.fs.content(u.setName()+"/secrets/key") != "k" {
		t.Error("the relocated secrets directory was not backed up")
	}
}

func TestUpgradePreflightRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(u *upTest)
		want  string
	}{
		{"not root", func(u *upTest) { u.th.host.Privileged = false }, "must run as root"},
		{"self-deployed binary", func(u *upTest) { u.th.host.Executable = Binary }, "refusing to run"},
		{"no host.json", func(u *upTest) { u.fs.remove(ConfigPath) }, "does not exist"},
		{"missing tool", func(u *upTest) { u.r.missing["systemctl"] = true }, "missing required tools"},
		{"no lcm account", func(u *upTest) { delete(u.r.acc, "lcm") }, "account lcm does not exist"},
		{"no installed binary", func(u *upTest) { u.fs.remove(Binary) }, "not an installation to upgrade"},
		{"no database", func(u *upTest) { u.fs.remove(DatabaseFile) }, "not an installation to upgrade"},
		{"database is a link", func(u *upTest) { u.fs.link(DatabaseFile, "/etc/shadow", 990, 990) }, "not a regular file"},
		{"no unit", func(u *upTest) { u.fs.remove(Unit) }, "not an installation to upgrade"},
		{"installed version unreadable", func(u *upTest) { u.fs.file(Binary, "garbage-binary", 0o755, 0, 0) }, "well-formed version"},
		{"downgrade", func(u *upTest) {
			u.th.host.OpenExecutable = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("low-binary")), nil }
		}, "downgrade"},
		{"older build", func(u *upTest) {
			u.th.host.OpenExecutable = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("older-binary")), nil }
		}, "newer than this build"},
		{"database ahead of the binary", func(u *upTest) { u.fs.file(DatabaseFile, "db-17", 0o600, 990, 990) }, "database is at storage schema 17"},
		{"env sets automation", func(u *upTest) { u.fs.file(EnvFile, "LCM_AUTOMATION=on\n", 0o640, 0, 990) }, "LCM_AUTOMATION"},
		{"env exported automation", func(u *upTest) { u.fs.file(EnvFile, "export LCM_AUTOMATION='off'\n", 0o640, 0, 990) }, "LCM_AUTOMATION"},
		{"env moves the data dir", func(u *upTest) { u.fs.file(EnvFile, "LCM_DATA_DIR=/srv/lcm\n", 0o640, 0, 990) }, "LCM_DATA_DIR"},
		{"env secrets dir relative", func(u *upTest) { u.fs.file(EnvFile, "LCM_SECRETS_DIR=secrets\n", 0o640, 0, 990) }, "LCM_SECRETS_DIR"},
		{"env owned by lcm", func(u *upTest) { u.fs.file(EnvFile, "", 0o640, 990, 990) }, "owned by root"},
		{"self-install verifying", func(u *upTest) {
			u.fs.file(selfInstallPending, fmt.Sprintf("%d\n", u.th.clock.Now().Unix()-60), 0o644, 0, 0)
		}, "still being verified"},
		{"no /var/backups", func(u *upTest) { u.fs.remove("/var/backups") }, "does not exist"},
		{"backup root owned by another user", func(u *upTest) { u.fs.dir(BackupRoot, 0o700, aliceUID, aliceUID) }, "not owned by root"},
		{"backup root is a file", func(u *upTest) { u.fs.file(BackupRoot, "", 0o600, 0, 0) }, "not a directory"},
		{"no space", func(u *upTest) { u.free = func(string) (uint64, error) { return 1 << 20, nil } }, "not enough free space"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpTest(t)
			tc.setup(u)
			res, out, err := u.upgrade(UpgradeOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if res.Changes != 0 {
				t.Errorf("changes = %d", res.Changes)
			}
			u.noWrites()
			u.noStop()
		})
	}
}

func TestUpgradeOlderBuildNeedsAllowOlder(t *testing.T) {
	u := newUpTest(t)
	u.th.host.OpenExecutable = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("older-binary")), nil }
	if _, _, err := u.upgrade(UpgradeOptions{}); err == nil || !strings.Contains(err.Error(), "--allow-older") {
		t.Fatalf("err = %v", err)
	}
	// Both versions are named for the decision.
	if _, _, err := u.upgrade(UpgradeOptions{}); !strings.Contains(err.Error(), "commit aaaaaaa") || !strings.Contains(err.Error(), "commit eeeeeee") {
		t.Errorf("err = %v", err)
	}
	if _, out, err := u.upgrade(UpgradeOptions{AllowOlder: true}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if u.fs.content(Binary) != "older-binary" {
		t.Error("--allow-older did not install the build")
	}
}

func TestUpgradeIgnoresFinishedSelfInstall(t *testing.T) {
	u := newUpTest(t)
	u.fs.file(selfInstallPending, fmt.Sprintf("%d\n", u.th.clock.Now().Unix()-3600), 0o644, 0, 0)
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestUpgradeRemovesStaleDropIn(t *testing.T) {
	u := newUpTest(t)
	u.fs.dir(UpgradeDropInDir, 0o755, 0, 0)
	u.fs.file(UpgradeDropIn, dropInContent, 0o644, 0, 0)
	_, out, err := u.upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "left by an interrupted upgrade") {
		t.Errorf("output = %s", out)
	}
	u.order("remove "+UpgradeDropIn, "run systemctl daemon-reload", "run systemctl stop")
}

// A failure before the binary is replaced changes nothing but the stop.
func TestUpgradeFailureBeforeInstallStartsOldAgain(t *testing.T) {
	cases := []struct {
		name  string
		setup func(u *upTest)
		want  string
	}{
		{"stop fails", func(u *upTest) {
			u.r.hook = func(l string) error {
				if matches(l, "systemctl stop") {
					return &ExitError{Command: l, Code: 1, Stderr: "Access denied"}
				}
				return nil
			}
		}, "Access denied"},
		{"service never stops", func(u *upTest) { u.r.stuck = true }, "still active after 1m0s"},
		{"backup of the binary fails", func(u *upTest) {
			u.th.host.FS = failWriteFS{memFS: u.fs, fail: func(p string) error {
				if strings.HasSuffix(p, "/bin/lcm") {
					return errors.New("disk full")
				}
				return nil
			}}
		}, "disk full"},
		{"secret owned by another user", func(u *upTest) { u.fs.file(DataDir+"/secrets/other", "x", 0o600, 4242, 4242) }, "owned by user 4242"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpTest(t)
			tc.setup(u)
			_, out, err := u.upgrade(UpgradeOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if got := u.fs.content(Binary); got != "old-binary" {
				t.Errorf("binary = %q", got)
			}
			if u.fs.content(DatabaseFile) != "db-16" {
				t.Error("database changed")
			}
			if sets, _ := u.fs.ReadDir(BackupRoot); len(sets) > 0 {
				t.Errorf("a partial backup set was left: %v", sets)
			}
			u.r.mu.Lock()
			defer u.r.mu.Unlock()
			if !u.r.active || u.r.running != "v1" {
				t.Errorf("the old service is not running: active=%v running=%q", u.r.active, u.r.running)
			}
			if len(u.r.startedOff) != 1 || u.r.startedOff[0] {
				t.Errorf("starts = %v, want one normal start", u.r.startedOff)
			}
		})
	}
}

// A failure at any step after the binary is installed restores the database
// and the old binary before the old binary runs.
func TestUpgradeFailureAfterInstallRestores(t *testing.T) {
	cases := []struct {
		name  string
		setup func(u *upTest)
		want  string
		// startedOff is every start in order; the last is the rollback's.
		startedOff string
	}{
		{"install fails", func(u *upTest) {
			var once sync.Once
			u.th.host.FS = failWriteFS{memFS: u.fs, fail: func(p string) error {
				var err error
				if p == Binary {
					once.Do(func() { err = errors.New("read-only file system") })
				}
				return err
			}}
		}, "read-only file system", "[false]"},
		{"drop-in cannot be written", func(u *upTest) {
			u.th.host.FS = failWriteFS{memFS: u.fs, fail: func(p string) error {
				if p == UpgradeDropIn {
					return errors.New("no space left")
				}
				return nil
			}}
		}, "no space left", "[false]"},
		{"drop-in not loaded by systemd", func(u *upTest) { u.r.ignoreDropIn = true }, "refusing to start LCM with automation on", "[false]"},
		{"start fails after migrating", func(u *upTest) {
			u.r.onStart = func(n int) error {
				if n == 1 {
					return exit(1)
				}
				return nil
			}
		}, "exited with status 1", "[true false]"},
		{"never reports the new version", func(u *upTest) {
			u.healthOverride = func(running string) (string, error) {
				if running == "v2" {
					return "", errors.New("connection refused")
				}
				return running, nil
			}
		}, "within 2m0s", "[true false]"},
		{"reports another version", func(u *upTest) {
			u.healthOverride = func(running string) (string, error) {
				if running == "v2" {
					return "v9", nil
				}
				return running, nil
			}
		}, `health reports version "v9"`, "[true false]"},
		{"database schema is not the new one", func(u *upTest) { u.r.schemaAfterStart = 16 }, "expected 17", "[true false]"},
		{"normal restart fails", func(u *upTest) {
			u.r.onStart = func(n int) error {
				if n == 2 {
					return exit(1)
				}
				return nil
			}
		}, "exited with status 1", "[true false false]"},
		{"normal restart never healthy", func(u *upTest) {
			u.healthOverride = func(running string) (string, error) {
				if running == "v2" && len(u.r.startedOff) >= 2 {
					return "", errors.New("connection refused")
				}
				return running, nil
			}
		}, "within 2m0s", "[true false false]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpTest(t)
			u.fs.file(DatabaseFile+"-wal", "wal-16", 0o600, 990, 990)
			tc.setup(u)
			set := u.setName()
			_, out, err := u.upgrade(UpgradeOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "rolled back") || !strings.Contains(err.Error(), set) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			// The -wal of the old database comes back; none of the new one's.
			if got := u.fs.content(DatabaseFile + "-wal"); got != "wal-16" {
				t.Errorf("wal = %q", got)
			}
			if u.fs.content(Binary) != "old-binary" || u.fs.content(DatabaseFile) != "db-16" {
				t.Errorf("binary %q database %q", u.fs.content(Binary), u.fs.content(DatabaseFile))
			}
			if u.fs.get(DatabaseFile+"-shm") != nil {
				t.Error("stale -shm of the migrated database")
			}
			u.r.mu.Lock()
			defer u.r.mu.Unlock()
			if !u.r.active || u.r.running != "v1" {
				t.Errorf("the old service is not running: active=%v running=%q", u.r.active, u.r.running)
			}
			if u.fs.get(UpgradeDropIn) != nil {
				t.Error("drop-in left behind")
			}
			// The rollback restores before the old binary starts: its start is the last one.
			if got := fmt.Sprint(u.r.startedOff); got != tc.startedOff {
				t.Errorf("starts = %s, want %s", got, tc.startedOff)
			}
			if !strings.Contains(out, "restored "+DatabaseFile) || !strings.Contains(out, "restored "+Binary) {
				t.Errorf("output lacks the restore lines:\n%s", out)
			}
			// The set is kept for inspection and still verifies.
			if u.fs.content(set+"/database/lcm.db") != "db-16" {
				t.Error("the backup set was removed or changed")
			}
		})
	}
}

// Restoring comes before the old binary starts, never after.
func TestUpgradeRollbackRestoresBeforeStartingOld(t *testing.T) {
	u := newUpTest(t)
	u.r.onStart = func(n int) error {
		if n == 1 {
			return exit(1)
		}
		return nil
	}
	if _, _, err := u.upgrade(UpgradeOptions{}); err == nil {
		t.Fatal("expected an error")
	}
	set := u.setName()
	u.order(
		"run systemctl start lcm.service", // the new binary, which fails
		"remove "+UpgradeDropIn,
		"run systemctl stop lcm.service",
		"remove "+DatabaseFile+"-wal",
		"write "+DatabaseFile,
		"write "+Binary,
		"run systemctl start lcm.service", // the old binary
	)
	_ = set
}

func TestUpgradeRollbackAfterCancellation(t *testing.T) {
	u := newUpTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.r.onStart = func(n int) error {
		if n == 1 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	_, out, err := u.upgradeCtx(ctx, UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	u.restoredOld()
}

func TestUpgradeRollbackRefusesATamperedBackup(t *testing.T) {
	u := newUpTest(t)
	set := u.setName()
	u.r.onStart = func(n int) error {
		if n == 1 {
			u.fs.file(set+"/database/lcm.db", "tampered", 0o600, 0, 0)
			return exit(1)
		}
		return nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK FAILED") || !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	// Nothing runs on a migrated database or an unverified restore.
	if got := u.fs.content(DatabaseFile); got != "db-17" {
		t.Errorf("database = %q, want it left alone", got)
	}
	if n := len(u.r.called("systemctl start")); n != 1 {
		t.Errorf("start calls = %d, want only the failed new start", n)
	}
}

func TestUpgradeRollbackReportsAnUnhealthyOldBinary(t *testing.T) {
	u := newUpTest(t)
	u.r.onStart = func(n int) error {
		if n == 1 {
			return exit(1)
		}
		return nil
	}
	u.healthOverride = func(running string) (string, error) {
		if running == "v1" {
			return "", errors.New("connection refused")
		}
		return running, nil
	}
	set := u.setName()
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK FAILED") || !strings.Contains(err.Error(), set) {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestUpgradeRollbackWhenStopCannotBeConfirmed(t *testing.T) {
	u := newUpTest(t)
	u.r.onStart = func(n int) error {
		if n == 1 {
			u.r.stuck = true
			return exit(1)
		}
		return nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK FAILED") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	// A running process must never get the database swapped under it.
	if got := u.fs.content(DatabaseFile); got != "db-17" {
		t.Errorf("database = %q, want it left alone", got)
	}
}

func TestUpgradeRetentionKeepsThreeSets(t *testing.T) {
	u := newUpTest(t)
	for _, n := range []string{"20260101T000000Z", "20260201T000000Z", "20260301T000000Z", "20260401T000000Z"} {
		u.fs.dir(BackupRoot+"/upgrade-"+n, 0o700, 0, 0)
		u.fs.file(BackupRoot+"/upgrade-"+n+"/manifest.json", "{}", 0o600, 0, 0)
	}
	// Only the sets this command writes are touched.
	u.fs.dir(BackupRoot+"/lcm-pre-16", 0o700, 0, 0)
	u.fs.file(BackupRoot+"/upgrade-20250101T000000Z", "a file, not a set", 0o600, 0, 0)
	u.fs.dir(BackupRoot+"/upgrade-20250202T000000Z-keep", 0o700, 0, 0)
	u.fs.dir(BackupRoot+"/upgrade-20250303T000000Z", 0o700, aliceUID, aliceUID)
	u.fs.link(BackupRoot+"/upgrade-20250404T000000Z", "/etc", 0, 0)
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	names, _ := u.fs.ReadDir(BackupRoot)
	var sets []string
	for _, n := range names {
		if info, _ := u.fs.Lstat(BackupRoot + "/" + n); upgradeSet.MatchString(n) && info.IsDir() && info.UID == 0 {
			sets = append(sets, n)
		}
	}
	want := []string{"upgrade-20260301T000000Z", "upgrade-20260401T000000Z", baseName(u.setName())}
	if fmt.Sprint(sets) != fmt.Sprint(want) {
		t.Errorf("sets = %v, want %v", sets, want)
	}
	for _, kept := range []string{"lcm-pre-16", "upgrade-20250101T000000Z", "upgrade-20250202T000000Z-keep", "upgrade-20250303T000000Z", "upgrade-20250404T000000Z"} {
		if u.fs.get(BackupRoot+"/"+kept) == nil {
			t.Errorf("%s was removed", kept)
		}
	}
}

func baseName(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func TestRetentionNeverRemovesTheCurrentSet(t *testing.T) {
	// A clock that went backwards must not delete the set just written.
	got := retentionRemovals([]string{"upgrade-20270101T000000Z", "upgrade-20270102T000000Z", "upgrade-20270103T000000Z", "upgrade-20270104T000000Z"}, "upgrade-20260101T000000Z", 3)
	if contains(got, "upgrade-20260101T000000Z") {
		t.Errorf("removals = %v include the current set", got)
	}
	if fmt.Sprint(got) != "[upgrade-20270101T000000Z]" {
		t.Errorf("removals = %v", got)
	}
	if got := retentionRemovals(nil, "upgrade-20270101T000000Z", 3); len(got) != 0 {
		t.Errorf("removals = %v", got)
	}
}

func TestUpgradeKeepsAllSetsOnFailure(t *testing.T) {
	u := newUpTest(t)
	for _, n := range []string{"20260101T000000Z", "20260201T000000Z", "20260301T000000Z", "20260401T000000Z"} {
		u.fs.dir(BackupRoot+"/upgrade-"+n, 0o700, 0, 0)
	}
	u.r.onStart = func(n int) error {
		if n == 1 {
			return exit(1)
		}
		return nil
	}
	if _, _, err := u.upgrade(UpgradeOptions{}); err == nil {
		t.Fatal("expected an error")
	}
	if names, _ := u.fs.ReadDir(BackupRoot); len(names) != 5 {
		t.Errorf("sets = %v, want all four old ones and the new one", names)
	}
}

func TestUpgradeOutputIsSanitized(t *testing.T) {
	u := newUpTest(t)
	u.healthOverride = func(running string) (string, error) {
		if running == "v2" {
			return "\x1b[2Jv2\u202e", nil
		}
		return running, nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{out, err.Error()} {
		if strings.ContainsAny(s, "\x1b\u202e") {
			t.Errorf("control characters reached the terminal: %q", s)
		}
	}
}
