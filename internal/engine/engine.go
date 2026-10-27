package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/expr"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/store"
)

// Config configures an Engine.
type Config struct {
	DB       *store.DB
	Registry *node.Registry
	// Modules resolves module references at deploy and validate time.
	Modules ModuleResolver
	// Workers is the size of the shared worker pool; 0 selects
	// max(GOMAXPROCS*2, 4).
	Workers int
	// Clock defaults to the real clock.
	Clock  clock.Clock
	Logger *slog.Logger
	// Audit defaults to an audit log on DB using Clock.
	Audit     *audit.Log
	Observers []Observer
	// Interceptor, when set, is consulted before every node execution.
	Interceptor Interceptor
	// Vars are extra top-level expression variables.
	Vars []Var
}

// DefaultWorkers returns the default worker pool size.
func DefaultWorkers() int { return max(runtime.GOMAXPROCS(0)*2, 4) }

type deployment struct {
	plan   *Plan
	paused bool
}

type planKey struct {
	name    string
	version int
}

// group tracks the runs of one policy for its concurrency mode.
type group struct {
	active map[string]*run
	queue  []*run
}

// Engine executes policies. See the package documentation for semantics.
type Engine struct {
	db          *store.DB
	reg         *node.Registry
	modules     ModuleResolver
	clock       clock.Clock
	log         *slog.Logger
	audit       *audit.Log
	interceptor Interceptor
	vars        map[string]Var
	varNames    []string

	sched   *scheduler
	journal *journal
	obs     *observers
	ids     idGen
	workers sync.WaitGroup

	baseCtx    context.Context
	baseCancel context.CancelCauseFunc
	closing    atomic.Bool
	closed     chan struct{}
	closeOnce  sync.Once
	recovered  atomic.Bool

	mu       sync.Mutex
	deployed map[string]*deployment
	plans    map[planKey]*Plan
	groups   map[string]*group
	runs     map[string]*run
}

// New starts an engine: its worker pool, checkpoint journal and observer
// dispatchers. Call Recover once before starting runs, and Close to stop.
func New(cfg Config) (*Engine, error) {
	if cfg.DB == nil {
		return nil, errors.New("engine: DB is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("engine: Registry is required")
	}
	if cfg.Workers < 0 {
		return nil, fmt.Errorf("engine: workers %d: must not be negative", cfg.Workers)
	}
	if cfg.Workers == 0 {
		cfg.Workers = DefaultWorkers()
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Audit == nil {
		cfg.Audit = audit.New(cfg.DB, audit.WithClock(cfg.Clock.Now))
	}
	vars := map[string]Var{}
	var names []string
	for _, v := range cfg.Vars {
		switch {
		case v.Value == nil:
			return nil, fmt.Errorf("engine: variable %q has no Value function", v.Name)
		case slices.Contains(expr.BuiltinVars, v.Name):
			return nil, fmt.Errorf("engine: variable %q is built in", v.Name)
		case !paramIdent(v.Name):
			return nil, fmt.Errorf("engine: variable %q is not an identifier", v.Name)
		}
		if _, dup := vars[v.Name]; dup {
			return nil, fmt.Errorf("engine: duplicate variable %q", v.Name)
		}
		vars[v.Name] = v
		names = append(names, v.Name)
	}
	e := &Engine{
		db: cfg.DB, reg: cfg.Registry, modules: cfg.Modules, clock: cfg.Clock, log: cfg.Logger,
		audit: cfg.Audit, interceptor: cfg.Interceptor, vars: vars, varNames: names,
		sched: newScheduler(), journal: newJournal(cfg.DB, cfg.Logger),
		obs:    newObservers(cfg.Observers, cfg.Logger),
		closed: make(chan struct{}), deployed: map[string]*deployment{}, plans: map[planKey]*Plan{},
		groups: map[string]*group{}, runs: map[string]*run{},
	}
	e.baseCtx, e.baseCancel = context.WithCancelCause(context.Background())
	for range cfg.Workers {
		e.workers.Add(1)
		go e.worker()
	}
	return e, nil
}

func paramIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func (e *Engine) worker() {
	defer e.workers.Done()
	for {
		r, i, ok := e.sched.pop()
		if !ok {
			return
		}
		e.execute(r, i)
	}
}

func (e *Engine) compileOptions(mods ModuleResolver) CompileOptions {
	return CompileOptions{Registry: e.reg, Modules: mods, Vars: e.varNames}
}

// Validate decodes and compiles a policy without side effects and returns
// its warnings.
func (e *Engine) Validate(ctx context.Context, doc []byte) (model.Issues, error) {
	p, err := decodeDeployable(doc)
	if err != nil {
		return nil, err
	}
	plan, err := Compile(ctx, p, e.compileOptions(e.modules))
	if err != nil {
		return nil, err
	}
	return plan.warnings, nil
}

func decodeDeployable(doc []byte) (*model.Policy, error) {
	d, err := model.Decode(doc)
	if err != nil {
		return nil, err
	}
	p, ok := d.(*model.Policy)
	if !ok {
		return nil, ErrNotPolicy
	}
	return p, nil
}

func actorFrom(ctx context.Context) (auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return auth.Actor{}, audit.ErrNoActor
	}
	return a, nil
}

// record writes an audit entry; a failure is logged because the action has
// already happened.
func (e *Engine) record(ctx context.Context, action, target string, err error, details map[string]any) {
	outcome := audit.OutcomeOK
	if err != nil {
		outcome = audit.OutcomeError
		if details == nil {
			details = map[string]any{}
		}
		var is model.Issues
		if errors.As(err, &is) {
			// Issue messages may quote document content; record only
			// their number.
			details["issues"] = len(is)
		} else {
			details["error"] = err.Error()
		}
	}
	if aerr := e.audit.Record(ctx, action, target, outcome, details); aerr != nil {
		e.log.Error("audit record failed", "action", action, "target", target, "error", aerr)
	}
}

// Deploy validates, compiles and stores a policy document and makes it the
// deployed version. Deploying a document identical to the latest version
// (including the modules it uses) creates no new version. The actor comes
// from ctx.
func (e *Engine) Deploy(ctx context.Context, doc []byte) (int, error) {
	if _, err := actorFrom(ctx); err != nil {
		return 0, err
	}
	version, name, err := e.deploy(ctx, doc)
	target := "policy:" + name
	if name == "" {
		target = "policy"
	}
	e.record(ctx, "policy.deploy", target, err, map[string]any{"version": version})
	return version, err
}

func (e *Engine) deploy(ctx context.Context, doc []byte) (int, string, error) {
	if e.closing.Load() {
		return 0, "", ErrClosed
	}
	actor, _ := auth.ActorFrom(ctx)
	p, err := decodeDeployable(doc)
	if err != nil {
		return 0, "", err
	}
	plan, err := Compile(ctx, p, e.compileOptions(e.modules))
	if err != nil {
		return 0, p.Metadata.Name, err
	}
	version, _, paused, err := storeVersion(ctx, e.db, plan, actor, e.clock.Now())
	if err != nil {
		return 0, plan.name, err
	}
	plan.version = version
	e.mu.Lock()
	e.deployed[plan.name] = &deployment{plan: plan, paused: paused}
	e.plans[planKey{plan.name, version}] = plan
	e.mu.Unlock()
	e.obs.emit(Event{Kind: EventPolicyDeployed, Time: e.clock.Now(), Policy: plan.name, Version: version})
	return version, plan.name, nil
}

// Undeploy stops accepting events for a policy and cancels its queued
// runs; active runs finish normally. Stored versions are kept.
func (e *Engine) Undeploy(ctx context.Context, name string) error {
	if _, err := actorFrom(ctx); err != nil {
		return err
	}
	err := e.undeploy(ctx, name)
	e.record(ctx, "policy.undeploy", "policy:"+name, err, nil)
	return err
}

func (e *Engine) undeploy(ctx context.Context, name string) error {
	if e.closing.Load() {
		return ErrClosed
	}
	var n int64
	err := e.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM policy_deployments WHERE name = ?`, name)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("engine: undeploy: %w", err)
	}
	if n == 0 {
		return ErrPolicyNotDeployed
	}
	e.mu.Lock()
	delete(e.deployed, name)
	if g := e.groups[name]; g != nil {
		queued := g.queue
		g.queue = nil
		for _, r := range queued {
			r.mu.Lock()
			r.finalizeLocked(RunCancelled, "policy undeployed")
			r.mu.Unlock()
		}
	}
	e.mu.Unlock()
	e.obs.emit(Event{Kind: EventPolicyUndeployed, Time: e.clock.Now(), Policy: name})
	return nil
}

// SetPaused pauses or unpauses a deployed policy. A paused policy rejects
// new events with ErrPolicyPaused; runs already started continue.
func (e *Engine) SetPaused(ctx context.Context, name string, paused bool) error {
	if _, err := actorFrom(ctx); err != nil {
		return err
	}
	action := "policy.unpause"
	if paused {
		action = "policy.pause"
	}
	err := e.setPaused(ctx, name, paused)
	e.record(ctx, action, "policy:"+name, err, nil)
	return err
}

func (e *Engine) setPaused(ctx context.Context, name string, paused bool) error {
	var n int64
	err := e.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE policy_deployments SET paused = ? WHERE name = ?`, paused, name)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("engine: set paused: %w", err)
	}
	if n == 0 {
		return ErrPolicyNotDeployed
	}
	e.mu.Lock()
	if d := e.deployed[name]; d != nil {
		d.paused = paused
	}
	e.mu.Unlock()
	return nil
}

// Plan returns the deployed plan of a policy.
func (e *Engine) Plan(name string) (*Plan, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.deployed[name]
	if !ok {
		return nil, false
	}
	return d.plan, true
}

// Plans returns all deployed plans sorted by name.
func (e *Engine) Plans() []*Plan {
	e.mu.Lock()
	out := make([]*Plan, 0, len(e.deployed))
	for _, d := range e.deployed {
		out = append(out, d.plan)
	}
	e.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (e *Engine) groupLocked(name string) *group {
	g := e.groups[name]
	if g == nil {
		g = &group{active: map[string]*run{}}
		e.groups[name] = g
	}
	return g
}

// limitLocked returns the number of runs the policy may have active. The
// deployed version decides; a policy that is no longer deployed keeps the
// limit of the run's own version.
func (e *Engine) limitLocked(name string, fallback *Plan) int {
	if d := e.deployed[name]; d != nil {
		return d.plan.concurrencyLimit()
	}
	return fallback.concurrencyLimit()
}

// Start begins a run of a deployed policy at the given trigger node with
// event as its payload, honouring the policy's concurrency mode. It returns
// the run id once the run is stored. In drop mode an event that arrives
// while a run is active is stored as a dropped run and Start returns its id
// with ErrDropped. Start writes an audit entry when ctx carries an actor
// (calls made on behalf of a person or token); trigger subsystems call it
// with a context without an actor.
func (e *Engine) Start(ctx context.Context, policy, triggerNode string, event any, mode Mode) (string, error) {
	id, err := e.start(ctx, policy, triggerNode, event, mode)
	if _, ok := auth.ActorFrom(ctx); ok {
		target := "policy:" + policy
		if id != "" {
			target = "run:" + id
		}
		e.record(ctx, "run.start", target, err, map[string]any{"policy": policy, "trigger": triggerNode, "mode": string(mode)})
	}
	return id, err
}

func (e *Engine) start(ctx context.Context, policy, triggerNode string, event any, mode Mode) (string, error) {
	if e.closing.Load() {
		return "", ErrClosed
	}
	if !mode.Valid() {
		return "", ErrInvalidMode
	}
	ev, evJSON, err := model.NormalizeJSON(event)
	if err != nil {
		return "", fmt.Errorf("engine: event: %w", err)
	}
	if len(evJSON) > MaxValueBytes {
		return "", fmt.Errorf("%w: event is %d bytes, limit %d", ErrValueTooLarge, len(evJSON), MaxValueBytes)
	}

	e.mu.Lock()
	d := e.deployed[policy]
	if d == nil {
		e.mu.Unlock()
		return "", ErrPolicyNotDeployed
	}
	if d.paused {
		e.mu.Unlock()
		return "", ErrPolicyPaused
	}
	plan := d.plan
	ti, ok := plan.index[triggerNode]
	if !ok || !plan.nodes[ti].trigger {
		e.mu.Unlock()
		return "", fmt.Errorf("%w: %q", ErrUnknownTrigger, triggerNode)
	}
	now := e.clock.Now()
	id := e.ids.next(now)
	g := e.groupLocked(policy)
	row := runRow{id: id, policy: policy, version: plan.version, status: RunQueued, triggerNode: triggerNode,
		event: evJSON, mode: mode, createdAt: now}

	startNow := false
	var replaced []*run
	switch plan.mode {
	case model.ConcurrencyDrop:
		if len(g.active)+len(g.queue) > 0 {
			row.status, row.finishedAt, row.err = RunDropped, now, "dropped: a run of this policy is active"
		} else {
			startNow = true
		}
	case model.ConcurrencyReplace:
		for _, r := range g.active {
			replaced = append(replaced, r)
		}
		startNow = len(g.active) == 0
	default:
		startNow = len(g.queue) == 0 && len(g.active) < e.limitLocked(policy, plan)
	}
	if err := insertRun(ctx, e.db, row); err != nil {
		e.mu.Unlock()
		return "", err
	}
	if row.status == RunDropped {
		e.mu.Unlock()
		e.obs.emit(Event{Kind: EventRunFinished, Time: now, Policy: policy, Version: plan.version, RunID: id,
			Mode: mode, Status: string(RunDropped), Error: row.err})
		return id, ErrDropped
	}
	r := newRun(e, plan, id, ti, ev, evJSON, mode, now)
	e.runs[id] = r
	if plan.mode == model.ConcurrencyReplace {
		// The newest event wins: older queued events are replaced too.
		for _, q := range g.queue {
			q.mu.Lock()
			q.finalizeLocked(RunCancelled, "replaced by run "+id)
			q.mu.Unlock()
		}
		g.queue = nil
	}
	if startNow {
		g.active[id] = r
	} else {
		g.queue = append(g.queue, r)
	}
	for _, old := range replaced {
		old.mu.Lock()
		old.failLocked(RunCancelled, "replaced by run "+id)
		old.checkDoneLocked()
		old.mu.Unlock()
	}
	r.mu.Lock()
	r.emitLocked(EventRunQueued, -1, nil)
	r.mu.Unlock()
	e.mu.Unlock()
	if startNow {
		r.begin()
	}
	return id, nil
}

// runFinished is called once a run's final status is stored.
func (e *Engine) runFinished(r *run) {
	e.mu.Lock()
	g := e.groupLocked(r.plan.name)
	delete(g.active, r.id)
	if i := slices.Index(g.queue, r); i >= 0 {
		g.queue = slices.Delete(g.queue, i, i+1)
	}
	delete(e.runs, r.id)
	next := e.fillLocked(g, r.plan)
	e.mu.Unlock()
	close(r.done)
	for _, n := range next {
		n.begin()
	}
}

// fillLocked moves queued runs to active while the limit allows.
func (e *Engine) fillLocked(g *group, fallback *Plan) []*run {
	if e.closing.Load() {
		return nil
	}
	var next []*run
	for len(g.queue) > 0 && len(g.active) < e.limitLocked(fallback.name, g.queue[0].plan) {
		r := g.queue[0]
		g.queue = g.queue[1:]
		g.active[r.id] = r
		next = append(next, r)
	}
	return next
}

// Resume resumes a suspended node whose suspension key matches. payload is
// passed to the node as RunContext.Resumed.Payload and stored before Resume
// returns, so a resume survives a restart. The actor comes from ctx.
func (e *Engine) Resume(ctx context.Context, runID, nodeID, key string, payload any) error {
	if _, err := actorFrom(ctx); err != nil {
		return err
	}
	err := e.resume(ctx, runID, nodeID, key, payload)
	e.record(ctx, "run.resume", "run:"+runID, err, map[string]any{"node": nodeID})
	return err
}

func (e *Engine) resume(ctx context.Context, runID, nodeID, key string, payload any) error {
	if e.closing.Load() {
		return ErrClosed
	}
	pl, plJSON, err := model.NormalizeJSON(payload)
	if err != nil {
		return fmt.Errorf("engine: resume payload: %w", err)
	}
	if len(plJSON) > MaxValueBytes {
		return fmt.Errorf("%w: resume payload", ErrValueTooLarge)
	}
	r, err := e.activeRun(ctx, runID)
	if err != nil {
		return err
	}
	i, ok := r.plan.index[nodeID]
	if !ok {
		return fmt.Errorf("%w: unknown node %q", ErrNotSuspended, nodeID)
	}
	r.mu.Lock()
	ns := &r.nodes[i]
	switch {
	case r.stopping || r.finalized:
		r.mu.Unlock()
		return ErrRunNotActive
	case ns.status != sSuspended:
		r.mu.Unlock()
		return ErrNotSuspended
	case ns.suspendKey == "" || ns.suspendKey != key:
		r.mu.Unlock()
		return ErrKeyMismatch
	}
	// sResuming blocks a second Resume and the resume timer while the
	// payload is being stored.
	ns.status = sResuming
	pn := r.plan.nodes[i]
	rec := &nodeRec{
		runID: r.id, nodeID: pn.id, status: rowResumed, props: ns.propsJSON, output: ns.suspendJSON,
		attempts: ns.attempts, startedAt: ns.startedAt, suspendKey: ns.suspendKey, resumePayload: plJSON,
	}
	resumed := &node.Resumed{Key: key, Payload: pl, Output: ns.suspendOutput}
	r.mu.Unlock()

	// Wait for the commit even if ctx ends, so memory and storage agree on
	// whether the resume happened.
	werr := e.journal.write(context.WithoutCancel(ctx), rec)

	r.mu.Lock()
	defer r.mu.Unlock()
	if ns.status != sResuming {
		return ErrRunNotActive
	}
	if werr != nil {
		ns.status = sSuspended
		// The resume timer may have fired (and been skipped) meanwhile.
		r.armResumeTimerLocked(i)
		return fmt.Errorf("engine: store resume: %w", werr)
	}
	ns.status = sSuspended
	if r.stopping || r.finalized {
		return ErrRunNotActive
	}
	r.resumeLocked(i, resumed)
	return nil
}

func (e *Engine) activeRun(ctx context.Context, id string) (*run, error) {
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	if r != nil {
		return r, nil
	}
	if _, err := loadRun(ctx, e.db, id); err != nil {
		return nil, err
	}
	return nil, ErrRunNotActive
}

// Cancel stops a queued or active run: in-flight nodes are cancelled and
// the run ends as cancelled. The actor comes from ctx.
func (e *Engine) Cancel(ctx context.Context, runID string) error {
	actor, err := actorFrom(ctx)
	if err != nil {
		return err
	}
	err = e.cancelRun(ctx, runID, "cancelled by "+actor.Name)
	e.record(ctx, "run.cancel", "run:"+runID, err, nil)
	return err
}

func (e *Engine) cancelRun(ctx context.Context, runID, reason string) error {
	if e.closing.Load() {
		return ErrClosed
	}
	r, err := e.activeRun(ctx, runID)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	g := e.groupLocked(r.plan.name)
	if i := slices.Index(g.queue, r); i >= 0 {
		g.queue = slices.Delete(g.queue, i, i+1)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized || r.stopping {
		return ErrRunNotActive
	}
	if r.status == RunQueued {
		r.finalizeLocked(RunCancelled, reason)
		return nil
	}
	r.failLocked(RunCancelled, reason)
	r.checkDoneLocked()
	return nil
}

// Run returns the status and decision trace of a run.
func (e *Engine) Run(ctx context.Context, runID string) (RunView, error) {
	e.mu.Lock()
	r := e.runs[runID]
	e.mu.Unlock()
	if r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.snapshotLocked(), nil
	}
	return e.storedView(ctx, runID)
}

// Wait blocks until the run reaches a terminal status (or ctx ends, or the
// engine closes) and returns its view.
func (e *Engine) Wait(ctx context.Context, runID string) (RunView, error) {
	e.mu.Lock()
	r := e.runs[runID]
	e.mu.Unlock()
	if r != nil {
		select {
		case <-r.done:
		case <-ctx.Done():
			return RunView{}, ctx.Err()
		case <-e.closed:
			return RunView{}, ErrClosed
		}
	}
	return e.Run(ctx, runID)
}

// ObserverDrops returns how many events observers have missed because
// their buffers were full.
func (e *Engine) ObserverDrops() uint64 { return e.obs.dropped() }

// Prune deletes terminal runs of each policy except the newest keep runs
// and runs created within olderThan of now. It returns the number of runs
// deleted.
func (e *Engine) Prune(ctx context.Context, keep int, olderThan time.Duration) (int64, error) {
	if keep < 0 || olderThan < 0 {
		return 0, errors.New("engine: prune: keep and olderThan must not be negative")
	}
	cutoff := e.clock.Now().Add(-olderThan).UnixMilli()
	var n int64
	err := e.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM runs
			WHERE status NOT IN ('queued', 'running', 'suspended') AND created_at < ?
			AND id NOT IN (SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY policy ORDER BY id DESC) AS rn FROM runs
			) WHERE rn <= ?)`, cutoff, keep)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("engine: prune: %w", err)
	}
	return n, nil
}

// Close stops accepting work, waits for in-flight node executions to
// return, and flushes checkpoints. When ctx ends first, in-flight nodes are
// cancelled; nodes must honour their context for Close to return. Runs keep
// their stored state and continue after Recover in the next process.
func (e *Engine) Close(ctx context.Context) error {
	var err error
	e.closeOnce.Do(func() {
		e.closing.Store(true)
		e.sched.stop()
		idle := make(chan struct{})
		go func() {
			e.workers.Wait()
			close(idle)
		}()
		select {
		case <-idle:
		case <-ctx.Done():
			err = ctx.Err()
			e.baseCancel(errShutdown)
			<-idle
		}
		e.mu.Lock()
		for _, r := range e.runs {
			r.mu.Lock()
			r.stopTimersLocked()
			r.mu.Unlock()
		}
		e.mu.Unlock()
		e.baseCancel(errShutdown)
		e.journal.close()
		close(e.closed)
		e.obs.close(ctx.Done())
	})
	return err
}
