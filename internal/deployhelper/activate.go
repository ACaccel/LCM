package deployhelper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// interpreters are the programs of a runtime that run a release's start
// file, in order of preference.
var interpreters = []string{"node", "python3"}

func unitName(svc string) string { return svc + ".service" }

// serviceSandbox is the hardening of a service's unit and migration: it
// runs as its own account, can write only its data directory, sees neither
// LCM's data, the backups nor the build trees, may bind no port but its
// own, uses only IP, Unix and netlink sockets (Node reads its network
// interfaces over netlink) and has its own IPC namespace. It may
// still connect anywhere, and the kernel does not stop it from binding its
// port on another address than HOST: systemd has no per-address bind
// rule, and IPAddressAllow= would also block its outbound connections.
// A service or instance without a port (port 0) may bind none.
func serviceSandbox(svc string, port int, backup string) []string {
	props := sandbox("0027", backup)
	if port != 0 {
		props = append(props, "SocketBindAllow=tcp:"+strconv.Itoa(port))
	}
	props = append(props,
		"SocketBindDeny=any",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
		"PrivateIPC=yes",
		"ReadWritePaths="+hostsetup.ServiceDataDir(svc))
	return append(props, "InaccessiblePaths=-"+hostsetup.BuildDir+" -"+hostsetup.BuildCacheDir)
}

// unitSpec is everything a rendered unit holds. Instance is empty for a
// service's single unit; After, Wants and Groups come from the grant.
type unitSpec struct {
	svc, instance        string
	port                 int
	execStart            []string
	rtBin, backup        string
	limits               *UnitLimits
	after, wants, groups []string
}

// renderUnit is the built-in template of a service's single unit.
func renderUnit(svc string, port int, execStart []string, rtBin, backup string, limits *UnitLimits) string {
	return renderSpec(unitSpec{svc: svc, port: port, execStart: execStart, rtBin: rtBin, backup: backup, limits: limits})
}

// renderSpec is the built-in unit template. Every value in it is checked
// to need no quoting or escaping: the limits were validated with the
// request, and the names, units and groups with host.json. Without an
// instance, After, Wants and Groups, the unit is the one earlier versions
// wrote, byte for byte, so that a helper upgrade changes no unit.
func renderSpec(u unitSpec) string {
	svc := u.svc
	desc := svc
	if u.instance != "" {
		desc = svc + " " + u.instance
	}
	lines := []string{
		hostsetup.ServiceUnitMarker,
		"[Unit]",
		"Description=" + desc + " (managed by LCM)",
		strings.Join(append([]string{"After=network.target"}, u.after...), " "),
	}
	if len(u.wants) > 0 {
		lines = append(lines, "Wants="+strings.Join(u.wants, " "))
	}
	lines = append(lines,
		"",
		"[Service]",
		"Type=simple",
		"User="+svc,
		"Group="+svc,
	)
	if len(u.groups) > 0 {
		lines = append(lines, "SupplementaryGroups="+strings.Join(u.groups, " "))
	}
	lines = append(lines,
		"WorkingDirectory="+hostsetup.ServiceOptDir(svc)+"/current",
		"ExecStart="+strings.Join(u.execStart, " "),
		"EnvironmentFile="+hostsetup.ServiceEnvFile(svc),
	)
	if u.instance != "" {
		lines = append(lines,
			"EnvironmentFile="+hostsetup.ServiceInstanceEnvFile(svc, u.instance),
			"Environment=LCM_SERVICE="+svc+" LCM_INSTANCE="+u.instance)
	}
	if u.port != 0 {
		lines = append(lines, "Environment=PORT="+strconv.Itoa(u.port)+" HOST=127.0.0.1")
	}
	port, rtBin, backup, limits := u.port, u.rtBin, u.backup, u.limits
	if rtBin != "" {
		lines = append(lines, "Environment=PATH="+rtBin+":"+systemPath)
	}
	lines = append(lines, "Restart=on-failure", "RestartSec=5")
	if l := limits; l != nil {
		if l.MemoryHigh != "" {
			lines = append(lines, "MemoryHigh="+l.MemoryHigh)
		}
		if l.MemoryMax != "" {
			lines = append(lines, "MemoryMax="+l.MemoryMax)
		}
		if l.StopTimeoutSec != 0 {
			lines = append(lines, "TimeoutStopSec="+strconv.Itoa(l.StopTimeoutSec))
		}
	}
	lines = append(lines, serviceSandbox(svc, port, backup)...)
	lines = append(lines, "", "[Install]", "WantedBy=multi-user.target")
	return strings.Join(lines, "\n") + "\n"
}

// activate makes a release current and (re)starts the service with a unit
// from the built-in template. The optional migration runs first, in the new
// release, then the release's static content is published; when either
// fails nothing is switched. A service granted no port runs no process: its
// release is published and made current without a unit.
func (h *Helper) activate(ctx context.Context, req Request, cfg hostsetup.Config, g hostsetup.ServiceGrant) Response {
	resp, svc := reply(req), req.Service
	releases := hostsetup.ServiceReleases(svc)
	if _, err := h.trustedPath(releases, false); err != nil {
		return refuse(resp, CodeRefused, "%v; run sudo bin/lcm host allow or host apply", err)
	}
	relDir := releases + "/" + req.ReleaseID
	var m Manifest
	if err := h.readJSON(manifestPath(svc, req.ReleaseID), &m); errors.Is(err, fs.ErrNotExist) {
		return refuse(resp, CodeNotFound, "there is no release %s of %s", req.ReleaseID, svc)
	} else if err != nil {
		return refuse(resp, CodeFailed, "read the release manifest: %v", err)
	}
	if _, err := h.trustedPath(relDir, false); err != nil || m.Service != svc || m.ReleaseID != req.ReleaseID {
		return refuse(resp, CodeFailed, "release %s is incomplete or not this service's", req.ReleaseID)
	}
	switch {
	case g.MultiInstance() && req.Instances == nil:
		return refuse(resp, CodeRefused, "services.%s has instances in %s, but the request has none: give the document config.instances, or grant the service a port instead with sudo bin/lcm host allow",
			svc, hostsetup.ConfigPath)
	case !g.MultiInstance() && req.Instances != nil:
		return refuse(resp, CodeRefused, "the request has instances, but services.%s has none in %s: an administrator grants them with sudo bin/lcm host allow %s --instance NAME[:PORT]",
			svc, hostsetup.ConfigPath, svc)
	case g.MultiInstance():
		return h.activateInstances(ctx, req, resp, cfg, g, relDir)
	}
	static := req.Start == nil
	switch {
	case static && g.Port != 0:
		return refuse(resp, CodeRefused, "services.%s has the port %d in %s, so it runs a process: the request needs start", svc, g.Port, hostsetup.ConfigPath)
	case !static && g.Port == 0:
		return refuse(resp, CodeRefused, "services.%s has no port in %s: it only publishes static content, so the request must not have start", svc, hostsetup.ConfigPath)
	case req.Publish != nil && (g.Route == nil || g.Route.Static != hostsetup.ServicePublishDir(svc)):
		return refuse(resp, CodeRefused, "publish needs services.%s.route.static to be %s in %s; an administrator sets it with sudo bin/lcm host allow %s --route PREFIX --publish",
			svc, hostsetup.ServicePublishDir(svc), hostsetup.ConfigPath, svc)
	}
	if req.Runtime != "" && !slices.Contains(g.Runtimes, req.Runtime) {
		return refuse(resp, CodeRefused, "the runtime %s is not granted to %s in %s", req.Runtime, svc, hostsetup.ConfigPath)
	}
	if static {
		published, notes, err := h.publish(svc, relDir, *req.Publish)
		if err != nil {
			return refuse(resp, CodeFailed, "%v; nothing was switched", err)
		}
		resp.Log = sanitizeLog(strings.Join(notes, "\n"))
		return h.switchAndRestart(ctx, req, resp, "", published, nil)
	}
	acc, err := h.account(ctx, svc, hostsetup.ServiceDataDir(svc), hostsetup.ServiceComment(svc))
	if err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	if conflict, err := hostsetup.UnitConflict(h.FS, svc, h.RootUID); err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	} else if conflict != "" {
		return refuse(resp, CodeRefused, "%s", conflict)
	}
	// Instance units left from a grant with instances would hold the
	// service's port and data: they are found before anything changes and
	// stop before the single unit starts.
	units, conflict, err := h.scanUnits(svc)
	switch {
	case err != nil:
		return refuse(resp, CodeFailed, "read %s: %v", hostsetup.UnitDir, err)
	case conflict != "":
		return refuse(resp, CodeRefused, "%s", conflict)
	}
	var orphans []string
	for _, in := range units.instances {
		orphans = append(orphans, hostsetup.InstanceUnitName(svc, in))
	}
	if err := h.checkServiceFiles(svc, acc); err != nil {
		return refuse(resp, CodeRefused, "%v; run sudo bin/lcm host allow", err)
	}
	if err := h.checkGroups(ctx, cfg, svc, g.GrantedGroups("")); err != nil {
		return refuse(resp, CodeRefused, "services.%s: %v", svc, err)
	}
	rtBin := ""
	if req.Runtime != "" {
		rt := hostsetup.RuntimesDir + "/" + req.Runtime
		if _, err := h.trustedPath(rt, false); err != nil {
			return refuse(resp, CodeRefused, "the runtime %s is not installed (%v); run sudo bin/lcm host apply", req.Runtime, err)
		}
		rtBin = rt + "/bin"
	}
	execStart, err := h.startCommand(svc, relDir, req.Start, rtBin)
	if err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	if req.Migrate != nil {
		argv, err := h.migrateCommand(relDir, req.Migrate, rtBin)
		if err != nil {
			return refuse(resp, CodeRefused, "%v", err)
		}
		unit := "lcm-migrate-" + svc + "-" + req.OperationID[:8]
		res, err := h.run(ctx, migrateTimeout+migrateRunMargin, migrateArgs(unit, svc, acc, relDir, g.Port, rtBin, backupDir(cfg), argv)...)
		resp.Log = sanitizeLog(res.Output)
		if err != nil {
			h.stopUnit(ctx, unit, err)
			return refuse(resp, CodeFailed, "%s; nothing was switched", commandFailure("the migration", res.Output, err))
		}
	}
	var published *PublishResult
	if req.Publish != nil {
		var notes []string
		if published, notes, err = h.publish(svc, relDir, *req.Publish); err != nil {
			return refuse(resp, CodeFailed, "%v; nothing was switched", err)
		}
		if len(notes) > 0 {
			resp.Log = sanitizeLog(strings.TrimRight(resp.Log, "\n") + "\n" + strings.Join(notes, "\n"))
		}
	}
	unit := renderSpec(unitSpec{svc: svc, port: g.Port, execStart: execStart, rtBin: rtBin, backup: backupDir(cfg), limits: req.Limits,
		after: g.After, wants: g.Wants, groups: g.GrantedGroups("")})
	return h.switchAndRestart(ctx, req, resp, unit, published, orphans)
}

// switchAndRestart installs the unit, switches the current and previous
// links, reloads systemd when the unit changed or systemd reports an
// outdated copy, stops and removes the orphaned instance units, and
// restarts the service. A unit that does not verify or load is put back
// together with the links. Without a unit (static content only) it
// switches the links and prunes the releases.
func (h *Helper) switchAndRestart(ctx context.Context, req Request, resp Response, unit string, published *PublishResult, orphans []string) Response {
	svc, id := req.Service, req.ReleaseID
	opt := hostsetup.ServiceOptDir(svc)
	cur, prev, unitPath := opt+"/current", opt+"/previous", hostsetup.ServiceUnit(svc)
	oldCur, err := h.linkTarget(cur)
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	oldPrev, err := h.linkTarget(prev)
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	var oldUnit []byte
	hadUnit := false
	// Without a unit to write, an existing one is left alone.
	if _, err := h.FS.Lstat(unitPath); err == nil && unit != "" {
		if oldUnit, err = h.readFile(unitPath); err != nil {
			return refuse(resp, CodeFailed, "%v", err)
		}
		hadUnit = true
	}
	result := &ActivateResult{ReleaseID: id, Previous: oldCur, UnitChanged: unit != "" && (!hadUnit || string(oldUnit) != unit), Published: published}
	if oldCur == id {
		result.Previous = oldPrev
	}
	resp.Activate = result
	restore := func() error {
		var errs []error
		if result.UnitChanged {
			if hadUnit {
				errs = append(errs, h.FS.WriteFile(unitPath, strings.NewReader(string(oldUnit)), 0o644, h.RootUID, h.RootGID))
			} else {
				errs = append(errs, h.FS.Remove(unitPath))
			}
		}
		if result.Switched && oldCur != id {
			errs = append(errs, h.restoreLink(cur, oldCur), h.restoreLink(prev, oldPrev))
			result.Switched = false
		}
		return errors.Join(errs...)
	}

	if result.UnitChanged {
		if err := h.FS.WriteFile(unitPath, strings.NewReader(unit), 0o644, h.RootUID, h.RootGID); err != nil {
			return refuse(resp, CodeFailed, "write %s: %v", unitPath, err)
		}
	}
	if oldCur != id {
		if err := h.setLink(cur, "releases/"+id); err != nil {
			return refuse(resp, CodeFailed, "switch %s: %v", cur, errors.Join(err, restore()))
		}
		result.Switched = true
		if oldCur != "" {
			if err := h.setLink(prev, "releases/"+oldCur); err != nil {
				return refuse(resp, CodeFailed, "switch %s: %v", prev, errors.Join(err, restore()))
			}
		}
	}
	result.Switched = true
	keep := req.KeepReleases
	if keep == 0 {
		keep = DefaultKeepReleases
	}
	if unit == "" {
		h.pruneReleases(svc, keep, id, result.Previous)
		return resp
	}
	if result.UnitChanged {
		if res, err := h.run(ctx, analyzeTimeout, systemdAnalyze, "verify", unitPath); err != nil {
			resp.Log = sanitizeLog(res.Output)
			if rerr := restore(); rerr != nil {
				return refuse(resp, CodeFailed, "the unit does not verify, and restoring the previous unit and release failed: %v", rerr)
			}
			return refuse(resp, CodeFailed, "the unit does not verify; the previous unit and release were restored")
		}
	}
	// systemd may also hold an older copy of an unchanged unit, for example
	// after an earlier reload failed.
	reload := result.UnitChanged
	if !reload {
		res, err := h.run(ctx, systemctlTimeout, systemctl, "show", "--property=NeedDaemonReload", "--value", unitName(svc))
		reload = err != nil || strings.TrimSpace(res.Output) != "no"
	}
	if reload {
		if res, err := h.run(ctx, systemctlTimeout, systemctl, "daemon-reload"); err != nil {
			resp.Log = sanitizeLog(res.Output)
			rerr := restore()
			if rerr == nil {
				_, rerr = h.run(ctx, systemctlTimeout, systemctl, "daemon-reload")
			}
			if rerr != nil {
				return refuse(resp, CodeFailed, "%s, and restoring the previous unit and release failed: %v", commandFailure("systemctl daemon-reload", res.Output, err), rerr)
			}
			return refuse(resp, CodeFailed, "%s; the previous unit and release were restored", commandFailure("systemctl daemon-reload", res.Output, err))
		}
	}
	for _, u := range orphans {
		inst, _ := strings.CutPrefix(strings.TrimSuffix(u, ".service"), svc+"@")
		result.Removed = append(result.Removed, inst)
	}
	if len(orphans) > 0 {
		if res, err := h.run(ctx, systemctlTimeout, append([]string{systemctl, "disable", "--now", "--quiet"}, orphans...)...); err != nil {
			resp.Log = sanitizeLog(res.Output)
			result.Removed = nil
			return refuse(resp, CodeFailed, "%s; the release is current", commandFailure("systemctl disable --now", res.Output, err))
		}
	}
	if res, _ := h.run(ctx, systemctlTimeout, systemctl, "is-enabled", unitName(svc)); strings.TrimSpace(res.Output) != "enabled" {
		if res, err := h.run(ctx, systemctlTimeout, systemctl, "enable", "--quiet", unitName(svc)); err != nil {
			resp.Log = sanitizeLog(res.Output)
			return refuse(resp, CodeFailed, "%s", commandFailure("systemctl enable", res.Output, err))
		}
	}
	if res, err := h.run(ctx, systemctlTimeout, systemctl, "restart", unitName(svc)); err != nil {
		resp.Log = sanitizeLog(res.Output)
		h.removeUnits(ctx, &resp, orphans)
		return refuse(resp, CodeFailed, "%s; the release is current", commandFailure("systemctl restart", res.Output, err))
	}
	h.removeUnits(ctx, &resp, orphans)
	h.pruneReleases(svc, keep, id, result.Previous)
	return resp
}

// removeUnits removes the files of stopped and disabled units the helper
// wrote and reloads systemd; a failure is noted in the log only.
func (h *Helper) removeUnits(ctx context.Context, resp *Response, units []string) {
	if len(units) == 0 {
		return
	}
	var notes []string
	for _, u := range units {
		p := hostsetup.UnitDir + "/" + u
		if err := h.FS.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			notes = append(notes, fmt.Sprintf("could not remove %s: %v", p, err))
		}
	}
	if res, err := h.run(ctx, systemctlTimeout, systemctl, "daemon-reload"); err != nil {
		notes = append(notes, commandFailure("systemctl daemon-reload", res.Output, err))
	}
	if len(notes) > 0 {
		resp.Log = sanitizeLog(strings.TrimRight(resp.Log, "\n") + "\n" + strings.Join(notes, "\n"))
	}
}

// checkServiceFiles checks what host setup made for the service: its env
// file, which must not set what host.json decides, and its data directory.
func (h *Helper) checkServiceFiles(svc string, acc hostsetup.Account) error {
	env := hostsetup.ServiceEnvFile(svc)
	if _, err := h.trustedPath(env, true); err != nil {
		return err
	}
	data, err := h.readFile(env)
	if err != nil {
		return err
	}
	if key := envSets(string(data), "PORT", "HOST"); key != "" {
		return fmt.Errorf("%s sets %s, which comes from host.json; remove it", env, key)
	}
	info, err := h.FS.Lstat(hostsetup.ServiceDataDir(svc))
	if err != nil || !info.IsDir() || info.UID != acc.UID {
		return fmt.Errorf("%s is not a directory of the account %s", hostsetup.ServiceDataDir(svc), svc)
	}
	return nil
}

// envSets returns the first of keys that an environment file may assign,
// or "". It errs on the side of finding one: every physical line and every
// line joined by backslash continuations is read the way systemd reads an
// assignment (whitespace around the key, an "export" prefix, a quoted key),
// keys are compared without regard to case, and comments are skipped only
// where they start a line.
func envSets(data string, keys ...string) string {
	data = strings.ReplaceAll(data, "\r", "\n")
	physical := strings.Split(data, "\n")
	logical := strings.Split(strings.ReplaceAll(data, "\\\n", ""), "\n")
	for _, line := range append(physical, logical...) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && strings.TrimLeft(rest, " \t") != rest {
			line = strings.TrimSpace(rest)
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		for _, k := range keys {
			if strings.EqualFold(strings.TrimSpace(key), k) {
				return k
			}
		}
	}
	return ""
}

// startCommand is ExecStart: the runtime's interpreter with a release file,
// or an executable of the release, with the start arguments.
func (h *Helper) startCommand(svc, relDir string, start []string, rtBin string) ([]string, error) {
	info, err := h.FS.Lstat(relDir + "/" + start[0])
	if err != nil || !info.IsRegular() {
		return nil, fmt.Errorf("start: %s is not a regular file of the release", start[0])
	}
	if rtBin == "" {
		if info.Mode&0o111 == 0 {
			return nil, fmt.Errorf("start: %s is not executable; name a runtime to run it with", start[0])
		}
		return append([]string{hostsetup.ServiceOptDir(svc) + "/current/" + start[0]}, start[1:]...), nil
	}
	for _, name := range interpreters {
		if _, err := h.FS.Lstat(rtBin + "/" + name); err == nil {
			return append([]string{rtBin + "/" + name, start[0]}, start[1:]...), nil
		}
	}
	return nil, fmt.Errorf("the runtime has no %s", strings.Join(interpreters, " or "))
}

// migrateCommand resolves the migration program: a program of the runtime's
// bin directory when the runtime has one of that name, otherwise an
// executable of the release.
func (h *Helper) migrateCommand(relDir string, migrate []string, rtBin string) ([]string, error) {
	p := migrate[0]
	if rtBin != "" && !strings.Contains(p, "/") {
		if _, err := h.FS.Lstat(rtBin + "/" + p); err == nil {
			return append([]string{rtBin + "/" + p}, migrate[1:]...), nil
		}
	}
	if !relPath(p) {
		return nil, fmt.Errorf("migrate: %s is not a program of the runtime", p)
	}
	info, err := h.FS.Lstat(relDir + "/" + p)
	if err != nil || !info.IsRegular() || info.Mode&0o111 == 0 {
		return nil, fmt.Errorf("migrate: %s is neither a program of the runtime nor an executable of the release", p)
	}
	return append([]string{relDir + "/" + p}, migrate[1:]...), nil
}

// migrateArgs runs a migration like the service would run: as its account,
// in the new release, with its env file and sandbox. Port 0 (a service with
// instances, whose migration no instance owns) sets no PORT or HOST.
func migrateArgs(unit, svc string, acc hostsetup.Account, relDir string, port int, rtBin, backup string, argv []string) []string {
	args := []string{
		systemdRun, "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
		"--unit=" + unit,
		"--uid=" + strconv.Itoa(acc.UID), "--gid=" + strconv.Itoa(acc.GID),
		"--working-directory=" + relDir,
	}
	if port != 0 {
		args = append(args, "--setenv=PORT="+strconv.Itoa(port), "--setenv=HOST=127.0.0.1")
	}
	if rtBin != "" {
		args = append(args, "--setenv=PATH="+rtBin+":"+systemPath)
	}
	props := append(serviceSandbox(svc, port, backup), "EnvironmentFile="+hostsetup.ServiceEnvFile(svc),
		"RuntimeMaxSec="+strconv.Itoa(int(migrateTimeout.Seconds())))
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	return append(append(args, "--"), argv...)
}

// linkTarget returns the release a root-owned "releases/<id>" link names,
// or "" when there is no link.
func (h *Helper) linkTarget(p string) (string, error) {
	info, err := h.FS.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsLink() || info.UID != h.RootUID {
		return "", fmt.Errorf("%s is not a root-owned symbolic link", p)
	}
	target, err := h.FS.Readlink(p)
	if err != nil {
		return "", err
	}
	id, ok := strings.CutPrefix(target, "releases/")
	if !ok || !idPattern.MatchString(id) {
		return "", fmt.Errorf("%s points to %q, not a release", p, hostsetup.Printable(target, 100))
	}
	return id, nil
}

// setLink replaces the link p atomically.
func (h *Helper) setLink(p, target string) error {
	tmp := path.Dir(p) + "/." + path.Base(p) + ".new"
	if err := h.FS.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := h.FS.Symlink(target, tmp, h.RootUID, h.RootGID); err != nil {
		return err
	}
	return h.FS.Rename(tmp, p)
}

// restoreLink points p back to a release, or removes it.
func (h *Helper) restoreLink(p, id string) error {
	if id == "" {
		err := h.FS.Remove(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return h.setLink(p, "releases/"+id)
}

// readFile reads a file of at most 1 MiB; a larger one is refused rather
// than read in part.
func (h *Helper) readFile(p string) ([]byte, error) {
	f, err := h.FS.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err == nil && len(data) > 1<<20 {
		err = fmt.Errorf("%s is larger than 1 MiB", p)
	}
	return data, err
}

// pruneReleases keeps the newest keep releases and always current and
// previous; it also removes leftovers of interrupted installs.
func (h *Helper) pruneReleases(svc string, keep int, current, previous string) {
	releases := hostsetup.ServiceReleases(svc)
	names, err := h.FS.ReadDir(releases)
	if err != nil {
		return
	}
	var ids []string
	for _, n := range names {
		id, _, _ := strings.Cut(n, ".")
		switch {
		case !idPattern.MatchString(id):
		case n == id:
			ids = append(ids, id)
		case n == id+".new" || n == id+".old":
			_ = h.FS.RemoveAll(releases + "/" + n)
		}
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	for i, id := range ids {
		if i < keep || id == current || id == previous {
			continue
		}
		if err := h.FS.RemoveAll(releases + "/" + id); err == nil {
			_ = h.FS.Remove(manifestPath(svc, id))
		}
	}
}
