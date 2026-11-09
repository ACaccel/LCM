package deployhelper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// interpreters are the programs of a runtime that run a release's start
// file, in order of preference.
var interpreters = []string{"node", "python3"}

func unitName(svc string) string { return svc + ".service" }

// serviceSandbox is the hardening of a service's unit and migration: it
// runs as its own account, can write only its data directory, sees neither
// LCM's data, the backups nor the build trees, may bind no port but its
// own, uses only IP and Unix sockets and has its own IPC namespace. It may
// still connect anywhere, and the kernel does not stop it from binding its
// port on another address than HOST: systemd has no per-address bind
// rule, and IPAddressAllow= would also block its outbound connections.
func serviceSandbox(svc string, port int, backup string) []string {
	props := sandbox("0027", backup)
	props = append(props,
		"SocketBindAllow=tcp:"+strconv.Itoa(port),
		"SocketBindDeny=any",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX",
		"PrivateIPC=yes",
		"ReadWritePaths="+hostsetup.ServiceDataDir(svc))
	return append(props, "InaccessiblePaths=-"+hostsetup.BuildDir+" -"+hostsetup.BuildCacheDir)
}

// renderUnit is the built-in unit template. Every value in it is checked
// to need no quoting or escaping.
func renderUnit(svc string, port int, execStart []string, rtBin, backup string) string {
	lines := []string{
		hostsetup.ServiceUnitMarker,
		"[Unit]",
		"Description=" + svc + " (managed by LCM)",
		"After=network.target",
		"",
		"[Service]",
		"Type=simple",
		"User=" + svc,
		"Group=" + svc,
		"WorkingDirectory=" + hostsetup.ServiceOptDir(svc) + "/current",
		"ExecStart=" + strings.Join(execStart, " "),
		"EnvironmentFile=" + hostsetup.ServiceEnvFile(svc),
		"Environment=PORT=" + strconv.Itoa(port) + " HOST=127.0.0.1",
	}
	if rtBin != "" {
		lines = append(lines, "Environment=PATH="+rtBin+":"+systemPath)
	}
	lines = append(lines, "Restart=on-failure", "RestartSec=5")
	lines = append(lines, serviceSandbox(svc, port, backup)...)
	lines = append(lines, "", "[Install]", "WantedBy=multi-user.target")
	return strings.Join(lines, "\n") + "\n"
}

// activate makes a release current and (re)starts the service with a unit
// from the built-in template. The optional migration runs first, in the new
// release; when it fails nothing is switched.
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
	acc, err := h.account(ctx, svc, hostsetup.ServiceDataDir(svc), hostsetup.ServiceComment(svc))
	if err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	if conflict, err := hostsetup.UnitConflict(h.FS, svc, h.RootUID); err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	} else if conflict != "" {
		return refuse(resp, CodeRefused, "%s", conflict)
	}
	if err := h.checkServiceFiles(svc, acc); err != nil {
		return refuse(resp, CodeRefused, "%v; run sudo bin/lcm host allow", err)
	}
	rtBin := ""
	if req.Runtime != "" {
		if !slices.Contains(g.Runtimes, req.Runtime) {
			return refuse(resp, CodeRefused, "the runtime %s is not granted to %s in %s", req.Runtime, svc, hostsetup.ConfigPath)
		}
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
		res, err := h.run(ctx, migrateTimeout+time.Minute, migrateArgs(unit, svc, acc, relDir, g.Port, rtBin, backupDir(cfg), argv)...)
		resp.Log = sanitizeLog(res.Output)
		if err != nil {
			h.stopUnit(ctx, unit, err)
			return refuse(resp, CodeFailed, "%s; nothing was switched", commandFailure("the migration", err))
		}
	}
	return h.switchAndRestart(ctx, req, resp, renderUnit(svc, g.Port, execStart, rtBin, backupDir(cfg)))
}

// switchAndRestart installs the unit, switches the current and previous
// links, reloads systemd when the unit changed and restarts the service. A
// unit that does not verify is put back together with the links.
func (h *Helper) switchAndRestart(ctx context.Context, req Request, resp Response, unit string) Response {
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
	if _, err := h.FS.Lstat(unitPath); err == nil {
		if oldUnit, err = h.readFile(unitPath); err != nil {
			return refuse(resp, CodeFailed, "%v", err)
		}
		hadUnit = true
	}
	result := &ActivateResult{ReleaseID: id, Previous: oldCur, UnitChanged: !hadUnit || string(oldUnit) != unit}
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
	if result.UnitChanged {
		if res, err := h.run(ctx, analyzeTimeout, systemdAnalyze, "verify", unitPath); err != nil {
			resp.Log = sanitizeLog(res.Output)
			if rerr := restore(); rerr != nil {
				return refuse(resp, CodeFailed, "the unit does not verify, and restoring the previous unit and release failed: %v", rerr)
			}
			return refuse(resp, CodeFailed, "the unit does not verify; the previous unit and release were restored")
		}
		if res, err := h.run(ctx, systemctlTimeout, systemctl, "daemon-reload"); err != nil {
			resp.Log = sanitizeLog(res.Output)
			return refuse(resp, CodeFailed, "%s", commandFailure("systemctl daemon-reload", err))
		}
	}
	if res, _ := h.run(ctx, systemctlTimeout, systemctl, "is-enabled", unitName(svc)); strings.TrimSpace(res.Output) != "enabled" {
		if res, err := h.run(ctx, systemctlTimeout, systemctl, "enable", "--quiet", unitName(svc)); err != nil {
			resp.Log = sanitizeLog(res.Output)
			return refuse(resp, CodeFailed, "%s", commandFailure("systemctl enable", err))
		}
	}
	if res, err := h.run(ctx, systemctlTimeout, systemctl, "restart", unitName(svc)); err != nil {
		resp.Log = sanitizeLog(res.Output)
		return refuse(resp, CodeFailed, "%s; the release is current", commandFailure("systemctl restart", err))
	}
	keep := req.KeepReleases
	if keep == 0 {
		keep = DefaultKeepReleases
	}
	h.pruneReleases(svc, keep, id, result.Previous)
	return resp
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
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")
		for _, key := range []string{"PORT", "HOST"} {
			if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
				return fmt.Errorf("%s sets %s, which comes from host.json; remove it", env, key)
			}
		}
	}
	info, err := h.FS.Lstat(hostsetup.ServiceDataDir(svc))
	if err != nil || !info.IsDir() || info.UID != acc.UID {
		return fmt.Errorf("%s is not a directory of the account %s", hostsetup.ServiceDataDir(svc), svc)
	}
	return nil
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
// in the new release, with its env file and sandbox.
func migrateArgs(unit, svc string, acc hostsetup.Account, relDir string, port int, rtBin, backup string, argv []string) []string {
	args := []string{
		systemdRun, "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
		"--unit=" + unit,
		"--uid=" + strconv.Itoa(acc.UID), "--gid=" + strconv.Itoa(acc.GID),
		"--working-directory=" + relDir,
		"--setenv=PORT=" + strconv.Itoa(port), "--setenv=HOST=127.0.0.1",
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

func (h *Helper) readFile(p string) ([]byte, error) {
	f, err := h.FS.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, 1<<20))
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
