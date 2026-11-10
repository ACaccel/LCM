package deployhelper

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

const (
	commit1 = "1111111111111111111111111111111111111111"
	commit2 = "2222222222222222222222222222222222222222"
	input   = "/srv/inputs/web/assets"
)

// env is a host in a temporary directory, with every account mapped to the
// test's own user and fake systemd commands.
type env struct {
	t      *testing.T
	root   string
	runner *execx.Fake
	clock  *clock.Fake
	h      *Helper
	cfg    hostsetup.Config

	mu sync.Mutex
	// build runs as the fake build in the build's src directory (a real
	// path) and returns its output and exit status.
	build      func(src string) (string, int)
	migrate    func(argv []string) (string, int)
	analyze    int
	reloadFail int
	needReload bool
	enabled    bool
	active     bool
	// activeState, when set, is what is-active prints for web.service until
	// it is stopped (unless stopStuck): "activating" is a unit waiting to
	// restart after a failure.
	activeState string
	stopStuck   bool
	// workerWrap, when set, runs in place of the backup worker: it gets the
	// worker's mode and a function that runs the real one.
	workerWrap func(mode string, run func() (string, int)) (string, int)
	// tools maps database tool names to test programs.
	tools      map[string]string
	opCounter  int
	systemctls []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("the test host maps every account to the test user, which must not be root")
	}
	e := &env{t: t, root: t.TempDir(), runner: &execx.Fake{}, clock: clock.NewFake(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))}
	if err := os.Chmod(e.root, 0o755); err != nil { //nolint:gosec // the test host root, readable like /
		t.Fatal(err)
	}
	for _, d := range []string{"/etc/lcm", "/etc/systemd/system", "/etc/web", "/var/lib/lcm/work/web/.git/refs/heads", "/var/lib/lcm-build",
		"/var/cache/lcm-build", "/opt/lcm/runtimes/node24/bin", "/opt/web/releases", "/var/lib/web", "/run", input} {
		e.mkdir(d, 0o755)
	}
	e.mkdir("/var/lib/lcm-deploy", 0o700)
	e.mkdir("/var/backups/lcm", 0o700)
	e.mkdir("/var/lib/lcm-build/web", 0o750)
	e.mkdir("/var/cache/lcm-build/web", 0o750)
	e.write("/etc/login.defs", "UID_MIN 100000\n", 0o644)
	e.write("/etc/web/env", "SECRET=1\n", 0o640)
	e.write("/opt/lcm/runtimes/node24/bin/node", "node", 0o755)
	e.write(input+"/song.ogg", "music", 0o644)
	e.checkout(commit1)
	e.write("/var/lib/lcm/work/web/package.json", "{}", 0o644)
	e.write("/var/lib/lcm/work/web/src/app.js", "app", 0o644)

	uid, gid := os.Getuid(), os.Getgid()
	accounts := map[string]hostsetup.Account{}
	for name, a := range map[string][2]string{
		"lcm": {hostsetup.DataDir, ""}, "lcmb-web": {"/var/lib/lcm-build/web", "LCM build web"},
		"web": {"/var/lib/web", "LCM service web"}, "alice": {"/home/alice", ""},
	} {
		accounts[name] = hostsetup.Account{Name: name, UID: uid, GID: gid, Home: a[0], GECOS: a[1], Shell: "/usr/sbin/nologin"}
	}
	e.cfg = hostsetup.Config{
		Operator: "alice", Components: hostsetup.Components{Deploy: true}, Backup: &hostsetup.Backup{Dir: "/var/backups/lcm"},
		Runtimes: map[string]string{"node24": "/home/alice/.local/node"},
		Services: map[string]hostsetup.ServiceGrant{"web": {Port: 3200, Runtimes: []string{"node24"}, Inputs: map[string]string{"assets": input}}},
	}
	e.build = func(src string) (string, int) {
		e.mkdirReal(filepath.Join(src, "dist/bin"))
		e.writeReal(filepath.Join(src, "dist/index.js"), "index", 0o644)
		e.writeReal(filepath.Join(src, "dist/bin/server"), "#!/bin/sh\n", 0o755)
		return "building\x1b[31m red\x1b[0m\ndone\n", 0
	}
	e.migrate = func([]string) (string, int) { return "migrated\n", 0 }
	e.runner.On(func(execx.Cmd) bool { return true }, e.handle)

	ledger, err := OpenLedger(filepath.Join(e.root, hostsetup.DeployLedger), uid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	e.h = &Helper{Host{
		FS: hostsetup.OS{Root: e.root}, Runner: e.runner, Clock: e.clock, Ledger: ledger, RootUID: uid, RootGID: gid,
		Config: func() (hostsetup.Config, error) { return e.cfg, nil },
		Lookup: func(_ context.Context, name string) (hostsetup.Account, bool, error) {
			acc, ok := accounts[name]
			return acc, ok, nil
		},
	}}
	return e
}

func (e *env) real(p string) string { return filepath.Join(e.root, p) }

func (e *env) mkdirReal(p string) {
	e.t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil { //nolint:gosec // test host directories with the usual modes
		e.t.Fatal(err)
	}
}

func (e *env) mkdir(p string, perm fs.FileMode) {
	e.t.Helper()
	e.mkdirReal(e.real(p))
	// Fix the modes of the whole chain, whatever the umask.
	for q := p; q != "/"; q = filepath.Dir(q) {
		mode := fs.FileMode(0o755)
		if q == p {
			mode = perm
		}
		if err := os.Chmod(e.real(q), mode); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) writeReal(p, data string, perm fs.FileMode) {
	e.t.Helper()
	if err := os.WriteFile(p, []byte(data), perm); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) write(p, data string, perm fs.FileMode) {
	e.t.Helper()
	e.mkdirReal(filepath.Dir(e.real(p)))
	e.writeReal(e.real(p), data, perm)
}

func (e *env) read(p string) string {
	b, err := os.ReadFile(e.real(p)) //nolint:gosec // a file of the test's own tree
	if err != nil {
		return ""
	}
	return string(b)
}

func (e *env) exists(p string) bool {
	_, err := os.Lstat(e.real(p))
	return err == nil
}

// checkout points the checkout's HEAD at a branch whose ref is commit.
func (e *env) checkout(commit string) {
	e.write("/var/lib/lcm/work/web/.git/HEAD", "ref: refs/heads/main\n", 0o644)
	e.write("/var/lib/lcm/work/web/.git/refs/heads/main", commit+"\n", 0o644)
}

func arg(c execx.Cmd, prefix string) string {
	for _, a := range c.Argv {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			return v
		}
	}
	return ""
}

func (e *env) handle(c execx.Cmd) (execx.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := func(out string, code int) (execx.Result, error) {
		if code != 0 {
			return execx.Result{Output: out, ExitCode: code}, &execx.ExitError{Code: code}
		}
		return execx.Result{Output: out}, nil
	}
	if c.Argv[0] == systemdRun {
		// The fake build runs unlocked: it may advance the clock, and the
		// build watcher then runs systemctl.
		build, migrate := e.build, e.migrate
		e.mu.Unlock()
		defer e.mu.Lock()
		if strings.HasPrefix(arg(c, "--unit="), "lcm-build-") {
			return result(build(e.real(arg(c, "--working-directory="))))
		}
		if unit := arg(c, "--unit="); strings.HasPrefix(unit, "lcm-backup-") || strings.HasPrefix(unit, "lcm-restore-") {
			return result(e.worker(c))
		}
		i := slices.Index(c.Argv, "--")
		return result(migrate(c.Argv[i+1:]))
	}
	switch c.Argv[0] {
	case systemdAnalyze:
		return result("bad unit\n", e.analyze)
	case systemctl:
		line := strings.Join(c.Argv[1:], " ")
		e.systemctls = append(e.systemctls, line)
		switch c.Argv[1] {
		case "is-active":
			if e.activeState != "" {
				return result(e.activeState+"\n", 3)
			}
			if e.active {
				return result("active\n", 0)
			}
			return result("inactive\n", 3)
		case "stop", "start":
			if c.Argv[2] == "web.service" {
				e.active = c.Argv[1] == "start"
				if !e.stopStuck || c.Argv[1] == "start" {
					e.activeState = ""
				}
			}
		case "is-enabled":
			if !e.enabled {
				return result("disabled\n", 1)
			}
			return result("enabled\n", 0)
		case "enable":
			e.enabled = true
		case "daemon-reload":
			if e.reloadFail > 0 {
				e.reloadFail--
				return result("Failed to reload daemon\n", 1)
			}
			e.needReload = false
		case "show":
			if c.Argv[2] == "--property=NeedDaemonReload" {
				return result(map[bool]string{true: "yes\n", false: "no\n"}[e.needReload], 0)
			}
			return result("LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nMainPID=4242\n", 0)
		}
		return result("", 0)
	}
	return result("unexpected command", 127)
}

func (e *env) systemctlCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.systemctls...)
}

// call sends a request with a fresh operation ID unless it has one.
func (e *env) call(req Request) Response {
	e.t.Helper()
	req.Version = Version
	if req.OperationID == "" {
		e.opCounter++
		req.OperationID = fmt.Sprintf("00000000-0000-4000-8000-%012d", e.opCounter)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp := e.h.Handle(context.Background(), raw)
	// What the helper writes must decode as a reply.
	if _, err := DecodeResponse(encodeResponse(resp)); err != nil {
		e.t.Fatalf("reply does not decode: %v", err)
	}
	return resp
}

func (e *env) ok(req Request) Response {
	e.t.Helper()
	resp := e.call(req)
	if resp.Code != "" || (req.Op.Changes() && resp.State != StateSucceeded) {
		e.t.Fatalf("%s: %+v", req.Op, resp)
	}
	return resp
}

func (e *env) refused(req Request, code Code, want string) Response {
	e.t.Helper()
	resp := e.call(req)
	if resp.Code != code || !strings.Contains(resp.Message, want) {
		e.t.Fatalf("%s: got %s %q, want %s %q", req.Op, resp.Code, resp.Message, code, want)
	}
	return resp
}

func buildReq(commit string) Request {
	return Request{Op: OpBuild, Service: "web", Commit: commit, Command: "npm ci && npm run build", TimeoutSec: 600, Runtimes: []string{"node24"}}
}

func (e *env) deploy(commit string) (string, string) {
	e.t.Helper()
	e.checkout(commit)
	b := e.ok(buildReq(commit))
	in := e.ok(Request{Op: OpInstall, Service: "web", BuildID: b.Build.BuildID, Artifact: "dist"})
	e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: in.Install.ReleaseID, Start: []string{"index.js"}, Runtime: "node24"})
	e.clock.Advance(time.Second)
	return b.Build.BuildID, in.Install.ReleaseID
}

func TestBuildInstallActivate(t *testing.T) {
	e := newEnv(t)
	b := e.ok(buildReq(commit1))
	id := b.Build.BuildID
	if !idPattern.MatchString(id) || b.Build.Commit != commit1 || !strings.HasPrefix(id, "20261109T120000Z-") {
		t.Fatalf("build result %+v", b.Build)
	}
	if strings.ContainsRune(b.Log, 0x1b) || !strings.Contains(b.Log, "building?[31m red") {
		t.Errorf("log not sanitized: %q", b.Log)
	}
	dir := "/var/lib/lcm-build/web/" + id
	if e.read(dir+"/src/package.json") != "{}" || e.read(dir+"/src/src/app.js") != "app" || e.read(dir+"/build.sh") != "npm ci && npm run build\n" {
		t.Errorf("build tree incomplete")
	}
	if e.exists(dir + "/src/.git") {
		t.Error("the build got the checkout's .git")
	}
	var rec buildRecord
	if err := json.Unmarshal([]byte(e.read(dir+"/build.json")), &rec); err != nil || rec.State != StateSucceeded || rec.Commit != commit1 {
		t.Errorf("build record %+v %v", rec, err)
	}
	if fi, err := os.Stat(e.real(dir)); err != nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("build directory %v %v", fi, err)
	}
	calls := e.runner.Calls()
	argv := calls[len(calls)-1].Argv
	for _, want := range []string{
		"--wait", "--pipe", "--collect", "--unit=lcm-build-web-" + id, fmt.Sprintf("--uid=%d", os.Getuid()),
		"--working-directory=" + dir + "/src", "--setenv=PATH=/opt/lcm/runtimes/node24/bin:" + systemPath,
		"--setenv=HOME=" + dir + "/home", "--setenv=LCM_INPUTS_DIR=/run/lcm-build/inputs",
		"--property=ProtectSystem=strict", "--property=ProtectHome=yes", "--property=PrivateTmp=yes", "--property=NoNewPrivileges=yes",
		"--property=ReadWritePaths=" + dir + "/src", "--property=ReadWritePaths=/var/cache/lcm-build/web",
		"--property=TemporaryFileSystem=/run/lcm-build:ro", "--property=BindReadOnlyPaths=" + input + ":/run/lcm-build/inputs/assets",
		"--property=RuntimeMaxSec=600", "--property=MemoryMax=4G", "--property=CapabilityBoundingSet=",
		"--property=InaccessiblePaths=-/var/lib/lcm -/etc/lcm -/var/lib/lcm-deploy -/var/lib/lcm-machine-power -/var/backups/lcm",
		"--property=TemporaryFileSystem=/var/lib/lcm-build:ro", "--property=TemporaryFileSystem=/var/cache/lcm-build:ro",
		"--property=BindPaths=" + dir + "/src", "--property=BindPaths=" + dir + "/home", "--property=BindPaths=/var/cache/lcm-build/web",
		"--property=BindReadOnlyPaths=" + dir + "/build.sh", "--property=ProtectProc=invisible",
		"--property=IPAddressDeny=localhost",
	} {
		if !slices.Contains(argv, want) {
			t.Errorf("build argv lacks %q: %q", want, argv)
		}
	}
	for _, a := range argv {
		if strings.Contains(a, "ProcSubset") {
			t.Errorf("build argv hides the system-wide /proc files: %q", a)
		}
	}
	if tail := argv[len(argv)-3:]; !slices.Equal(tail, []string{"--", "/bin/sh", dir + "/build.sh"}) {
		t.Errorf("build command %q", tail)
	}
	for _, a := range argv {
		if (strings.HasPrefix(a, "--property=ReadWritePaths=") || strings.HasPrefix(a, "--property=BindPaths=")) &&
			!strings.Contains(a, "="+dir+"/") && !strings.HasSuffix(a, "=/var/cache/lcm-build/web") {
			t.Errorf("argv exposes more than this build: %q", a)
		}
		if strings.Contains(a, "npm ci") || strings.Contains(a, "SECRET") {
			t.Errorf("argv carries the command or a secret: %q", a)
		}
	}

	in := e.ok(Request{Op: OpInstall, Service: "web", BuildID: id, Artifact: "dist"})
	rel := in.Install.ReleaseID
	m := in.Install.Manifest
	if m.Commit != commit1 || m.BuildID != id || m.Files != 2 || m.Size != int64(len("index")+len("#!/bin/sh\n")) || len(m.TreeSHA256) != 64 || !slices.Equal(m.Runtimes, []string{"node24"}) {
		t.Errorf("manifest %+v", m)
	}
	relDir := "/opt/web/releases/" + rel
	for p, want := range map[string]fs.FileMode{relDir: 0o755, relDir + "/bin": 0o755, relDir + "/index.js": 0o644, relDir + "/bin/server": 0o755} {
		if fi, err := os.Lstat(e.real(p)); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %04o", p, fi, err, want)
		}
	}
	if sum, _, _, err := treeDigest(e.h.FS, relDir); err != nil || sum != m.TreeSHA256 {
		t.Errorf("digest %s %v, manifest %s", sum, err, m.TreeSHA256)
	}

	a := e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "node24"})
	if a.Activate.ReleaseID != rel || a.Activate.Previous != "" || !a.Activate.Switched || !a.Activate.UnitChanged {
		t.Errorf("activate %+v", a.Activate)
	}
	unit := e.read("/etc/systemd/system/web.service")
	for _, want := range []string{
		hostsetup.ServiceUnitMarker + "\n[Unit]\n",
		"\nUser=web\nGroup=web\nWorkingDirectory=/opt/web/current\n",
		"\nExecStart=/opt/lcm/runtimes/node24/bin/node index.js\n",
		"\nEnvironmentFile=/etc/web/env\nEnvironment=PORT=3200 HOST=127.0.0.1\nEnvironment=PATH=/opt/lcm/runtimes/node24/bin:" + systemPath + "\n",
		"\nNoNewPrivileges=yes\n", "\nProtectSystem=strict\n", "\nProtectHome=yes\n", "\nPrivateTmp=yes\n", "\nPrivateDevices=yes\n",
		"\nRestrictSUIDSGID=yes\n", "\nCapabilityBoundingSet=\n", "\nReadWritePaths=/var/lib/web\n",
		"\nInaccessiblePaths=-/var/lib/lcm -/etc/lcm -/var/lib/lcm-deploy -/var/lib/lcm-machine-power -/var/backups/lcm\n",
		"\nInaccessiblePaths=-/var/lib/lcm-build -/var/cache/lcm-build\n", "\nProtectProc=invisible\n",
		"\nSocketBindAllow=tcp:3200\nSocketBindDeny=any\nRestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK\nPrivateIPC=yes\n", "\n[Install]\nWantedBy=multi-user.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if target, _ := os.Readlink(e.real("/opt/web/current")); target != "releases/"+rel {
		t.Errorf("current -> %q", target)
	}
	if got := e.systemctlCalls(); !slices.Equal(got, []string{"daemon-reload", "is-enabled web.service", "enable --quiet web.service", "restart web.service"}) {
		t.Errorf("systemctl %q", got)
	}

	// A second deployment with the same unit needs no reload; the first
	// release becomes previous.
	e.clock.Advance(time.Minute)
	_, rel2 := e.deploy(commit2)
	if target, _ := os.Readlink(e.real("/opt/web/previous")); target != "releases/"+rel {
		t.Errorf("previous -> %q", target)
	}
	if got := e.systemctlCalls(); slices.Contains(got[4:], "daemon-reload") {
		t.Errorf("reloaded for an unchanged unit: %q", got)
	}
	list := e.ok(Request{Op: OpReleases, Service: "web"}).Releases
	if len(list) != 2 || list[0].ReleaseID != rel2 || !list[0].Current || list[1].ReleaseID != rel || !list[1].Previous || list[1].Manifest.Commit != commit1 {
		t.Errorf("releases %+v", list)
	}
	st := e.ok(Request{Op: OpStatus, Service: "web"}).Unit
	if st == nil || st.ActiveState != "active" || st.MainPID != 4242 || st.Release != rel2 {
		t.Errorf("status %+v", st)
	}
	for _, op := range []Op{OpStop, OpStart, OpRestart} {
		if r := e.ok(Request{Op: op, Service: "web"}); r.State != StateSucceeded {
			t.Errorf("%s: %+v", op, r)
		}
	}
	if got := e.systemctlCalls(); !slices.Equal(got[len(got)-4:], []string{"show --property=LoadState,ActiveState,SubState,UnitFileState,MainPID web.service", "stop web.service", "start web.service", "restart web.service"}) {
		t.Errorf("systemctl %q", got)
	}
}

func TestBuildLimits(t *testing.T) {
	e := newEnv(t)
	free := uint64(10 << 30)
	e.h.Free = func(p string) (uint64, uint64, error) {
		if p != "/var/lib/lcm-build/web" {
			t.Errorf("measured %s", p)
		}
		return free, 1 << 20, nil
	}
	e.h.Limits = Limits{MaxBytes: 1000, CacheBytes: 100, Interval: time.Second}

	// A cache that grew too large is emptied before the build.
	e.write("/var/cache/lcm-build/web/npm/big", strings.Repeat("x", 200), 0o644)
	e.ok(buildReq(commit1))
	if e.exists("/var/cache/lcm-build/web/npm") {
		t.Error("the oversized cache was kept")
	}

	// A build that writes too much is stopped.
	e.build = func(src string) (string, int) {
		e.writeReal(filepath.Join(src, "huge"), strings.Repeat("x", 2000), 0o644)
		e.clock.Advance(time.Second)
		return "killed\n", 143
	}
	e.refused(buildReq(commit1), CodeFailed, "the build was stopped: its files exceed 1000 bytes")
	if got := e.systemctlCalls(); len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "stop lcm-build-web-") {
		t.Errorf("systemctl %q", got)
	}

	// So is one that fills the file system, and none starts without room.
	e.build = func(string) (string, int) {
		free = 1 << 20
		e.clock.Advance(time.Second)
		return "", 143
	}
	e.refused(buildReq(commit1), CodeFailed, "the build was stopped: the file system of /var/lib/lcm-build/web has only 1048576 bytes")
	e.refused(buildReq(commit1), CodeRefused, "not enough room for a build")
}

func TestPruning(t *testing.T) {
	e := newEnv(t)
	var builds, releases []string
	for i := range 5 {
		b, r := e.deploy(fmt.Sprintf("%040d", i))
		builds, releases = append(builds, b), append(releases, r)
	}
	for i, b := range builds {
		if got := e.exists("/var/lib/lcm-build/web/" + b); got != (i >= 2) {
			t.Errorf("build %d kept: %v", i, got)
		}
	}
	for i, r := range releases {
		if got := e.exists("/opt/web/releases/" + r); got != (i >= 2) {
			t.Errorf("release %d kept: %v", i, got)
		}
		if got := e.exists("/opt/web/releases/" + r + ".json"); got != (i >= 2) {
			t.Errorf("manifest %d kept: %v", i, got)
		}
	}
	// Activating an old release keeps it and the one it replaces, beyond
	// keepReleases.
	e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: releases[2], Start: []string{"index.js"}, Runtime: "node24", KeepReleases: 2})
	for i, r := range releases {
		if got := e.exists("/opt/web/releases/" + r); got != (i >= 2) {
			t.Errorf("after keepReleases 2, release %d kept: %v", i, got)
		}
	}
}

func TestRefusals(t *testing.T) {
	e := newEnv(t)
	_, rel := e.deploy(commit1)
	b := e.ok(buildReq(commit1)).Build.BuildID
	act := Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "node24"}

	e.refused(Request{Op: OpStatus, Service: "other"}, CodeRefused, "other is not in the services of /etc/lcm/host.json")
	e.refused(Request{Op: OpBuild, Service: "other", Commit: commit1, Command: "x", TimeoutSec: 1}, CodeRefused, "not in the services")
	e.refused(Request{Op: OpBuild, Service: "web", Commit: commit1, Command: "x", TimeoutSec: 1, Runtimes: []string{"python3"}}, CodeRefused, "the runtime python3 is not granted to web")
	e.refused(Request{Op: OpBuild, Service: "web", Commit: commit2, Command: "x", TimeoutSec: 1}, CodeRefused, "is at "+commit1+", not "+commit2)
	e.refused(Request{Op: OpInstall, Service: "web", BuildID: "20200101T000000Z-00000000", Artifact: "dist"}, CodeNotFound, "there is no build")
	e.refused(Request{Op: OpInstall, Service: "web", BuildID: b, Artifact: "missing"}, CodeNotFound, "the artifact missing is not in build")
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: "20200101T000000Z-00000000", Start: []string{"index.js"}}, CodeNotFound, "there is no release")
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "python3"}, CodeRefused, "the runtime python3 is not granted")
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}}, CodeRefused, "index.js is not executable; name a runtime")
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"nope.js"}, Runtime: "node24"}, CodeRefused, "nope.js is not a regular file")
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "node24", Migrate: []string{"migrate.sh"}}, CodeRefused, "neither a program of the runtime")

	e.write("/etc/web/env", "export PORT=80\n", 0o640)
	e.refused(act, CodeRefused, "/etc/web/env sets PORT, which comes from host.json")
	e.write("/etc/web/env", "", 0o640)
	if err := os.Chmod(e.real("/etc/web/env"), 0o666); err != nil { //nolint:gosec // an env file others may write, to be refused
		t.Fatal(err)
	}
	e.refused(act, CodeRefused, "/etc/web/env is not root's alone")
	e.write("/etc/web/env", "", 0o640)
	e.write("/usr/lib/systemd/system/web.service", "[Service]\n", 0o644)
	e.refused(act, CodeRefused, "the unit /usr/lib/systemd/system/web.service exists")
	e.refused(Request{Op: OpRestart, Service: "web"}, CodeRefused, "/usr/lib/systemd/system/web.service exists")
	if err := os.Remove(e.real("/usr/lib/systemd/system/web.service")); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(e.real("/srv/inputs"), 0o777); err != nil { //nolint:gosec // an input directory others may write, to be refused
		t.Fatal(err)
	}
	e.refused(buildReq(commit1), CodeRefused, "input assets: /srv/inputs is writable by others")
	if err := os.Chmod(e.real("/srv/inputs"), 0o755); err != nil { //nolint:gosec // the usual mode of an input directory
		t.Fatal(err)
	}

	e.build = func(string) (string, int) { return "npm ERR! \x07boom\n", 1 }
	failed := e.refused(buildReq(commit1), CodeFailed, "the build exited with status 1")
	if failed.State != StateFailed || failed.Log != "npm ERR! ?boom\n" || failed.Build == nil {
		t.Errorf("failed build %+v", failed)
	}
	e.refused(Request{Op: OpInstall, Service: "web", BuildID: failed.Build.BuildID, Artifact: "dist"}, CodeRefused, "did not succeed")

	e.cfg.Components.Deploy = false
	e.refused(Request{Op: OpRestart, Service: "web"}, CodeRefused, "components.deploy is false")
	e.ok(Request{Op: OpStatus, Service: "web"})
}

func TestConfinedCopies(t *testing.T) {
	t.Run("checkout link leaving the data directory", func(t *testing.T) {
		e := newEnv(t)
		if err := os.Symlink("/etc", e.real("/var/lib/lcm/work/web/evil")); err != nil {
			t.Fatal(err)
		}
		e.refused(buildReq(commit1), CodeFailed, "points outside")
	})
	t.Run("checkout entry of another owner", func(t *testing.T) {
		e := newEnv(t)
		e.h.Lookup = func(_ context.Context, name string) (hostsetup.Account, bool, error) {
			uid := os.Getuid()
			if name == "lcm" {
				uid++
			}
			home := map[string]string{"lcm": hostsetup.DataDir, "lcmb-web": "/var/lib/lcm-build/web", "web": "/var/lib/web"}[name]
			comment := map[string]string{"lcmb-web": "LCM build web", "web": "LCM service web"}[name]
			return hostsetup.Account{Name: name, UID: uid, GID: os.Getgid(), Home: home, GECOS: comment, Shell: "/usr/sbin/nologin"}, true, nil
		}
		e.refused(buildReq(commit1), CodeFailed, "is owned by user")
	})
	for name, artifact := range map[string]func(src string) string{
		"artifact is a link out of the build": func(src string) string {
			if err := os.Symlink("/etc", filepath.Join(src, "dist")); err != nil {
				t.Fatal(err)
			}
			return "dist"
		},
		"artifact below a link out of the build": func(src string) string {
			if err := os.Symlink("../../../../../..", filepath.Join(src, "out")); err != nil {
				t.Fatal(err)
			}
			return "out/etc"
		},
		"artifact link leaving the release": func(src string) string {
			e2 := filepath.Join(src, "dist")
			if err := os.MkdirAll(e2, 0o755); err != nil { //nolint:gosec // a build output directory with the usual mode
				t.Fatal(err)
			}
			if err := os.Symlink("../package.json", filepath.Join(e2, "pkg")); err != nil {
				t.Fatal(err)
			}
			return "dist"
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			var art string
			e.build = func(src string) (string, int) {
				art = artifact(src)
				return "", 0
			}
			b := e.ok(buildReq(commit1)).Build.BuildID
			resp := e.call(Request{Op: OpInstall, Service: "web", BuildID: b, Artifact: art})
			if resp.Code == "" || resp.State != StateFailed {
				t.Fatalf("install of %s: %+v", art, resp)
			}
			names, _ := os.ReadDir(e.real("/opt/web/releases"))
			if len(names) != 0 {
				t.Errorf("release left behind: %v", names)
			}
		})
	}
}

func TestActivateFailures(t *testing.T) {
	e := newEnv(t)
	_, rel1 := e.deploy(commit1)
	unit1 := e.read("/etc/systemd/system/web.service")
	e.checkout(commit2)
	b := e.ok(buildReq(commit2)).Build.BuildID
	rel2 := e.ok(Request{Op: OpInstall, Service: "web", BuildID: b, Artifact: "dist"}).Install.ReleaseID

	// A migration failure switches nothing.
	e.migrate = func(argv []string) (string, int) { return "relation exists\n", 3 }
	r := e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"index.js"}, Runtime: "node24", Migrate: []string{"node", "migrate.js", "--up"}},
		CodeFailed, "the migration exited with status 3; nothing was switched")
	if r.Log != "relation exists\n" {
		t.Errorf("log %q", r.Log)
	}
	calls := e.runner.Calls()
	argv := calls[len(calls)-1].Argv
	for _, want := range []string{"--unit=lcm-migrate-web-00000000", "--working-directory=/opt/web/releases/" + rel2, "--setenv=PORT=3200",
		"--property=EnvironmentFile=/etc/web/env", "--property=ReadWritePaths=/var/lib/web", "--property=RuntimeMaxSec=600"} {
		if !slices.Contains(argv, want) {
			t.Errorf("migrate argv lacks %q: %q", want, argv)
		}
	}
	if tail := argv[slices.Index(argv, "--"):]; !slices.Equal(tail, []string{"--", "/opt/lcm/runtimes/node24/bin/node", "migrate.js", "--up"}) {
		t.Errorf("migrate command %q", tail)
	}
	if target, _ := os.Readlink(e.real("/opt/web/current")); target != "releases/"+rel1 {
		t.Errorf("current -> %q after a failed migration", target)
	}

	// A unit that does not verify is put back with the links.
	e.analyze = 1
	r = e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"bin/server", "--listen"}},
		CodeFailed, "the unit does not verify; the previous unit and release were restored")
	if r.Activate == nil || r.Activate.Switched || r.Log != "bad unit\n" {
		t.Errorf("activate %+v", r.Activate)
	}
	if e.read("/etc/systemd/system/web.service") != unit1 {
		t.Error("the unit was not restored")
	}
	if target, _ := os.Readlink(e.real("/opt/web/current")); target != "releases/"+rel1 {
		t.Errorf("current -> %q after a failed verification", target)
	}
	e.analyze = 0
	r = e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"bin/server", "--listen"}})
	if !strings.Contains(e.read("/etc/systemd/system/web.service"), "\nExecStart=/opt/web/current/bin/server --listen\n") || r.Activate.Previous != rel1 {
		t.Errorf("activate %+v\n%s", r.Activate, e.read("/etc/systemd/system/web.service"))
	}

	// An unchanged unit is reloaded when systemd holds an outdated copy.
	e.needReload = true
	n := len(e.systemctlCalls())
	e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"bin/server", "--listen"}})
	if got := e.systemctlCalls()[n:]; !slices.Equal(got[:2], []string{"show --property=NeedDaemonReload --value web.service", "daemon-reload"}) {
		t.Errorf("systemctl %q", got)
	}

	// A reload that fails puts back the previous unit and release.
	unit2 := e.read("/etc/systemd/system/web.service")
	e.reloadFail = 1
	r = e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel1, Start: []string{"index.js"}, Runtime: "node24"},
		CodeFailed, "systemctl daemon-reload exited with status 1; the previous unit and release were restored")
	if e.read("/etc/systemd/system/web.service") != unit2 || r.Activate.Switched {
		t.Errorf("not restored: %+v", r.Activate)
	}
	if target, _ := os.Readlink(e.real("/opt/web/current")); target != "releases/"+rel2 {
		t.Errorf("current -> %q after a failed reload", target)
	}
	// The restored unit is loaded again, and the service is not restarted.
	if got := e.systemctlCalls(); !slices.Equal(got[len(got)-2:], []string{"daemon-reload", "daemon-reload"}) {
		t.Errorf("systemctl %q", got)
	}

	// A unit the helper did not write is never replaced.
	e.write("/etc/systemd/system/web.service", "[Service]\nExecStart=/bin/true\n", 0o644)
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"bin/server"}}, CodeRefused, "was not written by the deploy helper")
}

func TestLedger(t *testing.T) {
	e := newEnv(t)
	id := "11111111-2222-4333-8444-555555555555"
	e.deploy(commit1)
	first := e.ok(Request{Op: OpRestart, Service: "web", OperationID: id})
	n := len(e.systemctlCalls())

	// The same request again is answered from the ledger.
	again := e.ok(Request{Op: OpRestart, Service: "web", OperationID: id})
	if len(e.systemctlCalls()) != n || again.State != first.State {
		t.Errorf("a replay ran again: %+v", again)
	}
	e.refused(Request{Op: OpStop, Service: "web", OperationID: id}, CodeConflict, "was used for another request")
	q := e.call(Request{Op: OpQuery, OperationID: id})
	if q.Op != OpRestart || q.State != StateSucceeded || q.Code != "" {
		t.Errorf("query %+v", q)
	}
	e.refused(Request{Op: OpQuery, OperationID: "99999999-2222-4333-8444-555555555555"}, CodeNotFound, "no operation")

	// A refusal before anything changed is not recorded.
	refusedID := "22222222-2222-4333-8444-555555555555"
	e.refused(Request{Op: OpRestart, Service: "other", OperationID: refusedID}, CodeRefused, "not in the services")
	e.refused(Request{Op: OpQuery, OperationID: refusedID}, CodeNotFound, "no operation")

	// Another operation of the service is busy while one runs.
	unlock, err := e.h.Ledger.Lock("web")
	if err != nil {
		t.Fatal(err)
	}
	e.refused(Request{Op: OpStart, Service: "web"}, CodeBusy, "another operation of web is running")
	// A build has its own lock: it neither waits for unit actions nor
	// blocks them.
	e.ok(buildReq(commit1))
	unlockBuild, err := e.h.Ledger.Lock("web.build")
	if err != nil {
		t.Fatal(err)
	}
	e.refused(buildReq(commit1), CodeBusy, "another build of web is running")
	unlockBuild()
	// Ungranted services never get a lock file.
	if e.exists("/var/lib/lcm-deploy/locks/other") {
		t.Error("a lock file was created for a service host.json does not grant")
	}
	// A running record is reported as running while its lock is held, and
	// as interrupted once it is free.
	runningID := "33333333-2222-4333-8444-555555555555"
	if err := e.h.Ledger.put(record{Version: Version, OperationID: runningID, Service: "web", Op: OpRestart, RequestHash: "x", State: StateRunning, StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if q := e.call(Request{Op: OpQuery, OperationID: runningID}); q.State != StateRunning || q.Op != OpRestart || q.Code != "" {
		t.Errorf("query while running %+v", q)
	}
	unlock()
	q = e.call(Request{Op: OpQuery, OperationID: runningID})
	if q.State != StateInterrupted || q.Code != CodeFailed || !strings.Contains(q.Message, "stopped before the operation finished") {
		t.Errorf("query after the helper stopped %+v", q)
	}
	if r, _, _ := e.h.Ledger.get(runningID); r.State != StateInterrupted {
		t.Errorf("record %+v", r)
	}

	// Records are files only the owner can use.
	fi, err := os.Lstat(e.real("/var/lib/lcm-deploy/ops/" + id + ".json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record file %v %v", fi, err)
	}
	if err := os.Chmod(e.real("/var/lib/lcm-deploy/ops/"+id+".json"), 0o644); err != nil { //nolint:gosec // a record others may read, to be refused
		t.Fatal(err)
	}
	e.refused(Request{Op: OpQuery, OperationID: id}, CodeFailed, "must be a regular file with mode 0600")

	// Finished records are removed after the retention period, by the first
	// request after an hour; the times are the clock's.
	start := e.clock.Now()
	for _, f := range e.records() {
		if err := os.Chtimes(f, start, start); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(e.real("/var/lib/lcm-deploy/prune-stamp"), start, start); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(ledgerRetention + time.Minute)
	old := start.Add(-time.Hour)
	if err := os.Chtimes(e.real("/var/lib/lcm-deploy/ops/"+runningID+".json"), old, old); err != nil {
		t.Fatal(err)
	}
	e.ok(Request{Op: OpRestart, Service: "web"})
	if e.exists("/var/lib/lcm-deploy/ops/" + runningID + ".json") {
		t.Error("an expired record was kept")
	}
}

// records returns the real paths of the ledger's records.
func (e *env) records() []string {
	e.t.Helper()
	files, err := filepath.Glob(e.real("/var/lib/lcm-deploy/ops/*.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	return files
}

func TestLedgerIsBounded(t *testing.T) {
	e := newEnv(t)
	e.deploy(commit1)
	e.h.Ledger.maxRecords = 3
	for range 3 {
		e.ok(Request{Op: OpRestart, Service: "web"})
	}
	// Within the hour the ledger is not listed again.
	if n := len(e.records()); n != 6 {
		t.Fatalf("%d records", n)
	}
	// Then the oldest finished records go first, and a running one stays.
	for i, f := range e.records() {
		at := e.clock.Now().Add(time.Duration(i-10) * time.Minute)
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
	running := "44444444-2222-4333-8444-555555555555"
	if err := e.h.Ledger.put(record{Version: Version, OperationID: running, Service: "web", Op: OpStart, RequestHash: "x", State: StateRunning, StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	old := e.clock.Now().Add(-time.Hour)
	if err := os.Chtimes(e.real("/var/lib/lcm-deploy/ops/"+running+".json"), old, old); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(pruneEvery + time.Minute)
	last := e.ok(Request{Op: OpRestart, Service: "web"}).OperationID
	files := e.records()
	if len(files) != 3 || !e.exists("/var/lib/lcm-deploy/ops/"+running+".json") || !e.exists("/var/lib/lcm-deploy/ops/"+last+".json") {
		t.Errorf("kept %v", files)
	}
}

func TestInstallKeepsReleasesBounded(t *testing.T) {
	e := newEnv(t)
	b := e.ok(buildReq(commit1)).Build.BuildID
	for range MaxKeepReleases + 3 {
		e.ok(Request{Op: OpInstall, Service: "web", BuildID: b, Artifact: "dist"})
		e.clock.Advance(time.Second)
	}
	names, err := os.ReadDir(e.real("/opt/web/releases"))
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, n := range names {
		if n.IsDir() {
			dirs++
		}
	}
	if dirs != MaxKeepReleases || len(names) != 2*MaxKeepReleases {
		t.Errorf("%d release directories, %d entries", dirs, len(names))
	}
}

func TestLedgerMustBePrivate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ledger")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // a ledger its group may read, to be refused
		t.Fatal(err)
	}
	if _, err := OpenLedger(dir, os.Getuid()); err == nil || !strings.Contains(err.Error(), "mode 0700") {
		t.Errorf("open a group-readable ledger: %v", err)
	}
	if _, err := OpenLedger(dir, os.Getuid()+1); err == nil {
		t.Error("opened a ledger of another owner")
	}
	if err := os.Symlink(dir, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(filepath.Join(root, "link"), os.Getuid()); err == nil {
		t.Error("opened a ledger through a link")
	}
	if _, err := OpenLedger(filepath.Join(root, "missing"), os.Getuid()); err == nil {
		t.Error("opened a missing ledger")
	}
}

func TestServeLocalNeedsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	if err := ServeLocal(context.Background(), strings.NewReader("{}"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("err = %v", err)
	}
}

func TestParsePasswd(t *testing.T) {
	acc, ok, err := parsePasswd("web:x:120:121::/var/lib/web:/usr/sbin/nologin\n", "web")
	if err != nil || !ok || acc.UID != 120 || acc.GID != 121 || acc.Home != "/var/lib/web" || acc.Shell != "/usr/sbin/nologin" {
		t.Fatalf("%+v %v %v", acc, ok, err)
	}
	for _, out := range []string{"other:x:1:1::/:/bin/sh", "web:x:a:1::/:/bin/sh", "web:x:1:1::relative:/bin/sh", "web:x:1:1"} {
		if _, _, err := parsePasswd(out, "web"); err == nil {
			t.Errorf("%q accepted", out)
		}
	}
}
