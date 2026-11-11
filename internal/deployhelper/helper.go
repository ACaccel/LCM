package deployhelper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// Programs the helper runs, by absolute path.
const (
	systemdRun     = "/usr/bin/systemd-run"
	systemctl      = "/usr/bin/systemctl"
	systemdAnalyze = "/usr/bin/systemd-analyze"
	getent         = "/usr/bin/getent"
	shell          = "/bin/sh"
)

// Command timeouts. The run margins bound how long systemd-run may outlast
// the unit's own RuntimeMaxSec.
const (
	systemctlTimeout = 3 * time.Minute
	migrateTimeout   = 10 * time.Minute
	analyzeTimeout   = time.Minute
	buildRunMargin   = 2 * time.Minute
	migrateRunMargin = time.Minute
)

// budget is the longest the commands the helper runs for req may take
// together, in its worst case; copying trees comes on top. The client's
// timeout is derived from it, so that it never gives up on a helper that is
// still within its own limits.
func budget(req Request) time.Duration {
	switch req.Op {
	case OpBuild:
		// The build, then stopping its unit (by the watcher or after a
		// timeout).
		return time.Duration(req.TimeoutSec)*time.Second + buildRunMargin + 2*systemctlTimeout
	case OpActivate:
		// The migration and stopping its unit, the unit verification, the
		// reload check, reload and reload after a restore, is-enabled,
		// enable and restart.
		return migrateTimeout + migrateRunMargin + analyzeTimeout + 7*systemctlTimeout
	case OpBackup:
		// The producing process, then stopping its unit when it was
		// stopped by the watcher or timed out, and stopping and starting
		// the service.
		return time.Duration(req.TimeoutSec)*time.Second + buildRunMargin + 4*systemctlTimeout
	case OpRestore:
		// The copy of the current data, the restore and the undo, each
		// with its unit stopped on a timeout, and stopping and starting
		// the service.
		return 3*(restoreWorkerSec*time.Second+buildRunMargin+systemctlTimeout) + 4*systemctlTimeout
	case OpStart, OpStop, OpRestart, OpStatus:
		return systemctlTimeout
	case OpIngressApply:
		// The ingress and legacy locks, nginx -t and a reload, the probes,
		// and nginx -t and a reload after putting the previous site back.
		return 2*ingressLockWait + 2*time.Minute + 2*systemctlTimeout + hostsetup.ProbePhase
	}
	return 0
}

// ingressLockWait bounds the wait for each ingress lock.
const ingressLockWait = 30 * time.Second

// commandEnv is added to the fixed environment of every command.
var commandEnv = []string{"LC_ALL=C", "SYSTEMD_PAGER=cat", "SYSTEMD_COLORS=0", "SYSTEMD_LOG_COLOR=0"}

// Host is everything the helper reads and changes, so that tests run on a
// directory tree with fake commands.
type Host struct {
	// FS reaches every host path.
	FS     hostsetup.FS
	Runner execx.Runner
	Clock  clock.Clock
	// Config reads host.json with its trust checks.
	Config func() (hostsetup.Config, error)
	// Lookup reads an account.
	Lookup func(ctx context.Context, name string) (hostsetup.Account, bool, error)
	Ledger *Ledger
	// RootUID owns every directory and file the helper trusts, and RootUID
	// and RootGID own what it writes: 0 on a host.
	RootUID, RootGID int
	// Free reports the free bytes and inodes of the file system holding a
	// host path; nil checks nothing.
	Free func(p string) (bytes, inodes uint64, err error)
	// Limits bound builds.
	Limits Limits
	// IngressLock takes the ingress lock and, while it exists, the legacy
	// lock named; Probe, ProbeAttempts and ProbeWait check the routes after
	// a reload.
	IngressLock   func(legacy string) (func(), error)
	Probe         func(ctx context.Context, p hostsetup.Probe) (int, error)
	ProbeAttempts int
	ProbeWait     time.Duration
}

// Helper serves deploy helper requests.
type Helper struct {
	Host
}

// Serve answers one request read from in. Every outcome, refusals
// included, is a reply on out; only a failure to write it is an error.
func (h *Helper) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, MaxRequest+1))
	resp := Response{Version: Version, Code: CodeRefused, Message: "the request could not be read"}
	if err == nil {
		resp = h.Handle(ctx, raw)
	}
	_, err = out.Write(encodeResponse(resp))
	return err
}

// Handle answers one encoded request.
func (h *Helper) Handle(ctx context.Context, raw []byte) Response {
	req, err := DecodeRequest(raw)
	if err != nil {
		// Echo only well-formed identifiers, so that the client can match
		// the refusal.
		resp := Response{Version: Version}
		if _, known := opFields[req.Op]; known {
			resp.Op = req.Op
		}
		if uuidPattern.MatchString(req.OperationID) {
			resp.OperationID = req.OperationID
		}
		return refuse(resp, CodeRefused, "%v", err)
	}
	switch {
	case req.Op == OpQuery:
		return h.query(req)
	case req.Op == OpIngressStatus:
		return h.ingressStatus(req)
	case !req.Op.Changes():
		return h.read(ctx, req)
	}
	return h.change(ctx, req)
}

func reply(req Request) Response {
	return Response{Version: Version, Op: req.Op, OperationID: req.OperationID}
}

// refuse sets the code and a printable message of resp.
func refuse(resp Response, code Code, format string, args ...any) Response {
	resp.Code = code
	resp.Message = hostsetup.Printable(fmt.Sprintf(format, args...), 2000)
	return resp
}

// grant returns the configuration and the service's grant, or a refusal.
func (h *Helper) grant(req Request) (hostsetup.Config, hostsetup.ServiceGrant, *Response) {
	cfg, err := h.Config()
	if err != nil {
		resp := refuse(reply(req), CodeRefused, "the host configuration cannot be used: %v", err)
		return cfg, hostsetup.ServiceGrant{}, &resp
	}
	g, ok := cfg.Services[req.Service]
	if !ok {
		resp := refuse(reply(req), CodeRefused, "%s is not in the services of %s; an administrator adds it with sudo bin/lcm host allow", req.Service, hostsetup.ConfigPath)
		return cfg, g, &resp
	}
	if req.Op.Ingress() {
		switch {
		case cfg.Components.Ingress == nil:
			resp := refuse(reply(req), CodeDisabled, "components.ingress is not configured in %s", hostsetup.ConfigPath)
			return cfg, g, &resp
		case g.Route == nil:
			resp := refuse(reply(req), CodeRefused, "services.%s has no route in %s; an administrator grants one with sudo bin/lcm host allow %s --route PREFIX", req.Service, hostsetup.ConfigPath, req.Service)
			return cfg, g, &resp
		}
		return cfg, g, nil
	}
	if g.External {
		resp := refuse(reply(req), CodeRefused, "services.%s is external in %s: LCM only publishes its route and does not deploy, run or back it up", req.Service, hostsetup.ConfigPath)
		return cfg, g, &resp
	}
	if req.Op.Changes() && !cfg.Components.Deploy {
		resp := refuse(reply(req), CodeRefused, "components.deploy is false in %s", hostsetup.ConfigPath)
		return cfg, g, &resp
	}
	return cfg, g, nil
}

func (h *Helper) read(ctx context.Context, req Request) Response {
	cfg, g, refused := h.grant(req)
	if refused != nil {
		return *refused
	}
	switch req.Op {
	case OpStatus:
		return h.status(ctx, req)
	case OpBackups:
		return h.backups(req, cfg, g)
	}
	return h.releases(req)
}

// lockName is the lock a changing operation holds. A service's builds have
// their own, so that a long build never keeps its unit from being stopped or
// restarted; install, activate and the unit actions share the service's.
// Service names contain no ".".
func lockName(service string, op Op) string {
	if op.Ingress() {
		// One site for every service; no service name starts with "lcm".
		return "lcm-ingress"
	}
	if op == OpBuild {
		return service + ".build"
	}
	return service
}

// change carries out a changing operation once per operationId, under its
// lock, with its outcome recorded before the reply.
func (h *Helper) change(ctx context.Context, req Request) Response {
	now := h.Clock.Now()
	rec := record{Version: Version, OperationID: req.OperationID, Service: req.Service, Op: req.Op,
		RequestHash: req.Hash(), State: StateRunning, StartedAt: now.UnixMilli()}
	// A finished operation is answered from the ledger whatever host.json
	// says now; a running one needs its lock to be judged.
	if old, found, err := h.Ledger.get(req.OperationID); err != nil || found && old.State != StateRunning {
		return h.recorded(req, rec, old, err)
	}
	// The grant comes before the lock, so that only granted services ever
	// get a lock file.
	cfg, g, refused := h.grant(req)
	if refused != nil {
		return *refused
	}
	unlock, err := h.Ledger.Lock(lockName(req.Service, req.Op))
	if errors.Is(err, ErrBusy) {
		what := "another operation"
		switch {
		case req.Op == OpBuild:
			what = "another build"
		case req.Op.Ingress():
			return refuse(reply(req), CodeBusy, "another change of the nginx site is running")
		}
		return refuse(reply(req), CodeBusy, "%s of %s is running", what, req.Service)
	}
	if err != nil {
		return refuse(reply(req), CodeFailed, "the ledger cannot be used: %v", err)
	}
	defer unlock()
	if old, found, err := h.Ledger.begin(rec); err != nil || found {
		return h.recorded(req, rec, old, err)
	}
	resp := h.perform(ctx, req, cfg, g)
	resp.State = StateSucceeded
	if resp.Code != "" {
		resp.State = StateFailed
	}
	rec.State, rec.FinishedAt, rec.Response = resp.State, h.Clock.Now().UnixMilli(), &resp
	if err := h.Ledger.put(rec); err != nil {
		resp.Message = strings.TrimSpace(resp.Message + " (the outcome could not be recorded; a query will report the operation as interrupted)")
	}
	h.Ledger.maybePrune(h.Clock.Now())
	return resp
}

// recorded answers a request whose operationId the ledger already holds.
// A running record reaches it only while the caller holds the record's
// lock, so it is one whose helper stopped.
func (h *Helper) recorded(req Request, want, old record, err error) Response {
	switch {
	case err != nil:
		return refuse(reply(req), CodeFailed, "the ledger cannot be read: %v", err)
	case old.RequestHash != want.RequestHash || old.Service != want.Service || old.Op != want.Op:
		return refuse(reply(req), CodeConflict, "operation %s was used for another request", req.OperationID)
	case old.State == StateRunning:
		old = h.interrupt(old)
		if err := h.Ledger.put(old); err != nil {
			return refuse(reply(req), CodeFailed, "the ledger cannot be written: %v", err)
		}
	}
	return *old.Response
}

// interrupt marks a running record whose helper stopped.
func (h *Helper) interrupt(r record) record {
	resp := Response{Version: Version, Op: r.Op, OperationID: r.OperationID, State: StateInterrupted,
		Code: CodeFailed, Message: "the helper stopped before the operation finished; what it changed is unknown and must be inspected on the host"}
	r.State, r.FinishedAt, r.Response = StateInterrupted, h.Clock.Now().UnixMilli(), &resp
	return r
}

func (h *Helper) query(req Request) Response {
	r, found, err := h.Ledger.get(req.OperationID)
	switch {
	case err != nil:
		return refuse(reply(req), CodeFailed, "the ledger cannot be read: %v", err)
	case !found:
		return refuse(reply(req), CodeNotFound, "no operation %s is recorded", req.OperationID)
	case r.State != StateRunning:
		return *r.Response
	}
	unlock, err := h.Ledger.Lock(lockName(r.Service, r.Op))
	if errors.Is(err, ErrBusy) {
		return Response{Version: Version, Op: r.Op, OperationID: r.OperationID, State: StateRunning}
	}
	if err != nil {
		return refuse(reply(req), CodeFailed, "the ledger cannot be used: %v", err)
	}
	defer unlock()
	return h.recorded(req, r, r, nil)
}

func (h *Helper) perform(ctx context.Context, req Request, cfg hostsetup.Config, g hostsetup.ServiceGrant) Response {
	switch req.Op {
	case OpBuild:
		return h.build(ctx, req, cfg, g)
	case OpInstall:
		return h.install(ctx, req)
	case OpActivate:
		return h.activate(ctx, req, cfg, g)
	case OpBackup:
		return h.backup(ctx, req, cfg, g)
	case OpRestore:
		return h.restore(ctx, req, cfg, g)
	case OpPrune:
		return h.prune(ctx, req, cfg, g)
	case OpIngressApply:
		return h.ingressApply(ctx, req, cfg)
	}
	return h.unitAction(ctx, req)
}

// account reads an account; with home set it must be the dedicated system
// account that host setup creates, with its comment and without a login
// shell.
func (h *Helper) account(ctx context.Context, name, home, comment string) (hostsetup.Account, error) {
	acc, ok, err := h.Lookup(ctx, name)
	if err != nil {
		return acc, err
	}
	if !ok {
		return acc, fmt.Errorf("there is no account %s; run sudo bin/lcm host apply", name)
	}
	if acc.UID == 0 {
		return acc, fmt.Errorf("account %s has user ID 0", name)
	}
	if home != "" {
		if p := hostsetup.SystemAccountProblem(h.FS, acc, home, comment); p != "" {
			return acc, errors.New(p)
		}
	}
	return acc, nil
}

// trustedPath checks that p and its ancestors exist, are not symbolic links
// and that only RootUID can change them; p must be a directory or, with
// file set, a regular file.
func (h *Helper) trustedPath(p string, file bool) (hostsetup.Info, error) {
	var info hostsetup.Info
	for _, a := range ancestry(p) {
		var err error
		info, err = h.FS.Lstat(a)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return info, fmt.Errorf("%s does not exist", a)
		case err != nil:
			return info, err
		case info.IsLink():
			return info, fmt.Errorf("%s is a symbolic link", a)
		case info.UID != h.RootUID || info.Mode.Perm()&0o022 != 0:
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

// trustedInput checks a build input like host setup checks a toolchain:
// it and its ancestors are owned by root or the operator, are not symbolic
// links and are not writable by anyone else.
func (h *Helper) trustedInput(p string, operator hostsetup.Account) error {
	for _, a := range ancestry(p) {
		info, err := h.FS.Lstat(a)
		if err != nil {
			return err
		}
		perm := info.Mode.Perm()
		switch {
		case info.IsLink():
			return fmt.Errorf("%s is a symbolic link", a)
		case info.UID != h.RootUID && info.UID != operator.UID:
			return fmt.Errorf("%s is owned by user %d, not root or the operator", a, info.UID)
		case perm&0o002 != 0:
			return fmt.Errorf("%s is writable by others", a)
		case perm&0o020 != 0 && info.GID != 0 && info.GID != operator.GID:
			return fmt.Errorf("%s is writable by group %d", a, info.GID)
		}
		if a == p && !info.IsDir() {
			return fmt.Errorf("%s is not a directory", a)
		}
	}
	return nil
}

// ensureDir creates p with its owner and mode, or checks that it exists
// with that owner and is a directory.
func (h *Helper) ensureDir(p string, perm fs.FileMode, uid, gid int) error {
	info, err := h.FS.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return h.FS.Mkdir(p, perm, uid, gid)
	case err != nil:
		return err
	case !info.IsDir() || info.IsLink() || info.UID != uid:
		return fmt.Errorf("%s is not a directory owned by user %d", p, uid)
	}
	return nil
}

// ancestry returns "/", each ancestor of p and p itself.
func ancestry(p string) []string {
	var out []string
	for ; ; p = path.Dir(p) {
		out = append(out, p)
		if p == "/" {
			break
		}
	}
	slices.Reverse(out)
	return out
}

// newID returns a build or release ID: the UTC time and random digits, so
// that IDs sort by creation.
func (h *Helper) newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b) // crypto/rand never fails on Linux
	return h.Clock.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

// run runs a fixed program with the helper's environment.
func (h *Helper) run(ctx context.Context, timeout time.Duration, argv ...string) (execx.Result, error) {
	return h.Runner.Run(ctx, execx.Cmd{Argv: argv, Dir: "/", Env: commandEnv, Timeout: timeout, MaxOutput: 2 * MaxLog})
}

// sanitizeLog keeps the last MaxLog bytes of a command's output, with
// control and format characters other than newlines and tabs replaced:
// builds and services choose what they print.
func sanitizeLog(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return '?'
		}
		return r
	}, s)
	if len(s) > MaxLog {
		cut := len(s) - MaxLog
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
		s = s[cut:]
	}
	return s
}

// commandFailure describes a failed command with the tail of what it
// printed, which usually names the cause.
func commandFailure(what, output string, err error) string {
	var msg string
	switch code := execx.ExitCode(err); {
	case code > 0:
		msg = fmt.Sprintf("%s exited with status %d", what, code)
	case errors.Is(err, execx.ErrTimeout):
		msg = what + " timed out"
	default:
		msg = fmt.Sprintf("%s did not finish: %v", what, err)
	}
	if tail := hostsetup.Excerpt(output); tail != "" {
		msg += ": " + tail
	}
	return msg
}
