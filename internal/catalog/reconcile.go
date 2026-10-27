package catalog

import (
	"context"
	"fmt"

	"github.com/ACaccel/lcm/internal/audit"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Change is one correction made by Reconcile.
type Change struct {
	Service string
	From    State
	To      State
	Reason  string
}

// Reconcile compares every service's stored state with what its runtime
// reports and corrects states that cannot be true any more. Call it at
// startup, before actions are accepted, and on demand afterwards.
//
//   - A transient state (deploying, updating, rolling_back) with no action
//     running means the process died mid-action: running if the runtime is
//     active, failed otherwise. Deployments left running are marked
//     interrupted.
//   - running while the runtime is inactive or failed becomes failed: the
//     process died without LCM stopping it.
//   - stopped or registered while the runtime is active becomes running,
//     which adopts services that were started outside LCM.
//   - failed stays failed until an action or the operator changes it, so
//     that a failure is never silently forgotten.
//
// Services with an action in flight are skipped, and an unknown runtime
// status leaves the state alone except for transient states.
func (m *Manager) Reconcile(ctx context.Context) ([]Change, error) {
	ctx = withActor(ctx)
	recs, err := m.cat.List(ctx)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, rec := range recs {
		if m.isBusy(rec.Name) {
			continue
		}
		ch, err := m.reconcileOne(ctx, rec)
		if err != nil {
			m.log.Warn("reconcile service failed", "service", rec.Name, "error", err)
			continue
		}
		if ch != nil {
			changes = append(changes, *ch)
		}
	}
	if err := m.interruptDeployments(ctx); err != nil {
		return changes, err
	}
	return changes, nil
}

func (m *Manager) reconcileOne(ctx context.Context, rec Record) (*Change, error) {
	if rec.State == StateRetired || rec.State == StateFailed {
		return nil, nil
	}
	adapter, err := m.runtimes.For(rec.Spec)
	if err != nil {
		return nil, err
	}
	st, statusErr := adapter.Status(ctx, rec.Spec)
	if statusErr != nil {
		st.State = rt.StateUnknown
	}

	var to State
	var reason string
	switch {
	case rec.State.Transient():
		to, reason = StateFailed, "interrupted while "+string(rec.State)
		if st.State == rt.StateActive {
			to = StateRunning
		}
	case rec.State == StateRunning && (st.State == rt.StateInactive || st.State == rt.StateFailed):
		to, reason = StateFailed, "runtime reports "+string(st.State)
	case (rec.State == StateStopped || rec.State == StateRegistered) && st.State == rt.StateActive:
		to, reason = StateRunning, "runtime reports active"
	case (rec.State == StateStopped || rec.State == StateRegistered) && st.State == rt.StateFailed:
		to, reason = StateFailed, "runtime reports failed"
	default:
		return nil, nil
	}
	if statusErr != nil {
		reason += fmt.Sprintf(" (status unavailable: %v)", statusErr)
	}
	reason = "reconcile: " + reason

	// Re-check the busy mark just before writing; the compare-and-set in
	// Transition catches an action that changed the state meanwhile.
	if m.isBusy(rec.Name) {
		return nil, nil
	}
	err = m.cat.Transition(ctx, rec.Name, rec.State, to, TransitionInfo{Action: "reconcile", Reason: reason})
	if err != nil {
		return nil, err
	}
	m.recordAudit(ctx, "reconcile", rec.Name, audit.OutcomeOK,
		map[string]any{"from": string(rec.State), "to": string(to), "reason": reason})
	return &Change{Service: rec.Name, From: rec.State, To: to, Reason: reason}, nil
}

// interruptDeployments closes deployments that were running when the
// process stopped.
func (m *Manager) interruptDeployments(ctx context.Context) error {
	deps, err := m.cat.RunningDeployments(ctx)
	if err != nil {
		return err
	}
	for _, d := range deps {
		if m.isBusy(d.Service) {
			continue
		}
		if err := m.cat.FinishDeployment(ctx, d.ID, OutcomeInterrupted, "interrupted by a restart of LCM", d.LogExcerpt); err != nil {
			return err
		}
	}
	return nil
}
