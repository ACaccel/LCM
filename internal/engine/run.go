package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/expr"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
)

// Limits on stored values.
const (
	// MaxValueBytes caps the JSON size of events, outputs and props kept
	// per node. Larger outputs are replaced by a truncation marker both in
	// storage and in memory, so a recovered run sees the same values as
	// the original one.
	MaxValueBytes = 256 << 10
	// maxErrorLen caps stored error messages.
	maxErrorLen = 4 << 10
)

// node states inside a run.
type nstate uint8

const (
	sPending nstate = iota
	sReady
	sRunning
	sRetrying
	sSuspended
	sResuming
	sCompleted
	sFailed
	sDead
)

type nodeState struct {
	status     nstate
	pendingIn  int
	liveIn     int
	port       string
	output     any
	props      any
	propsJSON  []byte
	attempts   int
	err        string
	startedAt  time.Time
	finishedAt time.Time

	suspendKey    string
	resumeAt      time.Time
	suspendOutput any
	suspendJSON   []byte
	resumed       *node.Resumed

	timer clock.Timer
	gen   uint64
}

// run is the mutable state of one execution of a plan. All fields below mu
// are guarded by it; sq and inSched belong to the scheduler.
type run struct {
	e       *Engine
	id      string
	plan    *Plan
	mode    Mode
	trigger int
	event   any
	evJSON  []byte
	created time.Time
	log     *slog.Logger
	done    chan struct{}

	sq      []int
	inSched bool

	mu         sync.Mutex
	status     RunStatus
	nodes      []nodeState
	started    time.Time
	finished   time.Time
	ctx        context.Context
	cancel     context.CancelCauseFunc
	inflight   int
	commits    int
	ready      int
	retrying   int
	suspended  int
	stopping   bool
	stopStatus RunStatus
	stopErr    string
	errMsg     string
	finalized  bool
	runTimer   clock.Timer

	// replay is non-nil while the state is rebuilt from stored rows.
	replay  map[string]nodeRow
	reached []int
	// quiet suppresses events and side effects (views of stored runs).
	quiet bool
}

func newRun(e *Engine, plan *Plan, id string, trigger int, event any, evJSON []byte, mode Mode, created time.Time) *run {
	r := &run{
		e: e, id: id, plan: plan, mode: mode, trigger: trigger, event: event, evJSON: evJSON,
		created: created, status: RunQueued, nodes: make([]nodeState, len(plan.nodes)),
		done: make(chan struct{}),
	}
	r.log = e.log.With("run", id, "policy", plan.name)
	for i, n := range plan.nodes {
		r.nodes[i].pendingIn = n.inDegree
	}
	return r
}

// emitLocked publishes an event about the run or one of its nodes (i < 0).
func (r *run) emitLocked(kind EventKind, i int, fill func(*Event)) {
	if r.quiet || r.replay != nil {
		return
	}
	ev := Event{
		Kind: kind, Time: r.e.clock.Now(), Policy: r.plan.name, Version: r.plan.version,
		RunID: r.id, Mode: r.mode, Status: string(r.status),
	}
	if i >= 0 {
		pn, ns := r.plan.nodes[i], &r.nodes[i]
		ev.NodeID, ev.NodeType, ev.Port, ev.Attempt, ev.Error = pn.id, pn.typ, ns.port, ns.attempts, ns.err
		ev.Status = string(ns.view())
	}
	if fill != nil {
		fill(&ev)
	}
	r.e.obs.emit(ev)
}

// begin starts a queued run: it stores the start and the trigger output,
// and once that is durable marks every other root dead and propagates the
// trigger's output.
func (r *run) begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized || r.status != RunQueued {
		return
	}
	now := r.e.clock.Now()
	r.status, r.started = RunRunning, now
	r.ctx, r.cancel = context.WithCancelCause(r.e.baseCtx)
	tn := r.plan.nodes[r.trigger]
	recs := []record{
		&runRec{id: r.id, status: RunRunning, startedAt: now},
		&nodeRec{runID: r.id, nodeID: tn.id, status: rowCompleted, port: node.PortOut, output: r.evJSON, startedAt: now, finishedAt: now},
	}
	r.commits++
	r.e.journal.submit(func(err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.commits--
		if err != nil {
			r.failLocked(RunFailed, "checkpoint failed: "+err.Error())
		} else if !r.stopping {
			r.initGraphLocked()
		}
		r.checkDoneLocked()
	}, recs...)
	r.emitLocked(EventRunStarted, -1, nil)
	r.armRunTimeoutLocked()
}

func (r *run) armRunTimeoutLocked() {
	if r.plan.timeout <= 0 || r.quiet {
		return
	}
	remaining := r.started.Add(r.plan.timeout).Sub(r.e.clock.Now())
	msg := fmt.Sprintf("run timed out after %s", r.plan.timeout)
	if remaining <= 0 {
		r.failLocked(RunFailed, msg)
		return
	}
	r.runTimer = r.e.clock.AfterFunc(remaining, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.failLocked(RunFailed, msg)
		r.checkDoneLocked()
	})
}

// initGraphLocked resolves the roots: the chosen trigger completes with the
// event as output, every other root (other triggers, unreachable nodes) is
// dead.
func (r *run) initGraphLocked() {
	now := r.e.clock.Now()
	for _, i := range r.plan.topo {
		if r.plan.nodes[i].inDegree != 0 {
			continue
		}
		if i != r.trigger {
			r.markDeadLocked(i)
			continue
		}
		ns := &r.nodes[i]
		ns.status, ns.port, ns.output = sCompleted, node.PortOut, r.event
		ns.attempts, ns.startedAt, ns.finishedAt = 1, r.started, now
		if row, ok := r.replay[r.plan.nodes[i].id]; ok {
			ns.startedAt, ns.finishedAt = row.startedAt, row.finishedAt
		}
		r.emitLocked(EventNodeFinished, i, nil)
		r.propagateLocked(i, node.PortOut)
	}
}

// propagateLocked resolves the outgoing edges of completed node i: edges of
// the selected port are live, all others dead (dead-path elimination).
func (r *run) propagateLocked(i int, port string) {
	for _, g := range r.plan.nodes[i].out {
		live := g.port == port
		for _, t := range g.targets {
			r.resolveEdgeLocked(t, live)
		}
	}
}

func (r *run) resolveEdgeLocked(t int, live bool) {
	ns := &r.nodes[t]
	ns.pendingIn--
	if live {
		ns.liveIn++
	}
	if ns.status != sPending {
		return // joinMode any already fired; only bookkeeping remains
	}
	switch {
	case live && r.plan.nodes[t].joinMode == model.JoinAny:
		r.reachLocked(t)
	case ns.pendingIn == 0 && ns.liveIn > 0:
		r.reachLocked(t)
	case ns.pendingIn == 0:
		r.markDeadLocked(t)
	}
}

func (r *run) markDeadLocked(t int) {
	ns := &r.nodes[t]
	ns.status = sDead
	r.emitLocked(EventNodeDead, t, nil)
	for _, g := range r.plan.nodes[t].out {
		for _, x := range g.targets {
			r.resolveEdgeLocked(x, false)
		}
	}
}

// reachLocked is called when node t's inputs allow it to run.
func (r *run) reachLocked(t int) {
	if r.replay != nil {
		r.replayReachLocked(t)
		return
	}
	if r.stopping || r.finalized {
		return
	}
	r.readyLocked(t)
}

func (r *run) readyLocked(t int) {
	r.nodes[t].status = sReady
	r.ready++
	r.e.sched.push(r, t)
}

// replayReachLocked applies the stored state of a node reached during
// recovery or a stored view. Completed nodes propagate as they did
// originally; every other reached node is collected for the caller.
func (r *run) replayReachLocked(t int) {
	pn, ns := r.plan.nodes[t], &r.nodes[t]
	row, ok := r.replay[pn.id]
	if ok && (row.status == rowCompleted || (row.status == rowFailed && row.port != "")) {
		ns.status, ns.port, ns.attempts, ns.err = sCompleted, row.port, row.attempts, row.err
		ns.output = decodeJSON(row.output)
		ns.props, ns.propsJSON = decodeJSON(row.props), row.props
		ns.startedAt, ns.finishedAt = row.startedAt, row.finishedAt
		r.propagateLocked(t, row.port)
		return
	}
	ns.status = sReady // placeholder: not counted, not queued
	r.reached = append(r.reached, t)
}

// checkDoneLocked finalizes the run once nothing can make progress.
func (r *run) checkDoneLocked() {
	if r.finalized || r.quiet || r.replay != nil || r.inflight > 0 || r.commits > 0 {
		return
	}
	if r.stopping {
		r.finalizeLocked(r.stopStatus, r.stopErr)
		return
	}
	if r.status == RunQueued || r.ready > 0 || r.retrying > 0 {
		return
	}
	if r.suspended > 0 {
		r.setStatusLocked(RunSuspended)
		return
	}
	r.finalizeLocked(RunSucceeded, "")
}

func (r *run) setStatusLocked(s RunStatus) {
	if r.status == s || r.finalized {
		return
	}
	r.status = s
	r.e.journal.submit(nil, &runRec{id: r.id, status: s})
	kind := EventRunSuspended
	if s == RunRunning {
		kind = EventRunResumed
	}
	r.emitLocked(kind, -1, nil)
}

// failLocked stops the run: no new nodes start, in-flight nodes are
// cancelled, and the run finalizes with status once they have returned.
func (r *run) failLocked(status RunStatus, msg string) {
	if r.stopping || r.finalized {
		return
	}
	r.stopping, r.stopStatus, r.stopErr = true, status, truncate(msg)
	r.stopTimersLocked()
	if r.cancel != nil {
		r.cancel(errRunStopped)
	}
}

func (r *run) stopTimersLocked() {
	if r.runTimer != nil {
		r.runTimer.Stop()
		r.runTimer = nil
	}
	for i := range r.nodes {
		if t := r.nodes[i].timer; t != nil {
			t.Stop()
			r.nodes[i].timer = nil
			r.nodes[i].gen++
		}
	}
}

func (r *run) finalizeLocked(status RunStatus, msg string) {
	now := r.e.clock.Now()
	r.finalized, r.status, r.errMsg, r.finished = true, status, msg, now
	r.stopTimersLocked()
	if r.cancel != nil {
		r.cancel(errRunStopped)
	}
	r.e.journal.submit(func(error) { r.e.runFinished(r) },
		&runRec{id: r.id, status: status, err: msg, finishedAt: now})
	r.emitLocked(EventRunFinished, -1, func(ev *Event) { ev.Error = msg })
	r.log.Debug("run finished", "status", status, "error", msg)
}

// outcome is the result of one node execution, computed outside the run
// lock.
type outcome struct {
	res        node.Result
	err        error
	cause      error
	props      any
	propsJSON  []byte
	output     any
	outputJSON []byte
}

// execute runs ready node i of r on a worker goroutine.
func (e *Engine) execute(r *run, i int) {
	pn := r.plan.nodes[i]
	r.mu.Lock()
	ns := &r.nodes[i]
	if ns.status != sReady || r.finalized || e.closing.Load() {
		// Stale entry, or shutting down: the node stays ready and recovery
		// will run it.
		r.mu.Unlock()
		return
	}
	r.ready--
	if r.stopping {
		ns.status = sPending
		r.checkDoneLocked()
		r.mu.Unlock()
		return
	}
	ns.status = sRunning
	r.inflight++
	resumed := ns.resumed
	ns.resumed = nil
	if resumed == nil {
		ns.attempts++
		ns.err = ""
	}
	attempt := ns.attempts
	now := e.clock.Now()
	if ns.startedAt.IsZero() {
		ns.startedAt = now
	}
	started := ns.startedAt
	base := r.envLocked(pn, attempt)
	ctx := r.ctx
	r.emitLocked(EventNodeStarted, i, nil)
	r.mu.Unlock()

	oc := e.invoke(ctx, r, pn, attempt, started, resumed, base)
	r.finish(i, attempt, oc)
}

// envLocked collects the expression inputs of node pn.
func (r *run) envLocked(pn *planNode, attempt int) expr.Base {
	nodes := make(map[string]any, len(pn.refs))
	for _, ref := range pn.refs {
		for _, t := range ref.targets {
			if r.nodes[t].status == sCompleted {
				nodes[ref.name] = r.nodes[t].output
				break
			}
		}
	}
	var params any = r.plan.params
	if in := pn.evalScope.input; in >= 0 {
		params = r.nodes[in].output
	}
	return expr.Base{
		Trigger: r.event, Nodes: nodes, Params: params,
		Run: expr.RunVars{ID: r.id, Policy: r.plan.name, Attempt: attempt},
	}
}

func (e *Engine) invoke(ctx context.Context, r *run, pn *planNode, attempt int, started time.Time, resumed *node.Resumed, base expr.Base) (oc outcome) {
	var extra map[string]any
	for _, name := range pn.extraVars {
		v, ok := e.vars[name]
		if !ok {
			continue
		}
		val, err := v.Value(ctx, RunInfo{RunID: r.id, Policy: r.plan.name, Version: r.plan.version, NodeID: pn.id, Mode: r.mode, Trigger: r.event})
		if err != nil {
			oc.err = fmt.Errorf("variable %s: %w", name, err)
			oc.cause = context.Cause(ctx)
			return oc
		}
		if extra == nil {
			extra = map[string]any{}
		}
		extra[name] = val
	}
	decoded, resolved, err := pn.props.resolve(expr.NewEnv(base, extra))
	if resolved != nil {
		oc.props, oc.propsJSON, _ = capValue(resolved)
	}
	if err != nil {
		// Evaluation and decoding are deterministic, so retrying is futile.
		oc.err = node.Permanent(err)
		return oc
	}

	nctx, cancel := ctx, context.CancelFunc(func() {})
	if pn.timeout > 0 {
		nctx, cancel = clock.WithTimeoutCause(ctx, e.clock, pn.timeout, errNodeTimeout)
	}
	defer cancel()

	rc := &node.RunContext{
		RunID: r.id, Policy: r.plan.name, NodeID: pn.id, Attempt: attempt,
		IdempotencyKey: r.id + "/" + pn.id, Mode: r.mode, Resumed: resumed, Clock: e.clock,
		Logger: r.log.With("node", pn.id, "type", pn.typ, "attempt", attempt),
	}
	handled := false
	if e.interceptor != nil {
		var it Interception
		it, handled = e.interceptor.Intercept(nctx, NodeCall{
			RunID: r.id, Policy: r.plan.name, Version: r.plan.version, NodeID: pn.id, Type: pn.typ,
			Effect: pn.effect, Mode: r.mode, Attempt: attempt, Props: oc.props,
		})
		if handled {
			oc.res, oc.err = it.Result, it.Err
		}
	}
	if !handled {
		switch {
		case r.mode == ModeDry && pn.effect == node.EffectWrite && !pn.def.HasDryRun():
			oc.res = node.Result{Output: map[string]any{"planned": true, "props": oc.props}}
		case r.mode == ModeDry && pn.effect == node.EffectWrite:
			oc.res, oc.err = safeRun(nctx, pn.def.DryRun, rc, decoded)
		default:
			if pn.effect == node.EffectWrite {
				// Record that a side effect may be in progress before it
				// starts, so recovery never repeats it blindly.
				err := e.journal.write(nctx, &nodeRec{
					runID: r.id, nodeID: pn.id, status: rowRunning, props: oc.propsJSON,
					attempts: attempt, startedAt: started,
				})
				if err != nil {
					oc.err = node.Permanent(fmt.Errorf("checkpoint before side effect: %w", err))
					oc.cause = context.Cause(nctx)
					return oc
				}
			}
			oc.res, oc.err = safeRun(nctx, pn.def.Run, rc, decoded)
		}
	}
	if nctx.Err() != nil {
		oc.cause = context.Cause(nctx)
	}
	if oc.err != nil {
		return oc
	}
	if s := oc.res.Suspend; s != nil {
		out, b, err := capValue(s.Output)
		if err != nil {
			oc.err = node.Permanent(fmt.Errorf("suspend output: %w", err))
			return oc
		}
		oc.output, oc.outputJSON = out, b
		return oc
	}
	out, b, err := capValue(oc.res.Output)
	if err != nil {
		oc.err = node.Permanent(fmt.Errorf("output: %w", err))
		return oc
	}
	oc.output, oc.outputJSON = out, b
	return oc
}

func safeRun(ctx context.Context, fn func(context.Context, *node.RunContext, any) (node.Result, error), rc *node.RunContext, props any) (res node.Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = node.Permanent(fmt.Errorf("node panicked: %v", p))
		}
	}()
	return fn(ctx, rc, props)
}

// finish applies an execution outcome.
func (r *run) finish(i int, attempt int, oc outcome) {
	e := r.e
	pn := r.plan.nodes[i]
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inflight--
	ns := &r.nodes[i]
	ns.props, ns.propsJSON = oc.props, oc.propsJSON

	if e.closing.Load() && errors.Is(oc.cause, errShutdown) {
		// Interrupted by shutdown: leave it to recovery.
		ns.status = sReady
		r.ready++
		return
	}
	now := e.clock.Now()
	rec := &nodeRec{
		runID: r.id, nodeID: pn.id, props: oc.propsJSON, attempts: attempt,
		startedAt: ns.startedAt, finishedAt: now,
	}
	if r.stopping || r.finalized {
		// Record what happened for the trace; nothing downstream starts.
		ns.finishedAt = now
		if oc.err != nil || oc.res.Suspend != nil {
			ns.status, ns.err = sFailed, r.stopErr
			rec.status, rec.err = rowFailed, r.stopErr
		} else {
			ns.status, ns.port, ns.output = sCompleted, portOf(oc.res), oc.output
			rec.status, rec.port, rec.output = rowCompleted, ns.port, oc.outputJSON
		}
		e.journal.submit(nil, rec)
		r.checkDoneLocked()
		return
	}

	err := oc.err
	if err == nil && oc.res.Suspend != nil && oc.res.Suspend.Key == "" && oc.res.Suspend.ResumeAt == nil {
		err = node.Permanent(errors.New("suspend requires a key or resumeAt"))
	}
	if err == nil && oc.res.Suspend == nil {
		if port := portOf(oc.res); port == node.PortError || !pn.ports[port] {
			err = node.Permanent(fmt.Errorf("node returned unknown port %q", port))
		}
	}
	switch {
	case err != nil:
		r.failNodeLocked(i, attempt, errorMessage(err, oc.cause, pn), node.IsPermanent(err), rec)
	case oc.res.Suspend != nil:
		r.suspendLocked(i, oc, rec)
	default:
		ns.status, ns.port, ns.output, ns.finishedAt, ns.err = sCompleted, portOf(oc.res), oc.output, now, ""
		rec.status, rec.port, rec.output = rowCompleted, ns.port, oc.outputJSON
		r.commitThenPropagateLocked(i, ns.port, rec)
		r.emitLocked(EventNodeFinished, i, nil)
	}
	r.checkDoneLocked()
}

func portOf(res node.Result) string {
	if res.Port == "" {
		return node.PortOut
	}
	return res.Port
}

func errorMessage(err, cause error, pn *planNode) string {
	if errors.Is(cause, errNodeTimeout) {
		return truncate(fmt.Sprintf("timed out after %s: %v", pn.timeout, err))
	}
	return truncate(err.Error())
}

// commitThenPropagateLocked stores rec and starts downstream nodes only
// after the store has committed it.
func (r *run) commitThenPropagateLocked(i int, port string, rec record) {
	r.commits++
	r.e.journal.submit(func(err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.commits--
		switch {
		case err != nil:
			r.failLocked(RunFailed, "checkpoint failed: "+err.Error())
		case !r.stopping && !r.finalized:
			r.propagateLocked(i, port)
		}
		r.checkDoneLocked()
	}, rec)
}

// failNodeLocked handles a failed attempt: retry, route to the error port,
// or fail the run.
func (r *run) failNodeLocked(i, attempt int, msg string, permanent bool, rec *nodeRec) {
	pn, ns := r.plan.nodes[i], &r.nodes[i]
	now := r.e.clock.Now()
	ns.err = msg
	rec.err = msg
	if !permanent && attempt <= pn.retry.max {
		delay := backoff(pn.retry, attempt)
		ns.status = sRetrying
		r.retrying++
		rec.status, rec.resumeAt, rec.finishedAt = rowRetrying, now.Add(delay), time.Time{}
		r.e.journal.submit(nil, rec)
		at := now.Add(delay)
		r.emitLocked(EventNodeRetrying, i, func(ev *Event) { ev.RetryAt = &at })
		r.armTimerLocked(i, delay, r.retryFired)
		return
	}
	ns.finishedAt = now
	if pn.hasErrorEdge {
		ns.status, ns.port = sCompleted, node.PortError
		ns.output = map[string]any{"message": msg, "attempts": int64(attempt)}
		out, b, _ := capValue(ns.output)
		ns.output = out
		rec.status, rec.port, rec.output = rowFailed, node.PortError, b
		r.commitThenPropagateLocked(i, node.PortError, rec)
		r.emitLocked(EventNodeFinished, i, nil)
		return
	}
	ns.status = sFailed
	rec.status = rowFailed
	r.e.journal.submit(nil, rec)
	r.emitLocked(EventNodeFinished, i, nil)
	r.failLocked(RunFailed, fmt.Sprintf("node %s failed: %s", pn.id, msg))
}

func backoff(p retryPolicy, attempt int) time.Duration {
	if p.backoff <= 0 {
		return 0
	}
	d := p.backoff
	for n := 1; n < attempt && d < p.maxBackoff; n++ {
		d *= 2
	}
	d = min(d, p.maxBackoff)
	// Equal jitter: at least half the delay, so retries never cluster at
	// zero, plus a random part to spread simultaneous failures.
	half := d / 2
	return half + rand.N(d-half+1) //nolint:gosec // jitter needs no cryptographic randomness
}

// armTimerLocked calls fire(i) after delay unless the timer is stopped or
// superseded. A non-positive delay fires immediately.
func (r *run) armTimerLocked(i int, delay time.Duration, fire func(int)) {
	ns := &r.nodes[i]
	if ns.timer != nil {
		ns.timer.Stop()
	}
	ns.gen++
	gen := ns.gen
	if delay <= 0 {
		ns.timer = nil
		fire(i)
		return
	}
	ns.timer = r.e.clock.AfterFunc(delay, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.nodes[i].gen != gen {
			return
		}
		r.nodes[i].timer = nil
		fire(i)
		r.checkDoneLocked()
	})
}

func (r *run) retryFired(i int) {
	ns := &r.nodes[i]
	if ns.status != sRetrying || r.stopping || r.finalized {
		return
	}
	r.retrying--
	r.readyLocked(i)
}

func (r *run) suspendLocked(i int, oc outcome, rec *nodeRec) {
	ns, s := &r.nodes[i], oc.res.Suspend
	ns.status, ns.suspendKey, ns.suspendOutput, ns.suspendJSON = sSuspended, s.Key, oc.output, oc.outputJSON
	ns.resumeAt = time.Time{}
	if s.ResumeAt != nil {
		ns.resumeAt = s.ResumeAt.UTC()
	}
	r.suspended++
	rec.status, rec.suspendKey, rec.resumeAt, rec.output, rec.finishedAt = rowSuspended, s.Key, ns.resumeAt, oc.outputJSON, time.Time{}
	r.e.journal.submit(nil, rec)
	r.emitLocked(EventNodeSuspended, i, func(ev *Event) {
		if !ns.resumeAt.IsZero() {
			at := ns.resumeAt
			ev.RetryAt = &at
		}
	})
	r.armResumeTimerLocked(i)
}

func (r *run) armResumeTimerLocked(i int) {
	ns := &r.nodes[i]
	if ns.resumeAt.IsZero() {
		return
	}
	r.armTimerLocked(i, ns.resumeAt.Sub(r.e.clock.Now()), r.resumeFired)
}

func (r *run) resumeFired(i int) {
	ns := &r.nodes[i]
	if ns.status != sSuspended || r.stopping || r.finalized {
		return
	}
	r.resumeLocked(i, &node.Resumed{Key: ns.suspendKey, Timer: true, Output: ns.suspendOutput})
}

// resumeLocked makes a suspended (or resuming) node ready again.
func (r *run) resumeLocked(i int, res *node.Resumed) {
	ns := &r.nodes[i]
	if ns.timer != nil {
		ns.timer.Stop()
		ns.timer = nil
		ns.gen++
	}
	r.suspended--
	ns.resumed = res
	ns.suspendKey, ns.resumeAt = "", time.Time{}
	r.setStatusLocked(RunRunning)
	r.readyLocked(i)
}

func (ns *nodeState) view() NodeStatus {
	switch ns.status {
	case sReady:
		return NodeReady
	case sRunning:
		return NodeRunning
	case sRetrying:
		return NodeRetrying
	case sSuspended, sResuming:
		return NodeSuspended
	case sCompleted:
		return NodeCompleted
	case sFailed:
		return NodeFailed
	case sDead:
		return NodeDead
	}
	return NodePending
}

// snapshotLocked builds the run's view.
func (r *run) snapshotLocked() RunView {
	v := RunView{
		ID: r.id, Policy: r.plan.name, Version: r.plan.version, Status: r.status, Mode: r.mode,
		TriggerNode: r.plan.nodes[r.trigger].id, Event: r.event, CreatedAt: r.created,
		StartedAt: timePtr(r.started), FinishedAt: timePtr(r.finished), Error: r.errMsg,
		Nodes: make([]NodeView, 0, len(r.plan.topo)),
	}
	if !r.finalized && r.stopping {
		v.Error = r.stopErr
	}
	for _, i := range r.plan.topo {
		pn, ns := r.plan.nodes[i], &r.nodes[i]
		nv := NodeView{
			ID: pn.id, Type: pn.typ, Status: ns.view(), Port: ns.port, Props: ns.props,
			Output: ns.output, Attempts: ns.attempts, Error: ns.err,
			StartedAt: timePtr(ns.startedAt), FinishedAt: timePtr(ns.finishedAt),
			SuspendKey: ns.suspendKey, ResumeAt: timePtr(ns.resumeAt),
		}
		if ns.status == sSuspended || ns.status == sResuming {
			nv.Output = ns.suspendOutput
		}
		if ns.status == sReady && r.status.Terminal() {
			nv.Status = NodePending
		}
		v.Nodes = append(v.Nodes, nv)
	}
	return v
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func truncate(s string) string {
	if len(s) <= maxErrorLen {
		return s
	}
	return s[:maxErrorLen] + "...(truncated)"
}

// capValue normalizes v and replaces it by a marker when its JSON exceeds
// MaxValueBytes.
func capValue(v any) (any, []byte, error) {
	n, b, err := model.NormalizeJSON(v)
	if err != nil {
		return nil, nil, err
	}
	if len(b) <= MaxValueBytes {
		return n, b, nil
	}
	m := map[string]any{"$truncated": true, "bytes": int64(len(b))}
	mb, err := json.Marshal(m)
	if err != nil {
		return nil, nil, err
	}
	return m, mb, nil
}

func decodeJSON(b []byte) any {
	if b == nil {
		return nil
	}
	v, err := model.DecodeJSONValue(b)
	if err != nil {
		return nil
	}
	return v
}
