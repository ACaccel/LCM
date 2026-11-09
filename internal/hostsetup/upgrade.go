package hostsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// "lcm host upgrade" installs a build whose storage schema differs from the
// installed one, which self-install refuses because migrations are
// forward-only. It follows docs/operations.md#upgrade: stop LCM, take a
// backup set, install the build, validate the migration with automation
// off, restart normally, and on any failure after the stop put the database
// and the old binary back before the old binary runs again.
//
// Root files are written only through the primitives of apply (FS.WriteFile,
// descriptor-checked Chown/Chmod, Tree copies); the build installed is the
// staged copy of the running executable, checked as nobody.

// Upgrade paths and bounds.
const (
	// BackupRoot holds one "upgrade-<UTC stamp>" backup set per upgrade.
	BackupRoot = "/var/backups/lcm"
	// UpgradeDropIn is the unit drop-in that switches automation off for
	// the validation start. It lives in /run, so a reboot removes it even
	// if the command is killed.
	UpgradeDropInDir = "/run/systemd/system/lcm.service.d"
	UpgradeDropIn    = UpgradeDropInDir + "/zz-lcm-upgrade.conf"

	selfInstallPending = "/run/lcm-self-install.pending"
	selfInstallLock    = "/run/lcm-self-install.lock"
	// selfInstallVerify is how long self-install treats an update as still
	// being verified.
	selfInstallVerify = 10 * time.Minute

	upgradeHealthWait = 120 * time.Second
	upgradeStopWait   = 60 * time.Second
	keepUpgradeSets   = 3
	// backupSlack is free space the preflight wants beyond the copies
	// themselves; backupMargin is what must stay free while copying.
	backupSlack  = 16 << 20
	backupMargin = 8 << 20
)

const dropInContent = "# Written by lcm host upgrade for the validation start; removed when it ends.\n[Service]\nEnvironment=LCM_AUTOMATION=off\n"

var upgradeSet = regexp.MustCompile(`^upgrade-[0-9]{8}T[0-9]{6}Z$`)

// UpgradeOptions control Upgrade.
type UpgradeOptions struct {
	// ConfigPath is the host configuration; empty selects ConfigPath.
	ConfigPath string
	// Check reports what would happen without changing anything.
	Check bool
	// AllowOlder permits a build whose commit is older than the installed
	// binary's.
	AllowOlder bool
	// Out receives one line per step result.
	Out io.Writer
	// HealthVersion returns the version LCM's health endpoint reports; nil
	// selects the real endpoint.
	HealthVersion func(ctx context.Context) (string, error)
	// FreeBytes returns the free space of the filesystem holding path; nil
	// selects statfs.
	FreeBytes func(path string) (uint64, error)
	// LockSelfInstall takes the lock that self-install holds while it
	// installs and verifies an update, and returns what releases it; nil
	// selects the real lock file. It fails when self-install holds it.
	LockSelfInstall func() (release func(), err error)
}

// manifestFile is one file (or link) of a backup set.
type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Link   string `json:"link,omitempty"`
	// Restore is set for the files that a rollback puts back, with the
	// owner and mode they had.
	Restore *restoreInfo `json:"restore,omitempty"`
}

type restoreInfo struct {
	Source string `json:"source"`
	Mode   uint32 `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
}

type manifestVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Schema  int    `json:"schema"`
}

type upgradeManifest struct {
	Format    int             `json:"format"`
	CreatedAt string          `json:"createdAt"`
	From      manifestVersion `json:"from"`
	To        manifestVersion `json:"to"`
	Notes     []string        `json:"notes,omitempty"`
	Files     []manifestFile  `json:"files"`
}

type upgrader struct {
	a *applier
	h Host
	o UpgradeOptions

	old, cand             buildVersion
	oldSchema, candSchema int
	secretsDir            string
	staleDropIn           bool
	// Sizes of what the backup set copies, for the space checks.
	dbBytes, etcBytes, secretBytes int64

	set      string
	manifest upgradeManifest

	// What has been done, for the rollback to undo.
	stopRequested bool
	installed     bool
	dropIn        bool
	// unlock releases the self-install lock.
	unlock func()
}

// errNothingToUpgrade ends Upgrade without error when both builds use the
// same storage schema.
var errNothingToUpgrade = errors.New("nothing to upgrade")

// Upgrade installs the running executable as /usr/local/bin/lcm across a
// storage schema change. With Options.Check it only reports the plan.
func Upgrade(ctx context.Context, h Host, o UpgradeOptions) (Result, error) {
	if o.ConfigPath == "" {
		o.ConfigPath = ConfigPath
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	// Names, versions and program output are controlled by lcm or the
	// operator; none of it may reach root's terminal as control sequences.
	o.Out = safeWriter{w: o.Out}
	if o.HealthVersion == nil {
		o.HealthVersion = httpHealthVersion
	}
	if o.FreeBytes == nil {
		o.FreeBytes = freeBytes
	}
	if o.LockSelfInstall == nil {
		o.LockSelfInstall = lockSelfInstall
	}
	a := &applier{h: h, o: Options{ConfigPath: o.ConfigPath, Check: o.Check, Out: o.Out}, planned: map[string]bool{}}
	defer a.cleanup()
	u := &upgrader{a: a, h: h, o: o}
	defer func() {
		if u.unlock != nil {
			u.unlock()
		}
	}()
	err := u.run(ctx)
	if errors.Is(err, errNothingToUpgrade) {
		return a.res, nil
	}
	if err != nil {
		return a.res, safeError{err}
	}
	return a.res, nil
}

func (u *upgrader) run(ctx context.Context) error {
	if err := u.preflight(ctx); err != nil {
		return err
	}
	steps := []struct {
		name string
		fn   func(context.Context, *step) error
	}{
		{"automation", u.clearStaleDropIn},
		{"stop", u.stop},
		{"backup", u.backup},
		{"binary", u.installBinary},
		{"validate", u.validate},
		{"restart", u.restart},
	}
	for _, st := range steps {
		s := &step{a: u.a, name: st.name}
		if err := st.fn(ctx, s); err != nil {
			err = fmt.Errorf("%s: %w", st.name, err)
			if u.a.o.Check || !u.stopRequested {
				return err
			}
			return u.rollback(ctx, err)
		}
		if st.name != "automation" || s.reported {
			s.finish()
		}
	}
	u.retention(ctx)
	if !u.a.o.Check {
		_, _ = fmt.Fprintf(u.o.Out, "\nLCM is upgraded from %s to %s (storage schema %d to %d) and healthy.\nThe backup set %s stays for a manual rollback (docs/operations.md#upgrade).\n",
			u.old, u.cand, u.oldSchema, u.candSchema, u.set)
	}
	return nil
}

// upgradeTools are the programs the upgrade itself runs.
var upgradeTools = []string{"getent", "systemctl", "visudo"}

func (u *upgrader) preflight(ctx context.Context) error {
	a, h := u.a, u.h
	if !h.Privileged {
		return errors.New("must run as root, from a reviewed build: sudo bin/lcm host upgrade")
	}
	if err := CheckExecutable(h.Executable); err != nil {
		return err
	}
	if _, err := h.FS.Lstat(u.o.ConfigPath); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s does not exist; create it with: sudo bin/lcm host init --operator USER", u.o.ConfigPath)
	}
	cfg, err := LoadConfig(h.FS, u.o.ConfigPath)
	if err != nil {
		return err
	}
	a.cfg = cfg
	var missing []string
	for _, t := range upgradeTools {
		if _, err := h.Runner.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required tools: %s", strings.Join(missing, ", "))
	}
	for _, d := range []string{"/etc", "/usr/local/bin", "/run/systemd/system", "/var/backups"} {
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
	if a.lcm == nil {
		return errors.New("the account lcm does not exist; this is not an installation to upgrade (run host apply)")
	}
	if err := u.preflightInstallation(); err != nil {
		return err
	}
	if err := u.preflightEnvFile(); err != nil {
		return err
	}
	if err := u.takeSelfInstallLock(); err != nil {
		return err
	}
	if err := u.preflightSelfInstall(); err != nil {
		return err
	}
	if err := a.preflightStage(ctx); err != nil {
		return err
	}
	if err := u.preflightVersions(ctx); err != nil {
		return err
	}
	if err := u.preflightBackupRoot(); err != nil {
		return err
	}
	return u.preflightSpace()
}

// preflightInstallation requires the pieces of an installation that the
// upgrade stops, backs up and starts again.
func (u *upgrader) preflightInstallation() error {
	fsys := u.h.FS
	for _, f := range []struct{ path, what string }{
		{Binary, "the installed binary"}, {DatabaseFile, "the database"}, {Unit, "the unit"},
	} {
		info, err := fsys.Lstat(f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%s does not exist (%s); this is not an installation to upgrade (run host apply)", f.path, f.what)
		case err != nil:
			return err
		case !info.IsRegular():
			return fmt.Errorf("%s is not a regular file", f.path)
		}
	}
	if _, err := fsys.Lstat(UpgradeDropIn); err == nil {
		u.staleDropIn = true
	}
	return nil
}

// preflightEnvFile reads the settings of lcm.env that matter here. The file
// is root's configuration, so its paths can be trusted like host.json's.
func (u *upgrader) preflightEnvFile() error {
	u.secretsDir = DataDir + "/secrets"
	info, err := u.h.FS.Lstat(EnvFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := rootOwned(EnvFile, info); err != nil || !info.IsRegular() {
		return fmt.Errorf("%s must be a regular file owned by root; review it, then run host apply", EnvFile)
	}
	data, err := readAll(u.h.FS, EnvFile)
	if err != nil {
		return err
	}
	env := parseEnvFile(data)
	if _, set := env["LCM_AUTOMATION"]; set {
		return fmt.Errorf("%s sets LCM_AUTOMATION; remove it, because the upgrade switches automation off for its validation start and on again", EnvFile)
	}
	if v, set := env["LCM_DATA_DIR"]; set && v != DataDir {
		return fmt.Errorf("%s sets LCM_DATA_DIR to %q; host setup manages %s only", EnvFile, printable(v, 100), DataDir)
	}
	if v, set := env["LCM_SECRETS_DIR"]; set {
		if !path.IsAbs(v) || path.Clean(v) != v || v == "/" || strings.ContainsAny(v, "\n\x00") {
			return fmt.Errorf("%s: LCM_SECRETS_DIR must be an absolute, clean path (got %q)", EnvFile, printable(v, 100))
		}
		u.secretsDir = v
	}
	return nil
}

// parseEnvFile reads KEY=VALUE lines the way systemd's EnvironmentFile
// does for the simple settings lcm.env holds.
func parseEnvFile(data []byte) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env
}

// takeSelfInstallLock holds self-install's lock until the command ends, so
// that no update is installed or verified (its verification restarts the
// unit) while the upgrade runs. Check mode changes nothing and does not lock.
func (u *upgrader) takeSelfInstallLock() error {
	if u.a.o.Check {
		return nil
	}
	release, err := u.o.LockSelfInstall()
	if err != nil {
		return fmt.Errorf("%w; wait for it to finish (a few minutes) and run the command again", err)
	}
	u.unlock = release
	return nil
}

// lockSelfInstall takes an exclusive flock on the file that self-install
// locks with flock(1), without waiting.
func lockSelfInstall() (func(), error) {
	fd, err := syscall.Open(selfInstallLock, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: selfInstallLock, Err: err}
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return nil, &fs.PathError{Op: "fstat", Path: selfInstallLock, Err: err}
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != 0 {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("%s is not a regular file owned by root", selfInstallLock)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("a self-install holds %s: an update is being installed or verified", selfInstallLock)
		}
		return nil, &fs.PathError{Op: "flock", Path: selfInstallLock, Err: err}
	}
	// Closing the descriptor releases the lock.
	return func() { _ = syscall.Close(fd) }, nil
}

// preflightSelfInstall refuses while a self-install is being verified: its
// scheduled restart would race with the upgrade.
func (u *upgrader) preflightSelfInstall() error {
	info, err := u.h.FS.Lstat(selfInstallPending)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsRegular() {
		return fmt.Errorf("%s is not a regular file; remove it", selfInstallPending)
	}
	data, err := readAll(u.h.FS, selfInstallPending)
	if err != nil {
		return err
	}
	since, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return nil // self-install itself treats an unreadable stamp as old
	}
	if age := u.h.Clock.Now().Sub(time.Unix(since, 0)); age < selfInstallVerify {
		return fmt.Errorf("a self-install is still being verified (%s ago); try again in %s", age.Truncate(time.Second), (selfInstallVerify - age).Truncate(time.Second))
	}
	return nil
}

// lcmCommand runs a program as the lcm account with a small bounded output.
func (u *upgrader) lcmCommand(ctx context.Context, program string, args ...string) ([]byte, error) {
	return u.a.run(ctx, Command{Path: program, Args: args, As: u.a.lcm, MaxOutput: 256, Timeout: 30 * time.Second})
}

func (u *upgrader) preflightVersions(ctx context.Context) error {
	a := u.a
	out, err := u.lcmCommand(ctx, Binary, "version")
	if err != nil {
		return fmt.Errorf("the installed %s does not report its version: %w", Binary, err)
	}
	var ok bool
	if u.old, ok = parseVersion(out); !ok {
		return fmt.Errorf("the installed %s reports no well-formed version", Binary)
	}
	stage := a.stage + "/lcm"
	out, err = a.run(ctx, Command{Path: stage, Args: []string{"version"}, As: &a.nobody, MaxOutput: 256, Timeout: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("this build does not report its version: %w", err)
	}
	if u.cand, ok = parseVersion(out); !ok {
		return errors.New("this build reports no well-formed version (use a release build: make build)")
	}
	if u.old.time.After(u.cand.time) && !u.o.AllowOlder {
		return fmt.Errorf("the installed %s (%s) is newer than this build (%s); build the current reviewed checkout, or pass --allow-older to install this one anyway", Binary, u.old, u.cand)
	}
	if out, err = u.lcmCommand(ctx, Binary, "storage-schema"); err == nil {
		u.oldSchema, err = parseSchema(out)
	}
	if err != nil {
		return fmt.Errorf("the installed %s does not report its storage schema: %w", Binary, err)
	}
	if out, err = a.run(ctx, Command{Path: stage, Args: []string{"storage-schema"}, As: &a.nobody, MaxOutput: 32, Timeout: 10 * time.Second}); err == nil {
		u.candSchema, err = parseSchema(out)
	}
	if err != nil {
		return fmt.Errorf("this build does not report its storage schema: %w", err)
	}
	switch {
	case u.candSchema < u.oldSchema:
		return fmt.Errorf("this build uses storage schema %d, below the installed %d: migrations are forward-only, so this would be a downgrade", u.candSchema, u.oldSchema)
	case u.candSchema == u.oldSchema:
		s := &step{a: a, name: "preflight"}
		s.line(fmt.Sprintf("storage schema %d on both %s and %s: nothing to upgrade here; self-deploy or host apply --replace-binary installs a build with the same schema", u.candSchema, u.old, u.cand))
		return errNothingToUpgrade
	}
	// The candidate reads the database, which only the lcm account may: it
	// shows that the database is at the installed schema, not partway
	// through an earlier attempt. The staged copy is root's reviewed build.
	out, err = a.run(ctx, Command{Path: stage, Args: []string{"storage-schema", "--data-dir", DataDir}, As: a.lcm, MaxOutput: 32, Timeout: 30 * time.Second})
	if err == nil {
		var db int
		if db, err = parseSchema(out); err == nil && db != u.oldSchema {
			return fmt.Errorf("the database is at storage schema %d but the installed binary uses %d; resolve that by hand first (docs/operations.md#upgrade)", db, u.oldSchema)
		}
	}
	if err != nil {
		return fmt.Errorf("cannot read the database schema: %w", err)
	}
	s := &step{a: a, name: "preflight"}
	s.line(fmt.Sprintf("%s (storage schema %d) -> %s (storage schema %d)", u.old, u.oldSchema, u.cand, u.candSchema))
	return nil
}

// parseSchema accepts exactly the number "lcm storage-schema" prints.
func parseSchema(out []byte) (int, error) {
	s := strings.TrimSuffix(string(out), "\n")
	if s == "" || len(s) > 9 || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("unexpected output")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, errors.New("unexpected output")
	}
	return n, nil
}

// preflightBackupRoot refuses a backup directory that someone other than
// root could have prepared; it is created later, just before the backup.
func (u *upgrader) preflightBackupRoot() error {
	info, err := u.h.FS.Lstat(BackupRoot)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s exists but is not a directory; move it away", BackupRoot)
	}
	return rootOwned(BackupRoot, info)
}

func (u *upgrader) preflightSpace() error {
	fsys := u.h.FS
	for _, p := range []string{DatabaseFile, DatabaseFile + "-wal", DatabaseFile + "-shm"} {
		info, err := fsys.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		u.dbBytes += info.Size
	}
	binInfo, err := fsys.Lstat(Binary)
	if err != nil {
		return err
	}
	if u.etcBytes, err = treeBytes(fsys, EtcDir, &budget{}); err != nil {
		return fmt.Errorf("size of %s: %w", EtcDir, err)
	}
	if _, err := fsys.Lstat(u.secretsDir); err == nil {
		if u.secretBytes, err = treeBytes(fsys, u.secretsDir, &budget{}); err != nil {
			return fmt.Errorf("size of %s: %w", u.secretsDir, err)
		}
	}
	// The backup holds the database once more (twice, with the room that a
	// restore beside the migrated database needs), the binary and the two
	// trees.
	for _, need := range []struct {
		dir   string
		bytes int64
	}{
		{nearestDir(fsys, BackupRoot), 2*u.dbBytes + binInfo.Size + u.etcBytes + u.secretBytes + backupSlack},
		{DataDir, u.dbBytes + backupSlack},
	} {
		free, err := u.o.FreeBytes(need.dir)
		if err != nil {
			return fmt.Errorf("free space of %s: %w", need.dir, err)
		}
		if free < uint64(need.bytes) { //nolint:gosec // sizes are non-negative
			return fmt.Errorf("not enough free space on %s: %d MiB free, %d MiB needed", need.dir, free>>20, need.bytes>>20)
		}
	}
	return nil
}

// treeBytes adds up the sizes of the regular files below dir.
func treeBytes(fsys FS, dir string, b *budget) (int64, error) {
	names, err := fsys.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, n := range names {
		if err := b.entry(); err != nil {
			return 0, err
		}
		info, err := fsys.Lstat(dir + "/" + n)
		if err != nil {
			return 0, err
		}
		switch {
		case info.IsDir():
			sub, err := treeBytes(fsys, dir+"/"+n, b)
			if err != nil {
				return 0, err
			}
			total += sub
		case info.IsRegular():
			total += info.Size
		}
	}
	return total, nil
}

// nearestDir returns p or its closest existing ancestor.
func nearestDir(fsys FS, p string) string {
	for ; p != "/"; p = path.Dir(p) {
		if _, err := fsys.Lstat(p); err == nil {
			return p
		}
	}
	return p
}

// freeBytes is the space available to unprivileged writers of the
// filesystem holding p; root's reserved blocks do not count.
func freeBytes(p string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // Bsize is positive
}

// clearStaleDropIn removes the automation drop-in that an interrupted
// upgrade left in /run, so that this one starts from a known state.
func (u *upgrader) clearStaleDropIn(ctx context.Context, s *step) error {
	if !u.staleDropIn {
		return nil
	}
	return s.change("remove "+UpgradeDropIn+" left by an interrupted upgrade", func() error {
		return u.removeDropIn(ctx)
	})
}

func (u *upgrader) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return u.a.systemctl(ctx, args...)
}

func (u *upgrader) stop(ctx context.Context, s *step) error {
	return s.change("stop "+UnitName+" and wait until it is inactive", func() error {
		u.stopRequested = true
		if _, err := u.systemctl(ctx, "stop", UnitName); err != nil {
			return err
		}
		return u.waitInactive(ctx)
	})
}

// waitInactive polls until the unit is no longer active.
func (u *upgrader) waitInactive(ctx context.Context) error {
	deadline := u.h.Clock.Now().Add(upgradeStopWait)
	for {
		_, err := u.systemctl(ctx, "is-active", "--quiet", UnitName)
		switch {
		case err != nil && exitCode(err) < 0:
			return err
		case err != nil && exitCode(err) == 4:
			return fmt.Errorf("%s is unknown to systemd", UnitName)
		case err != nil:
			return nil
		}
		if !u.h.Clock.Now().Before(deadline) {
			return fmt.Errorf("%s is still active after %s", UnitName, upgradeStopWait)
		}
		if err := sleep(ctx, u.h.Clock, time.Second); err != nil {
			return err
		}
	}
}

// versionLabel names a build in the manifest.
func versionLabel(v buildVersion, schema int) manifestVersion {
	return manifestVersion{Version: v.version, Commit: v.commit, Schema: schema}
}

func (u *upgrader) backup(ctx context.Context, s *step) error {
	stamp := u.h.Clock.Now().UTC().Format("20060102T150405Z")
	u.set = BackupRoot + "/upgrade-" + stamp
	return s.change("back up the database, the binary, "+EtcDir+" and the secrets to "+u.set, func() error {
		// Only a set this run created is ever removed: a set of the same name
		// that already exists is somebody's backup.
		if err := u.createSet(); err != nil {
			return err
		}
		if err := u.writeBackup(ctx); err != nil {
			// A partial set must not look like a usable one.
			if rerr := u.h.FS.RemoveAll(u.set); rerr != nil {
				err = fmt.Errorf("%w; removing the partial set: %w", err, rerr)
			}
			return err
		}
		return nil
	})
}

// createSet makes the new, empty backup set. It refuses a name that exists
// and does not touch it.
func (u *upgrader) createSet() error {
	fsys := u.h.FS
	if _, err := fsys.Lstat(BackupRoot); errors.Is(err, fs.ErrNotExist) {
		if err := fsys.Mkdir(BackupRoot, 0o700, 0, 0); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := fsys.Lstat(u.set); err == nil {
		return fmt.Errorf("the backup set %s already exists; it was left alone, run the command again in a second", u.set)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := fsys.Mkdir(u.set, 0o700, 0, 0); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("the backup set %s already exists; it was left alone, run the command again in a second", u.set)
		}
		return err
	}
	return nil
}

// ensureRoom fails when the backup filesystem could not take need more bytes
// and still keep backupMargin free.
func (u *upgrader) ensureRoom(need int64) error {
	free, err := u.o.FreeBytes(u.set)
	if err != nil {
		return fmt.Errorf("free space of %s: %w", u.set, err)
	}
	if free < uint64(need)+backupMargin { //nolint:gosec // sizes are non-negative
		return fmt.Errorf("the backup stopped: %d MiB free on %s, but the next copy needs %d MiB and %d MiB must stay free", free>>20, u.set, need>>20, backupMargin>>20)
	}
	return nil
}

func (u *upgrader) writeBackup(_ context.Context) error {
	fsys := u.h.FS
	u.manifest = upgradeManifest{
		Format: 1, CreatedAt: u.h.Clock.Now().UTC().Format(time.RFC3339),
		From: versionLabel(u.old, u.oldSchema), To: versionLabel(u.cand, u.candSchema),
	}
	if err := fsys.Mkdir(u.set+"/database", 0o700, 0, 0); err != nil {
		return err
	}
	// LCM is stopped and has checkpointed its WAL, so the files are
	// consistent; any -wal and -shm left behind are copied with them.
	for _, name := range []string{"lcm.db", "lcm.db-wal", "lcm.db-shm"} {
		if _, err := fsys.Lstat(DataDir + "/" + name); errors.Is(err, fs.ErrNotExist) && name != "lcm.db" {
			continue
		}
		if err := u.backupFile(DataDir, name, "database/"+name); err != nil {
			return err
		}
	}
	if err := fsys.Mkdir(u.set+"/bin", 0o700, 0, 0); err != nil {
		return err
	}
	if err := u.backupFile(path.Dir(Binary), path.Base(Binary), "bin/lcm"); err != nil {
		return err
	}
	owners := map[int]bool{0: true, u.a.lcm.UID: true}
	if err := u.backupTree(EtcDir, "etc-lcm", owners, u.etcBytes); err != nil {
		return err
	}
	switch _, err := fsys.Lstat(u.secretsDir); {
	case errors.Is(err, fs.ErrNotExist):
		u.manifest.Notes = append(u.manifest.Notes, "no secrets directory at "+u.secretsDir)
	case err != nil:
		return err
	default:
		if err := u.backupTree(u.secretsDir, "secrets", owners, u.secretBytes); err != nil {
			return err
		}
	}
	if err := u.hashTrees(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(u.manifest, "", "  ")
	if err != nil {
		return err
	}
	return fsys.WriteFile(u.set+"/manifest.json", bytes.NewReader(append(data, '\n')), 0o600, 0, 0)
}

// backupFile copies the regular file name of dir to rel in the set and
// records the digest of what was read, which the copy must match. The file
// is opened below a confined directory, and its size, mode and owner come
// from the opened descriptor, which must be the file that was looked at
// first: a file swapped in meanwhile is refused, not copied with the
// metadata of another.
func (u *upgrader) backupFile(dir, name, rel string) error {
	fsys := u.h.FS
	src := dir + "/" + name
	t, err := openSource(fsys, dir)
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	before, err := t.Lstat(name)
	if err != nil {
		return err
	}
	if !before.IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	r, info, err := t.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if !info.IsRegular() || !sameFile(before, info) {
		return fmt.Errorf("%s changed while it was opened; nothing was copied", src)
	}
	if err := u.ensureRoom(info.Size); err != nil {
		return err
	}
	h := sha256.New()
	dst := u.set + "/" + rel
	if err := fsys.WriteFile(dst, io.TeeReader(r, h), 0o600, 0, 0); err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	copied, err := hashFile(fsys, dst)
	if err != nil {
		return err
	}
	if copied != sum {
		return fmt.Errorf("the copy of %s does not match what was read", src)
	}
	ci, err := fsys.Lstat(dst)
	if err != nil {
		return err
	}
	u.manifest.Files = append(u.manifest.Files, manifestFile{
		Path: rel, SHA256: sum, Size: ci.Size,
		Restore: &restoreInfo{Source: src, Mode: uint32(info.Mode.Perm()), UID: info.UID, GID: info.GID},
	})
	return nil
}

// backupTree copies a directory tree into the set as a confined, checked
// copy; ownership is not kept (the set is root's), so a manual restore sets
// it again.
func (u *upgrader) backupTree(src, rel string, owners map[int]bool, size int64) error {
	fsys := u.h.FS
	if err := u.ensureRoom(size); err != nil {
		return err
	}
	t, err := openSource(fsys, src)
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	root, err := t.Root()
	if err != nil {
		return err
	}
	c := &copier{src: t, fsys: fsys, owners: owners}
	if err := c.dir(".", u.set+"/"+rel, root); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return nil
}

// hashTrees records every file and link of the copied trees.
func (u *upgrader) hashTrees() error {
	b := &budget{}
	for _, rel := range []string{"etc-lcm", "secrets"} {
		if _, err := u.h.FS.Lstat(u.set + "/" + rel); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := u.hashDir(rel, b); err != nil {
			return err
		}
	}
	sort.Slice(u.manifest.Files, func(i, j int) bool { return u.manifest.Files[i].Path < u.manifest.Files[j].Path })
	return nil
}

func (u *upgrader) hashDir(rel string, b *budget) error {
	fsys := u.h.FS
	names, err := fsys.ReadDir(u.set + "/" + rel)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := b.entry(); err != nil {
			return err
		}
		p := rel + "/" + n
		info, err := fsys.Lstat(u.set + "/" + p)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			err = u.hashDir(p, b)
		case info.IsLink():
			var target string
			if target, err = fsys.Readlink(u.set + "/" + p); err == nil {
				u.manifest.Files = append(u.manifest.Files, manifestFile{Path: p, Link: target})
			}
		case info.IsRegular():
			var sum string
			if sum, err = hashFile(fsys, u.set+"/"+p); err == nil {
				u.manifest.Files = append(u.manifest.Files, manifestFile{Path: p, SHA256: sum, Size: info.Size})
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *upgrader) installBinary(_ context.Context, s *step) error {
	a := u.a
	// Set before the write: whether the new binary ran or not, the old one
	// is what a rollback puts back.
	u.installed = true
	if _, err := a.ensureFile(s, Binary, a.exe, 0o755); err != nil {
		return err
	}
	if a.o.Check {
		return nil
	}
	sum, err := hashFile(u.h.FS, Binary)
	if err != nil {
		return err
	}
	if sum != a.exe.Sum {
		return fmt.Errorf("%s does not hold the staged build after the install", Binary)
	}
	return nil
}

// unitState is what systemd reports about lcm.service.
type unitState struct {
	active   string
	mainPID  int
	restarts int
	cgroup   string
}

func (u *upgrader) unitState(ctx context.Context) (unitState, error) {
	out, err := u.systemctl(ctx, "show", "--property=ActiveState,MainPID,NRestarts,ControlGroup", UnitName)
	if err != nil {
		return unitState{}, err
	}
	var st unitState
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "ActiveState":
			st.active = v
		case "MainPID":
			st.mainPID, _ = strconv.Atoi(v)
		case "NRestarts":
			st.restarts, _ = strconv.Atoi(v)
		case "ControlGroup":
			st.cgroup = v
		}
	}
	return st, nil
}

// inCgroup reports whether process pid belongs to the control group cg.
func (u *upgrader) inCgroup(pid int, cg string) bool {
	if cg == "" || cg == "/" {
		return false
	}
	data, err := readAll(u.h.FS, "/proc/"+strconv.Itoa(pid)+"/cgroup")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && (parts[2] == cg || strings.HasPrefix(parts[2], cg+"/")) {
			return true
		}
	}
	return false
}

// confirmUnit checks that the health answer comes from the unit's own
// process: the unit is active, its main process belongs to its control
// group, and neither the process nor the restart count change while the
// version is read twice. Any other process that listens on the health port
// therefore cannot stand in for it.
func (u *upgrader) confirmUnit(ctx context.Context, want string, baseRestarts int) error {
	first, err := u.unitState(ctx)
	if err != nil {
		return err
	}
	switch {
	case first.active != "active":
		return fmt.Errorf("%s is %q, not active", UnitName, printable(first.active, 32))
	case first.mainPID <= 0:
		return fmt.Errorf("%s has no main process", UnitName)
	case first.restarts != baseRestarts:
		return fmt.Errorf("%s was restarted by systemd %d time(s) since the start", UnitName, first.restarts-baseRestarts)
	case !u.inCgroup(first.mainPID, first.cgroup):
		return fmt.Errorf("process %d is not in the control group of %s", first.mainPID, UnitName)
	}
	v, err := u.o.HealthVersion(ctx)
	if err != nil {
		return err
	}
	if v != want {
		return fmt.Errorf("health reports version %q, not %q", printable(v, 64), want)
	}
	second, err := u.unitState(ctx)
	if err != nil {
		return err
	}
	if second.active != "active" || second.mainPID != first.mainPID || second.restarts != first.restarts {
		return fmt.Errorf("%s changed its main process while it was checked", UnitName)
	}
	return nil
}

// startUnit runs systemctl start or restart and waits until the unit's own
// process reports the wanted version.
func (u *upgrader) startUnit(ctx context.Context, verb, want string) error {
	base, err := u.unitState(ctx)
	if err != nil {
		return err
	}
	if _, err := u.systemctl(ctx, verb, UnitName); err != nil {
		return err
	}
	return u.waitVersion(ctx, want, base.restarts)
}

// waitVersion polls until the unit reports the wanted version or the wait
// ends. baseRestarts is the unit's restart count before the start.
func (u *upgrader) waitVersion(ctx context.Context, want string, baseRestarts int) error {
	deadline := u.h.Clock.Now().Add(upgradeHealthWait)
	for {
		v, err := u.o.HealthVersion(ctx)
		if err == nil && v == want {
			err = u.confirmUnit(ctx, want, baseRestarts)
			if err == nil {
				return nil
			}
		} else if err == nil {
			err = fmt.Errorf("health reports version %q, not %q", printable(v, 64), want)
		}
		if !u.h.Clock.Now().Before(deadline) {
			return fmt.Errorf("LCM does not report version %s within %s (%w); see journalctl -u lcm", want, upgradeHealthWait, err)
		}
		if serr := sleep(ctx, u.h.Clock, time.Second); serr != nil {
			return serr
		}
	}
}

func (u *upgrader) validate(ctx context.Context, s *step) error {
	what := fmt.Sprintf("start %s with LCM_AUTOMATION=off, wait up to %s for version %s, and check the database is at storage schema %d",
		UnitName, upgradeHealthWait, u.cand.version, u.candSchema)
	return s.change(what, func() error {
		if err := u.writeDropIn(ctx); err != nil {
			return err
		}
		if err := u.expectAutomation(ctx, true); err != nil {
			return err
		}
		if err := u.startUnit(ctx, "start", u.cand.version); err != nil {
			return err
		}
		out, err := u.lcmCommand(ctx, Binary, "storage-schema", "--data-dir", DataDir)
		if err != nil {
			return fmt.Errorf("read the database schema: %w", err)
		}
		got, err := parseSchema(out)
		if err != nil {
			return fmt.Errorf("read the database schema: %w", err)
		}
		if got != u.candSchema {
			return fmt.Errorf("the database is at storage schema %d after the start, expected %d", got, u.candSchema)
		}
		return nil
	})
}

func (u *upgrader) writeDropIn(ctx context.Context) error {
	fsys := u.h.FS
	info, err := fsys.Lstat(UpgradeDropInDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := fsys.Mkdir(UpgradeDropInDir, 0o755, 0, 0); err != nil {
			return err
		}
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", UpgradeDropInDir)
	default:
		if err := rootOwned(UpgradeDropInDir, info); err != nil {
			return err
		}
	}
	u.dropIn = true
	if err := u.a.writeContent(UpgradeDropIn, bytesContent([]byte(dropInContent)), 0o644, 0, 0); err != nil {
		return err
	}
	_, err = u.systemctl(ctx, "daemon-reload")
	return err
}

// removeDropIn deletes the drop-in and has systemd forget it.
func (u *upgrader) removeDropIn(ctx context.Context) error {
	fsys := u.h.FS
	var errs []error
	if err := fsys.Remove(UpgradeDropIn); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	// The directory is ours only if nothing else was put there; Remove
	// refuses a directory that is not empty.
	_ = fsys.Remove(UpgradeDropInDir)
	if len(errs) == 0 {
		u.dropIn = false
	}
	if _, err := u.systemctl(ctx, "daemon-reload"); err != nil {
		errs = append(errs, err)
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	}
	return fmt.Errorf("%w; %w", errs[0], errs[1])
}

// expectAutomation checks the unit definition systemd has loaded, so that
// a start never runs with the opposite of what the step intends.
func (u *upgrader) expectAutomation(ctx context.Context, off bool) error {
	out, err := u.systemctl(ctx, "show", "--property=Environment", "--value", UnitName)
	if err != nil {
		return err
	}
	// The last assignment is the effective one; systemd lists the
	// environment in the order the files and drop-ins define it.
	has := false
	for _, f := range strings.Fields(string(out)) {
		if v, ok := strings.CutPrefix(strings.TrimPrefix(f, `"`), "LCM_AUTOMATION="); ok {
			has = strings.TrimSuffix(v, `"`) == "off"
		}
	}
	switch {
	case off && !has:
		return fmt.Errorf("systemd did not load %s; refusing to start LCM with automation on", UpgradeDropIn)
	case !off && has:
		return fmt.Errorf("systemd still has LCM_AUTOMATION=off for %s; refusing to leave automation off", UnitName)
	}
	return nil
}

func (u *upgrader) restart(ctx context.Context, s *step) error {
	what := fmt.Sprintf("restart %s with automation on and wait up to %s for version %s", UnitName, upgradeHealthWait, u.cand.version)
	return s.change(what, func() error {
		if err := u.removeDropIn(ctx); err != nil {
			return err
		}
		if err := u.expectAutomation(ctx, false); err != nil {
			return err
		}
		return u.startUnit(ctx, "restart", u.cand.version)
	})
}

// rollback undoes a failed upgrade. Before the new binary was installed
// nothing changed except that LCM is stopped, so it is started again.
// After that the new binary may have migrated the database, so LCM is
// stopped, the database files and the old binary come back from the
// verified backup set, and only then does the old binary run again. It
// does not stop for a cancelled ctx: the host must not be left stopped.
func (u *upgrader) rollback(ctx context.Context, cause error) error {
	ctx = context.WithoutCancel(ctx)
	s := &step{a: u.a, name: "rollback"}
	s.line("failed: " + printable(cause.Error(), 1000))
	fail := func(err error) error {
		// One line: the error is printed through the terminal sanitizer.
		return fmt.Errorf("%w; ROLLBACK FAILED, LCM needs manual repair (backup set %s, docs/operations.md#upgrade): %w", cause, u.set, err)
	}
	if u.dropIn {
		if err := u.removeDropIn(ctx); err != nil {
			s.line("could not remove " + UpgradeDropIn + ": " + printable(err.Error(), 300))
		}
	}
	if u.installed {
		if _, err := u.systemctl(ctx, "stop", UnitName); err != nil {
			return fail(fmt.Errorf("stop %s: %w", UnitName, err))
		}
		if err := u.waitInactive(ctx); err != nil {
			return fail(fmt.Errorf("the database was not restored, because %w", err))
		}
		if err := u.restore(ctx, s); err != nil {
			return fail(err)
		}
	}
	_, _ = u.systemctl(ctx, "reset-failed", UnitName)
	if err := u.startUnit(ctx, "start", u.old.version); err != nil {
		return fail(fmt.Errorf("start %s: %w", UnitName, err))
	}
	if u.installed {
		s.line(fmt.Sprintf("restored the database and %s from %s; %s is healthy again", Binary, u.set, u.old))
		return fmt.Errorf("the upgrade failed and was rolled back; the backup set %s was kept: %w", u.set, cause)
	}
	s.line(fmt.Sprintf("nothing was changed; %s is started again and healthy", UnitName))
	return fmt.Errorf("the upgrade failed before the binary was replaced; nothing was changed: %w", cause)
}

// restore puts the database files and the binary back from the backup set
// after checking them against the manifest. The -wal and -shm files of the
// migrated database go first: a stale one next to a restored database
// would corrupt it.
func (u *upgrader) restore(_ context.Context, s *step) error {
	fsys := u.h.FS
	if u.set == "" || len(u.manifest.Files) == 0 {
		return errors.New("no backup set was completed, so there is nothing to restore")
	}
	var items []manifestFile
	for _, f := range u.manifest.Files {
		if f.Restore != nil {
			sum, err := hashFile(fsys, u.set+"/"+f.Path)
			if err != nil || sum != f.SHA256 {
				return fmt.Errorf("the backup file %s/%s does not verify against its manifest; nothing was restored", u.set, f.Path)
			}
			items = append(items, f)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := fsys.Remove(DatabaseFile + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// The database first, the binary last: the old binary then never sits
	// next to a migrated database.
	sort.SliceStable(items, func(i, j int) bool { return items[i].Path != "bin/lcm" && items[j].Path == "bin/lcm" })
	for _, f := range items {
		if err := u.restoreFile(f); err != nil {
			return err
		}
		s.line("restored " + f.Restore.Source)
	}
	return nil
}

func (u *upgrader) restoreFile(f manifestFile) error {
	fsys := u.h.FS
	r, err := fsys.Open(u.set + "/" + f.Path)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := fsys.WriteFile(f.Restore.Source, r, fs.FileMode(f.Restore.Mode), f.Restore.UID, f.Restore.GID); err != nil {
		return err
	}
	sum, err := hashFile(fsys, f.Restore.Source)
	if err != nil {
		return err
	}
	if sum != f.SHA256 {
		return fmt.Errorf("%s does not match the backup after it was restored", f.Restore.Source)
	}
	return nil
}

// retention keeps the newest keepUpgradeSets backup sets and always the one
// just written. A failure is a warning: the upgrade itself has succeeded.
func (u *upgrader) retention(_ context.Context) {
	s := &step{a: u.a, name: "retention"}
	names, err := u.h.FS.ReadDir(BackupRoot)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.line("warning: cannot list " + BackupRoot + ": " + printable(err.Error(), 300))
		return
	}
	var sets []string
	for _, n := range names {
		if !upgradeSet.MatchString(n) {
			continue
		}
		if info, err := u.h.FS.Lstat(BackupRoot + "/" + n); err == nil && info.IsDir() && !info.IsLink() && info.UID == 0 {
			sets = append(sets, n)
		}
	}
	current := path.Base(u.set)
	for _, n := range retentionRemovals(sets, current, keepUpgradeSets) {
		p := BackupRoot + "/" + n
		if err := s.change("remove the old backup set "+p, func() error { return u.h.FS.RemoveAll(p) }); err != nil {
			s.line("warning: " + printable(err.Error(), 300))
		}
	}
	s.finish()
}

// retentionRemovals returns the sets to delete: all but the newest keep,
// never current.
func retentionRemovals(sets []string, current string, keep int) []string {
	all := append([]string{}, sets...)
	if !contains(all, current) {
		all = append(all, current)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(all)))
	var out []string
	for i, n := range all {
		if i >= keep && n != current {
			out = append(out, n)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// httpHealthVersion asks LCM's health endpoint for the version it runs.
func httpHealthVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, HealthURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", &httpStatusError{code: resp.StatusCode}
	}
	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("health endpoint: %w", err)
	}
	if body.Status != "ok" {
		return "", fmt.Errorf("health endpoint reports status %q", printable(body.Status, 32))
	}
	return body.Version, nil
}
