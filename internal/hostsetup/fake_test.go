package hostsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// memFS is an in-memory host filesystem with owners and modes, so that
// tests can check everything apply does without touching the machine.
type memFS struct {
	mu     sync.Mutex
	nodes  map[string]*memNode
	ops    []string
	denied map[string]bool // Open fails with a permission error
	seq    int
}

type memNode struct {
	mode     fs.FileMode
	uid, gid int
	data     []byte
	target   string
	ino      uint64
	dev      uint64
}

var inodes atomic.Uint64

// node returns a new node with its own inode number.
func node(mode fs.FileMode, uid, gid int) *memNode {
	return &memNode{mode: mode, uid: uid, gid: gid, ino: inodes.Add(1)}
}

// OpenTree implements FS; paths are taken literally, so the in-memory tree
// needs no confinement.
func (m *memFS) OpenTree(root string) (Tree, error) { return fsTree{fs: m, root: root}, nil }

func newMemFS() *memFS {
	return &memFS{nodes: map[string]*memNode{"/": node(fs.ModeDir|0o755, 0, 0)}, denied: map[string]bool{}}
}

func notExist(op, p string) error { return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist} }

func (m *memFS) log(op, p string) { m.ops = append(m.ops, op+" "+p) }

// writes returns the mutating operations outside /tmp, where apply stages
// files even in check mode.
func (m *memFS) writes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, op := range m.ops {
		if !strings.Contains(op, " /tmp/") {
			out = append(out, op)
		}
	}
	return out
}

func (m *memFS) resetOps() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = nil
}

// Setup helpers, which do not log.

func (m *memFS) dir(p string, perm fs.FileMode, uid, gid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mkdirAll(path.Dir(p))
	m.nodes[p] = node(fs.ModeDir|perm, uid, gid)
}

func (m *memFS) file(p, data string, perm fs.FileMode, uid, gid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mkdirAll(path.Dir(p))
	n := node(perm, uid, gid)
	n.data = []byte(data)
	m.nodes[p] = n
}

func (m *memFS) link(p, target string, uid, gid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mkdirAll(path.Dir(p))
	n := node(fs.ModeSymlink|0o777, uid, gid)
	n.target = target
	m.nodes[p] = n
}

func (m *memFS) mkdirAll(p string) {
	if _, ok := m.nodes[p]; ok {
		return
	}
	m.mkdirAll(path.Dir(p))
	m.nodes[p] = node(fs.ModeDir|0o755, 0, 0)
}

func (m *memFS) get(p string) *memNode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nodes[p]
}

func (m *memFS) remove(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.nodes {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(m.nodes, k)
		}
	}
}

func (m *memFS) content(p string) string {
	n := m.get(p)
	if n == nil {
		return ""
	}
	return string(n.data)
}

// FS implementation.

func (m *memFS) Lstat(p string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[p]
	if !ok {
		return Info{}, notExist("lstat", p)
	}
	return Info{Mode: n.mode, UID: n.uid, GID: n.gid, Size: int64(len(n.data)), Ino: n.ino, Dev: n.dev}, nil
}

func (m *memFS) Open(p string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[p]
	switch {
	case !ok:
		return nil, notExist("open", p)
	case m.denied[p]:
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
	case !n.mode.IsRegular():
		return nil, fmt.Errorf("open %s: not a regular file", p)
	}
	return io.NopCloser(bytes.NewReader(append([]byte{}, n.data...))), nil
}

func (m *memFS) ReadDir(p string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[p]
	if !ok {
		return nil, notExist("readdir", p)
	}
	if !n.mode.IsDir() {
		return nil, fmt.Errorf("readdir %s: not a directory", p)
	}
	var names []string
	for k := range m.nodes {
		if k != "/" && path.Dir(k) == p {
			names = append(names, path.Base(k))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (m *memFS) Readlink(p string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[p]
	if !ok {
		return "", notExist("readlink", p)
	}
	if n.mode&fs.ModeSymlink == 0 {
		return "", fmt.Errorf("readlink %s: not a link", p)
	}
	return n.target, nil
}

func (m *memFS) parentOK(p string) error {
	parent, ok := m.nodes[path.Dir(p)]
	if !ok {
		return notExist("create", p)
	}
	if !parent.mode.IsDir() {
		return fmt.Errorf("%s: parent is not a directory", p)
	}
	return nil
}

func (m *memFS) Mkdir(p string, perm fs.FileMode, uid, gid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.parentOK(p); err != nil {
		return err
	}
	if _, ok := m.nodes[p]; ok {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}
	m.log("mkdir", p)
	m.nodes[p] = node(fs.ModeDir|perm, uid, gid)
	return nil
}

func (m *memFS) WriteFile(p string, r io.Reader, perm fs.FileMode, uid, gid int) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.parentOK(p); err != nil {
		return err
	}
	if n, ok := m.nodes[p]; ok && n.mode.IsDir() {
		return fmt.Errorf("write %s: is a directory", p)
	}
	m.log("write", p)
	n := node(perm, uid, gid)
	n.data = data
	m.nodes[p] = n
	return nil
}

func (m *memFS) Symlink(target, p string, uid, gid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.parentOK(p); err != nil {
		return err
	}
	if _, ok := m.nodes[p]; ok {
		return &fs.PathError{Op: "symlink", Path: p, Err: fs.ErrExist}
	}
	m.log("symlink", p)
	n := node(fs.ModeSymlink|0o777, uid, gid)
	n.target = target
	m.nodes[p] = n
	return nil
}

func (m *memFS) Chown(p string, want Info, uid, gid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.expected(p, want)
	if err != nil {
		return err
	}
	m.log("chown", p)
	n.uid, n.gid = uid, gid
	return nil
}

// expected returns the node at p if it is the regular file or directory
// want describes.
func (m *memFS) expected(p string, want Info) (*memNode, error) {
	n, ok := m.nodes[p]
	switch {
	case !ok:
		return nil, notExist("open", p)
	case !want.IsDir() && !want.IsRegular():
		return nil, fmt.Errorf("%s is not a regular file or directory", p)
	case n.mode.Type() != want.Mode.Type() || n.ino != want.Ino:
		return nil, fmt.Errorf("%s changed since it was checked; nothing was changed", p)
	}
	return n, nil
}

func (m *memFS) Chmod(p string, want Info, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.expected(p, want)
	if err != nil {
		return err
	}
	m.log("chmod", p)
	n.mode = n.mode.Type() | perm
	return nil
}

func (m *memFS) Rename(from, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[from]; !ok {
		return notExist("rename", from)
	}
	if err := m.parentOK(to); err != nil {
		return err
	}
	if n, ok := m.nodes[to]; ok && n.mode.IsDir() {
		return fmt.Errorf("rename %s: target directory exists", to)
	}
	m.log("rename", to)
	moved := map[string]*memNode{}
	for k, n := range m.nodes {
		if k == from || strings.HasPrefix(k, from+"/") {
			moved[to+strings.TrimPrefix(k, from)] = n
			delete(m.nodes, k)
		}
	}
	for k, n := range moved {
		m.nodes[k] = n
	}
	return nil
}

func (m *memFS) Remove(p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[p]; !ok {
		return notExist("remove", p)
	}
	for k := range m.nodes {
		if strings.HasPrefix(k, p+"/") {
			return fmt.Errorf("remove %s: directory not empty", p)
		}
	}
	m.log("remove", p)
	delete(m.nodes, p)
	return nil
}

func (m *memFS) RemoveAll(p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[p]; !ok {
		return nil
	}
	m.log("removeall", p)
	for k := range m.nodes {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(m.nodes, k)
		}
	}
	return nil
}

func (m *memFS) MkdirTemp(dir, pattern string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	p := fmt.Sprintf("%s/%s%d", dir, pattern, m.seq)
	if err := m.parentOK(p); err != nil {
		return "", err
	}
	m.log("mkdir", p)
	m.nodes[p] = node(fs.ModeDir|0o700, 0, 0)
	return p, nil
}

// fakeRunner answers the commands apply runs and keeps the host state they
// change (accounts, systemd, tailscale, the checkout).
type fakeRunner struct {
	mu    sync.Mutex
	fs    *memFS
	calls []string

	missingTools map[string]bool
	accounts     map[string]Account

	enabled, active, needReload bool
	serve                       string
	dns                         string
	origin                      string
	healthy                     bool

	failVisudo, failVisudoStaged, failAnalyze       bool
	visudoHook                                      func() error
	versionOutput, installedVersion, systemdVersion string
	passwordInput, goVersion, analyzeStderr         string
	installPy                                       []string
}

const (
	testDNS   = "host.example.ts.net"
	testExe   = "/home/alice/src/lcm/bin/lcm"
	testToken = "lcm_test-token" //nolint:gosec // a fake token the fake runner prints
	ourServe  = `{"TCP":{"9443":{"HTTPS":true}},"Web":{"host.example.ts.net:9443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7800"}}}}}`
)

func (r *fakeRunner) LookPath(name string) (string, error) {
	if r.missingTools[name] {
		return "", errors.New(name + " not found")
	}
	return "/usr/bin/" + name, nil
}

// called returns the recorded command lines that start with prefix.
func (r *fakeRunner) called(prefix string) []string {
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

// matches reports whether a command line is prefix or starts with prefix
// followed by more arguments.
func matches(line, prefix string) bool {
	return line == prefix || strings.HasPrefix(line, prefix+" ")
}

func (r *fakeRunner) index(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.calls {
		if matches(c, prefix) {
			return i
		}
	}
	return -1
}

func (r *fakeRunner) resetCalls() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

func exit(code int) error { return &ExitError{Command: "fake", Code: code} }

func (r *fakeRunner) Run(_ context.Context, c Command) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := c.String()
	if c.As != nil {
		line = "[" + c.As.Name + "] " + line
	}
	r.calls = append(r.calls, line)
	if _, isFile := c.Stdin.(*os.File); isFile {
		return nil, fmt.Errorf("fake runner: %s got a file as stdin", line)
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
		acc, ok := r.accounts[arg(1)]
		if !ok {
			return nil, exit(2)
		}
		shell := acc.Shell
		if shell == "" {
			shell = "/bin/sh"
		}
		return fmt.Appendf(nil, "%s:x:%d:%d:%s:%s:%s\n", acc.Name, acc.UID, acc.GID, acc.GECOS, acc.Home, shell), nil
	case c.Path == "useradd":
		name, uid := a[len(a)-1], 900+len(r.accounts)
		if name == ServiceAccount {
			uid = 990
		}
		acc := Account{Name: name, UID: uid, GID: uid}
		for i := 0; i+1 < len(a); i++ {
			switch a[i] {
			case "--home-dir":
				acc.Home = a[i+1]
			case "--shell":
				acc.Shell = a[i+1]
			case "--comment":
				acc.GECOS = a[i+1]
			}
		}
		r.accounts[name] = acc
		return nil, nil
	case strings.HasPrefix(c.Path, "/tmp/") && arg(0) == "version":
		if c.As == nil || c.As.Name != "nobody" {
			return nil, errors.New("version must run as nobody")
		}
		if r.versionOutput != "" {
			return []byte(r.versionOutput), nil
		}
		return []byte("lcm v1 (abc1234) 2026-11-05T00:00:00Z\n"), nil
	case c.Path == Binary && arg(0) == "version":
		if c.As == nil || c.As.Name != "lcm" || c.MaxOutput == 0 || c.MaxOutput > 4096 {
			return nil, errors.New("the installed binary must run as lcm with a bounded output")
		}
		if r.installedVersion != "" {
			return []byte(r.installedVersion), nil
		}
		return []byte("lcm v1 (abc1234) 2026-11-05T00:00:00Z\n"), nil
	case c.Path == "visudo" && arg(0) == "-cf":
		if r.fs.get(arg(1)) == nil {
			return nil, errors.New("visudo: no such file")
		}
		if r.failVisudoStaged {
			return nil, exit(1)
		}
		return nil, nil
	case c.Path == "visudo" && arg(0) == "-c":
		if r.visudoHook != nil {
			return nil, r.visudoHook()
		}
		if r.failVisudo {
			return nil, &ExitError{Command: "visudo -c", Code: 1, Stderr: "parse error"}
		}
		return nil, nil
	case c.Path == "tailscale" && arg(0) == "status":
		return fmt.Appendf(nil, `{"Self":{"DNSName":%q}}`, r.dns+"."), nil
	case c.Path == "tailscale" && arg(0) == "serve" && arg(1) == "status":
		return []byte(r.serve), nil
	case c.Path == "tailscale" && arg(0) == "serve":
		r.serve = ourServe
		return nil, nil
	case c.Path == "git" && arg(0) == "-C":
		return []byte(r.origin + "\n"), nil
	case c.Path == "git" && arg(0) == "clone":
		if c.As == nil || c.As.Name != "lcm" {
			return nil, errors.New("clone must run as lcm")
		}
		r.origin = arg(5)
		r.fs.dir(Checkout+"/.git", 0o755, c.As.UID, c.As.GID)
		return nil, nil
	case c.Path == "systemd-analyze":
		if r.failAnalyze {
			return nil, &ExitError{Command: "systemd-analyze verify", Code: 1, Stderr: lastLine(r.analyzeStderr)}
		}
		return nil, nil
	case c.Path == "systemctl":
		return r.systemctl(a)
	case strings.HasSuffix(c.Path, "/bin/go"):
		if r.goVersion != "" {
			return []byte(r.goVersion), nil
		}
		return []byte("go version go1.26.0 linux/amd64\n"), nil
	case strings.HasSuffix(c.Path, "/bin/node"):
		return []byte("v24.0.0\n"), nil
	case c.Path == Binary && arg(0) == "token":
		return []byte(testToken + "\n"), nil
	case c.Path == Binary && arg(0) == "admin":
		data, _ := io.ReadAll(c.Stdin)
		r.passwordInput = string(data)
		return []byte("User \"admin\" created.\n"), nil
	case c.Path == "sh":
		data, _ := io.ReadAll(c.Stdin)
		file := arg(3)
		r.fs.dir(path.Dir(file), 0o700, c.As.UID, c.As.GID)
		r.fs.file(file, string(data), 0o600, c.As.UID, c.As.GID)
		return nil, nil
	case c.Path == python3:
		r.installPy = append([]string{}, a...)
		r.fs.file(SecurityHelper, string(asset("security-host/helper.py")), 0o644, 0, 0)
		r.fs.file(SecurityPolicy, fmt.Sprintf(`{"machineId":%q}`, arg(3)), 0o600, 0, 0)
		r.fs.file(SecuritySudoers, "lcm ALL=(root) ...\n", 0o440, 0, 0)
		return []byte(`{"installed":true}` + "\n"), nil
	}
	return nil, fmt.Errorf("fake runner: unexpected command %s", line)
}

func (r *fakeRunner) systemctl(a []string) ([]byte, error) {
	switch a[0] {
	case "--version":
		if r.systemdVersion != "" {
			return []byte(r.systemdVersion), nil
		}
		return []byte("systemd 255 (255.4-1ubuntu8.18)\n+PAM +AUDIT +SELINUX +APPARMOR +BPF_FRAMEWORK -XKBCOMMON\n"), nil
	case "show":
		if r.needReload {
			return []byte("yes\n"), nil
		}
		return []byte("no\n"), nil
	case "daemon-reload":
		r.needReload = false
	case "is-enabled":
		if !r.enabled {
			return []byte("disabled\n"), exit(1)
		}
		return []byte("enabled\n"), nil
	case "enable":
		r.enabled = true
	case "is-active":
		if !r.active {
			return nil, exit(3)
		}
	case "start", "restart":
		r.active = true
		r.healthy = true
	default:
		return nil, fmt.Errorf("fake systemctl: unexpected %v", a)
	}
	return nil, nil
}

// testHost is a fake host: alice is the operator, with toolchains in
// ~/.local and a built lcm in ~/src/lcm.
type testHost struct {
	t     *testing.T
	fs    *memFS
	r     *fakeRunner
	clock *clock.Fake
	host  Host
	cfg   Config
}

const (
	aliceUID = 1000
	exeData  = "\x7fELF lcm test binary"
)

func newTestHost(t *testing.T) *testHost {
	t.Helper()
	m := newMemFS()
	for _, d := range []string{"/etc", "/etc/sudoers.d", "/etc/systemd", "/etc/systemd/system", "/usr", "/usr/local",
		"/usr/local/bin", "/usr/bin", "/usr/sbin", "/var", "/var/lib", "/home"} {
		m.dir(d, 0o755, 0, 0)
	}
	m.dir("/tmp", 0o777|fs.ModeSticky, 0, 0)
	m.file("/sys/fs/cgroup/cgroup.controllers", "cpu memory pids\n", 0o444, 0, 0)
	m.file(python3, "python", 0o755, 0, 0)
	m.file(visudo, "visudo", 0o755, 0, 0)
	m.dir("/home/alice", 0o750, aliceUID, aliceUID)
	m.file(testExe, exeData, 0o755, aliceUID, aliceUID)
	goRoot, nodeRoot := "/home/alice/.local/go", "/home/alice/.local/node"
	m.dir(goRoot, 0o755, aliceUID, aliceUID)
	m.file(goRoot+"/bin/go", "go binary", 0o775, aliceUID, aliceUID)
	m.file(goRoot+"/src/fmt/print.go", "package fmt", 0o664, aliceUID, aliceUID)
	m.link(goRoot+"/misc", "src/fmt", aliceUID, aliceUID)
	m.dir(nodeRoot, 0o775, aliceUID, aliceUID)
	m.file(nodeRoot+"/bin/node", "node binary", 0o755, aliceUID, aliceUID)
	m.file(nodeRoot+"/lib/node_modules/npm/bin/npm-cli.js", "npm", 0o755, aliceUID, aliceUID)
	m.link(nodeRoot+"/bin/npm", "../lib/node_modules/npm/bin/npm-cli.js", aliceUID, aliceUID)

	r := &fakeRunner{fs: m, dns: testDNS, missingTools: map[string]bool{}, accounts: map[string]Account{
		"alice":  {Name: "alice", UID: aliceUID, GID: aliceUID, Home: "/home/alice"},
		"nobody": {Name: "nobody", UID: 65534, GID: 65534, Home: "/nonexistent"},
	}}
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	th := &testHost{t: t, fs: m, r: r, clock: clk}
	th.host = Host{
		FS: m, Runner: r, Clock: clk, Privileged: true, Executable: testExe,
		OpenExecutable: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(exeData)), nil },
		Health: func(context.Context) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.healthy {
				return errors.New("connection refused")
			}
			return nil
		},
	}
	th.cfg = Config{
		Operator: "alice", Repository: DefaultRepository,
		Toolchains: Toolchains{Go: goRoot, Node: nodeRoot}, Tailscale: Tailscale{Port: 9443},
	}
	th.writeConfig(th.cfg)
	return th
}

func (th *testHost) writeConfig(c Config) {
	th.fs.dir(EtcDir, 0o755, 0, 0)
	th.fs.file(ConfigPath, string(c.Encode()), 0o644, 0, 0)
}

// apply runs Apply and returns its output.
func (th *testHost) apply(o Options) (Result, string, error) {
	th.t.Helper()
	var out bytes.Buffer
	o.Out = &out
	res, err := Apply(context.Background(), th.host, o)
	return res, out.String(), err
}

// converge applies until the host is converged, failing the test on error.
func (th *testHost) converge() string {
	th.t.Helper()
	_, out, err := th.apply(Options{})
	if err != nil {
		th.t.Fatalf("apply: %v\n%s", err, out)
	}
	return out
}

// expectConverged checks that check mode and apply both find nothing to
// change and write nothing.
func (th *testHost) expectConverged() {
	th.t.Helper()
	for _, check := range []bool{true, false} {
		th.fs.resetOps()
		res, out, err := th.apply(Options{Check: check})
		if err != nil || res.Changes != 0 || res.Manual != 0 {
			th.t.Fatalf("check=%v: %+v %v\n%s", check, res, err, out)
		}
		if w := th.fs.writes(); len(w) > 0 {
			th.t.Fatalf("check=%v wrote %v\n%s", check, w, out)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line != "" && !strings.Contains(line, "unchanged") && !strings.HasPrefix(line, "LCM is running") && !strings.HasPrefix(line, "Next,") {
				th.t.Fatalf("check=%v: unexpected line %q\n%s", check, line, out)
			}
		}
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
