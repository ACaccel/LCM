package deployhelper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

const (
	// keepBuilds is the number of builds kept per service.
	keepBuilds = 3
	// inputsRoot is where build inputs appear inside the build sandbox; a
	// read-only tmpfs is mounted there so that nothing is created on the
	// host's /run.
	inputsRoot = "/run/lcm-build"
	inputsDir  = inputsRoot + "/inputs"
	// systemPath is the system part of PATH in builds and services.
	systemPath = "/usr/local/bin:/usr/bin:/bin"
	// buildMemory bounds the memory of one build.
	buildMemory = "4G"
)

// buildRecord is build.json in a build directory, root-owned.
type buildRecord struct {
	Version    int      `json:"version"`
	Service    string   `json:"service"`
	BuildID    string   `json:"buildId"`
	Commit     string   `json:"commit"`
	Runtimes   []string `json:"runtimes"`
	State      State    `json:"state"`
	CreatedAt  int64    `json:"createdAt"`
	FinishedAt int64    `json:"finishedAt,omitempty"`
}

func serviceBuilds(service string) string { return hostsetup.ServiceBuildDir(service) }

// build copies the service's checkout into a new build directory owned by
// the service's build account and runs the command there in a sandbox.
//
// Layout: /var/lib/lcm-build and /var/cache/lcm-build are root's;
// /var/lib/lcm-build/<service> and each <buildId> in it are
// root:lcmb-<service> 0750, so the build account can never rename or
// replace what root writes; src/ and home/ in a build directory and
// /var/cache/lcm-build/<service> belong to the build account alone. Every
// service has its own build account, so no build can read or change
// another service's builds, caches or inputs.
func (h *Helper) build(ctx context.Context, req Request, cfg hostsetup.Config, g hostsetup.ServiceGrant) Response {
	resp, svc := reply(req), req.Service
	for _, rt := range req.Runtimes {
		if !slices.Contains(g.Runtimes, rt) {
			return refuse(resp, CodeRefused, "the runtime %s is not granted to %s in %s", rt, svc, hostsetup.ConfigPath)
		}
		if _, err := h.trustedPath(hostsetup.RuntimesDir+"/"+rt, false); err != nil {
			return refuse(resp, CodeRefused, "the runtime %s is not installed (%v); run sudo bin/lcm host apply", rt, err)
		}
	}
	if len(g.Inputs) > 0 {
		operator, err := h.account(ctx, cfg.Operator, "", "")
		if err != nil {
			return refuse(resp, CodeRefused, "operator: %v", err)
		}
		for _, name := range slices.Sorted(maps.Keys(g.Inputs)) {
			if err := h.trustedInput(g.Inputs[name], operator); err != nil {
				return refuse(resp, CodeRefused, "input %s: %v", name, err)
			}
		}
	}
	// The lcm account predates the deploy component; apply checks only its
	// home directory, and so does the helper.
	lcm, err := h.account(ctx, hostsetup.ServiceAccount, "", "")
	if err == nil && lcm.Home != hostsetup.DataDir {
		err = fmt.Errorf("account lcm has home directory %s, not %s", lcm.Home, hostsetup.DataDir)
	}
	if err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	builder, err := h.builder(ctx, svc)
	if err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	head, err := h.checkoutHead(svc)
	if err != nil {
		return refuse(resp, CodeRefused, "the checkout %s: %v", hostsetup.ServiceCheckout(svc), err)
	}
	if head != req.Commit {
		return refuse(resp, CodeRefused, "the checkout %s is at %s, not %s", hostsetup.ServiceCheckout(svc), head, req.Commit)
	}
	builds, cache := serviceBuilds(svc), hostsetup.ServiceCacheDir(svc)
	if err := h.buildDirs(svc, builder); err != nil {
		return refuse(resp, CodeRefused, "%v; run sudo bin/lcm host allow or host apply", err)
	}
	if len(g.Inputs) > 0 {
		if err := h.ensureDir(inputsRoot, 0o755, h.RootUID, h.RootGID); err != nil {
			return refuse(resp, CodeFailed, "%v", err)
		}
	}
	lim := h.Limits.withDefaults()
	if err := h.checkFree(builds, lim.MinFreeBytes, lim.MinFreeInodes); err != nil {
		return refuse(resp, CodeRefused, "not enough room for a build: %v", err)
	}
	// A cache that grew too large is started afresh; no build of the
	// service runs meanwhile.
	if size, n, err := usage(h.FS, cache, lim.MaxEntries); err != nil || size > lim.CacheBytes || n > lim.MaxEntries/2 {
		if err := h.emptyDir(cache); err != nil {
			return refuse(resp, CodeFailed, "empty the build cache: %v", err)
		}
	}

	id := h.newID()
	dir := builds + "/" + id
	if err := h.FS.Mkdir(dir, 0o750, h.RootUID, builder.GID); err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	rec := buildRecord{Version: Version, Service: svc, BuildID: id, Commit: req.Commit, Runtimes: req.Runtimes,
		State: StateRunning, CreatedAt: h.Clock.Now().UnixMilli()}
	if rec.Runtimes == nil {
		rec.Runtimes = []string{}
	}
	resp.Build = &BuildResult{BuildID: id, Commit: req.Commit}
	finish := func(r Response) Response {
		rec.State, rec.FinishedAt = StateSucceeded, h.Clock.Now().UnixMilli()
		if r.Code != "" {
			rec.State = StateFailed
		}
		if err := h.writeJSON(dir+"/build.json", rec); err != nil && r.Code == "" {
			r = refuse(r, CodeFailed, "record the build: %v", err)
		}
		h.pruneBuilds(builds, id)
		return r
	}
	if err := h.writeJSON(dir+"/build.json", rec); err != nil {
		return finish(refuse(resp, CodeFailed, "record the build: %v", err))
	}
	// The copy is root's until it is complete, then handed over bottom-up.
	// It leaves out .git: the repository's history and configuration are
	// lcm's, and the build gets the commit as LCM_COMMIT.
	gitDir := "work/" + svc + "/.git"
	err = hostsetup.CopyTree(h.FS, hostsetup.DataDir, "work/"+svc, dir+"/src", hostsetup.CopyOptions{
		Owners: map[int]bool{lcm.UID: true}, UID: h.RootUID, GID: h.RootGID,
		Exclude: func(rel string) bool { return rel == gitDir },
	})
	if err == nil {
		err = hostsetup.HandOver(h.FS, dir+"/src", builder.UID, builder.GID)
	}
	if err == nil {
		err = h.FS.Mkdir(dir+"/home", 0o700, builder.UID, builder.GID)
	}
	if err == nil {
		err = h.FS.WriteFile(dir+"/build.sh", strings.NewReader(req.Command+"\n"), 0o644, h.RootUID, h.RootGID)
	}
	if err != nil {
		return finish(refuse(resp, CodeFailed, "prepare the build: %v", err))
	}
	unit := "lcm-build-" + svc + "-" + id
	argv := buildArgs(unit, dir, cache, builder, req, g.Inputs, backupDir(cfg))
	stop := h.watchBuild(ctx, unit, []string{dir + "/src", dir + "/home", cache}, builds, lim)
	res, err := h.run(ctx, time.Duration(req.TimeoutSec)*time.Second+2*time.Minute, argv...)
	resp.Log = sanitizeLog(res.Output)
	if why := stop(); why != "" {
		return finish(refuse(resp, CodeFailed, "the build was stopped: %s", why))
	}
	if err != nil {
		h.stopUnit(ctx, unit, err)
		return finish(refuse(resp, CodeFailed, "%s", commandFailure("the build", err)))
	}
	return finish(resp)
}

// buildArgs is the systemd-run command line of a build. The command runs
// as the service's build account in a new service unit: everything is
// read-only except its source tree, its home and its cache; /home, /root and
// /run/user are hidden, and so are LCM's data, its configuration, the
// helper's ledgers and the backups; the build and cache roots are replaced
// by empty read-only file systems with only this build's directories bound
// back; other processes are invisible in /proc; it gets no capabilities, no
// new privileges, a private /tmp and /dev, a time limit and a memory limit.
// It has the network, to download dependencies, but not the local host,
// where LCM's API and the services listen. Its environment is fixed: PATH
// (the runtimes' bin directories first), HOME, LANG, the commit, the cache
// (also as the npm and Go caches) and, with inputs, where they are mounted
// read-only. The command is run from a root-owned script file, so that
// systemd never expands anything in it.
func buildArgs(unit, dir, cache string, builder hostsetup.Account, req Request, inputs map[string]string, backup string) []string {
	var paths []string
	for _, rt := range req.Runtimes {
		paths = append(paths, hostsetup.RuntimesDir+"/"+rt+"/bin")
	}
	paths = append(paths, systemPath)
	args := []string{
		systemdRun, "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
		"--unit=" + unit,
		"--uid=" + strconv.Itoa(builder.UID), "--gid=" + strconv.Itoa(builder.GID),
		"--working-directory=" + dir + "/src",
		"--setenv=PATH=" + strings.Join(paths, ":"),
		"--setenv=HOME=" + dir + "/home",
		"--setenv=LANG=C.UTF-8",
		"--setenv=XDG_CACHE_HOME=" + cache,
		"--setenv=LCM_CACHE_DIR=" + cache,
		"--setenv=npm_config_cache=" + cache + "/npm",
		"--setenv=GOCACHE=" + cache + "/go-build",
		"--setenv=GOMODCACHE=" + cache + "/go-mod",
		"--setenv=LCM_COMMIT=" + req.Commit,
	}
	props := sandbox("0022", backup)
	props = append(props,
		"ProcSubset=pid",
		"IPAddressDeny=localhost",
		"TemporaryFileSystem="+hostsetup.BuildDir+":ro",
		"TemporaryFileSystem="+hostsetup.BuildCacheDir+":ro",
		"BindPaths="+dir+"/src",
		"BindPaths="+dir+"/home",
		"BindPaths="+cache,
		"BindReadOnlyPaths="+dir+"/build.sh",
		"ReadWritePaths="+dir+"/src",
		"ReadWritePaths="+dir+"/home",
		"ReadWritePaths="+cache,
		"RuntimeMaxSec="+strconv.Itoa(req.TimeoutSec),
		"MemoryMax="+buildMemory,
		"TasksMax=4096",
	)
	if len(inputs) > 0 {
		args = append(args, "--setenv=LCM_INPUTS_DIR="+inputsDir)
		props = append(props, "TemporaryFileSystem="+inputsRoot+":ro")
		for _, name := range slices.Sorted(maps.Keys(inputs)) {
			props = append(props, "BindReadOnlyPaths="+inputs[name]+":"+inputsDir+"/"+name)
		}
	}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	return append(args, "--", shell, dir+"/build.sh")
}

// hidden are the trees no build or service may read, whatever their
// permissions.
var hidden = []string{hostsetup.DataDir, hostsetup.EtcDir, hostsetup.DeployLedger, hostsetup.PowerLedger}

// backupDir is the configured backup directory, or "".
func backupDir(cfg hostsetup.Config) string {
	if cfg.Backup == nil {
		return ""
	}
	return cfg.Backup.Dir
}

// sandbox is the hardening that builds, migrations and service units
// share; backup, when set, is hidden too.
func sandbox(umask, backup string) []string {
	var inaccessible []string
	for _, p := range append(slices.Clone(hidden), backup) {
		if p != "" {
			inaccessible = append(inaccessible, "-"+p)
		}
	}
	return []string{
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"PrivateDevices=yes",
		"ProtectKernelTunables=yes",
		"ProtectKernelModules=yes",
		"ProtectKernelLogs=yes",
		"ProtectControlGroups=yes",
		"ProtectClock=yes",
		"ProtectHostname=yes",
		"ProtectProc=invisible",
		"RestrictSUIDSGID=yes",
		"RestrictRealtime=yes",
		"LockPersonality=yes",
		"CapabilityBoundingSet=",
		"AmbientCapabilities=",
		"UMask=" + umask,
		"InaccessiblePaths=" + strings.Join(inaccessible, " "),
	}
}

// builder reads the service's build account.
func (h *Helper) builder(ctx context.Context, svc string) (hostsetup.Account, error) {
	return h.account(ctx, hostsetup.BuildAccount(svc), hostsetup.ServiceBuildDir(svc), hostsetup.BuildComment(svc))
}

// buildDirs checks the service's build and cache directories that host
// setup made: the builds root:<builder> and not writable by the group, the
// cache the build account's alone.
func (h *Helper) buildDirs(svc string, builder hostsetup.Account) error {
	builds := serviceBuilds(svc)
	info, err := h.trustedPath(builds, false)
	if err != nil {
		return err
	}
	if info.GID != builder.GID || info.Mode.Perm()&0o007 != 0 {
		return fmt.Errorf("%s is not root:%s 0750", builds, builder.Name)
	}
	if _, err := h.trustedPath(hostsetup.BuildCacheDir, false); err != nil {
		return err
	}
	cache := hostsetup.ServiceCacheDir(svc)
	info, err = h.FS.Lstat(cache)
	if err != nil || !info.IsDir() || info.IsLink() || info.UID != builder.UID || info.Mode.Perm()&0o007 != 0 {
		return fmt.Errorf("%s is not a directory of %s alone", cache, builder.Name)
	}
	return nil
}

// stopUnit stops a transient unit that a command left behind when it did
// not finish: killing systemd-run does not stop the unit it started.
func (h *Helper) stopUnit(ctx context.Context, unit string, err error) {
	if execx.ExitCode(err) >= 0 {
		return
	}
	_, _ = h.run(context.WithoutCancel(ctx), systemctlTimeout, systemctl, "stop", unit+".service")
}

var (
	headRef  = regexp.MustCompile(`^ref: (refs/heads/[A-Za-z0-9._/-]{1,200})$`)
	shaLine  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	maxRefs  = int64(4 << 20)
	maxSmall = int64(4 << 10)
)

// checkoutHead reads the commit that HEAD of the service's checkout names,
// from the files in .git, without running git as root. The checkout is
// read through a tree confined to /var/lib/lcm, so no link in it leads
// elsewhere. The account lcm owns the checkout and decides what it builds;
// this check keeps the recorded commit honest, it is not a boundary.
func (h *Helper) checkoutHead(svc string) (string, error) {
	if _, err := h.trustedPath(path.Dir(hostsetup.DataDir), false); err != nil {
		return "", err
	}
	before, err := h.FS.Lstat(hostsetup.DataDir)
	if err != nil {
		return "", err
	}
	t, err := h.FS.OpenTree(hostsetup.DataDir)
	if err != nil {
		return "", err
	}
	defer func() { _ = t.Close() }()
	if after, err := t.Root(); err != nil || after.Dev != before.Dev || after.Ino != before.Ino || before.IsLink() {
		return "", fmt.Errorf("%s changed while it was opened", hostsetup.DataDir)
	}
	git := "work/" + svc + "/.git"
	if info, err := t.Lstat(git); err != nil || !info.IsDir() {
		return "", errors.New("it has no .git directory")
	}
	head, err := readSmall(t, git+"/HEAD", maxSmall)
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	if shaLine.MatchString(head) {
		return head, nil
	}
	m := headRef.FindStringSubmatch(head)
	if m == nil || slices.Contains(strings.Split(m[1], "/"), "..") {
		return "", errors.New("HEAD names neither a commit nor a branch")
	}
	if ref, err := readSmall(t, git+"/"+m[1], maxSmall); err == nil {
		if ref = strings.TrimSpace(ref); shaLine.MatchString(ref) {
			return ref, nil
		}
		return "", fmt.Errorf("%s is not a commit", m[1])
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	packed, err := readSmall(t, git+"/packed-refs", maxRefs)
	if err != nil {
		return "", fmt.Errorf("%s is not found", m[1])
	}
	for line := range strings.SplitSeq(packed, "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == m[1] && shaLine.MatchString(sha) {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%s is not found", m[1])
}

func readSmall(t hostsetup.Tree, rel string, max int64) (string, error) {
	f, info, err := t.Open(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if info.Size > max {
		return "", fmt.Errorf("%s is larger than %d bytes", rel, max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	return string(b), err
}

// writeJSON writes a root-owned 0644 file.
func (h *Helper) writeJSON(p string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return h.FS.WriteFile(p, bytes.NewReader(append(raw, '\n')), 0o644, h.RootUID, h.RootGID)
}

// readJSON reads a root-owned regular file strictly.
func (h *Helper) readJSON(p string, v any) error {
	if _, err := h.trustedPath(p, true); err != nil {
		if _, lerr := h.FS.Lstat(p); errors.Is(lerr, fs.ErrNotExist) {
			return lerr
		}
		return err
	}
	f, err := h.FS.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, MaxResponse+1))
	if err != nil {
		return err
	}
	return decodeStrict(raw, MaxResponse, v)
}

// pruneBuilds keeps the newest keepBuilds builds of a service, and always
// the build keep.
func (h *Helper) pruneBuilds(builds, keep string) {
	names, err := h.FS.ReadDir(builds)
	if err != nil {
		return
	}
	var ids []string
	for _, n := range names {
		if idPattern.MatchString(n) {
			ids = append(ids, n)
		}
	}
	slices.Sort(ids)
	for i, id := range ids {
		if i < len(ids)-keepBuilds && id != keep {
			_ = h.FS.RemoveAll(builds + "/" + id)
		}
	}
}
