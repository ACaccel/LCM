package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Phase change names as offered in the resource actions.
const (
	PhaseActionRetire   = "retire"
	PhaseActionResume   = "resume"
	PhaseActionUnmanage = "unmanage"
)

// risks of the service actions for clients and the policy guard.
var actionRisks = map[string]string{
	ActionStart:    resource.RiskReversible,
	ActionStop:     resource.RiskDisruptive,
	ActionRestart:  resource.RiskDisruptive,
	ActionDeploy:   resource.RiskDisruptive,
	ActionRollback: resource.RiskDisruptive,
	ActionBackup:   resource.RiskReversible,
	ActionRunJob:   resource.RiskDisruptive,
}

// phaseRisks are the risks of the phase changes of a service.
var phaseRisks = map[string]string{
	PhaseActionRetire:   resource.RiskDisruptive,
	PhaseActionResume:   resource.RiskReversible,
	PhaseActionUnmanage: resource.RiskDestructive,
}

// phaseOwnerKey marks the context of a phase change that holds the
// service's busy mark itself, so the hook does not refuse its own change.
type phaseOwnerKey struct{}

// Hooks returns the resource store hooks of the service type.
func (m *Manager) Hooks() resource.TypeHooks { return serviceHooks{m: m} }

// Type returns the service implementation of resource.Type.
func (m *Manager) Type() resource.Type { return serviceType{m: m} }

type serviceHooks struct{ m *Manager }

// BeforeSave creates the lifecycle state of a new service and records the
// saved version on the timeline. The entry is written in the store's
// transaction, so Config.OnTimeline is not called for it.
func (h serviceHooks) BeforeSave(ctx context.Context, tx *sql.Tx, s resource.Save) error {
	c := h.m.cat
	now := c.now()
	if s.Old == nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_states (resource_id, state, state_since) VALUES (?, ?, ?)`,
			s.Next.ID, string(StateRegistered), now.UnixMilli()); err != nil {
			return fmt.Errorf("catalog: create lifecycle state: %w", err)
		}
	}
	summary := fmt.Sprintf("configuration version %d saved by %s", s.Next.Version, s.Actor.Name)
	if s.RestoredFrom > 0 {
		summary = fmt.Sprintf("configuration version %d restored from version %d by %s", s.Next.Version, s.RestoredFrom, s.Actor.Name)
	}
	return insertTimeline(ctx, tx, Entry{
		Resource: s.Next.Name, Time: now, Kind: KindConfig, Summary: summary,
		Data: map[string]any{"version": s.Next.Version, "restoredFrom": s.RestoredFrom, "actor": s.Actor.Name},
	})
}

// BeforePhase refuses phase changes the lifecycle state does not allow and
// records the change on the timeline.
func (h serviceHooks) BeforePhase(ctx context.Context, tx *sql.Tx, p resource.PhaseChange) error {
	rec := p.Record
	if owner, _ := ctx.Value(phaseOwnerKey{}).(string); owner != rec.Name && h.m.isBusy(rec.Name) {
		return fmt.Errorf("%w: an action is running on %s", ErrLockHeld, rec.Name)
	}
	state, err := stateOf(ctx, tx, rec.ID)
	if err != nil {
		return err
	}
	switch p.To {
	case resource.PhaseRetired:
		if state.Transient() {
			return &StateError{Resource: rec.Name, Action: PhaseActionRetire, State: state}
		}
	case resource.PhaseUnmanaged:
		if !unmanageable(state) {
			return &StateError{Resource: rec.Name, Action: PhaseActionUnmanage, State: state}
		}
		// A future incarnation with the same name must not replay results.
		if _, err := tx.ExecContext(ctx, `DELETE FROM action_completions WHERE resource = ?`, rec.Name); err != nil {
			return err
		}
	}
	summary := fmt.Sprintf("phase %s -> %s by %s", rec.Phase, p.To, p.Actor.Name)
	if p.Reason != "" {
		summary += ": " + p.Reason
	}
	return insertTimeline(ctx, tx, Entry{
		Resource: rec.Name, Time: h.m.cat.now(), Kind: KindConfig, Summary: summary,
		Data: map[string]any{"phase": p.To, "previous": rec.Phase, "actor": p.Actor.Name, "reason": p.Reason},
	})
}

// unmanageable reports whether a service in state may be archived: it must
// not be running or in the middle of an action.
func unmanageable(state State) bool {
	return state == StateRegistered || state == StateStopped || state == StateFailed
}

type serviceType struct{ m *Manager }

func (serviceType) Name() string { return model.ResourceService }

// Status reports the lifecycle state. Detail is nil: deployment history and
// runtime details have their own queries. An archived service migrated
// without a lifecycle state reports an empty state.
func (t serviceType) Status(ctx context.Context, rec resource.Record) (resource.Status, error) {
	var (
		state string
		since int64
	)
	err := t.m.cat.db.Reader().QueryRowContext(ctx, `SELECT state, state_since FROM service_states WHERE resource_id = ?`, rec.ID).Scan(&state, &since)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return resource.Status{}, nil
	case err != nil:
		return resource.Status{}, fmt.Errorf("catalog: read state: %w", err)
	}
	return resource.Status{State: state, StateSince: time.UnixMilli(since).UTC()}, nil
}

// Actions lists the lifecycle actions and the phase changes of a service.
// Archived services offer nothing.
func (t serviceType) Actions(ctx context.Context, rec resource.Record) []resource.Action {
	if rec.Phase == resource.PhaseUnmanaged {
		return nil
	}
	states, err := t.m.cat.states(ctx, `WHERE resource_id = ?`, rec.ID)
	if err != nil {
		t.m.log.Warn("read service state for actions failed", "resource", rec.Name, "error", err)
		return nil
	}
	r, err := t.m.cat.record(rec, states)
	if err != nil {
		t.m.log.Warn("read service for actions failed", "resource", rec.Name, "error", err)
		return nil
	}
	busy := t.m.isBusy(rec.Name)
	var out []resource.Action
	for _, a := range AvailableActions(r) {
		reason := a.Reason
		if a.Allowed && busy {
			reason = ReasonBusy
		}
		out = append(out, resource.Action{
			Name: a.Action, Available: reason == "", Reason: reason,
			Risk: actionRisks[a.Action], Policy: true, Kind: resource.KindAction,
		})
	}
	phase := func(name, reason string) {
		if reason == "" && busy {
			reason = ReasonBusy
		}
		out = append(out, resource.Action{Name: name, Available: reason == "", Reason: reason, Risk: phaseRisks[name], Kind: resource.KindPhase})
	}
	retire, resume, unmanage := "", "", ""
	switch {
	case r.Phase != resource.PhaseManaged:
		retire = ReasonInvalidState
	case r.State.Transient():
		retire = ReasonInvalidState
	}
	if r.Phase != resource.PhaseRetired {
		resume = ReasonInvalidState
	}
	if r.Phase != resource.PhaseRetired || !unmanageable(r.State) {
		unmanage = ReasonInvalidState
	}
	phase(PhaseActionRetire, retire)
	phase(PhaseActionResume, resume)
	phase(PhaseActionUnmanage, unmanage)
	return out
}

// ActionCatalog lists every action and phase change of the service type in
// the order Actions reports them, without availability, for clients that
// describe the type rather than one service.
func (serviceType) ActionCatalog() []resource.Action {
	out := make([]resource.Action, 0, len(Actions)+len(phaseRisks))
	for _, a := range Actions {
		out = append(out, resource.Action{Name: a, Risk: actionRisks[a], Policy: true, Kind: resource.KindAction})
	}
	for _, p := range []string{PhaseActionRetire, PhaseActionResume, PhaseActionUnmanage} {
		out = append(out, resource.Action{Name: p, Risk: phaseRisks[p], Kind: resource.KindPhase})
	}
	return out
}

// ChangePhase retires, resumes or unmanages a service. It holds the
// service's busy mark and lock, so no action overlaps the change.
//
// Retiring stops a running or failed service first when its runtime can
// stop it; a failed stop aborts the retirement. A runtime that cannot stop
// the service leaves its state as it is, because LCM never records a state
// change it did not make. Resuming recomputes the state from the runtime.
// Unmanaging requires a registered, stopped or failed service.
func (t serviceType) ChangePhase(ctx context.Context, rec resource.Record, to string, expected int, reason string) error {
	m := t.m
	if rec.Type != model.ResourceService {
		return fmt.Errorf("%w: %s is not a service", ErrNotFound, rec.Name)
	}
	ctx, releaseGate, err := m.cat.gate.Acquire(ctx)
	if err != nil {
		return err
	}
	defer releaseGate()
	actor, err := requireActor(ctx)
	if err != nil {
		return err
	}
	action := PhaseActionUnmanage
	switch to {
	case resource.PhaseRetired:
		action = PhaseActionRetire
	case resource.PhaseManaged:
		action = PhaseActionResume
	}
	s, err := m.lock(ctx, rec.Name, action, ActionOptions{}, actor)
	if err != nil {
		return err
	}
	closed := false
	closeOnce := func() {
		if !closed {
			closed = true
			s.close()
		}
	}
	defer closeOnce()
	if err := s.load(ctx); err != nil {
		return err
	}
	if s.rec.ID != rec.ID {
		return fmt.Errorf("%w: service %s", ErrNotFound, rec.Name)
	}
	// Check the version before stopping anything, so that a stale request
	// cannot stop the service and then fail to retire it.
	if s.rec.Version != expected {
		return fmt.Errorf("%w: %s is at version %d, expected %d", resource.ErrConflict, rec.Name, s.rec.Version, expected)
	}
	ctx = context.WithValue(ctx, phaseOwnerKey{}, rec.Name)

	if to == resource.PhaseRetired && s.rec.Phase == resource.PhaseManaged {
		if s.cur.Transient() {
			return &StateError{Resource: rec.Name, Action: action, State: s.cur}
		}
		if err := s.stopForRetire(ctx); err != nil {
			return err
		}
	}
	if err := m.cat.res.SetPhase(ctx, "@"+rec.ID, to, expected, reason); err != nil {
		return err
	}
	if to != resource.PhaseManaged {
		return nil
	}
	closeOnce()
	cur, err := m.cat.Get(ctx, rec.Name)
	if err != nil {
		return err
	}
	if _, err := m.reconcileOne(withActor(ctx), *cur); err != nil {
		m.log.Warn("reconcile resumed service failed", "resource", rec.Name, "error", err)
	}
	return nil
}

// stopForRetire stops a running or failed service when the runtime can,
// moving it to stopped like the stop action does. It is audited as
// service.retire.
func (s *session) stopForRetire(ctx context.Context) error {
	if (s.cur != StateRunning && s.cur != StateFailed) || !rt.CapabilitiesOf(s.rt).Stop {
		return nil
	}
	from := s.cur
	err := s.adapter.Stop(ctx, s.rt)
	switch {
	case errors.Is(err, rt.ErrUnsupported):
		return nil
	case err != nil:
		s.settle(ctx, false)
	default:
		err = s.moveDone(ctx, StateStopped)
	}
	fctx, cancel := detached(ctx)
	defer cancel()
	outcome := audit.OutcomeOK
	if err != nil {
		outcome = audit.OutcomeError
	}
	s.m.recordAudit(fctx, PhaseActionRetire, s.name, outcome, map[string]any{"from": string(from), "to": string(s.cur), "stopped": err == nil})
	return err
}
