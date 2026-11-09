package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// Options control Apply.
type Options struct {
	// ConfigPath is the host configuration; empty selects ConfigPath.
	ConfigPath string
	// Check reports what would change without changing anything.
	Check bool
	// ReplaceBinary installs the running executable as /usr/local/bin/lcm
	// even when one is installed (self-deploy keeps that one current).
	ReplaceBinary bool
	// Out receives one line per step result.
	Out io.Writer
	// Terminal, when set, is the terminal that answers the admin password
	// prompt of a fresh installation.
	Terminal *Terminal
}

// Terminal is an interactive terminal.
type Terminal struct {
	In  io.Reader
	Out io.Writer
}

// Result counts what Apply found.
type Result struct {
	// Changes is the number of changes made, or pending in check mode.
	Changes int
	// Manual is the number of differences apply does not fix by itself.
	Manual int
}

// healthWait bounds the wait for LCM to become healthy after a start.
const healthWait = 60 * time.Second

// requiredTools are the programs apply runs, followed by those that
// self-install and self-deploy run later as root and as lcm.
var requiredTools = []string{
	"getent", "useradd", "visudo", "systemctl", "systemd-analyze", "git", "tailscale", "sh",
	"systemd-run", "setpriv", "curl", "flock", "timeout", "stat", "make",
}

// trustedDirs are the existing directories that root files are installed
// in; each must exist and be trusted.
var trustedDirs = []string{"/etc", "/etc/sudoers.d", "/etc/systemd/system", "/usr/local/bin"}

var dnsName = regexp.MustCompile(`^[a-z0-9.-]+$`)

type applier struct {
	h   Host
	o   Options
	cfg Config
	res Result

	operator Account
	nobody   Account
	// lcm is the service account; nil until it exists.
	lcm *Account

	exe         Content
	stage       string
	freshDB     bool
	unitChanged bool
	dnsName     string
	serveState  string
	security    securityState
	// planned holds the directories check mode reported as created, so
	// that their children do not report them again.
	planned map[string]bool
}

type securityState struct {
	present   bool
	complete  bool
	helperSum string
	machineID string
	readErr   error
}

// Apply converges the host with the configuration and the embedded files.
// It validates everything first and changes nothing until that passes; with
// Options.Check it changes nothing at all. Errors stop the sequence.
func Apply(ctx context.Context, h Host, o Options) (Result, error) {
	if o.ConfigPath == "" {
		o.ConfigPath = ConfigPath
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	a := &applier{h: h, o: o, planned: map[string]bool{}}
	defer a.cleanup()
	if err := a.preflight(ctx); err != nil {
		return a.res, err
	}
	steps := []struct {
		name string
		fn   func(context.Context, *step) error
	}{
		{"account", a.account},
		{"directories", a.directories},
		{"lcm.env", a.envFile},
		{"toolchain go", func(ctx context.Context, s *step) error { return a.toolchain(ctx, s, "go", a.cfg.Toolchains.Go) }},
		{"toolchain node", func(ctx context.Context, s *step) error { return a.toolchain(ctx, s, "node", a.cfg.Toolchains.Node) }},
		{"binary", a.binary},
		{"self-install", func(_ context.Context, s *step) error {
			_, err := a.ensureFile(s, SelfInstall, bytesContent(asset("self-install")), 0o755)
			return err
		}},
		{"sudoers", func(ctx context.Context, s *step) error { return a.ensureGrant(ctx, s, Sudoers, asset("sudoers")) }},
		{"unit", a.unit},
		{"checkout", a.checkout},
		{"service", a.service},
		{"health", a.health},
		{ComponentMachinePower, a.machinePower},
		{ComponentSecurityHost, a.securityHost},
		{"admin password", a.adminPassword},
		{"token", a.token},
		{"tailscale serve", a.tailscaleServe},
	}
	for _, st := range steps {
		s := &step{a: a, name: st.name}
		if err := st.fn(ctx, s); err != nil {
			return a.res, fmt.Errorf("%s: %w", st.name, err)
		}
		s.finish()
	}
	if !o.Check {
		_, _ = fmt.Fprintf(o.Out, "\nLCM is running at https://%s:%d (tailnet only).\nNext, as %s, seed the self-deploy documents: deploy/seed.sh\n",
			a.dnsName, a.cfg.Tailscale.Port, a.operator.Name)
	}
	return a.res, nil
}

func (a *applier) cleanup() {
	if a.stage != "" {
		_ = a.h.FS.RemoveAll(a.stage)
	}
}

// step collects the results of one step.
type step struct {
	a        *applier
	name     string
	reported bool
	notes    []string
}

func (s *step) line(msg string) {
	_, _ = fmt.Fprintf(s.a.o.Out, "%-16s %s\n", s.name+":", msg)
}

// change counts one change and makes it, unless in check mode.
func (s *step) change(what string, do func() error) error {
	s.a.res.Changes++
	s.reported = true
	if s.a.o.Check {
		s.line("would change: " + what)
		return nil
	}
	if err := do(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	s.line("changed: " + what)
	return nil
}

// manual reports a difference that needs an administrator.
func (s *step) manual(what string) {
	s.a.res.Manual++
	s.reported = true
	s.line("needs manual action: " + what)
}

func (s *step) note(msg string) { s.notes = append(s.notes, msg) }

func (s *step) finish() {
	if !s.reported {
		msg := "unchanged"
		if len(s.notes) > 0 {
			msg += " (" + strings.Join(s.notes, "; ") + ")"
		}
		s.line(msg)
		return
	}
	for _, n := range s.notes {
		s.line("note: " + n)
	}
}

func (a *applier) run(ctx context.Context, c Command) ([]byte, error) {
	return a.h.Runner.Run(ctx, c)
}

// lcmIDs returns the service account's IDs, or -1 while it does not exist
// (in check mode), so that anything it should own differs.
func (a *applier) lcmIDs() (int, int) {
	if a.lcm == nil {
		return -1, -1
	}
	return a.lcm.UID, a.lcm.GID
}

// preflight validates the host and the configuration before any change.
func (a *applier) preflight(ctx context.Context) error {
	h := a.h
	if !h.Privileged {
		return errors.New("must run as root, from a reviewed build: sudo bin/lcm host apply")
	}
	if err := CheckExecutable(h.Executable); err != nil {
		return err
	}
	if _, err := h.FS.Lstat(a.o.ConfigPath); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s does not exist; create it with: sudo bin/lcm host init --operator USER", a.o.ConfigPath)
	}
	cfg, err := LoadConfig(h.FS, a.o.ConfigPath)
	if err != nil {
		return err
	}
	a.cfg = cfg
	var missing []string
	for _, t := range requiredTools {
		if _, err := h.Runner.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required tools: %s", strings.Join(missing, ", "))
	}
	for _, d := range trustedDirs {
		if err := trusted(h.FS, d); err != nil {
			return err
		}
	}
	if err := a.preflightPaths(); err != nil {
		return err
	}
	if err := a.preflightAccounts(ctx); err != nil {
		return err
	}
	if err := a.preflightToolchains(); err != nil {
		return err
	}
	if err := a.preflightTailscale(ctx); err != nil {
		return err
	}
	if err := a.preflightStage(ctx); err != nil {
		return err
	}
	if err := a.preflightCheckout(ctx); err != nil {
		return err
	}
	_, err = h.FS.Lstat(DatabaseFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		a.freshDB = true
	case err != nil:
		return err
	}
	return a.preflightSecurityHost()
}

// CheckExecutable refuses to run the self-deployed binary as root.
func CheckExecutable(exe string) error {
	if SelfDeployed(exe) {
		return fmt.Errorf("refusing to run %s as root: it is the self-deployed binary, which whoever can push to the deployed branch controls; run a reviewed build instead (make build && sudo bin/lcm host ...)", exe)
	}
	return nil
}

// preflightPaths refuses existing paths that apply would have to replace
// or that someone other than root could have prepared.
func (a *applier) preflightPaths() error {
	fsys := a.h.FS
	rootDirs := []string{EtcDir, OptDir, ToolchainDir, LibexecDir}
	for _, d := range append(append([]string{}, rootDirs...), DataDir, WorkDir, CacheDir) {
		info, err := fsys.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.IsLink() {
			return fmt.Errorf("%s exists but is not a directory; move it away", d)
		}
	}
	// Directories apply manages may have a mode it fixes, but never another
	// owner or group/other write access: someone else could have prepared
	// their content. The directories above them must be fully trusted.
	managed := map[string]bool{}
	for _, d := range rootDirs {
		managed[d] = true
	}
	for _, d := range rootDirs {
		for _, p := range ancestry(d) {
			info, err := fsys.Lstat(p)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if managed[p] && info.UID != 0 {
				return fmt.Errorf("%s is not owned by root; review its content by hand", p)
			}
			if managed[p] && info.Mode.Perm()&0o022 != 0 {
				return fmt.Errorf("%s is writable by group or others; review its content by hand", p)
			}
			if !managed[p] {
				if err := rootOwned(p, info); err != nil {
					return err
				}
			}
		}
	}
	files := []string{EnvFile, Binary}
	for _, f := range coreFiles {
		files = append(files, f.path)
	}
	if a.cfg.Components.MachinePower {
		for _, f := range powerFiles {
			files = append(files, f.path)
		}
	}
	for _, f := range files {
		info, err := fsys.Lstat(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() || (f == EnvFile && !info.IsRegular()) {
			return fmt.Errorf("%s exists but is not a regular file; move it away", f)
		}
	}
	return nil
}

func (a *applier) preflightAccounts(ctx context.Context) error {
	r := a.h.Runner
	op, ok, err := lookupAccount(ctx, r, a.cfg.Operator)
	if err != nil {
		return err
	}
	if !ok || op.UID == 0 {
		return fmt.Errorf("operator: no such non-root account: %s", a.cfg.Operator)
	}
	a.operator = op
	nobody, ok, err := lookupAccount(ctx, r, "nobody")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no account nobody to check the binary with")
	}
	a.nobody = nobody
	lcm, ok, err := lookupAccount(ctx, r, ServiceAccount)
	if err != nil {
		return err
	}
	if ok {
		if lcm.Home != DataDir {
			return fmt.Errorf("account lcm exists with home directory %s, not %s", lcm.Home, DataDir)
		}
		if lcm.UID == 0 {
			return errors.New("account lcm has user ID 0")
		}
		a.lcm = &lcm
	}
	return nil
}

func (a *applier) preflightToolchains() error {
	for _, tc := range []struct {
		key, dir string
		bins     []string
	}{
		{"toolchains.go", a.cfg.Toolchains.Go, []string{"bin/go"}},
		{"toolchains.node", a.cfg.Toolchains.Node, []string{"bin/node"}},
	} {
		if err := a.checkToolchain(tc.dir, tc.bins, tc.key == "toolchains.node"); err != nil {
			return fmt.Errorf("%s: %w", tc.key, err)
		}
	}
	return nil
}

func (a *applier) checkToolchain(dir string, bins []string, npm bool) error {
	if err := a.trustedSource(dir); err != nil {
		return err
	}
	t, err := openSource(a.h.FS, dir)
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	for _, b := range bins {
		bi, err := t.Lstat(b)
		if err != nil || !bi.IsRegular() || bi.Mode.Perm()&0o111 == 0 {
			return fmt.Errorf("no executable %s in %s", b, dir)
		}
	}
	if npm {
		if _, err := t.Lstat("bin/npm"); err != nil {
			return fmt.Errorf("no bin/npm in %s", dir)
		}
	}
	return checkLinks(t, dir)
}

// trustedSource requires a toolchain directory and each of its ancestors
// to be owned by root or the operator, not to be symbolic links and not to
// be writable by others (or by a group other than root's and the
// operator's), so that no third account, such as lcm, can change what is
// copied.
func (a *applier) trustedSource(dir string) error {
	for _, p := range ancestry(dir) {
		info, err := a.h.FS.Lstat(p)
		if err != nil {
			return err
		}
		perm := info.Mode.Perm()
		switch {
		case info.IsLink():
			return fmt.Errorf("%s is a symbolic link; name the resolved path", p)
		case info.UID != 0 && info.UID != a.operator.UID:
			return fmt.Errorf("%s is owned by user %d, not root or the operator", p, info.UID)
		case perm&0o002 != 0:
			return fmt.Errorf("%s is writable by others", p)
		case perm&0o020 != 0 && info.GID != 0 && info.GID != a.operator.GID:
			return fmt.Errorf("%s is writable by group %d", p, info.GID)
		}
	}
	return nil
}

// sourceOwners are the accepted owners of toolchain entries.
func (a *applier) sourceOwners() map[int]bool {
	return map[int]bool{0: true, a.operator.UID: true}
}

func (a *applier) preflightTailscale(ctx context.Context) error {
	out, err := a.run(ctx, Command{Path: "tailscale", Args: []string{"status", "--json"}, Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("tailscale status: %w", err)
	}
	var st struct {
		Self struct{ DNSName string }
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return fmt.Errorf("tailscale status: %w", err)
	}
	a.dnsName = strings.TrimSuffix(st.Self.DNSName, ".")
	if a.dnsName == "" || !dnsName.MatchString(a.dnsName) {
		return errors.New("tailscale reports no tailnet name for this node; sign in and enable HTTPS certificates")
	}
	out, err = a.run(ctx, Command{Path: "tailscale", Args: []string{"serve", "status", "--json"}, Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("tailscale serve status: %w", err)
	}
	state, err := serveState(out, a.dnsName, a.cfg.Tailscale.Port)
	if err != nil {
		return err
	}
	if state == "taken" {
		return fmt.Errorf("tailscale serve already uses port %d for something else; choose another tailscale.port", a.cfg.Tailscale.Port)
	}
	a.serveState = state
	return nil
}

// serveState classifies the port in "tailscale serve status --json": free,
// ours (LCM's handler and nothing else, not funneled) or taken.
func serveState(status []byte, host string, port int) (string, error) {
	var sv struct {
		TCP         map[string]json.RawMessage
		Web         map[string]struct{ Handlers map[string]map[string]any }
		AllowFunnel map[string]bool
	}
	if len(bytes.TrimSpace(status)) > 0 {
		if err := json.Unmarshal(status, &sv); err != nil {
			return "", fmt.Errorf("tailscale serve status: %w", err)
		}
	}
	p := strconv.Itoa(port)
	hp := host + ":" + p
	tcp, ok := sv.TCP[p]
	if !ok || string(tcp) == "null" || string(tcp) == "false" {
		return "free", nil
	}
	h := sv.Web[hp].Handlers
	if len(h) == 1 && len(h["/"]) == 1 && h["/"]["Proxy"] == ServeTarget && !sv.AllowFunnel[hp] {
		return "ours", nil
	}
	return "taken", nil
}

// preflightStage copies the running executable into a root-owned staging
// directory, checks that the copy runs as nobody, and checks the sudoers
// rules with visudo. The staged copy is the one installed later.
func (a *applier) preflightStage(ctx context.Context) error {
	fsys := a.h.FS
	stage, err := fsys.MkdirTemp("/tmp", "lcm-host-")
	if err != nil {
		return err
	}
	a.stage = stage
	if err := fsys.Chmod(stage, 0o755); err != nil {
		return err
	}
	exe, err := FileContent(fsys, a.h.Executable)
	if err != nil {
		return fmt.Errorf("read the running executable: %w", err)
	}
	bin := stage + "/lcm"
	if err := a.writeContent(bin, exe, 0o755, 0, 0); err != nil {
		return err
	}
	if a.exe, err = FileContent(fsys, bin); err != nil {
		return err
	}
	out, err := a.run(ctx, Command{Path: bin, Args: []string{"version"}, As: &a.nobody, Timeout: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("%s does not run as nobody: %w", a.h.Executable, err)
	}
	if !bytes.HasPrefix(out, []byte("lcm ")) {
		return fmt.Errorf("%s does not look like an lcm binary", a.h.Executable)
	}
	rules := map[string]string{"sudoers": stage + "/sudoers"}
	if a.cfg.Components.MachinePower {
		rules["machine-power-sudoers"] = stage + "/machine-power-sudoers"
	}
	for name, p := range rules {
		if err := a.writeContent(p, bytesContent(asset(name)), 0o440, 0, 0); err != nil {
			return err
		}
		if _, err := a.run(ctx, Command{Path: "visudo", Args: []string{"-cf", p}, Timeout: 30 * time.Second}); err != nil {
			return fmt.Errorf("the embedded %s rule does not pass visudo: %w", name, err)
		}
	}
	return nil
}

func (a *applier) preflightCheckout(ctx context.Context) error {
	fsys := a.h.FS
	info, err := fsys.Lstat(Checkout)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory; move it away", Checkout)
	}
	if _, err := fsys.Lstat(Checkout + "/.git"); err != nil {
		return fmt.Errorf("%s exists but is not a git checkout; move it away", Checkout)
	}
	if a.lcm == nil {
		return fmt.Errorf("%s exists but the account lcm does not", Checkout)
	}
	out, err := a.run(ctx, Command{Path: "git", Args: []string{"-C", Checkout, "remote", "get-url", "origin"}, As: a.lcm, Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("read the origin of %s: %w", Checkout, err)
	}
	if origin := strings.TrimSpace(string(out)); origin != a.cfg.Repository {
		return fmt.Errorf("%s tracks %s, not the configured repository %s", Checkout, origin, a.cfg.Repository)
	}
	return nil
}

func (a *applier) preflightSecurityHost() error {
	fsys := a.h.FS
	if a.cfg.Components.SecurityHost != nil {
		for _, p := range []string{python3, visudo} {
			if info, err := fsys.Lstat(p); err != nil || info.IsDir() {
				return fmt.Errorf("the security host installer needs %s", p)
			}
		}
	}
	st := &a.security
	n := 0
	for _, p := range []string{SecurityHelper, SecurityPolicy, SecuritySudoers} {
		_, err := fsys.Lstat(p)
		switch {
		case err == nil:
			n++
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	st.present, st.complete = n > 0, n == 3
	if !st.complete {
		return nil
	}
	if st.helperSum, st.readErr = hashFile(fsys, SecurityHelper); st.readErr != nil {
		return nil
	}
	f, err := fsys.Open(SecurityPolicy)
	if err != nil {
		st.readErr = err
		return nil
	}
	defer func() { _ = f.Close() }()
	var policy struct {
		MachineID string `json:"machineId"`
	}
	st.readErr = json.NewDecoder(io.LimitReader(f, maxConfigSize)).Decode(&policy)
	st.machineID = policy.MachineID
	return nil
}

func (a *applier) writeContent(p string, c Content, perm fs.FileMode, uid, gid int) error {
	r, err := c.open()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return a.h.FS.WriteFile(p, r, perm, uid, gid)
}

// ensureFile makes p a root-owned regular file with content c and mode
// perm, and reports whether it changed (or would change) the file. A file
// owned by someone else is replaced rather than taken over, so that content
// changed after the comparison never becomes root's.
func (a *applier) ensureFile(s *step, p string, c Content, perm fs.FileMode) (bool, error) {
	fsys := a.h.FS
	info, err := fsys.Lstat(p)
	var what string
	switch {
	case errors.Is(err, fs.ErrNotExist):
		what = "install " + p
	case err != nil:
		return false, err
	case info.IsDir():
		return false, fmt.Errorf("%s is a directory", p)
	case !info.IsRegular():
		what = "replace " + p + " (not a regular file)"
	case info.UID != 0 || info.GID != 0:
		what = fmt.Sprintf("replace %s (owned by %d:%d)", p, info.UID, info.GID)
	default:
		sum, err := hashFile(fsys, p)
		if err != nil {
			return false, err
		}
		if sum != c.Sum {
			what = "update " + p
			break
		}
		if info.Mode.Perm() == perm && info.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) == 0 {
			return false, nil
		}
		return true, s.change(fmt.Sprintf("set the mode of %s to %04o (was %04o)", p, perm, info.Mode.Perm()), func() error {
			return fsys.Chmod(p, perm)
		})
	}
	return true, s.change(what, func() error { return a.writeContent(p, c, perm, 0, 0) })
}

// ensureGrant installs a sudoers rule. A rule that breaks the host's
// sudoers configuration is removed again at once.
func (a *applier) ensureGrant(ctx context.Context, s *step, p string, rule []byte) error {
	fsys := a.h.FS
	// A root-owned rule that is replaced is kept, so that a failure puts
	// back the grant that worked instead of removing it.
	var prev []byte
	if info, err := fsys.Lstat(p); err == nil && info.IsRegular() && info.UID == 0 {
		if prev, err = readAll(fsys, p); err != nil {
			return err
		}
	}
	changed, err := a.ensureFile(s, p, bytesContent(rule), 0o440)
	if err != nil || !changed || a.o.Check {
		return err
	}
	visudoCheck := func() error {
		_, err := a.run(ctx, Command{Path: "visudo", Args: []string{"-c"}, Timeout: 30 * time.Second})
		return err
	}
	err = visudoCheck()
	if err == nil {
		return nil
	}
	if prev != nil {
		rerr := a.writeContent(p, bytesContent(prev), 0o440, 0, 0)
		if rerr == nil {
			rerr = visudoCheck()
		}
		if rerr == nil {
			return fmt.Errorf("sudoers no longer parses with the new %s; restored the previous rule: %w", p, err)
		}
		err = errors.Join(err, rerr)
	}
	if rerr := fsys.Remove(p); rerr != nil {
		return fmt.Errorf("sudoers no longer parses with %s, and removing it failed: %w", p, errors.Join(err, rerr))
	}
	return fmt.Errorf("sudoers no longer parses with %s; removed it: %w", p, err)
}

// ensureDir makes p a directory with the owner and mode given; missing
// parents are created as root-owned 0755 directories.
func (a *applier) ensureDir(s *step, p string, perm fs.FileMode, uid, gid int, owner string) error {
	fsys := a.h.FS
	for _, anc := range ancestry(path.Dir(p)) {
		_, err := fsys.Lstat(anc)
		switch {
		case errors.Is(err, fs.ErrNotExist) && !a.planned[anc]:
			a.planned[anc] = true
			if err := s.change("create "+anc+" (root:root 0755)", func() error { return fsys.Mkdir(anc, 0o755, 0, 0) }); err != nil {
				return err
			}
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	info, err := fsys.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		a.planned[p] = true
		return s.change(fmt.Sprintf("create %s (%s %04o)", p, owner, perm), func() error { return fsys.Mkdir(p, perm, uid, gid) })
	case err != nil:
		return err
	case info.UID != uid || info.GID != gid:
		return s.change(fmt.Sprintf("set %s to %s %04o", p, owner, perm), func() error {
			if err := fsys.Chown(p, uid, gid); err != nil {
				return err
			}
			return fsys.Chmod(p, perm)
		})
	case info.Mode.Perm() != perm || info.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0:
		return s.change(fmt.Sprintf("set the mode of %s to %04o (was %04o)", p, perm, info.Mode.Perm()), func() error {
			return fsys.Chmod(p, perm)
		})
	}
	return nil
}

func (a *applier) account(ctx context.Context, s *step) error {
	if a.lcm != nil {
		return nil
	}
	return s.change("create the system account lcm (home "+DataDir+", no login shell)", func() error {
		if _, err := a.run(ctx, Command{Path: "useradd", Args: []string{
			"--system", "--user-group", "--home-dir", DataDir, "--no-create-home", "--shell", "/usr/sbin/nologin", ServiceAccount,
		}}); err != nil {
			return err
		}
		lcm, ok, err := lookupAccount(ctx, a.h.Runner, ServiceAccount)
		if err != nil {
			return err
		}
		if !ok || lcm.Home != DataDir || lcm.UID == 0 {
			return errors.New("useradd did not create the expected account lcm")
		}
		a.lcm = &lcm
		return nil
	})
}

func (a *applier) directories(_ context.Context, s *step) error {
	for _, d := range []string{EtcDir, OptDir, ToolchainDir, LibexecDir} {
		if err := a.ensureDir(s, d, 0o755, 0, 0, "root:root"); err != nil {
			return err
		}
	}
	uid, gid := a.lcmIDs()
	for _, d := range []string{DataDir, WorkDir, CacheDir} {
		if err := a.ensureDir(s, d, 0o750, uid, gid, "lcm:lcm"); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) envFile(_ context.Context, s *step) error {
	fsys := a.h.FS
	want := bytesContent(asset("lcm.env"))
	_, gid := a.lcmIDs()
	info, err := fsys.Lstat(EnvFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s.change("write "+EnvFile, func() error { return a.writeContent(EnvFile, want, 0o640, 0, gid) })
	case err != nil:
		return err
	}
	sum, err := hashFile(fsys, EnvFile)
	if err != nil {
		return err
	}
	if sum != want.Sum {
		s.note("kept the existing file, which differs from the embedded lcm.env")
	}
	if info.UID != 0 {
		// Its owner could have changed it; taking it over would make that
		// content root's configuration.
		s.manual(fmt.Sprintf("%s is owned by user %d; review its content, then make it root:lcm 0640", EnvFile, info.UID))
		return nil
	}
	if info.GID != gid || info.Mode.Perm() != 0o640 {
		return s.change("set "+EnvFile+" to root:lcm 0640", func() error {
			if err := fsys.Chown(EnvFile, 0, gid); err != nil {
				return err
			}
			return fsys.Chmod(EnvFile, 0o640)
		})
	}
	return nil
}

func (a *applier) toolchain(ctx context.Context, s *step, name, src string) error {
	fsys := a.h.FS
	dst := ToolchainDir + "/" + name
	t, err := openSource(fsys, src)
	if err != nil {
		return err
	}
	diff, err := treeDiff(t, fsys, dst, 0, 0)
	_ = t.Close()
	if err != nil || diff == "" {
		return err
	}
	var version string
	verify := func(dir string) error {
		bin, args := dir+"/bin/go", []string{"version"}
		if name == "node" {
			bin, args = dir+"/bin/node", []string{"--version"}
		}
		out, err := a.run(ctx, Command{Path: bin, Args: args, As: a.lcm, Timeout: 30 * time.Second})
		if err != nil {
			return fmt.Errorf("the copied %s does not run as lcm: %w", name, err)
		}
		version = strings.TrimSpace(string(out))
		return nil
	}
	if err := s.change(fmt.Sprintf("copy %s to %s (%s)", src, dst, diff), func() error {
		return copyToolchain(fsys, src, dst, a.sourceOwners(), 0, 0, verify)
	}); err != nil {
		return err
	}
	if version != "" {
		s.note(version)
	}
	return nil
}

func (a *applier) binary(_ context.Context, s *step) error {
	_, err := a.h.FS.Lstat(Binary)
	switch {
	case errors.Is(err, fs.ErrNotExist) || a.o.ReplaceBinary:
		_, err := a.ensureFile(s, Binary, a.exe, 0o755)
		return err
	case err != nil:
		return err
	}
	sum, err := hashFile(a.h.FS, Binary)
	if err != nil {
		return err
	}
	if sum != a.exe.Sum {
		s.note("kept the installed binary, which self-deploy keeps current; --replace-binary installs this one")
	}
	return nil
}

func (a *applier) unit(ctx context.Context, s *step) error {
	fsys := a.h.FS
	var prev []byte
	if info, err := fsys.Lstat(Unit); err == nil && info.IsRegular() {
		if prev, err = readAll(fsys, Unit); err != nil {
			return err
		}
	}
	changed, err := a.ensureFile(s, Unit, bytesContent(asset("lcm.service")), 0o644)
	if err != nil {
		return err
	}
	a.unitChanged = changed
	if !changed || a.o.Check {
		return nil
	}
	if _, err := a.run(ctx, Command{Path: "systemd-analyze", Args: []string{"verify", Unit}, Timeout: time.Minute}); err != nil {
		if prev != nil {
			err = errors.Join(err, a.writeContent(Unit, bytesContent(prev), 0o644, 0, 0))
			return fmt.Errorf("%s does not verify; restored the previous unit: %w", Unit, err)
		}
		err = errors.Join(err, fsys.Remove(Unit))
		return fmt.Errorf("%s does not verify; removed it: %w", Unit, err)
	}
	return nil
}

func readAll(fsys FS, p string) ([]byte, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxConfigSize))
}

func (a *applier) checkout(ctx context.Context, s *step) error {
	if _, err := a.h.FS.Lstat(Checkout); err == nil {
		return nil
	}
	return s.change("clone "+a.cfg.Repository+" into "+Checkout+" as lcm", func() error {
		_, err := a.run(ctx, Command{
			Path: "git", Args: []string{"clone", "--quiet", "--branch", "main", "--", a.cfg.Repository, Checkout},
			As: a.lcm, Env: []string{"GIT_TERMINAL_PROMPT=0"}, Timeout: 10 * time.Minute,
		})
		return err
	})
}

func (a *applier) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return a.run(ctx, Command{Path: "systemctl", Args: args, Timeout: 2 * time.Minute})
}

func (a *applier) service(ctx context.Context, s *step) error {
	reload := a.unitChanged
	if !reload {
		out, err := a.systemctl(ctx, "show", "--property=NeedDaemonReload", "--value", UnitName)
		if err != nil {
			return err
		}
		reload = strings.TrimSpace(string(out)) == "yes"
	}
	if reload {
		if err := s.change("reload the systemd configuration", func() error {
			_, err := a.systemctl(ctx, "daemon-reload")
			return err
		}); err != nil {
			return err
		}
	}
	out, err := a.systemctl(ctx, "is-enabled", UnitName)
	if err != nil && exitCode(err) < 0 {
		return err
	}
	if strings.TrimSpace(string(out)) != "enabled" {
		if err := s.change("enable "+UnitName, func() error {
			_, err := a.systemctl(ctx, "enable", "--quiet", UnitName)
			return err
		}); err != nil {
			return err
		}
	}
	_, err = a.systemctl(ctx, "is-active", "--quiet", UnitName)
	switch {
	case err != nil && exitCode(err) < 0:
		return err
	case err != nil:
		return s.change("start "+UnitName, func() error {
			_, err := a.systemctl(ctx, "start", UnitName)
			return err
		})
	case a.unitChanged:
		return s.change("restart "+UnitName+" for the new unit", func() error {
			_, err := a.systemctl(ctx, "restart", UnitName)
			return err
		})
	}
	return nil
}

func (a *applier) health(ctx context.Context, s *step) error {
	if a.o.Check {
		if err := a.h.Health(ctx); err != nil {
			return s.change("wait until LCM is healthy (not healthy now)", nil)
		}
		s.note("healthy")
		return nil
	}
	if err := waitHealthy(ctx, a.h, healthWait); err != nil {
		return fmt.Errorf("LCM is not healthy after %s (%w); see journalctl -u lcm", healthWait, err)
	}
	s.note("healthy")
	return nil
}

// waitHealthy polls the health endpoint once a second until it answers or
// limit has passed on the host's clock.
func waitHealthy(ctx context.Context, h Host, limit time.Duration) error {
	deadline := h.Clock.Now().Add(limit)
	for {
		err := h.Health(ctx)
		if err == nil {
			return nil
		}
		if !h.Clock.Now().Before(deadline) {
			return err
		}
		if err := sleep(ctx, h.Clock, time.Second); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, c clock.Clock, d time.Duration) error {
	done := make(chan struct{})
	t := c.AfterFunc(d, func() { close(done) })
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// present reports whether any of the files exists.
func (a *applier) present(files []rootFile) bool {
	for _, f := range files {
		if _, err := a.h.FS.Lstat(f.path); err == nil {
			return true
		}
	}
	return false
}

func (a *applier) machinePower(ctx context.Context, s *step) error {
	if !a.cfg.Components.MachinePower {
		if a.present(powerFiles) {
			s.note("installed but disabled; components.machinePower is false and nothing is removed")
		}
		return nil
	}
	if _, err := a.ensureFile(s, PowerWrapper, bytesContent(asset("machine-power")), 0o755); err != nil {
		return err
	}
	if _, err := a.ensureFile(s, PowerHelper, a.exe, 0o755); err != nil {
		return err
	}
	// The grant comes last: it is useless before the wrapper and the
	// pinned helper are in place.
	if err := a.ensureGrant(ctx, s, PowerSudoers, asset("machine-power-sudoers")); err != nil {
		return err
	}
	for _, p := range []string{PowerConfig, PowerLedger} {
		if _, err := a.h.FS.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			s.note(p + " is not provisioned; see docs/operations.md#local-machine-power-helper")
		}
	}
	return nil
}

func (a *applier) securityHost(ctx context.Context, s *step) error {
	want := a.cfg.Components.SecurityHost
	st := a.security
	if want == nil {
		if st.present {
			s.note("installed but disabled; components.securityHost is null and nothing is removed")
		}
		return nil
	}
	helper := bytesContent(asset("security-host/helper.py"))
	switch {
	case !st.present:
		return s.change("install the security host helper with the embedded install.py", func() error {
			return a.installSecurityHost(ctx, want.MachineID, helper.Sum)
		})
	case !st.complete:
		s.manual("the installation is incomplete (helper, policy or sudoers rule missing); review it as described in deploy/security-host/README.md")
	case st.readErr != nil:
		s.manual("cannot read the installation: " + st.readErr.Error())
	case st.machineID != want.MachineID:
		s.manual(fmt.Sprintf("the installed policy is for Machine %q, the configuration names %q; install.py does not change an existing installation", st.machineID, want.MachineID))
	case st.helperSum != helper.Sum:
		s.manual("the installed helper.py differs from the embedded one; install.py does not upgrade an existing installation, so follow the reviewed upgrade in deploy/security-host/README.md")
	}
	return nil
}

// installSecurityHost runs the embedded installer from a root-only
// directory. It validates the host's security configuration itself and
// installs its sudoers rule last.
func (a *applier) installSecurityHost(ctx context.Context, machineID, helperSum string) error {
	fsys := a.h.FS
	dir, err := fsys.MkdirTemp("/tmp", "lcm-security-host-")
	if err != nil {
		return err
	}
	defer func() { _ = fsys.RemoveAll(dir) }()
	for _, name := range []string{"install.py", "helper.py"} {
		if err := a.writeContent(dir+"/"+name, bytesContent(asset("security-host/"+name)), 0o600, 0, 0); err != nil {
			return err
		}
	}
	out, err := a.run(ctx, Command{
		Path: python3, Args: []string{"-I", dir + "/install.py", "--machine-id", machineID, "--helper-sha256", helperSum},
		Env: []string{"LANG=C.UTF-8"}, Timeout: 3 * time.Minute,
	})
	if err != nil {
		return err
	}
	var reply struct {
		Installed bool `json:"installed"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &reply); err != nil || !reply.Installed {
		return errors.New("install.py did not report an installation")
	}
	return nil
}

func (a *applier) adminPassword(ctx context.Context, s *step) error {
	if !a.freshDB {
		return nil
	}
	hint := "sudo -u lcm " + Binary + " admin set-password --data-dir " + DataDir
	if a.o.Terminal == nil {
		s.note("not a terminal; set it with: " + hint)
		return nil
	}
	return s.change("set the web console's admin password (min 8 characters)", func() error {
		_, err := a.run(ctx, Command{
			Path: Binary, Args: []string{"admin", "set-password", "--data-dir", DataDir}, As: a.lcm,
			Stdin: a.o.Terminal.In, Stdout: a.o.Terminal.Out, Stderr: a.o.Terminal.Out, Timeout: 10 * time.Minute,
		})
		if err != nil {
			return fmt.Errorf("%w; set it later with: %s", err, hint)
		}
		return nil
	})
}

// privateWriteScript writes stdin to the file $1 with mode 0600, creating its
// directory; it runs as the operator, so root never writes into the
// operator's home directory.
const privateWriteScript = `umask 077 && mkdir -p -- "${1%/*}" && cat >"$1.tmp" && mv -f -- "$1.tmp" "$1"`

func (a *applier) token(ctx context.Context, s *step) error {
	file := a.operator.Home + "/.config/lcm/token"
	if _, err := a.h.FS.Lstat(file); err == nil {
		return nil
	}
	return s.change("write an admin API token to "+file+" (mode 0600, as "+a.operator.Name+")", func() error {
		out, err := a.run(ctx, Command{
			Path: Binary, Args: []string{"token", "create", "--name", "seed-" + a.operator.Name, "--data-dir", DataDir},
			As: a.lcm, Timeout: time.Minute,
		})
		if err != nil {
			return err
		}
		token := strings.TrimSpace(string(out))
		if token == "" || strings.ContainsAny(token, " \t\n") {
			return errors.New("lcm token create printed no token")
		}
		_, err = a.run(ctx, Command{
			Path: "sh", Args: []string{"-c", privateWriteScript, "sh", file}, As: &a.operator,
			Stdin: strings.NewReader(token + "\n"), Timeout: 30 * time.Second,
		})
		return err
	})
}

func (a *applier) tailscaleServe(ctx context.Context, s *step) error {
	if a.serveState == "ours" {
		return nil
	}
	port := strconv.Itoa(a.cfg.Tailscale.Port)
	return s.change("serve https://"+a.dnsName+":"+port+" from "+ServeTarget+" (tailnet only)", func() error {
		_, err := a.run(ctx, Command{Path: "tailscale", Args: []string{"serve", "--bg", "--yes", "--https=" + port, ServeTarget}, Timeout: time.Minute})
		return err
	})
}
