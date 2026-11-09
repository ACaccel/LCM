package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// ErrLockHeld is returned when another action holds the service lock.
// Locker implementations must return an error wrapping it when the lock is
// taken, so that callers can tell "busy" from a real failure.
var ErrLockHeld = errors.New("catalog: service is busy")

// ErrPrecondition is matched by every error that says the request cannot
// be satisfied as it stands (wrong state, missing configuration, unknown
// job). Retrying without changing something is pointless, so node
// wrappers treat these as permanent.
var ErrPrecondition = errors.New("catalog: precondition failed")

type preconditionError struct{ msg string }

func (e *preconditionError) Error() string        { return e.msg }
func (e *preconditionError) Is(target error) bool { return target == ErrPrecondition }

func precondition(msg string) error { return &preconditionError{msg: msg} }

// Precondition errors.
var (
	ErrNoSource         = precondition("catalog: service has no source configured")
	ErrDirtyRepo        = precondition("catalog: the build checkout has uncommitted changes to tracked files")
	ErrNoCheckout       = precondition("catalog: the build checkout does not exist yet; deploy the service first")
	ErrBadCheckout      = precondition("catalog: the build checkout is not a git repository; move it away so that the next deploy clones the source again")
	ErrSourceInWorkDir  = precondition("catalog: source.url points into LCM's build directory; set it to the repository the checkout was cloned from")
	ErrInvalidVersion   = precondition("catalog: version must be a commit sha of 7 to 64 hex digits")
	ErrNoRollbackTarget = precondition("catalog: no earlier successful deployment to roll back to")
	ErrNoBackup         = precondition("catalog: service has no backup configured")
	ErrNoRestore        = precondition("catalog: service has no typed backup with a source that can be restored")
	ErrUnknownBackup    = precondition("catalog: no such backup of this service, or retention removed it")
	ErrUnknownJob       = precondition("catalog: service has no such job")
	ErrUnsupported      = precondition("catalog: the service's runtime cannot perform this action")
	// ErrHelperUnavailable refuses the actions of a managed service while
	// the deploy helper is not installed.
	ErrHelperUnavailable = precondition("catalog: the deploy helper of managed services is not available")
	// ErrNotManaged refuses actions on a retired service: it must be
	// resumed first.
	ErrNotManaged = precondition("catalog: the service is retired; resume it before acting on it")
)

// Locker serializes actions on a service across holders. Acquire must not
// wait: it returns an error wrapping ErrLockHeld when the lock is taken.
// ttl bounds how long a lock outlives a crashed holder. release is called
// once, with a context that is not cancelled with the action.
// Acquire by the owner that already holds a lock extends it for ttl; the
// manager renews the lock of a running action that way.
type Locker interface {
	Acquire(ctx context.Context, name, owner string, ttl time.Duration) (release func(context.Context) error, err error)
}

// LockName is the lock name guarding a resource.
func LockName(name string) string { return "resource:" + name }

// Runtimes resolves the adapter of a runtime; *runtime.Registry implements it.
type Runtimes interface {
	For(r model.Runtime) (rt.Adapter, error)
}

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	Catalog  *Catalog
	Runtimes Runtimes
	Runner   execx.Runner
	Locker   Locker
	Log      *slog.Logger
	// LockTTL bounds a lock held by a crashed process; default 1h. A
	// running action renews its lock every third of the TTL, so actions
	// may take longer than the TTL.
	LockTTL time.Duration
	// Git is the git binary; default "git".
	Git string
	// WorkDir holds the build checkout of every service with a source, in a
	// directory named after the resource; required, absolute. It is the
	// work directory of the data directory, which LCM alone writes to.
	WorkDir string
	// Monitor, when set, forgets a service as soon as it leaves the managed
	// phase, so that no read after ChangePhase sees its health.
	Monitor Forgetter
	// Deploy is the deploy helper that builds, installs and runs managed
	// services; nil when it is not installed, which refuses their actions.
	Deploy rt.DeployHelper
	// RestoreSettle is how long a restored service gets to become active
	// before the restore is undone; default 15s, negative checks at once.
	RestoreSettle time.Duration
}

// Forgetter drops the monitor state of a resource; *monitor.Monitor
// implements it.
type Forgetter interface {
	Forget(resource string)
}

// Manager performs lifecycle actions and phase changes on service resources.
type Manager struct {
	cat      *Catalog
	runtimes Runtimes
	runner   execx.Runner
	locker   Locker
	log      *slog.Logger
	lockTTL  time.Duration
	git      string
	workDir  string
	monitor  Forgetter
	deploy   rt.DeployHelper
	settle   time.Duration

	mu       sync.Mutex
	inflight map[string]struct{}
}

// NewManager returns a Manager.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Catalog == nil || cfg.Runtimes == nil || cfg.Runner == nil || cfg.Locker == nil {
		return nil, errors.New("catalog: manager needs Catalog, Runtimes, Runner and Locker")
	}
	if !filepath.IsAbs(cfg.WorkDir) || filepath.Clean(cfg.WorkDir) != cfg.WorkDir {
		return nil, fmt.Errorf("catalog: WorkDir %q must be a clean absolute path", cfg.WorkDir)
	}
	m := &Manager{
		cat: cfg.Catalog, runtimes: cfg.Runtimes, runner: cfg.Runner, locker: cfg.Locker,
		log: cfg.Log, lockTTL: cfg.LockTTL, git: cfg.Git, workDir: cfg.WorkDir, monitor: cfg.Monitor, deploy: cfg.Deploy,
		settle: cfg.RestoreSettle, inflight: map[string]struct{}{},
	}
	if m.settle == 0 {
		m.settle = 15 * time.Second
	}
	if m.log == nil {
		m.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if m.lockTTL <= 0 {
		m.lockTTL = time.Hour
	}
	if m.git == "" {
		m.git = "git"
	}
	return m, nil
}

// CheckoutDir returns the build checkout of the service name: LCM clones
// source.url there and deploys, backs up and runs jobs in it.
func (m *Manager) CheckoutDir(name string) string { return filepath.Join(m.workDir, name) }

// sourceInWorkDir reports whether src names a local repository inside the
// build directory: a checkout LCM moves between commits, which never gains
// commits of its own.
func (m *Manager) sourceInWorkDir(src *model.Source) bool {
	p, ok := model.LocalRepoPath(src.URL)
	return ok && model.PathWithin(p, m.workDir)
}

// ActionOptions carries the optional inputs of an action.
type ActionOptions struct {
	// RunID identifies the policy run behind the action; it becomes the lock
	// owner and the reference of timeline entries. Manual actions leave it
	// empty and are owned by the actor.
	RunID string
	// IdempotencyKey makes a retried action return its earlier result
	// instead of acting again. Nodes pass RunContext.IdempotencyKey.
	IdempotencyKey string
	// Version is the commit to deploy; empty means the head of the
	// configured branch. Deploy only.
	Version string
	// Job names the job to run. RunJob only.
	Job string
	// BackupID is the backup set to restore; empty means the newest.
	// Restore only.
	BackupID string
}

// ActionResult describes a finished action. Fields are camelCase JSON so
// the node output and the stored completion share one shape.
type ActionResult struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
	From     State  `json:"from"`
	To       State  `json:"to"`
	// Version is the deployed commit (deploy and rollback).
	Version         string `json:"version,omitempty"`
	PreviousVersion string `json:"previousVersion,omitempty"`
	DeploymentID    int64  `json:"deploymentId,omitempty"`
	DurationMs      int64  `json:"durationMs"`
	// BackupID is the set a backup made or a restore restored, and
	// PreRestoreID the copy the restore took of the data it replaced.
	BackupID     string `json:"backupId,omitempty"`
	PreRestoreID string `json:"preRestoreId,omitempty"`
	// Output is the tail of a backup or job command's output.
	Output string `json:"output,omitempty"`
	// Replayed is true when the result was returned for a repeated
	// idempotency key and nothing was executed.
	Replayed bool `json:"replayed,omitempty"`
}

// session is one action in progress: it owns the service lock and the
// in-memory busy mark until close.
type session struct {
	m       *Manager
	name    string
	action  string
	opts    ActionOptions
	actor   auth.Actor
	rec     *Record
	cfg     *model.ServiceConfig
	rt      model.Runtime
	adapter rt.Adapter
	release func(context.Context) error
	cur     State

	// renewMu guards renewal against close: once closed is set no renewal
	// runs or is scheduled.
	renewMu sync.Mutex
	renewal clock.Timer
	closed  bool
}

func (m *Manager) markBusy(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.inflight[name]; busy {
		return false
	}
	m.inflight[name] = struct{}{}
	return true
}

func (m *Manager) clearBusy(name string) {
	m.mu.Lock()
	delete(m.inflight, name)
	m.mu.Unlock()
}

func (m *Manager) isBusy(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, busy := m.inflight[name]
	return busy
}

// lock takes the busy mark and the lock of a service. The caller closes
// the session.
func (m *Manager) lock(ctx context.Context, name, action string, opts ActionOptions, actor auth.Actor) (*session, error) {
	if !m.markBusy(name) {
		return nil, fmt.Errorf("%w: another action is running on %s", ErrLockHeld, name)
	}
	s := &session{m: m, name: name, action: action, opts: opts, actor: actor}
	owner := opts.RunID
	if owner == "" {
		owner = actor.ID
	}
	release, err := m.locker.Acquire(ctx, LockName(name), owner, m.lockTTL)
	if err != nil {
		m.clearBusy(name)
		return nil, fmt.Errorf("lock service %s: %w", name, err)
	}
	s.release = release
	s.renewLater(owner)
	return s, nil
}

// renewLater schedules the next renewal of the session's lock, a third of
// the TTL from now, so that a lock expires only when its process is gone.
func (s *session) renewLater(owner string) {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	if s.closed {
		return
	}
	s.renewal = s.m.cat.clk.AfterFunc(s.m.lockTTL/3, func() { s.renew(owner) })
}

func (s *session) renew(owner string) {
	s.renewMu.Lock()
	if s.closed {
		s.renewMu.Unlock()
		return
	}
	ctx, cancel := detached(context.Background())
	_, err := s.m.locker.Acquire(ctx, LockName(s.name), owner, s.m.lockTTL)
	cancel()
	s.renewMu.Unlock()
	if err != nil {
		// The action goes on; it already runs, and stopping it halfway
		// would be worse than an overlap the operator can see.
		s.m.log.Error("renew service lock failed", "service", s.name, "action", s.action, "error", err)
	}
	s.renewLater(owner)
}

// load reads the service after the lock is held (it may have changed
// while waiting for the lock holder) and resolves its adapter.
func (s *session) load(ctx context.Context) error {
	rec, err := s.m.cat.Get(ctx, s.name)
	if err != nil {
		return err
	}
	r := rec.Runtime()
	adapter, err := s.m.runtimes.For(r)
	if err != nil {
		return err
	}
	s.rec, s.cfg, s.rt, s.adapter, s.cur = rec, rec.Config, r, adapter, rec.State
	return nil
}

// prepare reloads the locked service and checks that the action is
// allowed in its current phase and state; on failure it closes the session.
func (s *session) prepare(ctx context.Context) error {
	if err := s.load(ctx); err != nil {
		s.close()
		return err
	}
	if err := s.m.checkAction(s.action, s.rec); err != nil {
		s.close()
		return err
	}
	return nil
}

// checkAction refuses an action the phase, state or runtime does not allow,
// and the actions of a managed service while its helper is unavailable.
func (m *Manager) checkAction(action string, rec *Record) error {
	if rec.Phase != resource.PhaseManaged {
		return ErrNotManaged
	}
	if !CanAct(action, rec.State) {
		return &StateError{Resource: rec.Name, Action: action, State: rec.State}
	}
	if err := checkRuntime(action, rec.Runtime()); err != nil {
		return err
	}
	return checkHelper(action, rec.Runtime(), m.helperReady())
}

// helperReady returns nil when the deploy helper can be called, otherwise
// why not.
func (m *Manager) helperReady() error {
	if m.deploy == nil {
		return rt.ErrHelperUnavailable
	}
	return m.deploy.Ready()
}

// detached returns a context for bookkeeping that must finish even when the
// action was cancelled.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

func (s *session) close() {
	s.renewMu.Lock()
	s.closed = true
	if s.renewal != nil {
		s.renewal.Stop()
	}
	s.renewMu.Unlock()
	ctx, cancel := detached(context.Background())
	defer cancel()
	if s.release != nil {
		if err := s.release(ctx); err != nil {
			s.m.log.Error("release service lock failed", "service", s.name, "error", err)
		}
	}
	s.m.clearBusy(s.name)
}

// move changes the service's state from the one this session last saw.
func (s *session) move(ctx context.Context, to State) error {
	if err := s.m.cat.Transition(ctx, s.name, s.cur, to, TransitionInfo{Action: s.action, Ref: s.opts.RunID}); err != nil {
		return err
	}
	s.cur = to
	return nil
}

// moveDone records the state an action the runtime completed leads to. It
// outlives a cancelled action: the change has happened, and leaving it
// unrecorded would let reconciliation report a working service as failed.
func (s *session) moveDone(ctx context.Context, to State) error {
	ctx, cancel := detached(ctx)
	defer cancel()
	return s.move(ctx, to)
}

// settle leaves the service in the state a failed action warrants: running
// when the runtime says it still runs, failed otherwise.
func (s *session) settle(ctx context.Context, alwaysFailed bool) {
	ctx, cancel := detached(ctx)
	defer cancel()
	to := StateFailed
	if !alwaysFailed {
		if st, err := s.adapter.Status(ctx, s.rt); err == nil && st.State == rt.StateActive {
			to = StateRunning
		}
	}
	if s.cur == to {
		return
	}
	if err := s.move(ctx, to); err != nil {
		s.m.log.Error("settle state after failed action", "service", s.name, "action", s.action, "to", to, "error", err)
	}
}

// exec runs one action: idempotency replay, locking, state check, the
// action body, and the audit, timeline and completion records.
func (m *Manager) exec(ctx context.Context, name, action string, opts ActionOptions,
	body func(ctx context.Context, s *session, res *ActionResult) error) (ActionResult, error) {
	ctx, releaseGate, gateErr := m.cat.gate.Acquire(ctx)
	if gateErr != nil {
		return ActionResult{}, gateErr
	}
	defer releaseGate()

	actor, err := requireActor(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	// Checked without the lock first, so that a replay never waits for or
	// fails on a lock another action holds.
	if res, ok, err := m.replay(ctx, name, action, opts); err != nil || ok {
		return res, err
	}

	start := m.cat.now()
	s, err := m.lock(ctx, name, action, opts, actor)
	if err != nil {
		m.recordAudit(ctx, action, name, audit.OutcomeError, nil)
		return ActionResult{Resource: name, Action: action}, err
	}
	// The same request may have completed between the first check and the
	// lock, such as a retry delivered while the original still ran.
	if res, ok, err := m.replay(ctx, name, action, opts); err != nil || ok {
		s.close()
		return res, err
	}
	if err := s.prepare(ctx); err != nil {
		m.recordAudit(ctx, action, name, audit.OutcomeError, nil)
		return ActionResult{Resource: name, Action: action}, err
	}
	defer s.close()

	res := ActionResult{Resource: name, Action: action, From: s.cur, To: s.cur}
	err = body(ctx, s, &res)
	res.To = s.cur
	res.DurationMs = m.cat.now().Sub(start).Milliseconds()

	fctx, cancel := detached(ctx)
	defer cancel()
	details := map[string]any{"from": string(res.From), "to": string(res.To), "durationMs": res.DurationMs}
	if res.Version != "" {
		details["version"] = res.Version
	}
	if res.DeploymentID != 0 {
		details["deploymentId"] = res.DeploymentID
	}
	if opts.Job != "" {
		details["job"] = opts.Job
	}
	if res.BackupID != "" {
		details["backupId"] = res.BackupID
	}
	if res.PreRestoreID != "" {
		details["preRestoreId"] = res.PreRestoreID
	}
	if opts.RunID != "" {
		details["runId"] = opts.RunID
	}
	outcome := audit.OutcomeOK
	if err != nil {
		outcome = audit.OutcomeError
	}
	m.recordAudit(fctx, action, name, outcome, details)
	m.timelineAction(fctx, s, &res, err)

	if err != nil {
		return res, err
	}
	if opts.IdempotencyKey != "" {
		raw, jerr := json.Marshal(res)
		if jerr == nil {
			jerr = m.cat.RecordCompletion(fctx, name, idemAction(action, opts), opts.IdempotencyKey, raw)
		}
		if jerr != nil {
			// The action happened; failing the node would make a retry repeat it.
			m.log.Error("record idempotency key failed", "service", name, "action", action, "error", jerr)
		}
	}
	return res, nil
}

// replay returns the stored result of an action completed under the same
// idempotency key, if there is one.
func (m *Manager) replay(ctx context.Context, name, action string, opts ActionOptions) (ActionResult, bool, error) {
	raw, ok, err := m.cat.CompletedAction(ctx, name, idemAction(action, opts), opts.IdempotencyKey)
	if err != nil || !ok {
		return ActionResult{}, false, err
	}
	var res ActionResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return ActionResult{}, false, fmt.Errorf("catalog: decode stored result: %w", err)
	}
	if res.Resource == "" {
		// Completions stored before resources named the field service.
		res.Resource = name
	}
	res.Replayed = true
	return res, true, nil
}

// idemAction distinguishes jobs: two run-job nodes for different jobs of
// one service keep separate keys.
func idemAction(action string, opts ActionOptions) string {
	if action == ActionRunJob {
		return action + ":" + opts.Job
	}
	return action
}

func (m *Manager) recordAudit(ctx context.Context, action, name string, outcome audit.Outcome, details map[string]any) {
	if err := m.cat.audit.Record(ctx, "service."+action, name, outcome, details); err != nil {
		m.log.Error("write audit entry failed", "service", name, "action", action, "error", err)
	}
}

// timelineAction adds the entry that summarizes an action. Deployments and
// state changes write their own entries as well.
func (m *Manager) timelineAction(ctx context.Context, s *session, res *ActionResult, actionErr error) {
	kind := KindAction
	switch s.action {
	case ActionDeploy, ActionRollback:
		kind = KindDeployment
	case ActionBackup:
		kind = KindBackup
	case ActionRunJob:
		kind = KindJob
	}
	outcome := "succeeded"
	data := map[string]any{
		"action": s.action, "outcome": "succeeded", "actor": s.actor.Name, "durationMs": res.DurationMs,
		"from": string(res.From), "to": string(res.To),
	}
	if actionErr != nil {
		outcome = "failed"
		data["outcome"] = outcome
		data["error"] = headCap(actionErr.Error(), 1<<10)
	}
	if res.Version != "" {
		data["version"] = res.Version
	}
	if s.opts.Job != "" {
		data["job"] = s.opts.Job
	}
	if res.Output != "" {
		data["output"] = res.Output
	}
	if res.BackupID != "" {
		data["backupId"] = res.BackupID
	}
	if res.PreRestoreID != "" {
		data["preRestoreId"] = res.PreRestoreID
	}
	summary := s.action + " " + outcome
	if s.opts.Job != "" {
		summary = fmt.Sprintf("%s %s %s", s.action, s.opts.Job, outcome)
	}
	if res.Version != "" {
		summary += " at " + shortSHA(res.Version)
	}
	ref := s.opts.RunID
	if res.DeploymentID != 0 {
		data["deploymentId"] = res.DeploymentID
		if ref == "" {
			ref = fmt.Sprint(res.DeploymentID)
		}
	}
	if err := m.cat.AppendTimeline(ctx, Entry{Resource: s.name, Kind: kind, Summary: summary, Data: data, Ref: ref}); err != nil {
		m.log.Error("append timeline entry failed", "service", s.name, "action", s.action, "error", err)
	}
}

func shortSHA(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
