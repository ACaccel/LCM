package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
)

const interruptedMsg = "interrupted during side effect: the process stopped while this write node was running, so it was not re-executed"

// planFor returns the plan of a stored policy version, compiling it from
// the stored document and module snapshot on first use.
func (e *Engine) planFor(ctx context.Context, name string, version int) (*Plan, error) {
	key := planKey{name, version}
	e.mu.Lock()
	p := e.plans[key]
	e.mu.Unlock()
	if p != nil {
		return p, nil
	}
	v, err := loadVersion(ctx, e.db, name, version)
	if err != nil {
		return nil, err
	}
	p, err = e.compileStored(ctx, v)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if cached := e.plans[key]; cached != nil {
		p = cached
	} else {
		e.plans[key] = p
	}
	e.mu.Unlock()
	return p, nil
}

func (e *Engine) compileStored(ctx context.Context, v versionRow) (*Plan, error) {
	pol, mods, err := decodeStoredPolicy(v)
	if err != nil {
		return nil, err
	}
	p, err := Compile(ctx, pol, e.compileOptions(mods))
	if err != nil {
		return nil, fmt.Errorf("engine: recompile %s v%d: %w", v.name, v.version, err)
	}
	p.version = v.version
	return p, nil
}

// Recover restores state at startup: deployed policies are recompiled from
// their stored versions, and every run that was queued, running or
// suspended is rebuilt from its checkpoints. Depending on the policy's
// onInterrupt, a running or suspended run is either resumed or marked
// interrupted (queued runs always stay queued). When resuming:
//
//   - completed nodes are not re-executed; their stored outputs feed
//     downstream expressions as before;
//   - nodes that were ready, or running with effect none or read, run
//     again;
//   - a write node that was running is not re-executed: it fails with an
//     "interrupted during side effect" error, which its error port can
//     handle;
//   - suspended nodes keep waiting and their resume timers are re-armed;
//     retry timers are re-armed; stored resumes are applied.
//
// Recover runs at most once. Policies or runs that cannot be restored are
// reported in the returned error while the rest are recovered.
func (e *Engine) Recover(ctx context.Context) error {
	if !e.recovered.CompareAndSwap(false, true) {
		return ErrAlreadyRecovered
	}
	if e.closing.Load() {
		return ErrClosed
	}
	var errs []error
	deps, err := loadDeployments(ctx, e.db)
	if err != nil {
		return err
	}
	for _, d := range deps {
		p, err := e.compileStored(ctx, d.versionRow)
		if err != nil {
			errs = append(errs, err)
			e.log.Error("policy not restored", "policy", d.name, "version", d.version, "error", err)
			continue
		}
		e.mu.Lock()
		e.deployed[p.name] = &deployment{plan: p, paused: d.paused}
		e.plans[planKey{p.name, p.version}] = p
		e.mu.Unlock()
	}

	rows, err := loadActiveRuns(ctx, e.db)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, rr := range rows {
		if err := e.recoverRun(ctx, rr); err != nil {
			errs = append(errs, fmt.Errorf("engine: recover run %s: %w", rr.id, err))
		}
	}

	e.mu.Lock()
	var next []*run
	for _, g := range e.groups {
		if len(g.queue) > 0 {
			next = append(next, e.fillLocked(g, g.queue[0].plan)...)
		}
	}
	e.mu.Unlock()
	for _, r := range next {
		r.begin()
	}
	return errors.Join(errs...)
}

func (e *Engine) recoverRun(ctx context.Context, rr runRow) error {
	now := e.clock.Now()
	plan, err := e.planFor(ctx, rr.policy, rr.version)
	if err != nil {
		msg := "recovery failed: " + err.Error()
		if werr := e.journal.write(ctx, &runRec{id: rr.id, status: RunFailed, err: truncate(msg), finishedAt: now}); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	ti, ok := plan.index[rr.triggerNode]
	if !ok {
		return fmt.Errorf("trigger node %q not in plan", rr.triggerNode)
	}
	ev := decodeJSON(rr.event)
	r := newRun(e, plan, rr.id, ti, ev, rr.event, rr.mode, rr.createdAt)

	if rr.status == RunQueued {
		e.mu.Lock()
		g := e.groupLocked(plan.name)
		g.queue = append(g.queue, r)
		e.runs[r.id] = r
		e.mu.Unlock()
		return nil
	}
	if plan.onInterrupt == model.OnInterruptAbort {
		return e.journal.write(ctx, &runRec{id: rr.id, status: RunInterrupted, err: "interrupted: the process stopped while the run was active", finishedAt: now})
	}
	nodeRows, err := loadNodeRows(ctx, e.db, rr.id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.groupLocked(plan.name).active[r.id] = r
	e.runs[r.id] = r
	e.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.status, r.started = rr.status, rr.startedAt
	if r.started.IsZero() {
		r.started = rr.createdAt
	}
	r.ctx, r.cancel = context.WithCancelCause(e.baseCtx)
	r.replay = nodeRows
	r.initGraphLocked()
	r.replay = nil
	reached := r.reached
	r.reached = nil
	for _, i := range reached {
		r.restoreLocked(i, nodeRows)
	}
	r.armRunTimeoutLocked()
	r.log.Info("run recovered", "status", r.status, "nodes", len(nodeRows))
	r.checkDoneLocked()
	return nil
}

// restoreLocked continues node i of a recovered run from its stored row.
func (r *run) restoreLocked(i int, rows map[string]nodeRow) {
	pn, ns := r.plan.nodes[i], &r.nodes[i]
	row, ok := rows[pn.id]
	if !ok {
		r.readyLocked(i)
		return
	}
	ns.attempts, ns.err, ns.startedAt = row.attempts, row.err, row.startedAt
	ns.props, ns.propsJSON = decodeJSON(row.props), row.props
	switch row.status {
	case rowRunning:
		if pn.effect != node.EffectWrite {
			r.readyLocked(i)
			return
		}
		rec := &nodeRec{runID: r.id, nodeID: pn.id, props: row.props, attempts: row.attempts,
			startedAt: row.startedAt, finishedAt: r.e.clock.Now()}
		// Permanent: retrying would repeat the side effect.
		r.failNodeLocked(i, row.attempts, interruptedMsg, true, rec)
	case rowRetrying:
		ns.status = sRetrying
		r.retrying++
		r.armTimerLocked(i, row.resumeAt.Sub(r.e.clock.Now()), r.retryFired)
	case rowSuspended:
		ns.status, ns.suspendKey, ns.resumeAt = sSuspended, row.suspendKey, row.resumeAt
		ns.suspendOutput, ns.suspendJSON = decodeJSON(row.output), row.output
		r.suspended++
		r.armResumeTimerLocked(i)
	case rowResumed:
		ns.suspendOutput, ns.suspendJSON = decodeJSON(row.output), row.output
		ns.resumed = &node.Resumed{Key: row.suspendKey, Payload: decodeJSON(row.resumePayload), Output: ns.suspendOutput}
		r.readyLocked(i)
	case rowFailed:
		ns.status, ns.finishedAt = sFailed, row.finishedAt
		r.failLocked(RunFailed, fmt.Sprintf("node %s failed: %s", pn.id, row.err))
	default:
		r.readyLocked(i)
	}
}

// storedView builds a run view from storage for runs not held in memory.
func (e *Engine) storedView(ctx context.Context, runID string) (RunView, error) {
	rr, err := loadRun(ctx, e.db, runID)
	if err != nil {
		return RunView{}, err
	}
	plan, err := e.planFor(ctx, rr.policy, rr.version)
	if err != nil {
		return RunView{}, err
	}
	ti, ok := plan.index[rr.triggerNode]
	if !ok {
		return RunView{}, fmt.Errorf("engine: run %s: trigger node %q not in plan", runID, rr.triggerNode)
	}
	rows, err := loadNodeRows(ctx, e.db, runID)
	if err != nil {
		return RunView{}, err
	}
	r := newRun(e, plan, rr.id, ti, decodeJSON(rr.event), rr.event, rr.mode, rr.createdAt)
	r.quiet = true
	r.status, r.started, r.finished, r.errMsg = rr.status, rr.startedAt, rr.finishedAt, rr.err
	r.finalized = rr.status.Terminal()
	if !rr.startedAt.IsZero() {
		r.replay = rows
		r.initGraphLocked()
		r.replay = nil
		for _, i := range r.reached {
			pn, ns := plan.nodes[i], &r.nodes[i]
			row, ok := rows[pn.id]
			if !ok {
				continue
			}
			ns.attempts, ns.err, ns.startedAt, ns.finishedAt = row.attempts, row.err, row.startedAt, row.finishedAt
			ns.props = decodeJSON(row.props)
			switch row.status {
			case rowRunning:
				ns.status = sRunning
			case rowRetrying:
				ns.status = sRetrying
			case rowSuspended, rowResumed:
				ns.status, ns.suspendKey, ns.resumeAt = sSuspended, row.suspendKey, row.resumeAt
				ns.suspendOutput = decodeJSON(row.output)
			case rowFailed:
				ns.status = sFailed
			}
		}
	}
	return r.snapshotLocked(), nil
}
