package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
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
	ErrDirtyRepo        = precondition("catalog: source repository has uncommitted changes")
	ErrInvalidVersion   = precondition("catalog: version must be a commit sha of 7 to 64 hex digits")
	ErrNoRollbackTarget = precondition("catalog: no earlier successful deployment to roll back to")
	ErrNoBackup         = precondition("catalog: service has no backup command")
	ErrUnknownJob       = precondition("catalog: service has no such job")
)

// Locker serializes actions on a service across holders. Acquire must not
// wait: it returns an error wrapping ErrLockHeld when the lock is taken.
// ttl bounds how long a lock outlives a crashed holder. release is called
// once, with a context that is not cancelled with the action.
type Locker interface {
	Acquire(ctx context.Context, name, owner string, ttl time.Duration) (release func(context.Context) error, err error)
}

// LockName is the lock name guarding a service.
func LockName(service string) string { return "service:" + service }

// Runtimes resolves the adapter of a service; *runtime.Registry implements it.
type Runtimes interface {
	For(svc *model.Service) (rt.Adapter, error)
}

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	Catalog  *Catalog
	Runtimes Runtimes
	Runner   execx.Runner
	Locker   Locker
	Log      *slog.Logger
	// LockTTL bounds a lock held by a crashed process; default 1h. It must
	// exceed the longest action.
	LockTTL time.Duration
	// Git is the git binary; default "git".
	Git string
}

// Manager performs lifecycle actions on services.
type Manager struct {
	cat      *Catalog
	runtimes Runtimes
	runner   execx.Runner
	locker   Locker
	log      *slog.Logger
	lockTTL  time.Duration
	git      string

	mu       sync.Mutex
	inflight map[string]struct{}
}

// NewManager returns a Manager.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Catalog == nil || cfg.Runtimes == nil || cfg.Runner == nil || cfg.Locker == nil {
		return nil, errors.New("catalog: manager needs Catalog, Runtimes, Runner and Locker")
	}
	m := &Manager{
		cat: cfg.Catalog, runtimes: cfg.Runtimes, runner: cfg.Runner, locker: cfg.Locker,
		log: cfg.Log, lockTTL: cfg.LockTTL, git: cfg.Git, inflight: map[string]struct{}{},
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
}

// ActionResult describes a finished action. Fields are camelCase JSON so
// the node output and the stored completion share one shape.
type ActionResult struct {
	Service string `json:"service"`
	Action  string `json:"action"`
	From    State  `json:"from"`
	To      State  `json:"to"`
	// Version is the deployed commit (deploy and rollback).
	Version         string `json:"version,omitempty"`
	PreviousVersion string `json:"previousVersion,omitempty"`
	DeploymentID    int64  `json:"deploymentId,omitempty"`
	DurationMs      int64  `json:"durationMs"`
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
	svc     *model.Service
	adapter rt.Adapter
	release func(context.Context) error
	cur     State
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

// open takes the busy mark and the lock, reloads the service and checks
// that the action is allowed in its current state.
func (m *Manager) open(ctx context.Context, name, action string, opts ActionOptions, actor auth.Actor) (*session, error) {
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

	// The state is read only after the lock is held: it may have changed
	// while waiting for the lock holder.
	rec, err := m.cat.Get(ctx, name)
	if err != nil {
		s.close()
		return nil, err
	}
	if !CanAct(action, rec.State) {
		s.close()
		return nil, &StateError{Service: name, Action: action, State: rec.State}
	}
	adapter, err := m.runtimes.For(rec.Spec)
	if err != nil {
		s.close()
		return nil, err
	}
	s.rec, s.svc, s.adapter, s.cur = rec, rec.Spec, adapter, rec.State
	return s, nil
}

// detached returns a context for bookkeeping that must finish even when the
// action was cancelled.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

func (s *session) close() {
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

// settle leaves the service in the state a failed action warrants: running
// when the runtime says it still runs, failed otherwise.
func (s *session) settle(ctx context.Context, alwaysFailed bool) {
	ctx, cancel := detached(ctx)
	defer cancel()
	to := StateFailed
	if !alwaysFailed {
		if st, err := s.adapter.Status(ctx, s.svc); err == nil && st.State == rt.StateActive {
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

	actor, err := requireActor(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	if raw, ok, err := m.cat.CompletedAction(ctx, name, idemAction(action, opts), opts.IdempotencyKey); err != nil {
		return ActionResult{}, err
	} else if ok {
		var res ActionResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return ActionResult{}, fmt.Errorf("catalog: decode stored result: %w", err)
		}
		res.Replayed = true
		return res, nil
	}

	start := m.cat.now()
	s, err := m.open(ctx, name, action, opts, actor)
	if err != nil {
		m.recordAudit(ctx, action, name, audit.OutcomeError, nil)
		return ActionResult{Service: name, Action: action}, err
	}
	defer s.close()

	res := ActionResult{Service: name, Action: action, From: s.cur, To: s.cur}
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
	if err := m.cat.AppendTimeline(ctx, Entry{Service: s.name, Kind: kind, Summary: summary, Data: data, Ref: ref}); err != nil {
		m.log.Error("append timeline entry failed", "service", s.name, "action", s.action, "error", err)
	}
}

func shortSHA(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
