package hostsetup

import (
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

// Limits of the multi-instance and unit keys of a service grant. They
// match internal/model.
const (
	MaxInstances    = 16
	MaxInstanceName = 24
	// maxGrantList bounds groups, after and wants of one grant.
	maxGrantList = 8
)

var (
	// instanceName allows no leading, trailing or double "-", which keeps
	// the prefixes systemd uses for drop-in directories unambiguous.
	instanceName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	groupName    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	// grantUnitName are the units a grant may order after or pull in.
	grantUnitName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]*\.(service|target|socket|mount)$`)
	// GrantTargets are the only targets after and wants may name: passive
	// synchronization points that pulling in starts nothing harmful. Others,
	// such as reboot.target, rescue.target or suspend.target, change the
	// state of the whole machine when they are started.
	GrantTargets = []string{"network-online.target", "nss-lookup.target", "time-sync.target", "remote-fs.target"}
)

// ValidInstanceName reports whether name is a valid instance name. "backup"
// is reserved: /etc/<service>/backup.env is the backup worker's env file.
func ValidInstanceName(name string) bool {
	return len(name) <= MaxInstanceName && instanceName.MatchString(name) && name != "backup"
}

// InstanceUnitName is the unit of instance inst of service svc.
func InstanceUnitName(svc, inst string) string { return svc + "@" + inst + ".service" }

// InstanceUnit is the path of the unit of instance inst of service svc.
func InstanceUnit(svc, inst string) string { return UnitDir + "/" + InstanceUnitName(svc, inst) }

// ServiceBackupEnvFile is the optional root-only environment file that only
// the backup worker of a service gets, after the service's own.
func ServiceBackupEnvFile(svc string) string { return ServiceEtcDir(svc) + "/backup.env" }

// ServiceInstanceEnvFile is the environment file of one instance, loaded
// after the service's shared one.
func ServiceInstanceEnvFile(svc, inst string) string { return ServiceEtcDir(svc) + "/" + inst + ".env" }

// DeniedGroups are groups that are never granted as supplementary groups:
// each gives its members administrative power over the host, its logs or
// its devices or files of other accounts (TLS keys, the files every login
// account shares, the mail queue, the file name index), or the data of LCM
// and its security component.
var DeniedGroups = []string{
	"root", "sudo", "admin", "wheel", "adm", "shadow", "disk", "kmem", "tty", "utmp", "docker", "lxd", "incus", "incus-admin",
	"libvirt", "libvirt-qemu", "kvm", "systemd-journal", "systemd-network", "staff", "video", "render", "input", "sgx",
	"netdev", "plugdev", "crontab", "_ssh", "ssh", "lpadmin", "sambashare", "ssl-cert", "users", "microk8s", "plocate",
	"mlocate", "postdrop", "sasl", "lcm", SecurityAccount,
}

// SecurityAccount is the account and group of the security component's
// collector.
const SecurityAccount = "workstation-security"

// minSystemGID is the first group ID that Debian and Ubuntu allocate
// dynamically; the ones up to 100 (users) are static groups such as disk,
// adm and shadow.
const minSystemGID = 101

// ProtectedAccounts are the accounts whose primary group a service's units
// may not get: LCM's, the security component's, the service's build
// account and the accounts of the other granted services.
func ProtectedAccounts(cfg Config, svc string) []string {
	accounts := []string{ServiceAccount, SecurityAccount, BuildAccount(svc)}
	for _, other := range slices.Sorted(maps.Keys(cfg.Services)) {
		if other != svc && !cfg.Services[other].External {
			accounts = append(accounts, other, BuildAccount(other))
		}
	}
	return accounts
}

// Group is an entry of the host's group database.
type Group struct {
	Name string
	GID  int
}

// ParseGroup parses the output of "getent group name".
func ParseGroup(out, name string) (Group, bool, error) {
	f := strings.Split(strings.TrimSpace(out), ":")
	if len(f) != 4 || f[0] != name {
		return Group{}, false, fmt.Errorf("getent group %s: unexpected output", name)
	}
	gid, err := strconv.Atoi(f[2])
	if err != nil || gid < 0 {
		return Group{}, false, fmt.Errorf("getent group %s: unexpected output", name)
	}
	return Group{Name: name, GID: gid}, true, nil
}

func lookupGroup(ctx context.Context, r Runner, name string) (Group, bool, error) {
	out, err := r.Run(ctx, Command{Path: "getent", Args: []string{"group", name}, Timeout: 10 * time.Second})
	if exitCode(err) == 2 {
		return Group{}, false, nil
	}
	if err != nil {
		return Group{}, false, err
	}
	return ParseGroup(string(out), name)
}

// GroupProblem returns why group may not be a supplementary group of a
// service's units, or "". A granted group must be a dynamically allocated
// system group (minSystemGID to below gidMin, the first group ID of login
// accounts): not a static system group, not a person's group, not one of
// DeniedGroups, and not the primary group of an account in primary (LCM's
// own, the build accounts and the other services' accounts, by group ID),
// whose files the service would otherwise reach.
func GroupProblem(g Group, gidMin int, primary map[int]string) string {
	switch {
	case slices.Contains(DeniedGroups, g.Name):
		return fmt.Sprintf("the group %s is never granted: it gives administrative power over the host", g.Name)
	case g.GID == 0 || g.GID < minSystemGID:
		return fmt.Sprintf("the group %s (ID %d) is a static system group; grant a dedicated group made with groupadd --system", g.Name, g.GID)
	case g.GID >= gidMin:
		return fmt.Sprintf("the group %s (ID %d) is not a system group; grant a dedicated group made with groupadd --system", g.Name, g.GID)
	}
	if acc, ok := primary[g.GID]; ok {
		return fmt.Sprintf("the group %s is the primary group of the account %s, whose files the service would reach; grant a dedicated group", g.Name, acc)
	}
	return ""
}

// gidMin reads the first group ID of login accounts from /etc/login.defs.
func gidMin(fsys FS) int { return loginDefs(fsys, "GID_MIN", 1000) }

// GIDMin is gidMin for the deploy helper.
func GIDMin(fsys FS) int { return gidMin(fsys) }

// GrantedGroups returns the supplementary groups of the unit of instance
// inst ("" for a service's single unit): the service's groups, then the
// instance's, each once.
func (g ServiceGrant) GrantedGroups(inst string) []string {
	var out []string
	add := func(list []string) {
		for _, n := range list {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	add(g.Groups)
	if inst != "" {
		add(g.Instances[inst].Groups)
	}
	return out
}

// allGroups returns every group the grant names, sorted, each once.
func (g ServiceGrant) allGroups() []string {
	out := slices.Clone(g.Groups)
	for _, in := range g.Instances {
		out = append(out, in.Groups...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// validateGrantExtras checks the multi-instance and unit keys of one grant
// and registers the instance ports in ports.
func (c Config) validateGrantExtras(name string, g ServiceGrant, ports map[int]string) []error {
	var errs []error
	at := "services." + name
	checkGroups := func(where string, groups []string) {
		if len(groups) > maxGrantList {
			errs = append(errs, fmt.Errorf("%s.groups: at most %d groups", where, maxGrantList))
		}
		seen := map[string]bool{}
		for _, gr := range groups {
			switch {
			case !groupName.MatchString(gr):
				errs = append(errs, fmt.Errorf("%s.groups: %q is not a group name", where, Printable(gr, 64)))
			case slices.Contains(DeniedGroups, gr):
				errs = append(errs, fmt.Errorf("%s.groups: the group %s is never granted: it gives administrative power over the host", where, gr))
			case gr == name:
				errs = append(errs, fmt.Errorf("%s.groups: %s is already the service's own group", where, gr))
			case seen[gr]:
				errs = append(errs, fmt.Errorf("%s.groups: %s is listed twice", where, gr))
			}
			seen[gr] = true
		}
	}
	checkGroups(at, g.Groups)
	for _, key := range []struct {
		name  string
		units []string
	}{{"after", g.After}, {"wants", g.Wants}} {
		if len(key.units) > maxGrantList {
			errs = append(errs, fmt.Errorf("%s.%s: at most %d units", at, key.name, maxGrantList))
		}
		seen := map[string]bool{}
		for _, u := range key.units {
			switch {
			case len(u) > 255 || !grantUnitName.MatchString(u):
				errs = append(errs, fmt.Errorf("%s.%s: %q is not a unit name such as db.service", at, key.name, Printable(u, 64)))
			case u == name+".service" || strings.HasPrefix(u, name+"@") || u == "lcm.service" || strings.HasPrefix(u, "lcm-") || strings.HasPrefix(u, "lcm@"):
				errs = append(errs, fmt.Errorf("%s.%s: %s is a unit of this service or of LCM itself", at, key.name, u))
			case strings.HasSuffix(u, ".target") && !slices.Contains(GrantTargets, u):
				errs = append(errs, fmt.Errorf("%s.%s: %s is a target; only the synchronization points %s may be named, as other targets can change the machine's state (reboot, rescue, sleep)",
					at, key.name, u, strings.Join(GrantTargets, ", ")))
			case seen[u]:
				errs = append(errs, fmt.Errorf("%s.%s: %s is listed twice", at, key.name, u))
			}
			seen[u] = true
		}
	}
	if (g.Groups != nil || g.After != nil || g.Wants != nil) && g.External {
		errs = append(errs, fmt.Errorf("%s: an external service has no units, so it takes no groups, after or wants", at))
	}
	if g.Instances == nil {
		return errs
	}
	switch {
	case g.External:
		errs = append(errs, fmt.Errorf("%s.instances: an external service runs no instances", at))
	case len(g.Instances) == 0:
		errs = append(errs, fmt.Errorf("%s.instances must name at least one instance; remove it for a service with a single unit", at))
	case len(g.Instances) > MaxInstances:
		errs = append(errs, fmt.Errorf("%s.instances: at most %d instances", at, MaxInstances))
	}
	for _, inst := range slices.Sorted(maps.Keys(g.Instances)) {
		ig, where := g.Instances[inst], at+".instances."+inst
		if !ValidInstanceName(inst) {
			errs = append(errs, fmt.Errorf("%s.instances: %q is not an instance name (lowercase letters and digits in groups joined by single '-', starting with a letter, at most %d characters, not backup)",
				at, Printable(inst, 64), MaxInstanceName))
		}
		checkGroups(where, ig.Groups)
		if ig.Port == 0 {
			continue
		}
		if err := c.checkPort(where, ig.Port, ports); err != nil {
			errs = append(errs, err)
			continue
		}
		ports[ig.Port] = where
	}
	return errs
}

// checkPort checks a port of a service or instance against the bounds,
// LCM's own ports and the ports already taken.
func (c Config) checkPort(at string, port int, ports map[int]string) error {
	other, dup := ports[port]
	switch {
	case port < 1024 || port > 65535:
		return fmt.Errorf("%s.port must be between 1024 and 65535 (got %d)", at, port)
	case port == ServePort || port == c.Tailscale.Port:
		return fmt.Errorf("%s.port %d is LCM's own", at, port)
	case dup:
		return fmt.Errorf("%s.port %d is also the port of %s", at, port, other)
	}
	return nil
}

// BackupToolNames are the database tools backup.tools may name.
var BackupToolNames = []string{"pg_dump", "pg_restore", "mongodump", "mongorestore", "bsondump"}

// validateBackupTools checks backup.tools: known tool names, each a clean
// path whose file name is the tool's (some tools dispatch on it), outside
// the trees LCM, the backups and the services use.
func (c Config) validateBackupTools() []error {
	if c.Backup == nil || c.Backup.Tools == nil {
		return nil
	}
	var errs []error
	owned := []string{DataDir, EtcDir, DeployLedger, PowerLedger, BuildDir, BuildCacheDir, OptDir, c.Backup.Dir}
	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		owned = append(owned, ServiceOptDir(name), ServiceDataDir(name), ServiceEtcDir(name))
	}
	for _, tool := range slices.Sorted(maps.Keys(c.Backup.Tools)) {
		p := c.Backup.Tools[tool]
		if !slices.Contains(BackupToolNames, tool) {
			errs = append(errs, fmt.Errorf("backup.tools: %q is not a database tool (%s)", Printable(tool, 64), strings.Join(BackupToolNames, ", ")))
			continue
		}
		if err := checkHostPath(p); err != nil {
			errs = append(errs, fmt.Errorf("backup.tools.%s %w", tool, err))
			continue
		}
		if path.Base(p) != tool {
			errs = append(errs, fmt.Errorf("backup.tools.%s must name a file called %s (got %s)", tool, tool, p))
		}
		for _, d := range owned {
			if within(p, d) {
				errs = append(errs, fmt.Errorf("backup.tools.%s must not be inside %s, which LCM, the backups or a service uses", tool, d))
				break
			}
		}
	}
	return errs
}

// dashDropIns are the drop-in directories systemd (246 and later) also
// applies to the unit unitBase.service: one for each prefix of the name
// that ends with "-", such as web-.service.d for web-app.service.
func dashDropIns(unitBase string) []string {
	var out []string
	for i := range len(unitBase) {
		if unitBase[i] == '-' {
			out = append(out, unitBase[:i+1]+".service.d")
		}
	}
	return out
}

// InstanceConflicts describes a unit or drop-in that would provide, change
// or override an instance unit of service svc, or returns "": a template
// <svc>@.service or its drop-ins anywhere in the search path, and for each
// instance what UnitConflict refuses.
func InstanceConflicts(fsys FS, svc string, instances []string, rootUID int) (string, error) {
	for _, dir := range unitSearchPath {
		for _, name := range []string{svc + "@.service", svc + "@.service.d"} {
			p := dir + "/" + name
			if _, err := fsys.Lstat(p); err == nil {
				return fmt.Sprintf("%s exists; a template or its drop-ins would provide or change instances of %s that LCM did not write", p, svc), nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
	}
	for _, inst := range instances {
		if c, err := UnitConflict(fsys, svc+"@"+inst, rootUID); err != nil || c != "" {
			return c, err
		}
	}
	return "", nil
}

// TopLevelDropIns returns the service.d drop-in directories that change
// every service unit of the host, LCM's included; they are reported, not
// refused.
func TopLevelDropIns(fsys FS) []string {
	var out []string
	for _, dir := range unitSearchPath {
		if _, err := fsys.Lstat(dir + "/service.d"); err == nil {
			out = append(out, dir+"/service.d")
		}
	}
	return out
}

// TrustedTool checks a tool path as root: every link on the way and the
// file it ends at must be owned by rootUID and in directories only rootUID
// can change, and the file must be executable. The path itself, not its
// target, is what runs, as some tools dispatch on the program's name. It
// returns the file the links end at.
func TrustedTool(fsys FS, p string, rootUID int) (Info, error) {
	for range 8 {
		info, err := fsys.Lstat(p)
		if err != nil {
			return Info{}, err
		}
		if !info.IsLink() {
			info, err := trustedChain(fsys, p, rootUID, true)
			if err != nil {
				return Info{}, err
			}
			if info.Mode.Perm()&0o111 == 0 {
				return Info{}, fmt.Errorf("%s is not executable", p)
			}
			return info, nil
		}
		if _, err := trustedChain(fsys, path.Dir(p), rootUID, false); err != nil {
			return Info{}, err
		}
		if info.UID != rootUID {
			return Info{}, fmt.Errorf("%s is a link that root does not own", p)
		}
		target, err := fsys.Readlink(p)
		if err != nil {
			return Info{}, err
		}
		if !path.IsAbs(target) {
			target = path.Join(path.Dir(p), target)
		}
		p = path.Clean(target)
	}
	return Info{}, errors.New("too many links")
}

// trustedChain checks that p and its ancestors exist, are not symbolic
// links and that only rootUID can change them; p must be a directory or,
// with file set, a regular file.
func trustedChain(fsys FS, p string, rootUID int, file bool) (Info, error) {
	var info Info
	for _, a := range ancestry(p) {
		var err error
		info, err = fsys.Lstat(a)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return info, fmt.Errorf("%s does not exist", a)
		case err != nil:
			return info, err
		case info.IsLink():
			return info, fmt.Errorf("%s is a symbolic link", a)
		case info.UID != rootUID || info.Mode.Perm()&0o022 != 0:
			return info, fmt.Errorf("%s is not root's alone", a)
		case a != p && !info.IsDir():
			return info, fmt.Errorf("%s is not a directory", a)
		}
	}
	if file && !info.IsRegular() || !file && !info.IsDir() {
		return info, fmt.Errorf("%s is not a %s", p, map[bool]string{true: "regular file", false: "directory"}[file])
	}
	return info, nil
}
