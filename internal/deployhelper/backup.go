package deployhelper

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Names of the entries of a service's backup directory.
const (
	manifestName     = "manifest.json"
	preRestorePrefix = "pre-restore-"
	stagePrefix      = ".stage-"
	tmpPrefix        = ".tmp-"
	delPrefix        = ".del-"
	// maxListed bounds the entries of backups.
	maxListed = 200
	// preRestoreMinAge is how long a pre-restore set is kept.
	preRestoreMinAge = 7 * 24 * time.Hour
	// restoreWorkerSec bounds each worker run of a restore.
	restoreWorkerSec = 3600
	// workerMemory bounds the memory of a worker.
	workerMemory = "2G"
	// maxManifest bounds manifest.json.
	maxManifest = 1 << 20
	// stageEntries bounds the entries of a staging output directory: the
	// worker writes a few artifacts there.
	stageEntries = 1000
)

// opErr is the failure of a step of an operation.
type opErr struct {
	code Code
	msg  string
	log  string
}

func (e *opErr) Error() string { return e.msg }

func failf(code Code, format string, args ...any) *opErr {
	return &opErr{code: code, msg: fmt.Sprintf(format, args...)}
}

// withLog adds the sanitized output of a command.
func (e *opErr) withLog(out string) *opErr {
	e.log = sanitizeLog(out)
	return e
}

// respond turns a failed step into the reply.
func respond(resp Response, e *opErr) Response {
	if e.log != "" {
		resp.Log = e.log
	}
	return refuse(resp, e.code, "%s", e.msg)
}

// manifestSource is a source in a set's manifest with its artifacts.
type manifestSource struct {
	BackupSource
	Artifacts []string `json:"artifacts"`
	// Absent: the path did not exist when the set was made (only the copy
	// of what a restore replaces).
	Absent bool `json:"absent,omitempty"`
	// Mode is the permission bits of a SQLite database file at backup time
	// (never special bits); a restore uses it when no file is there to take
	// the mode from. Sets made before it was recorded have none.
	Mode uint32 `json:"mode,omitempty"`
}

type manifestFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// setManifest is manifest.json of a backup set.
type setManifest struct {
	Version      int              `json:"version"`
	Service      string           `json:"service"`
	BackupID     string           `json:"backupId"`
	Kind         string           `json:"kind"`
	Mode         string           `json:"mode"`
	CreatedAt    int64            `json:"createdAt"`
	ReleaseID    string           `json:"releaseId,omitempty"`
	Commit       string           `json:"commit,omitempty"`
	RestoredFrom string           `json:"restoredFrom,omitempty"`
	Sources      []manifestSource `json:"sources"`
	Files        []manifestFile   `json:"files"`
	Size         int64            `json:"size"`
	Skipped      int              `json:"skipped"`
}

// restorableSources are the sources a restore of the set puts back.
func (m setManifest) restorableSources() []BackupSource {
	var out []BackupSource
	for _, i := range restorable(m) {
		out = append(out, m.Sources[i].BackupSource)
	}
	return out
}

func (m setManifest) plainSources() []BackupSource {
	out := make([]BackupSource, len(m.Sources))
	for i, s := range m.Sources {
		out[i] = s.BackupSource
	}
	return out
}

// validate checks a decoded manifest against the set it was read from.
func (m setManifest) validate(svc, id string) error {
	if m.Version != Version || m.Service != svc || m.BackupID != id {
		return errors.New("the manifest is not this set's")
	}
	wantKind := KindBackup
	if strings.HasPrefix(id, preRestorePrefix) {
		wantKind = KindPreRestore
	}
	switch {
	case m.Kind != wantKind:
		return errors.New("the manifest's kind does not match the set")
	case m.Mode != "" && m.Mode != ModeOnline && m.Mode != ModeStopped:
		return errors.New("the manifest has an unknown mode")
	case m.CreatedAt <= 0 || m.Size < 0 || m.Skipped < 0:
		return errors.New("the manifest has an invalid number")
	}
	if err := ValidateSources(m.plainSources()); err != nil {
		return err
	}
	listed := map[string]manifestFile{}
	var total int64
	for _, f := range m.Files {
		if _, dup := listed[f.Name]; dup || f.Size < 0 || !sha256Pattern.MatchString(f.SHA256) {
			return fmt.Errorf("the manifest lists %q wrongly", hostsetup.Printable(f.Name, 64))
		}
		listed[f.Name] = f
		total += f.Size
	}
	if total != m.Size {
		return errors.New("the manifest's size is not the sum of its files")
	}
	used := map[string]bool{}
	for i, s := range m.Sources {
		var want []string
		if !s.Absent {
			want = artifactNames(i, s.BackupSource)
		}
		if s.Absent && s.Type != SourceFiles && s.Type != SourceSQLite {
			return errors.New("only a path can be absent")
		}
		if s.Mode != 0 && (s.Type != SourceSQLite || s.Absent || s.Mode&^0o777 != 0) {
			return fmt.Errorf("source %d has an invalid mode", i)
		}
		if !slices.Equal(s.Artifacts, want) {
			return fmt.Errorf("source %d has unexpected artifacts", i)
		}
		for _, a := range s.Artifacts {
			if _, ok := listed[a]; !ok {
				return fmt.Errorf("artifact %s is not listed", a)
			}
			used[a] = true
		}
	}
	if len(used) != len(listed) {
		return errors.New("the manifest lists files that no source produced")
	}
	return nil
}

// idTime is the creation time an ID names.
func idTime(id string) time.Time {
	id = strings.TrimPrefix(id, preRestorePrefix)
	if len(id) < 16 {
		return time.Time{}
	}
	t, err := time.Parse("20060102T150405Z", id[:16])
	if err != nil {
		return time.Time{}
	}
	return t
}

// backupEnv is what the backup operations of one service share.
type backupEnv struct {
	cfg    hostsetup.Config
	g      hostsetup.ServiceGrant
	svc    string
	acc    hostsetup.Account
	root   string
	svcDir string
}

// backupContext checks what every backup operation relies on: the backup
// directory, which only root may use, the service's directory in it and
// the service account and data directory.
func (h *Helper) backupContext(ctx context.Context, svc string, cfg hostsetup.Config, g hostsetup.ServiceGrant, needData bool) (backupEnv, *opErr) {
	env := backupEnv{cfg: cfg, g: g, svc: svc}
	if cfg.Backup == nil {
		return env, failf(CodeRefused, "backup.dir is not set in %s; set it and run sudo bin/lcm host apply", hostsetup.ConfigPath)
	}
	env.root = cfg.Backup.Dir
	info, err := h.trustedPath(env.root, false)
	switch {
	case err != nil:
		return env, failf(CodeRefused, "backup.dir: %v; run sudo bin/lcm host apply", err)
	case info.Mode.Perm()&0o077 != 0:
		return env, failf(CodeRefused, "backup.dir %s must be accessible by root only (mode 0700); run sudo chmod 700 %s", env.root, env.root)
	}
	env.svcDir = env.root + "/" + svc
	if err := h.ensureDir(env.svcDir, 0o700, h.RootUID, h.RootGID); err != nil {
		return env, failf(CodeFailed, "%v", err)
	}
	if info, err := h.FS.Lstat(env.svcDir); err != nil || info.Mode.Perm()&0o077 != 0 {
		return env, failf(CodeRefused, "%s must be accessible by root only (mode 0700)", env.svcDir)
	}
	if needData {
		acc, err := h.account(ctx, svc, hostsetup.ServiceDataDir(svc), hostsetup.ServiceComment(svc))
		if err != nil {
			return env, failf(CodeRefused, "%v", err)
		}
		env.acc = acc
		info, err := h.FS.Lstat(hostsetup.ServiceDataDir(svc))
		if err != nil || !info.IsDir() || info.IsLink() || info.UID != acc.UID {
			return env, failf(CodeRefused, "%s is not a directory of the account %s", hostsetup.ServiceDataDir(svc), svc)
		}
	}
	return env, nil
}

// currentRelease returns the current release and its commit, best effort.
func (h *Helper) currentRelease(svc string) (string, string) {
	id, err := h.linkTarget(hostsetup.ServiceOptDir(svc) + "/current")
	if err != nil || id == "" {
		return "", ""
	}
	var m Manifest
	if h.readJSON(manifestPath(svc, id), &m) != nil || m.ReleaseID != id {
		return id, ""
	}
	return id, m.Commit
}

// toolDirs are the directories searched for the database tools.
var toolDirs = []string{"/usr/bin", "/usr/local/bin"}

// toolNames are the database tools a source type needs to back up or to
// restore.
func toolNames(s BackupSource, restore bool) []string {
	switch {
	case s.Type == SourcePostgres && restore:
		return []string{"pg_restore"}
	case s.Type == SourcePostgres:
		return []string{"pg_dump"}
	case s.Type == SourceMongoDB && restore:
		return []string{"mongorestore"}
	case s.Type == SourceMongoDB:
		return []string{"mongodump"}
	}
	return nil
}

// trustedTool checks a tool path as root: every link on the way and the file
// it ends at must be root's and in directories only root can change, and the
// file must be executable. The path itself, not its target, is what the
// worker runs, as some distributions dispatch on the program's name.
func (h *Helper) trustedTool(p string) error {
	for range 8 {
		info, err := h.FS.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsLink() {
			info, err := h.trustedPath(p, true)
			if err != nil {
				return err
			}
			if info.Mode.Perm()&0o111 == 0 {
				return fmt.Errorf("%s is not executable", p)
			}
			return nil
		}
		if _, err := h.trustedPath(path.Dir(p), false); err != nil {
			return err
		}
		if info.UID != h.RootUID {
			return fmt.Errorf("%s is a link that root does not own", p)
		}
		target, err := h.FS.Readlink(p)
		if err != nil {
			return err
		}
		if !path.IsAbs(target) {
			target = path.Join(path.Dir(p), target)
		}
		p = path.Clean(target)
	}
	return errors.New("too many links")
}

// resolveTools finds the database tools the sources need in the system's
// binary directories and checks that root alone can change each. It refuses
// before anything is changed when a tool is missing or not trustworthy.
func (h *Helper) resolveTools(sources []BackupSource, restore bool) (map[string]string, *opErr) {
	tools := map[string]string{}
	for _, s := range sources {
		for _, name := range toolNames(s, restore) {
			if tools[name] != "" {
				continue
			}
			var problems []string
			for _, d := range toolDirs {
				p := d + "/" + name
				if _, err := h.FS.Lstat(p); errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err := h.trustedTool(p); err != nil {
					problems = append(problems, err.Error())
					continue
				}
				tools[name] = p
				break
			}
			switch {
			case tools[name] != "":
			case len(problems) > 0:
				return nil, failf(CodeRefused, "%s cannot be trusted (%s); install it so that only root can change it", name, strings.Join(problems, "; "))
			default:
				return nil, failf(CodeRefused, "%s is not installed (looked in %s)", name, strings.Join(toolDirs, ", "))
			}
		}
	}
	return tools, nil
}

// quiesce stops the service's unit for an operation that needs its data
// still. It always stops a unit the helper wrote, whatever its state: a unit
// that is activating or waiting to restart after a failure (Restart=) is not
// "active", yet it would start a process again. running reports whether the
// unit was active, activating or reloading, so that the caller starts it
// again afterwards. The unit must be inactive or failed when it returns;
// otherwise the unit is started again if it was running and the operation is
// refused.
func (h *Helper) quiesce(ctx context.Context, svc string) (running bool, e *opErr) {
	conflict, err := hostsetup.UnitConflict(h.FS, svc, h.RootUID)
	switch {
	case err != nil:
		return false, failf(CodeFailed, "%v", err)
	case conflict != "":
		return false, failf(CodeRefused, "%s", conflict)
	}
	if _, err := h.FS.Lstat(hostsetup.ServiceUnit(svc)); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, failf(CodeFailed, "%v", err)
	}
	running = slices.Contains([]string{"active", "activating", "reloading"}, h.activeState(ctx, svc))
	if e := h.unitCtl(ctx, "stop", svc); e != nil {
		return running, e
	}
	if state := h.activeState(ctx, svc); state != "inactive" && state != "failed" {
		if running {
			_ = h.unitCtl(context.WithoutCancel(ctx), "start", svc)
		}
		return running, failf(CodeRefused, "%s is %q after being stopped, so its data cannot be used; nothing was changed", unitName(svc), hostsetup.Printable(state, 32))
	}
	return running, nil
}

// activeState is the unit's state as "systemctl is-active" prints it.
func (h *Helper) activeState(ctx context.Context, svc string) string {
	res, _ := h.run(ctx, systemctlTimeout, systemctl, "is-active", unitName(svc))
	return strings.TrimSpace(res.Output)
}

func (h *Helper) unitCtl(ctx context.Context, verb, svc string) *opErr {
	if res, err := h.run(ctx, systemctlTimeout, systemctl, verb, unitName(svc)); err != nil {
		return failf(CodeFailed, "%s", commandFailure("systemctl "+verb, err)).withLog(res.Output)
	}
	return nil
}

// backupLimits are the limits of the files a worker writes.
func (h *Helper) backupLimits() Limits {
	lim := h.Limits.withDefaults()
	lim.MaxBytes = lim.BackupBytes
	return lim
}

// setParams describe one backup set to make.
type setParams struct {
	backupEnv
	opID         string
	tag          string
	kind         string
	mode         string
	sources      []BackupSource
	timeoutSec   int
	rtBin        string
	restoredFrom string
	allowMissing bool
}

// backup makes a backup set of the service's sources.
func (h *Helper) backup(ctx context.Context, req Request, cfg hostsetup.Config, g hostsetup.ServiceGrant) Response {
	resp := reply(req)
	env, e := h.backupContext(ctx, req.Service, cfg, g, true)
	if e != nil {
		return respond(resp, e)
	}
	notes, e := h.sweep(env)
	if e != nil {
		return respond(resp, e)
	}
	resp.Log = notes
	p := setParams{backupEnv: env, opID: req.OperationID, tag: "b", kind: KindBackup, mode: req.Mode, sources: req.Sources, timeoutSec: req.TimeoutSec}
	if p.mode == "" {
		p.mode = ModeOnline
	}
	if req.Runtime != "" {
		if !slices.Contains(g.Runtimes, req.Runtime) {
			return refuse(resp, CodeRefused, "the runtime %s is not granted to %s in %s", req.Runtime, req.Service, hostsetup.ConfigPath)
		}
		rt := hostsetup.RuntimesDir + "/" + req.Runtime
		if _, err := h.trustedPath(rt, false); err != nil {
			return refuse(resp, CodeRefused, "the runtime %s is not installed (%v); run sudo bin/lcm host apply", req.Runtime, err)
		}
		p.rtBin = rt + "/bin"
	}
	if _, e := h.resolveTools(p.sources, false); e != nil {
		return respond(resp, e)
	}
	stopped := false
	if p.mode == ModeStopped {
		var e *opErr
		if stopped, e = h.quiesce(ctx, req.Service); e != nil {
			return respond(resp, e)
		}
	}
	m, sum, out, e := h.makeSet(ctx, p)
	var startErr *opErr
	if stopped {
		startErr = h.unitCtl(context.WithoutCancel(ctx), "start", req.Service)
	}
	if e != nil {
		if startErr != nil {
			e.msg += "; starting the service again failed too: " + startErr.msg
		}
		return respond(resp, e)
	}
	resp.Log = notes + out
	resp.Backup = &BackupResult{BackupID: m.BackupID, ManifestSHA256: sum, Size: m.Size, Files: len(m.Files),
		Skipped: m.Skipped, CreatedAt: m.CreatedAt, ReleaseID: m.ReleaseID}
	if startErr != nil {
		return refuse(resp, CodeFailed, "backup %s was made, but starting the service again failed: %s", m.BackupID, startErr.msg)
	}
	return resp
}

// workerArgs is the systemd-run command line of a worker. The worker runs as
// the service's account in a unit like the service's: a read-only file
// system except its staging output and the service's data directory, no LCM
// data or build trees, no ports, bounded time, memory, processes and file
// size. A backup does not write the data directory (its SQLite connection
// is read-only), but a read-only connection to a WAL database that was
// closed cleanly must create the -shm file next to it, which a read-only
// directory would refuse. The directory belongs to the worker's own account,
// so this grants nothing the service does not have. The backup directory is replaced by an empty
// read-only file system with only this operation's staging directories
// bound back.
func (h *Helper) workerArgs(unit string, p setParams, stage string, timeoutSec int, lim Limits) []string {
	args := []string{
		systemdRun, "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
		"--unit=" + unit,
		"--uid=" + strconv.Itoa(p.acc.UID), "--gid=" + strconv.Itoa(p.acc.GID),
		"--working-directory=/",
		"--setenv=LANG=C.UTF-8", "--setenv=HOME=/nonexistent",
	}
	path := systemPath
	if p.rtBin != "" {
		path = p.rtBin + ":" + systemPath
	}
	args = append(args, "--setenv=PATH="+path)
	props := sandbox("0077", "")
	props = append(props,
		"SocketBindDeny=any",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX",
		"PrivateIPC=yes",
		"ProcSubset=pid",
		"InaccessiblePaths=-"+hostsetup.BuildDir+" -"+hostsetup.BuildCacheDir,
		"EnvironmentFile="+hostsetup.ServiceEnvFile(p.svc),
		"TemporaryFileSystem="+p.root+":ro",
		"BindReadOnlyPaths="+stage+"/in",
		"BindPaths="+stage+"/out",
		"ReadWritePaths="+stage+"/out",
		"ReadWritePaths="+hostsetup.ServiceDataDir(p.svc),
		"RuntimeMaxSec="+strconv.Itoa(timeoutSec),
		"MemoryMax="+workerMemory,
		"TasksMax=512",
		"LimitFSIZE="+strconv.FormatInt(lim.MaxBytes, 10),
		"LimitCORE=0",
	)
	for _, prop := range props {
		args = append(args, "--property="+prop)
	}
	return append(args, "--", hostsetup.DeployHelper, "backup-worker", stage+"/in", stage+"/out")
}

// newStage makes the staging directories of an operation: in, written by
// root and read-only to the worker, and out, the worker's.
func (h *Helper) newStage(p backupEnv, name string, spec workerSpec) (string, *opErr) {
	stage := p.svcDir + "/" + stagePrefix + name
	if err := h.FS.RemoveAll(stage); err != nil {
		return "", failf(CodeFailed, "%v", err)
	}
	if err := h.FS.Mkdir(stage, 0o700, h.RootUID, h.RootGID); err != nil {
		return "", failf(CodeFailed, "%v", err)
	}
	fail := func(err error) (string, *opErr) {
		_ = h.FS.RemoveAll(stage)
		return "", failf(CodeFailed, "prepare the staging directory: %v", err)
	}
	if err := h.FS.Mkdir(stage+"/in", 0o755, h.RootUID, h.RootGID); err != nil {
		return fail(err)
	}
	if err := h.FS.Mkdir(stage+"/out", 0o700, p.acc.UID, p.acc.GID); err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(spec)
	if err == nil {
		err = h.FS.WriteFile(stage+"/in/"+specName, bytes.NewReader(raw), 0o644, h.RootUID, h.RootGID)
	}
	if err != nil {
		return fail(err)
	}
	return stage, nil
}

// runWorker runs a worker and watches the size of its output.
func (h *Helper) runWorker(ctx context.Context, unit, stage string, p setParams, restore bool, timeoutSec int) (string, *opErr) {
	lim := h.backupLimits()
	argv := h.workerArgs(unit, p, stage, timeoutSec, lim)
	watch := lim
	watch.MaxEntries = stageEntries
	var stop func() string
	if !restore {
		stop = h.watchBuild(ctx, unit, []string{stage + "/out"}, []string{p.svcDir}, watch)
	}
	res, err := h.run(ctx, time.Duration(timeoutSec)*time.Second+buildRunMargin, argv...)
	out := sanitizeLog(res.Output)
	if stop != nil {
		if why := stop(); why != "" {
			return out, failf(CodeFailed, "the backup was stopped: %s", why).withLog(res.Output)
		}
	}
	if err != nil {
		h.stopUnit(ctx, unit, err)
		return out, failf(CodeFailed, "%s", commandFailure("the backup worker", err)).withLog(res.Output)
	}
	return out, nil
}

// makeSet produces a backup set: it runs the worker over the sources,
// copies what it produced into a new root-owned directory while hashing
// it, writes the manifest and renames the directory into place.
func (h *Helper) makeSet(ctx context.Context, p setParams) (setManifest, string, string, *opErr) {
	var zero setManifest
	lim := h.backupLimits()
	if err := h.checkFree(p.root, lim.MinFreeBytes, lim.MinFreeInodes); err != nil {
		return zero, "", "", failf(CodeRefused, "not enough room for a backup: %v", err)
	}
	id := h.newID()
	if p.kind == KindPreRestore {
		id = preRestorePrefix + id
	}
	name := p.opID[:8] + p.tag
	spec := workerSpec{Version: Version, Mode: "backup", Service: p.svc, MaxBytes: lim.MaxBytes, MaxEntries: lim.MaxEntries, AllowMissing: p.allowMissing}
	for _, s := range p.sources {
		spec.Sources = append(spec.Sources, specSource{BackupSource: s})
	}
	var e *opErr
	if spec.Tools, e = h.resolveTools(p.sources, false); e != nil {
		return zero, "", "", e
	}
	stage, e := h.newStage(p.backupEnv, name, spec)
	if e != nil {
		return zero, "", "", e
	}
	tmp := p.svcDir + "/" + tmpPrefix + name
	defer func() {
		_ = h.discard(stage)
		_ = h.FS.RemoveAll(tmp)
	}()
	if err := h.FS.RemoveAll(tmp); err != nil {
		return zero, "", "", failf(CodeFailed, "%v", err)
	}
	out, e := h.runWorker(ctx, "lcm-backup-"+p.svc+"-"+name, stage, p, false, p.timeoutSec)
	if e != nil {
		return zero, "", out, e
	}
	m, e := h.collect(p, id, stage+"/out", tmp)
	if e != nil {
		return zero, "", out, e
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		raw = append(raw, '\n')
		err = h.FS.WriteFile(tmp+"/"+manifestName, bytes.NewReader(raw), 0o600, h.RootUID, h.RootGID)
	}
	if err == nil {
		err = h.FS.Rename(tmp, p.svcDir+"/"+id)
	}
	if err != nil {
		return zero, "", out, failf(CodeFailed, "record the backup: %v", err)
	}
	sum := sha256.Sum256(raw)
	return m, hex.EncodeToString(sum[:]), out, nil
}

// secureLimits bound the walk that takes the staging directories over.
func (h *Helper) secureLimits() hostsetup.MeasureLimits {
	lim := h.backupLimits()
	deadline := h.Clock.Now().Add(lim.MeasureTimeout)
	return hostsetup.MeasureLimits{MaxDepth: lim.MaxDepth, MaxEntries: stageEntries,
		Expired: func() bool { return !h.Clock.Now().Before(deadline) }}
}

// discard removes a staging directory after its worker ran. The service
// account may keep descriptors of directories in it, through which it could
// add entries while root removes the tree, so the directories are taken over
// first (best effort, bounded). A tree that still cannot be removed is
// renamed to .del-stuck-* so that it blocks nothing; a leftover already named
// so is reported as an error and left.
func (h *Helper) discard(p string) error {
	info, err := h.FS.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && info.IsDir() && !info.IsLink() {
		_ = hostsetup.SecureTree(h.FS, p, info, h.RootUID, h.RootGID, h.secureLimits())
	}
	rmErr := h.FS.RemoveAll(p)
	if rmErr == nil || strings.HasPrefix(path.Base(p), delPrefix) {
		return rmErr
	}
	aside := path.Dir(p) + "/" + delPrefix + "stuck-" + h.newID()
	if err := h.FS.Rename(p, aside); err != nil {
		return errors.Join(rmErr, err)
	}
	return nil
}

// collect copies the artifacts the worker left in out, which the service
// account controls, into the new directory tmp. It reads out through a tree
// confined to it, takes exactly the expected names, and only regular files
// that the service account owns.
func (h *Helper) collect(p setParams, id, out, tmp string) (setManifest, *opErr) {
	var zero setManifest
	m := setManifest{Version: Version, Service: p.svc, BackupID: id, Kind: p.kind, Mode: p.mode,
		CreatedAt: h.Clock.Now().UnixMilli(), RestoredFrom: p.restoredFrom}
	m.ReleaseID, m.Commit = h.currentRelease(p.svc)
	before, err := h.FS.Lstat(out)
	switch {
	case err != nil:
		return zero, failf(CodeFailed, "%v", err)
	case !before.IsDir() || before.IsLink() || before.UID != p.acc.UID:
		return zero, failf(CodeFailed, "the output directory is not the service account's directory")
	}
	// The worker is gone, but the service account may still hold a
	// descriptor of out (its own, or one a child passed over a socket, or
	// reached through /proc) and write to it later. Take the directory away
	// from the account, so that nothing can be added after the next
	// measurement, and measure again.
	// Every directory the account made in it is taken too: one it keeps
	// open stays writable to it and would keep root from removing the
	// staging directory.
	if err := hostsetup.SecureTree(h.FS, out, before, h.RootUID, h.RootGID, h.secureLimits()); err != nil {
		return zero, failf(CodeFailed, "secure the output directory: %v", err)
	}
	lim := h.backupLimits()
	watch := lim
	watch.MaxEntries = stageEntries
	if u := h.usage(watch, out); u.stopped != "" || u.bytes > lim.MaxBytes || u.entries > stageEntries {
		return zero, failf(CodeFailed, "the backup output exceeds its limits (%s)", cmp.Or(u.stopped, fmt.Sprintf("%d bytes in %d entries", u.bytes, u.entries)))
	}
	if before, err = h.FS.Lstat(out); err != nil {
		return zero, failf(CodeFailed, "%v", err)
	}
	t, err := h.FS.OpenTree(out)
	if err != nil {
		return zero, failf(CodeFailed, "%v", err)
	}
	defer func() { _ = t.Close() }()
	if after, err := t.Root(); err != nil || after.Dev != before.Dev || after.Ino != before.Ino || before.IsLink() {
		return zero, failf(CodeFailed, "the output directory changed while it was opened")
	}
	var res workerResult
	if raw, err := readTreeFile(t, resultName, maxResult); err != nil {
		return zero, failf(CodeFailed, "the worker left no result: %v", err)
	} else if err := decodeStrict(raw, maxResult, &res); err != nil || len(res.Sources) != len(p.sources) {
		return zero, failf(CodeFailed, "the worker's result is not valid")
	}
	for i, s := range p.sources {
		if res.Sources[i].Absent && (!p.allowMissing || (s.Type != SourceFiles && s.Type != SourceSQLite)) {
			return zero, failf(CodeFailed, "the worker reports %s as absent", describeSource(s))
		}
		if res.Sources[i].Mode != 0 && (s.Type != SourceSQLite || res.Sources[i].Absent || res.Sources[i].Mode&^0o777 != 0) {
			return zero, failf(CodeFailed, "the worker's result is not valid")
		}
		if res.Sources[i].Skipped < 0 || (s.Type != SourceFiles && res.Sources[i].Skipped != 0) {
			return zero, failf(CodeFailed, "the worker's result is not valid")
		}
	}
	if e := expectOutput(t, p.sources, res); e != nil {
		return zero, e
	}
	// The copy below is root's own write to the backup file system: make sure
	// it fits with the room a running build or backup is allowed to leave.
	var total, files uint64
	for i, s := range p.sources {
		if res.Sources[i].Absent {
			continue
		}
		for _, name := range artifactNames(i, s) {
			if info, err := t.Lstat(name); err == nil && info.Size > 0 {
				total += uint64(info.Size)
			}
			files++
		}
	}
	if err := h.checkFree(p.svcDir, lim.StopFreeBytes+total, lim.StopFreeInodes+files); err != nil {
		return zero, failf(CodeRefused, "not enough room to store the backup: %v", err)
	}
	if err := h.FS.Mkdir(tmp, 0o700, h.RootUID, h.RootGID); err != nil {
		return zero, failf(CodeFailed, "%v", err)
	}
	for i, s := range p.sources {
		ms := manifestSource{BackupSource: s, Artifacts: []string{}, Absent: res.Sources[i].Absent, Mode: res.Sources[i].Mode}
		m.Skipped += res.Sources[i].Skipped
		if !ms.Absent {
			for _, name := range artifactNames(i, s) {
				f, e := h.copyArtifact(t, p.acc.UID, name, tmp+"/"+name, lim.MaxBytes-m.Size)
				if e != nil {
					return zero, e
				}
				m.Files = append(m.Files, f)
				m.Size += f.Size
				ms.Artifacts = append(ms.Artifacts, name)
			}
		}
		m.Sources = append(m.Sources, ms)
	}
	if m.Files == nil {
		m.Files = []manifestFile{}
	}
	return m, nil
}

// expectOutput refuses an output directory that holds anything but the
// result and the artifacts the sources call for, or anything but regular
// files: the account that wrote it cannot add a subdirectory or a leftover
// that root would later walk.
func expectOutput(t hostsetup.Tree, sources []BackupSource, res workerResult) *opErr {
	want := map[string]bool{resultName: true}
	for i, s := range sources {
		if !res.Sources[i].Absent {
			for _, name := range artifactNames(i, s) {
				want[name] = true
			}
		}
	}
	names, _, err := t.ReadDir(".")
	if err != nil {
		return failf(CodeFailed, "%v", err)
	}
	for _, n := range names {
		if !want[n] {
			return failf(CodeFailed, "the worker left an unexpected entry %q", hostsetup.Printable(n, 64))
		}
		if info, err := t.Lstat(n); err != nil || !info.IsRegular() {
			return failf(CodeFailed, "%s is not a regular file", hostsetup.Printable(n, 64))
		}
	}
	return nil
}

func readTreeFile(t hostsetup.Tree, name string, max int64) ([]byte, error) {
	f, info, err := t.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if !info.IsRegular() || info.Size > max {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", name, max)
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// countingHash hashes what is written and counts it.
type countingHash struct {
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func (c *countingHash) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.h.Write(p)
}

// copyArtifact copies one regular file of the staging output, owned by
// owner, to dst as a root-owned 0600 file and returns its manifest entry.
func (h *Helper) copyArtifact(t hostsetup.Tree, owner int, name, dst string, room int64) (manifestFile, *opErr) {
	li, err := t.Lstat(name)
	if err != nil {
		return manifestFile{}, failf(CodeFailed, "the worker did not produce %s: %v", name, err)
	}
	if !li.IsRegular() {
		return manifestFile{}, failf(CodeFailed, "%s is not a regular file", name)
	}
	f, info, err := t.Open(name)
	if err != nil {
		return manifestFile{}, failf(CodeFailed, "open %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	switch {
	case !li.IsRegular() || !info.IsRegular() || li.Dev != info.Dev || li.Ino != info.Ino:
		return manifestFile{}, failf(CodeFailed, "%s is not a regular file", name)
	case info.UID != owner:
		return manifestFile{}, failf(CodeFailed, "%s is not owned by the service account", name)
	case info.Size > room:
		return manifestFile{}, failf(CodeFailed, "the backup exceeds its size limit")
	}
	sum := &countingHash{h: sha256.New()}
	if err := h.FS.WriteFile(dst, io.TeeReader(io.LimitReader(f, info.Size), sum), 0o600, h.RootUID, h.RootGID); err != nil {
		return manifestFile{}, failf(CodeFailed, "copy %s: %v", name, err)
	}
	if sum.n != info.Size {
		return manifestFile{}, failf(CodeFailed, "%s changed while it was copied", name)
	}
	return manifestFile{Name: name, Size: info.Size, SHA256: hex.EncodeToString(sum.h.Sum(nil))}, nil
}

// readSet reads and checks the manifest of a set: the set directory and
// the manifest must be root's alone, the manifest strictly decodes and
// matches the set. It returns the manifest and the SHA-256 of its bytes.
func (h *Helper) readSet(svcDir, svc, id string) (setManifest, string, *opErr) {
	var m setManifest
	dir := svcDir + "/" + id
	if _, err := h.FS.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return m, "", failf(CodeNotFound, "there is no backup %s of %s", id, svc)
	}
	info, err := h.trustedPath(dir, false)
	switch {
	case err != nil:
		return m, "", failf(CodeFailed, "backup %s: %v", id, err)
	case info.Mode.Perm()&0o077 != 0:
		return m, "", failf(CodeFailed, "backup %s is not accessible by root only", id)
	}
	mp := dir + "/" + manifestName
	if _, err := h.trustedPath(mp, true); err != nil {
		return m, "", failf(CodeFailed, "backup %s: %v", id, err)
	}
	f, err := h.FS.Open(mp)
	if err != nil {
		return m, "", failf(CodeFailed, "backup %s: %v", id, err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return m, "", failf(CodeFailed, "backup %s: %v", id, err)
	}
	if err := decodeStrict(raw, maxManifest, &m); err != nil {
		return m, "", failf(CodeFailed, "backup %s: the manifest is not valid: %v", id, err)
	}
	if err := m.validate(svc, id); err != nil {
		return m, "", failf(CodeFailed, "backup %s: %v", id, err)
	}
	sum := sha256.Sum256(raw)
	return m, hex.EncodeToString(sum[:]), nil
}

// info converts a manifest to its listing entry.
func (m setManifest) info(sum string) BackupInfo {
	return BackupInfo{BackupID: m.BackupID, Kind: m.Kind, CreatedAt: m.CreatedAt, Size: m.Size, ManifestSHA256: sum, Mode: m.Mode,
		ReleaseID: m.ReleaseID, Sources: m.plainSources(), Skipped: m.Skipped, RestoredFrom: m.RestoredFrom}
}

// listSets reads the sets of a service, newest first.
func (h *Helper) listSets(env backupEnv) ([]BackupInfo, *opErr) {
	names, err := h.FS.ReadDir(env.svcDir)
	if err != nil {
		return nil, failf(CodeFailed, "%v", err)
	}
	var out []BackupInfo
	for _, n := range names {
		if !backupPattern.MatchString(n) {
			continue
		}
		kind := KindBackup
		if strings.HasPrefix(n, preRestorePrefix) {
			kind = KindPreRestore
		}
		m, sum, e := h.readSet(env.svcDir, env.svc, n)
		if e != nil {
			out = append(out, BackupInfo{BackupID: n, Kind: kind, CreatedAt: idTime(n).UnixMilli(), Problem: hostsetup.Printable(e.msg, 300)})
			continue
		}
		out = append(out, m.info(sum))
	}
	slices.SortStableFunc(out, func(a, b BackupInfo) int {
		if c := b.CreatedAt - a.CreatedAt; c != 0 {
			if c < 0 {
				return -1
			}
			return 1
		}
		return strings.Compare(b.BackupID, a.BackupID)
	})
	return out, nil
}

func (h *Helper) backups(req Request, cfg hostsetup.Config, g hostsetup.ServiceGrant) Response {
	resp := reply(req)
	env, e := h.backupContext(context.Background(), req.Service, cfg, g, false)
	if e != nil {
		return respond(resp, e)
	}
	list, e := h.listSets(env)
	if e != nil {
		return respond(resp, e)
	}
	if len(list) > maxListed {
		list, resp.BackupsTruncated = list[:maxListed], true
	}
	resp.Backups = list
	return resp
}

// sweep removes the staging leftovers of an operation that was interrupted.
// Every changing operation calls it first: it holds the service's lock, so no
// backup or restore of the service is running and the leftovers are dead. A
// leftover that cannot be removed is skipped, not an error: it must not block
// the service's operations. The returned note names what was skipped.
func (h *Helper) sweep(env backupEnv) (string, *opErr) {
	names, err := h.FS.ReadDir(env.svcDir)
	if err != nil {
		return "", failf(CodeFailed, "%v", err)
	}
	var note string
	for _, n := range names {
		if strings.HasPrefix(n, stagePrefix) || strings.HasPrefix(n, tmpPrefix) || strings.HasPrefix(n, delPrefix) {
			if err := h.discard(env.svcDir + "/" + n); err != nil {
				note += fmt.Sprintf("left %s in place, as it cannot be removed: %s\n", hostsetup.Printable(n, 64), hostsetup.Printable(err.Error(), 200))
			}
		}
	}
	return note, nil
}

// prune removes what the retention rules allow.
func (h *Helper) prune(_ context.Context, req Request, cfg hostsetup.Config, g hostsetup.ServiceGrant) Response {
	resp := reply(req)
	env, e := h.backupContext(context.Background(), req.Service, cfg, g, false)
	if e != nil {
		return respond(resp, e)
	}
	notes, e := h.sweep(env)
	if e != nil {
		return respond(resp, e)
	}
	resp.Log = notes
	list, e := h.listSets(env)
	if e != nil {
		return respond(resp, e)
	}
	doomed := retention(list, *req.Keep, h.Clock.Now())
	removed := []string{}
	for _, id := range doomed {
		gone := env.svcDir + "/" + delPrefix + id
		if err := h.FS.Rename(env.svcDir+"/"+id, gone); err != nil {
			return refuse(resp, CodeFailed, "remove %s: %v (removed so far: %s)", id, err, strings.Join(removed, ", "))
		}
		if err := h.FS.RemoveAll(gone); err != nil {
			return refuse(resp, CodeFailed, "remove %s: %v", id, err)
		}
		removed = append(removed, id)
	}
	resp.Prune = &PruneResult{Removed: removed, Kept: len(list) - len(removed)}
	return resp
}

// retention returns the IDs of the sets that may be removed. Sets whose
// manifest cannot be read are never removed, nor is the newest set.
func retention(list []BackupInfo, keep Keep, now time.Time) []string {
	keepSet := map[string]bool{}
	var regular []BackupInfo
	for i, s := range list {
		if i == 0 || s.Problem != "" {
			keepSet[s.BackupID] = true
		}
		switch {
		case s.Problem != "":
		case s.Kind == KindPreRestore:
			if now.Sub(time.UnixMilli(s.CreatedAt)) < preRestoreMinAge {
				keepSet[s.BackupID] = true
			}
		default:
			regular = append(regular, s)
		}
	}
	days, weeks := map[string]bool{}, map[string]bool{}
	for _, s := range regular { // newest first
		t := time.UnixMilli(s.CreatedAt).UTC()
		day := t.Format("2006-01-02")
		y, w := t.ISOWeek()
		week := fmt.Sprintf("%d-W%02d", y, w)
		if !days[day] && len(days) < keep.Daily {
			days[day] = true
			keepSet[s.BackupID] = true
		}
		if !weeks[week] && len(weeks) < keep.Weekly {
			weeks[week] = true
			keepSet[s.BackupID] = true
		}
	}
	var doomed []string
	for _, s := range list {
		if !keepSet[s.BackupID] {
			doomed = append(doomed, s.BackupID)
		}
	}
	return doomed
}

// ---- restore ----

// restorable lists the indexes of the sources of a set that a restore
// puts back: everything but command artifacts, which have no restore.
func restorable(m setManifest) []int {
	var idx []int
	for i, s := range m.Sources {
		if s.Type != SourceCommand {
			idx = append(idx, i)
		}
	}
	return idx
}

// stageSet verifies a set against its manifest and copies its restorable
// artifacts into a new staging directory the service account can read. The
// users and roles of a MongoDB source are left out unless restoreUsers. The
// hashes are computed on the bytes copied, so what is staged is what was
// verified.
func (h *Helper) stageSet(env backupEnv, name string, m setManifest, restoreUsers bool, maxEntries int, maxBytes int64) (string, *opErr) {
	spec := workerSpec{Version: Version, Mode: "restore", Service: env.svc, MaxBytes: maxBytes, MaxEntries: maxEntries}
	files := map[string]manifestFile{}
	for _, f := range m.Files {
		files[f.Name] = f
	}
	for _, i := range restorable(m) {
		s := m.Sources[i]
		ss := specSource{BackupSource: s.BackupSource, Absent: s.Absent, Mode: s.Mode}
		arts := s.Artifacts
		if s.Type == SourceMongoDB && s.Users && !restoreUsers {
			// The users and roles are the whole instance's: only the database
			// is put back unless the request asks for more.
			arts, ss.Users = arts[:1], false
		}
		for _, a := range arts {
			ss.Artifacts = append(ss.Artifacts, artifactRef{Name: a, Size: files[a].Size, SHA256: files[a].SHA256})
		}
		spec.Sources = append(spec.Sources, ss)
	}
	var e *opErr
	if spec.Tools, e = h.resolveTools(m.restorableSources(), true); e != nil {
		return "", e
	}
	stage, e := h.newStage(env, name, spec)
	if e != nil {
		return "", e
	}
	setDir := env.svcDir + "/" + m.BackupID
	for _, s := range spec.Sources {
		for _, a := range s.Artifacts {
			if e := h.stageArtifact(env, setDir+"/"+a.Name, stage+"/in/"+a.Name, a); e != nil {
				_ = h.FS.RemoveAll(stage)
				return "", e
			}
		}
	}
	return stage, nil
}

func (h *Helper) stageArtifact(env backupEnv, src, dst string, a artifactRef) *opErr {
	info, err := h.trustedPath(src, true)
	if err != nil {
		return failf(CodeFailed, "backup artifact %s: %v", a.Name, err)
	}
	if info.Size != a.Size {
		return failf(CodeFailed, "backup artifact %s does not match its manifest (size %d, expected %d)", a.Name, info.Size, a.Size)
	}
	f, err := h.FS.Open(src)
	if err != nil {
		return failf(CodeFailed, "backup artifact %s: %v", a.Name, err)
	}
	defer func() { _ = f.Close() }()
	sum := &countingHash{h: sha256.New()}
	if err := h.FS.WriteFile(dst, io.TeeReader(io.LimitReader(f, a.Size), sum), 0o400, env.acc.UID, env.acc.GID); err != nil {
		return failf(CodeFailed, "stage %s: %v", a.Name, err)
	}
	if sum.n != a.Size || hex.EncodeToString(sum.h.Sum(nil)) != a.SHA256 {
		return failf(CodeFailed, "backup artifact %s does not match its manifest: it is corrupt or was modified", a.Name)
	}
	return nil
}

// applyStage runs the restore worker over a staged set.
func (h *Helper) applyStage(ctx context.Context, env backupEnv, name, stage string) *opErr {
	p := setParams{backupEnv: env}
	_, e := h.runWorker(ctx, "lcm-restore-"+env.svc+"-"+name, stage, p, true, restoreWorkerSec)
	return e
}

func describeAll(m setManifest, idx []int) []string {
	out := []string{}
	for _, i := range idx {
		out = append(out, describeSource(m.Sources[i].BackupSource))
	}
	return out
}

// currentSize estimates the bytes the pre-restore copy of the data a restore
// replaces will take: the files and databases are measured, and the
// database dumps are assumed to be about as large as the set's.
func (h *Helper) currentSize(env backupEnv, m setManifest, idx []int, lim Limits) (uint64, error) {
	var total uint64
	files := map[string]int64{}
	for _, f := range m.Files {
		files[f.Name] = f.Size
	}
	for _, i := range idx {
		s := m.Sources[i]
		switch s.Type {
		case SourceFiles, SourceSQLite:
			names := []string{s.Path}
			if s.Type == SourceSQLite {
				names = append(names, s.Path+"-wal", s.Path+"-shm")
			}
			for _, n := range names {
				size, err := h.pathSize(hostsetup.ServiceDataDir(env.svc), n, lim)
				if err != nil {
					return 0, err
				}
				total += size
			}
		default:
			for _, a := range s.Artifacts {
				total += uint64(files[a]) //nolint:gosec // validated, non-negative sizes
			}
		}
	}
	return total, nil
}

// pathSize is the size of the file or of the files below the directory rel
// of root, with the archive's overhead of a header for each; 0 when it does
// not exist. The data directory belongs to the service, so rel is resolved
// inside root only: a link in any component counts as 0 (the worker would
// not follow it either), and nothing outside root is looked at.
func (h *Helper) pathSize(root, rel string, lim Limits) (uint64, error) {
	t, err := h.FS.OpenTree(root)
	if err != nil {
		return 0, err
	}
	parts := strings.Split(rel, "/")
	var last hostsetup.Info
	var cur string
	for i, part := range parts {
		cur = path.Join(cur, part)
		info, err := t.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			_ = t.Close()
			return 0, nil
		case err != nil:
			_ = t.Close()
			return 0, err
		case info.IsLink() || (i < len(parts)-1 && !info.IsDir()):
			_ = t.Close()
			return 0, nil
		}
		last = info
	}
	_ = t.Close()
	switch {
	case last.IsRegular():
		return uint64(last.Size), nil //nolint:gosec // a file size is not negative
	case !last.IsDir():
		return 0, nil
	}
	deadline := h.Clock.Now().Add(lim.MeasureTimeout)
	u, err := hostsetup.MeasureTreeBelow(h.FS, root, rel, hostsetup.MeasureLimits{
		MaxDepth: lim.MaxDepth, MaxEntries: lim.MaxEntries,
		Expired: func() bool { return !h.Clock.Now().Before(deadline) },
	})
	if err != nil {
		return 0, fmt.Errorf("%s/%s is too large, too deep or unreadable to size: %w", root, rel, err)
	}
	return uint64(u.Bytes) + 1024*uint64(u.Entries), nil //nolint:gosec // measured sizes are not negative
}

// restore puts a backup set back. The order protects the data: the set is
// verified and staged first, the service stopped next, then the pre-restore
// copy is taken, and only then is anything replaced. A failed replacement
// is undone from the pre-restore copy.
func (h *Helper) restore(ctx context.Context, req Request, cfg hostsetup.Config, g hostsetup.ServiceGrant) Response {
	resp := reply(req)
	env, e := h.backupContext(ctx, req.Service, cfg, g, true)
	if e != nil {
		return respond(resp, e)
	}
	notes, e := h.sweep(env)
	if e != nil {
		return respond(resp, e)
	}
	resp.Log = notes
	m, sum, e := h.readSet(env.svcDir, env.svc, req.BackupID)
	if e != nil {
		return respond(resp, e)
	}
	if req.ManifestSHA256 != "" && req.ManifestSHA256 != sum {
		return refuse(resp, CodeRefused, "the manifest of %s does not match the checksum LCM recorded for it", req.BackupID)
	}
	idx := restorable(m)
	if len(idx) == 0 {
		return refuse(resp, CodeRefused, "backup %s holds only command output, which LCM does not restore", req.BackupID)
	}
	result := &RestoreResult{BackupID: req.BackupID, Restored: describeAll(m, idx), Skipped: []string{}}
	for i, s := range m.Sources {
		switch {
		case !slices.Contains(idx, i):
			result.Skipped = append(result.Skipped, describeSource(s.BackupSource))
		case s.Type == SourceMongoDB && s.Users && !req.RestoreUsers:
			result.Skipped = append(result.Skipped, "users and roles of "+describeSource(s.BackupSource)+" (restoreUsers is not set)")
		}
	}
	lim := h.backupLimits()
	need := uint64(m.Size) //nolint:gosec // a validated, non-negative size
	current, err := h.currentSize(env, m, idx, lim)
	if err != nil {
		return refuse(resp, CodeRefused, "the current data cannot be sized for the restore: %v", err)
	}
	// The backup file system holds the staged set, the copy of the current
	// data and, if the restore fails, the staged copy again for the undo.
	if err := h.checkFree(env.svcDir, lim.MinFreeBytes+need+2*current, lim.MinFreeInodes); err != nil {
		return refuse(resp, CodeRefused, "not enough room for the restore: %v", err)
	}
	if err := h.checkFree(hostsetup.ServiceDataDir(env.svc), lim.MinFreeBytes+need, lim.MinFreeInodes); err != nil {
		return refuse(resp, CodeRefused, "not enough room for the restore: %v", err)
	}
	opName := req.OperationID[:8]
	stage, e := h.stageSet(env, opName+"r", m, req.RestoreUsers, lim.MaxEntries, lim.MaxBytes)
	if e != nil {
		return respond(resp, e)
	}
	defer func() { _ = h.discard(stage) }()

	// The tools of the restore were checked by staging the set; the copy of
	// the current data needs its own, which must be there before the service
	// is stopped.
	if _, e := h.resolveTools(m.restorableSources(), false); e != nil {
		return respond(resp, e)
	}
	active, e := h.quiesce(ctx, env.svc)
	if e != nil {
		return respond(resp, e)
	}
	startAgain := func() *opErr {
		if !active {
			return nil
		}
		return h.unitCtl(context.WithoutCancel(ctx), "start", env.svc)
	}

	pre := setParams{backupEnv: env, opID: req.OperationID, tag: "p", kind: KindPreRestore, mode: ModeStopped,
		timeoutSec: restoreWorkerSec, restoredFrom: req.BackupID, allowMissing: true}
	for _, i := range idx {
		src := m.Sources[i].BackupSource
		if src.Type == SourceMongoDB && !req.RestoreUsers {
			// Nothing of the instance's accounts is replaced, so none is copied.
			src.Users = false
		}
		pre.sources = append(pre.sources, src)
	}
	pm, _, _, e := h.makeSet(ctx, pre)
	if e != nil {
		e.msg = "the copy of the current data could not be made, so nothing was restored: " + e.msg
		if serr := startAgain(); serr != nil {
			e.msg += "; starting the service again failed: " + serr.msg
		}
		return respond(resp, e)
	}
	result.PreRestoreID = pm.BackupID
	resp.Restore = result
	// Staging the copy again for an undo needs room too; find out before
	// anything is replaced.
	if err := h.checkFree(env.svcDir, lim.StopFreeBytes+uint64(pm.Size), lim.StopFreeInodes+uint64(len(pm.Files))); err != nil { //nolint:gosec // validated, non-negative sizes
		msg := "nothing was restored, as there would be no room to undo it: " + err.Error()
		if serr := startAgain(); serr != nil {
			msg += "; starting the service again failed: " + serr.msg
		}
		return refuse(resp, CodeRefused, "%s", msg)
	}

	if e := h.applyStage(ctx, env, opName+"r", stage); e != nil {
		msg := "the restore failed: " + e.msg
		if uerr := h.undoRestore(context.WithoutCancel(ctx), env, opName, pm, req.RestoreUsers, lim); uerr != nil {
			// The data is neither the old nor the new: starting the service on
			// it could corrupt it further or serve garbage.
			result.LeftStopped = true
			msg += "; putting the previous data back from " + pm.BackupID + " failed too: " + uerr.msg +
				"; the service was left stopped because its data may be incomplete, so repair it from " + pm.BackupID + " before starting it"
		} else {
			msg += "; the previous data was put back"
			if serr := startAgain(); serr != nil {
				msg += "; starting the service again failed: " + serr.msg
			}
		}
		resp.Log += e.log
		return refuse(resp, CodeFailed, "%s", msg)
	}
	if serr := startAgain(); serr != nil {
		return refuse(resp, CodeFailed, "backup %s was restored (the previous data is in %s), but starting the service failed: %s", req.BackupID, pm.BackupID, serr.msg)
	}
	result.Started = active
	return resp
}

// undoRestore applies the pre-restore set after a failed restore.
func (h *Helper) undoRestore(ctx context.Context, env backupEnv, opName string, pm setManifest, restoreUsers bool, lim Limits) *opErr {
	stage, e := h.stageSet(env, opName+"u", pm, restoreUsers, lim.MaxEntries, lim.MaxBytes)
	if e != nil {
		return e
	}
	defer func() { _ = h.discard(stage) }()
	return h.applyStage(ctx, env, opName+"u", stage)
}
