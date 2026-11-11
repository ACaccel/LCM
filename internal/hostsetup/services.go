package hostsetup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Paths of the deploy component and of the services it manages. They match
// internal/deployhelper and docs/operations.md.
const (
	DeployWrapper = "/usr/local/libexec/lcm-deploy"
	DeployHelper  = "/usr/local/libexec/lcm-deploy-helper"
	DeploySudoers = "/etc/sudoers.d/lcm-deploy"
	// DeployLedger is the helper's root-only operation ledger.
	DeployLedger = "/var/lib/lcm-deploy"
	// BuildDir and BuildCacheDir hold one root-owned directory per service,
	// for the builds and the build cache of that service's build account.
	BuildDir      = "/var/lib/lcm-build"
	BuildCacheDir = "/var/cache/lcm-build"
	RuntimesDir   = OptDir + "/runtimes"
	UnitDir       = "/etc/systemd/system"
	// ServiceUnitMarker is the first line of every unit the deploy helper
	// writes; a unit without it is never replaced.
	ServiceUnitMarker = "# Written by the LCM deploy helper from its built-in template; do not edit."
	// ServePort is the port LCM itself listens on.
	ServePort = 7800

	ComponentDeploy = "deploy"
)

// Directories of one managed service.
func ServiceOptDir(name string) string   { return "/opt/" + name }
func ServiceReleases(name string) string { return "/opt/" + name + "/releases" }
func ServiceDataDir(name string) string  { return "/var/lib/" + name }
func ServiceEtcDir(name string) string   { return "/etc/" + name }
func ServiceEnvFile(name string) string  { return "/etc/" + name + "/env" }
func ServiceUnit(name string) string     { return UnitDir + "/" + name + ".service" }
func ServiceCheckout(name string) string { return WorkDir + "/" + name }
func ServiceBuildDir(name string) string { return BuildDir + "/" + name }
func ServiceCacheDir(name string) string { return BuildCacheDir + "/" + name }

// ServicePublishDir is where the deploy helper publishes the static content
// of a managed service's releases; the only static directory such a
// service's route may serve.
func ServicePublishDir(name string) string { return "/opt/" + name + "/www" }

// BuildAccount is the account that builds service name: every service has
// its own, so that one service's build cannot read or change another's.
// Service names are short enough for it to stay within the 32 characters of
// a user name.
func BuildAccount(name string) string { return "lcmb-" + name }

// ServiceComment and BuildComment are the comments (GECOS) of the accounts
// host setup creates for a service; an existing account without its comment
// was not made by LCM and is never used.
func ServiceComment(name string) string { return "LCM service " + name }
func BuildComment(name string) string   { return "LCM build " + name }

var deployFiles = []rootFile{
	{path: DeployWrapper, perm: 0o755, asset: "lcm-deploy"},
	{path: DeployHelper, perm: 0o755},
	{path: DeploySudoers, perm: 0o440, asset: "lcm-deploy-sudoers"},
}

var (
	serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,26}$`)
	runtimeName = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	// safeHostPath excludes what systemd unit properties would split or
	// expand (spaces, quotes, ':', '%', '$', '\').
	safeHostPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
)

// ValidServiceName checks the name of a managed service. Names starting with
// "lcm" are LCM's own accounts and directories; at most 27 characters leave
// room for the build account's prefix.
func ValidServiceName(name string) error {
	if !serviceName.MatchString(name) || strings.HasPrefix(name, "lcm") {
		return fmt.Errorf("service names must match %s and must not start with \"lcm\" (got %q)", serviceName, name)
	}
	return nil
}

// ValidRuntimeName checks the name of a runtime or of a build input.
func ValidRuntimeName(name string) bool { return runtimeName.MatchString(name) }

// checkHostPath accepts an absolute, clean path other than "/" made of
// characters that systemd properties take literally.
func checkHostPath(p string) error {
	if !safeHostPath.MatchString(p) || path.Clean(p) != p || p == "/" {
		return fmt.Errorf("must be an absolute, clean path of letters, digits and ._-/ (got %q)", p)
	}
	return nil
}

func within(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// lcmOwned are the trees that hold LCM's data or the helper's records; no
// build may read them.
var lcmOwned = []string{DataDir, EtcDir, DeployLedger, PowerLedger}

// systemDirs are directories of the system that backup.dir must not be
// itself: apply gives the backup directory to root alone (0700), which would
// lock everyone else out of them.
var systemDirs = []string{
	"/", "/boot", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/usr", "/etc", "/dev", "/proc", "/sys", "/run", "/tmp", "/snap",
	"/var", "/var/lib", "/var/cache", "/var/log", "/var/tmp", "/var/backups", "/var/spool", "/opt", "/srv", "/mnt", "/media", "/home", "/root",
}

// systemTrees are the system's own trees, where backup.dir must not be
// either.
var systemTrees = []string{"/boot", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/usr", "/etc", "/dev", "/proc", "/sys", "/run", "/tmp", "/var/tmp", "/snap"}

// checkBackupDir refuses a backup directory that is, contains or lies in a
// tree that the system or LCM manages. The directory is made root-only, and
// the helper and the services read and write the trees below.
func (c Config) checkBackupDir(dir string) error {
	if dir == BackupRoot {
		return fmt.Errorf("must not be %s, where host upgrade keeps its sets; use %s-services", BackupRoot, BackupRoot)
	}
	if slices.Contains(systemDirs, dir) {
		return fmt.Errorf("must be a directory of its own, not the system directory %s", dir)
	}
	for _, t := range systemTrees {
		if within(dir, t) {
			return fmt.Errorf("must not be under the system tree %s", t)
		}
	}
	managed := []string{BuildCacheDir, OptDir}
	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		managed = append(managed, ServiceOptDir(name), ServiceDataDir(name), ServiceEtcDir(name), ServiceBuildDir(name))
	}
	for _, r := range slices.Sorted(maps.Keys(c.Runtimes)) {
		managed = append(managed, c.Runtimes[r])
	}
	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		for _, in := range slices.Sorted(maps.Keys(c.Services[name].Inputs)) {
			managed = append(managed, c.Services[name].Inputs[in])
		}
	}
	for _, d := range managed {
		if within(dir, d) || within(d, dir) {
			return fmt.Errorf("must not overlap %s, which LCM or a service uses", d)
		}
	}
	// /var/lib/lcm, /var/lib/lcm-build, /var/lib/lcm-deploy and the like.
	if strings.HasPrefix(dir, DataDir) {
		return fmt.Errorf("must not be in LCM's own %s* directories", DataDir)
	}
	return nil
}

func (c Config) validateDeploy() []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(c.Runtimes)) {
		if !ValidRuntimeName(name) {
			errs = append(errs, fmt.Errorf("runtimes: names must match %s (got %q)", runtimeName, name))
		}
		if err := checkHostPath(c.Runtimes[name]); err != nil {
			errs = append(errs, fmt.Errorf("runtimes.%s %w", name, err))
		}
	}
	if b := c.Backup; b != nil {
		if err := checkHostPath(b.Dir); err != nil {
			errs = append(errs, fmt.Errorf("backup.dir %w", err))
		} else if within(b.Dir, DataDir) {
			errs = append(errs, fmt.Errorf("backup.dir must not be under %s", DataDir))
		} else if err := c.checkBackupDir(b.Dir); err != nil {
			errs = append(errs, fmt.Errorf("backup.dir %w", err))
		}
	}
	ports := map[int]string{}
	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		g, at := c.Services[name], "services."+name
		if err := ValidServiceName(name); err != nil {
			errs = append(errs, fmt.Errorf("services: %w", err))
		}
		if g.External {
			switch {
			case g.Route == nil:
				errs = append(errs, fmt.Errorf("%s.external needs a route: LCM only publishes an external service", at))
			case len(g.Runtimes) > 0 || len(g.Inputs) > 0:
				errs = append(errs, fmt.Errorf("%s: an external service has no runtimes or inputs; LCM does not build or run it", at))
			}
		}
		switch other, dup := ports[g.Port]; {
		case g.Port == 0 && g.External && g.Route != nil && g.Route.Static != "":
			// A static-only external route needs no port.
		case g.Port == 0 && g.External:
			errs = append(errs, fmt.Errorf("%s.port is required unless the external service's route serves a static directory", at))
		case g.Port == 0 && g.Route != nil && g.Route.Static == ServicePublishDir(name):
			// A managed service that only publishes static content runs
			// no process.
		case g.Port == 0:
			errs = append(errs, fmt.Errorf("%s.port is required unless the service only publishes static content (route.static %s)", at, ServicePublishDir(name)))
		case g.Port < 1024 || g.Port > 65535:
			errs = append(errs, fmt.Errorf("%s.port must be between 1024 and 65535 (got %d)", at, g.Port))
		case g.Port == ServePort || g.Port == c.Tailscale.Port:
			errs = append(errs, fmt.Errorf("%s.port %d is LCM's own", at, g.Port))
		case dup:
			errs = append(errs, fmt.Errorf("%s.port %d is also the port of services.%s", at, g.Port, other))
		default:
			ports[g.Port] = name
		}
		seen := map[string]bool{}
		for _, r := range g.Runtimes {
			if _, ok := c.Runtimes[r]; !ok || seen[r] {
				errs = append(errs, fmt.Errorf("%s.runtimes: %q is not a runtime of the runtimes section, or is listed twice", at, r))
			}
			seen[r] = true
		}
		for _, in := range slices.Sorted(maps.Keys(g.Inputs)) {
			p := g.Inputs[in]
			if !ValidRuntimeName(in) {
				errs = append(errs, fmt.Errorf("%s.inputs: names must match %s (got %q)", at, runtimeName, in))
			}
			if err := checkHostPath(p); err != nil {
				errs = append(errs, fmt.Errorf("%s.inputs.%s %w", at, in, err))
				continue
			}
			for _, d := range lcmOwned {
				if within(p, d) || within(d, p) {
					errs = append(errs, fmt.Errorf("%s.inputs.%s must not contain or be inside %s", at, in, d))
				}
			}
		}
	}
	return errs
}

// namedStep is one step of Apply.
type namedStep struct {
	name string
	fn   func(context.Context, *step) error
}

// grantState is what preflight found out about one service grant.
type grantState struct {
	acc, build *Account
	// problem, when set, needs an administrator; the service's steps are
	// skipped.
	problem string
}

// nologinShells are the shells of accounts that cannot log in.
var nologinShells = []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false"}

// systemAccountProblem describes why acc cannot be the dedicated system
// account with home directory home and the given comment, or returns "".
func systemAccountProblem(acc Account, home, comment string, uidMin int) string {
	switch {
	case acc.GECOS != comment:
		return fmt.Sprintf("account %s exists and was not created by LCM (its comment is not %q)", acc.Name, comment)
	case acc.UID <= 0 || acc.UID >= uidMin:
		return fmt.Sprintf("account %s exists and is not a system account (user ID %d)", acc.Name, acc.UID)
	case !slices.Contains(nologinShells, acc.Shell):
		return fmt.Sprintf("account %s exists with a login shell (%s)", acc.Name, printable(acc.Shell, 100))
	case acc.Home != home:
		return fmt.Sprintf("account %s exists with home directory %s, not %s", acc.Name, printable(acc.Home, 200), home)
	}
	return ""
}

// uidMin reads the first user ID of login accounts from /etc/login.defs; IDs
// below it are system accounts.
func uidMin(fsys FS) int {
	data, err := readAll(fsys, "/etc/login.defs")
	if err != nil {
		return 1000
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "UID_MIN" {
			if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 1000
}

// unitSearchPath are the directories systemd loads system units and their
// drop-ins from, including generated, transient and runtime-control ones.
var unitSearchPath = []string{
	"/etc/systemd/system.control", "/run/systemd/system.control", "/run/systemd/transient", "/run/systemd/generator.early",
	UnitDir, "/run/systemd/system", "/run/systemd/generator", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system",
	"/lib/systemd/system", "/run/systemd/generator.late",
}

// UnitConflict describes another unit or drop-in that would replace,
// override or change the unit of service name, or returns "". Only a unit
// that the deploy helper wrote, owned by rootUID (0 on a host), may be
// replaced, and no drop-in may exist for it.
func UnitConflict(fsys FS, name string, rootUID int) (string, error) {
	for _, dir := range unitSearchPath {
		if _, err := fsys.Lstat(dir + "/" + name + ".service.d"); err == nil {
			return fmt.Sprintf("the drop-in directory %s/%s.service.d exists; it would change the service's unit", dir, name), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		p := dir + "/" + name + ".service"
		info, err := fsys.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return "", err
		case dir != UnitDir:
			return fmt.Sprintf("the unit %s exists; a service of that name would override it", p), nil
		case !info.IsRegular() || info.UID != rootUID:
			return fmt.Sprintf("%s is not a root-owned regular file", p), nil
		}
		data, err := readAll(fsys, p)
		if err != nil {
			return "", err
		}
		if first, _, _ := strings.Cut(string(data), "\n"); first != ServiceUnitMarker {
			return fmt.Sprintf("%s exists and was not written by the deploy helper", p), nil
		}
	}
	return "", nil
}

// checkGrant checks one service grant against the host and returns its
// state; errors are problems that stop apply or allow.
func (a *applier) checkGrant(ctx context.Context, name string, g ServiceGrant, uidMin int) (grantState, error) {
	// The parents of the service's directories must be root's alone.
	for _, d := range []string{"/opt", "/var/lib", "/etc"} {
		for _, p := range ancestry(d) {
			info, err := a.h.FS.Lstat(p)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return grantState{}, err
			}
			if err := rootOwned(p, info); err != nil {
				return grantState{}, err
			}
		}
	}
	for _, in := range slices.Sorted(maps.Keys(g.Inputs)) {
		p := g.Inputs[in]
		if err := a.trustedSource(p); err != nil {
			return grantState{}, fmt.Errorf("services.%s.inputs.%s: %w", name, in, err)
		}
		if info, err := a.h.FS.Lstat(p); err != nil || !info.IsDir() {
			return grantState{}, fmt.Errorf("services.%s.inputs.%s: %s is not a directory", name, in, p)
		}
	}
	var st grantState
	for _, want := range []struct {
		acc        **Account
		name, home string
		comment    string
		dirs       []string
	}{
		{&st.acc, name, ServiceDataDir(name), ServiceComment(name), []string{ServiceOptDir(name), ServiceDataDir(name), ServiceEtcDir(name)}},
		{&st.build, BuildAccount(name), ServiceBuildDir(name), BuildComment(name), []string{ServiceBuildDir(name), ServiceCacheDir(name)}},
	} {
		acc, ok, err := lookupAccount(ctx, a.h.Runner, want.name)
		if err != nil {
			return grantState{}, err
		}
		if ok {
			*want.acc = &acc
			if st.problem = systemAccountProblem(acc, want.home, want.comment, uidMin); st.problem != "" {
				return st, nil
			}
			continue
		}
		for _, d := range want.dirs {
			if _, err := a.h.FS.Lstat(d); err == nil {
				st.problem = fmt.Sprintf("%s exists but the account %s does not; review what it belongs to", d, want.name)
				return st, nil
			}
		}
	}
	problem, err := UnitConflict(a.h.FS, name, 0)
	if err != nil {
		return grantState{}, err
	}
	st.problem = problem
	return st, nil
}

// preflightDeploy checks the deploy component, runtimes and service grants.
func (a *applier) preflightDeploy(ctx context.Context) error {
	fsys := a.h.FS
	dirs := []string{BuildDir, BuildCacheDir, DeployLedger, RuntimesDir}
	if a.cfg.Backup != nil {
		dirs = append(dirs, a.cfg.Backup.Dir)
	}
	for _, d := range dirs {
		info, err := fsys.Lstat(d)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return err
		case !info.IsDir() || info.IsLink():
			return fmt.Errorf("%s exists but is not a directory; move it away", d)
		case info.UID != 0 || info.Mode.Perm()&0o022 != 0:
			return fmt.Errorf("%s is not owned by root or is writable by group or others; review its content by hand", d)
		case a.cfg.Backup != nil && d == a.cfg.Backup.Dir && (info.Mode.Perm() != 0o700 || info.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0):
			// Apply creates the backup directory; it never changes the mode of
			// one that exists, which may be a directory something else uses.
			return fmt.Errorf("backup.dir %s exists with mode %04o; apply does not change an existing directory: if it is meant for backups, run sudo chmod 700 %s and apply again", d, info.Mode.Perm(), d)
		}
		for _, p := range ancestry(path.Dir(d)) {
			info, err := fsys.Lstat(p)
			if err != nil {
				return err
			}
			if err := rootOwned(p, info); err != nil && p != OptDir {
				return err
			}
		}
	}
	min := uidMin(fsys)
	a.uidMin = min
	if a.cfg.Components.Deploy {
		for _, f := range deployFiles {
			if info, err := fsys.Lstat(f.path); err == nil && info.IsDir() {
				return fmt.Errorf("%s exists but is not a regular file; move it away", f.path)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(a.cfg.Runtimes)) {
		dir := a.cfg.Runtimes[name]
		if err := a.trustedSource(dir); err != nil {
			return fmt.Errorf("runtimes.%s: %w", name, err)
		}
		t, err := openSource(fsys, dir)
		if err != nil {
			return fmt.Errorf("runtimes.%s: %w", name, err)
		}
		err = checkLinks(t, dir)
		_ = t.Close()
		if err != nil {
			return fmt.Errorf("runtimes.%s: %w", name, err)
		}
	}
	a.grants = map[string]grantState{}
	for _, name := range slices.Sorted(maps.Keys(a.cfg.Services)) {
		if a.cfg.Services[name].External {
			continue
		}
		st, err := a.checkGrant(ctx, name, a.cfg.Services[name], min)
		if err != nil {
			return err
		}
		a.grants[name] = st
	}
	return nil
}

// deploySteps are the steps of the deploy component, the runtimes and each
// service grant.
func (a *applier) deploySteps() []namedStep {
	out := []namedStep{{ComponentDeploy, a.deployComponent}}
	for _, name := range slices.Sorted(maps.Keys(a.cfg.Runtimes)) {
		out = append(out, namedStep{"runtime " + name, func(ctx context.Context, s *step) error { return a.runtime(ctx, s, name) }})
	}
	for _, name := range slices.Sorted(maps.Keys(a.cfg.Services)) {
		out = append(out, namedStep{"service " + name, func(ctx context.Context, s *step) error { return a.grant(ctx, s, name) }})
	}
	return append(out, namedStep{"services", a.removedGrants})
}

// helper reports whether the deploy helper is installed: the deploy and
// the ingress component both use it.
func (c Components) helper() bool { return c.Deploy || c.Ingress != nil }

func (a *applier) deployComponent(ctx context.Context, s *step) error {
	if !a.cfg.Components.helper() {
		if a.present(deployFiles) {
			s.note("installed but disabled; components.deploy is false, components.ingress is null and nothing is removed")
		}
		return nil
	}
	if _, err := a.ensureFile(s, DeployWrapper, bytesContent(asset("lcm-deploy")), 0o755); err != nil {
		return err
	}
	if _, err := a.ensureFile(s, DeployHelper, a.exe, 0o755); err != nil {
		return err
	}
	type dir struct {
		path string
		perm fs.FileMode
	}
	dirs := []dir{{DeployLedger, 0o700}}
	if a.cfg.Components.Deploy {
		dirs = []dir{{BuildDir, 0o755}, {BuildCacheDir, 0o755}, {DeployLedger, 0o700}}
	}
	for _, d := range dirs {
		if err := a.ensureDir(s, d.path, d.perm, 0, 0, "root:root"); err != nil {
			return err
		}
	}
	if a.cfg.Components.Deploy {
		// Backups of services are kept here; only root may enter it.
		if b := a.cfg.Backup; b != nil {
			if err := a.ensureDir(s, b.Dir, 0o700, 0, 0, "root:root"); err != nil {
				return err
			}
			if a.otherFilesystem(b.Dir, "/var/lib") {
				s.note("backup.dir is on another file system than /var/lib; a service can keep a file of a backup open for writing and grow it past the limits, which only a quota or the same file system bounds (docs/operations.md#backups)")
			}
		}
		for _, limit := range SandboxLimits(ctx, a.h) {
			s.note(limit)
		}
	} else {
		s.note("installed for the ingress component; components.deploy is false, so the helper refuses deploy and backup operations")
	}
	// The grant comes last: it is useless before the wrapper and the
	// pinned helper are in place.
	return a.ensureGrant(ctx, s, DeploySudoers, asset("lcm-deploy-sudoers"))
}

// otherFilesystem reports whether p, or its nearest existing ancestor, is on
// another file system than dir's.
func (a *applier) otherFilesystem(p, dir string) bool {
	dev := func(p string) (uint64, bool) {
		for q := p; ; q = path.Dir(q) {
			if info, err := a.h.FS.Lstat(q); err == nil {
				return info.Dev, true
			}
			if q == "/" {
				return 0, false
			}
		}
	}
	d1, ok1 := dev(p)
	d2, ok2 := dev(dir)
	return ok1 && ok2 && d1 != d2
}

// addSystemAccount creates a system account with its own group, the comment
// that marks it as LCM's, no home directory of its own making and no login
// shell.
func (a *applier) addSystemAccount(ctx context.Context, name, home, comment string) (*Account, error) {
	if _, err := a.run(ctx, Command{Path: "useradd", Args: []string{
		"--system", "--user-group", "--comment", comment, "--home-dir", home, "--no-create-home", "--shell", "/usr/sbin/nologin", name,
	}}); err != nil {
		return nil, err
	}
	acc, ok, err := lookupAccount(ctx, a.h.Runner, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("useradd did not create the account %s", name)
	}
	if p := systemAccountProblem(acc, home, comment, a.uidMin); p != "" {
		return nil, errors.New("useradd did not create the expected account: " + p)
	}
	return &acc, nil
}

// runtimeVersions are the well-known programs whose version a runtime copy
// is checked with.
var runtimeVersions = []struct{ bin, arg string }{{"node", "--version"}, {"python3", "--version"}, {"go", "version"}}

// runtime copies one runtime to /opt/lcm/runtimes/<name>, like the
// toolchains, and checks the copy as nobody: the service accounts that run
// it have no more rights than that.
func (a *applier) runtime(ctx context.Context, s *step, name string) error {
	fsys := a.h.FS
	src, dst := a.cfg.Runtimes[name], RuntimesDir+"/"+name
	if err := a.ensureDir(s, RuntimesDir, 0o755, 0, 0, "root:root"); err != nil {
		return err
	}
	t, err := openSource(fsys, src)
	if err != nil {
		return err
	}
	diff, err := treeDiff(t, fsys, dst, 0, 0)
	_ = t.Close()
	if err != nil || diff == "" {
		return err
	}
	var versions []string
	verify := func(dir string) error {
		for _, v := range runtimeVersions {
			bin := dir + "/bin/" + v.bin
			if _, err := fsys.Lstat(bin); err != nil {
				continue
			}
			out, err := a.run(ctx, Command{Path: bin, Args: []string{v.arg}, As: &a.nobody, MaxOutput: 4096, Timeout: 30 * time.Second})
			if err != nil {
				return fmt.Errorf("the copied bin/%s does not run as nobody: %w", v.bin, err)
			}
			versions = append(versions, v.bin+" "+printable(firstLine(string(out)), 100))
		}
		return nil
	}
	if err := s.change(fmt.Sprintf("copy %s to %s (%s)", src, dst, diff), func() error {
		return copyToolchain(fsys, src, dst, a.sourceOwners(), 0, 0, verify)
	}); err != nil {
		return err
	}
	switch {
	case len(versions) > 0:
		s.note(strings.Join(versions, ", "))
	case !a.o.Check:
		s.note("no bin/node, bin/python3 or bin/go; the copy was not run")
	}
	return nil
}

// grant converges the account and directories of one service grant.
func (a *applier) grant(ctx context.Context, s *step, name string) error {
	if a.cfg.Services[name].External {
		s.note("external: LCM creates no account or directory for it; only its route is published")
		return nil
	}
	st := a.grants[name]
	if st.problem != "" {
		s.manual(st.problem)
		return nil
	}
	for _, acc := range []struct {
		p                   **Account
		name, home, comment string
	}{
		{&st.acc, name, ServiceDataDir(name), ServiceComment(name)},
		{&st.build, BuildAccount(name), ServiceBuildDir(name), BuildComment(name)},
	} {
		if *acc.p != nil {
			continue
		}
		if err := s.change("create the system account "+acc.name+" (home "+acc.home+", no login shell)", func() error {
			made, err := a.addSystemAccount(ctx, acc.name, acc.home, acc.comment)
			*acc.p = made
			return err
		}); err != nil {
			return err
		}
	}
	ids := func(acc *Account) (int, int) {
		if acc == nil {
			return -1, -1
		}
		return acc.UID, acc.GID
	}
	uid, gid := ids(st.acc)
	buid, bgid := ids(st.build)
	builder := BuildAccount(name)
	type dir struct {
		path     string
		perm     fs.FileMode
		uid, gid int
		desc     string
	}
	dirs := []dir{
		{ServiceOptDir(name), 0o755, 0, 0, "root:root"},
		{ServiceReleases(name), 0o755, 0, 0, "root:root"},
	}
	if a.cfg.Services[name].Route != nil {
		// The deploy helper publishes static content here; the route
		// serves it when route.static names it.
		dirs = append(dirs, dir{ServicePublishDir(name), 0o755, 0, 0, "root:root"})
	}
	dirs = append(dirs, []dir{
		{ServiceDataDir(name), 0o750, uid, gid, name + ":" + name},
		{ServiceEtcDir(name), 0o750, 0, gid, "root:" + name},
		{ServiceBuildDir(name), 0o750, 0, bgid, "root:" + builder},
		{ServiceCacheDir(name), 0o750, buid, bgid, builder + ":" + builder},
	}...)
	for _, d := range dirs {
		info, err := a.h.FS.Lstat(d.path)
		if err == nil && info.IsDir() && !info.IsLink() && (info.UID != d.uid || info.GID != d.gid) {
			// It was not made for this service; its owner could have
			// prepared what it holds.
			s.manual(fmt.Sprintf("%s is owned by %d:%d, not %s; review it by hand", d.path, info.UID, info.GID, d.desc))
			return nil
		}
		if err := a.ensureDir(s, d.path, d.perm, d.uid, d.gid, d.desc); err != nil {
			return err
		}
	}
	return a.serviceEnvFile(s, name, gid)
}

// serviceEnvFile creates the service's empty environment file; an existing
// one is never replaced, only its group and mode are fixed.
func (a *applier) serviceEnvFile(s *step, name string, gid int) error {
	fsys, p := a.h.FS, ServiceEnvFile(name)
	info, err := fsys.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s.change(fmt.Sprintf("create an empty %s (root:%s 0640)", p, name), func() error {
			return fsys.WriteFile(p, bytes.NewReader(nil), 0o640, 0, gid)
		})
	case err != nil:
		return err
	case !info.IsRegular():
		s.manual(p + " is not a regular file; move it away")
	case info.UID != 0:
		s.manual(fmt.Sprintf("%s is owned by user %d; review its content, then make it root:%s 0640", p, info.UID, name))
	case info.GID != gid || info.Mode.Perm() != 0o640:
		return s.change(fmt.Sprintf("set %s to root:%s 0640", p, name), func() error {
			if err := fsys.Chown(p, info, 0, gid); err != nil {
				return err
			}
			return fsys.Chmod(p, info, 0o640)
		})
	}
	return nil
}

// removedGrants reports services that were granted before: they keep their
// account, directories, releases and unit.
func (a *applier) removedGrants(_ context.Context, s *step) error {
	names, err := a.h.FS.ReadDir("/opt")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, granted := a.cfg.Services[n]; granted || ValidServiceName(n) != nil {
			continue
		}
		if info, err := a.h.FS.Lstat(ServiceReleases(n)); err == nil && info.IsDir() && info.UID == 0 {
			s.note(fmt.Sprintf("%s is no longer in services; its account, directories, releases and unit are kept", n))
		}
	}
	return nil
}
