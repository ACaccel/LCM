package router

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
)

// Hooks returns the resource store hooks of the router type.
func (m *Manager) Hooks() resource.TypeHooks { return hooks{m} }

// Type returns the router implementation of resource.Type.
func (m *Manager) Type() resource.Type { return routerType{m} }

type hooks struct{ m *Manager }

// BeforeSave refuses a document whose connection is not configured on this
// host, is of another type or does not allow the resource: documents name
// connections but never widen them. It creates the status row of a new
// router.
func (h hooks) BeforeSave(ctx context.Context, tx *sql.Tx, s resource.Save) error {
	doc := s.Next.Document
	if msg := h.m.conns.Check(doc.Spec.Connection, doc.Spec.Adapter, doc.Metadata.Name); msg != "" {
		return fmt.Errorf("%w: %w", resource.ErrInvalid, model.Issues{{Path: "spec.connection", Message: msg}})
	}
	if s.Old != nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO resource_status (resource_id, identity_hash, status, updated_at) VALUES (?, '', '{}', ?)`,
		s.Next.ID, h.m.clk.Now().UnixMilli())
	return err
}

// BeforePhase refuses to stop managing a router with an operation whose
// outcome is open and clears the observed status, which belongs to the
// managed period only.
func (h hooks) BeforePhase(ctx context.Context, tx *sql.Tx, p resource.PhaseChange) error {
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id = ? AND unresolved = 1`, p.Record.ID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved > 0 {
		return fmt.Errorf("%w: %w", resource.ErrPrecondition, precondition("resolve the outstanding operation first"))
	}
	_, err := tx.ExecContext(ctx, `UPDATE resource_status SET status = '{}', updated_at = ? WHERE resource_id = ?`, h.m.clk.Now().UnixMilli(), p.Record.ID)
	return err
}

// feedNative reports the native health of r to the monitor.
func (m *Manager) feedNative(r Record) {
	if m.native == nil {
		return
	}
	if r.Phase != resource.PhaseManaged {
		m.native.Forget(r.Name)
		return
	}
	h := monitor.HealthUnknown
	switch r.Status.Health {
	case healthHealthy:
		h = monitor.HealthHealthy
	case healthDegraded:
		h = monitor.HealthDegraded
	case healthDown:
		h = monitor.HealthDown
	}
	m.native.SetNative(r.Name, model.ResourceRouter, r.Document.Metadata.Labels, h, reasonsOf(r))
}

type routerType struct{ m *Manager }

// routerActions are the router's controls in display order. Policies may
// run none of them: a reboot cuts the network, so it is a direct
// administrator decision.
var routerActions = []resource.Action{
	{Name: "observe", Risk: resource.RiskRead, Kind: resource.KindAction},
	{Name: "reboot", Risk: resource.RiskDisruptive, Kind: resource.KindOperation},
	{Name: "retire", Risk: resource.RiskReversible, Kind: resource.KindPhase},
	{Name: "resume", Risk: resource.RiskReversible, Kind: resource.KindPhase},
	{Name: "unmanage", Risk: resource.RiskDestructive, Kind: resource.KindPhase},
}

// ActionCatalog lists the router's controls without availability.
func (routerType) ActionCatalog() []resource.Action { return slices.Clone(routerActions) }

func (routerType) Name() string { return model.ResourceRouter }

// Status reports online or offline only while the latest attempt is fresh;
// a missing or stale observation is unknown, never offline.
func (t routerType) Status(ctx context.Context, rec resource.Record) (resource.Status, error) {
	r, err := t.m.view(ctx, rec)
	if err != nil {
		return resource.Status{}, err
	}
	st := resource.Status{State: resource.StateUnknown, StateSince: rec.UpdatedAt, Detail: r.Status}
	s := r.Status.Snapshot
	switch {
	case r.Status.Fresh && (r.state == resource.StateOnline || r.state == resource.StateOffline):
		st.State, st.StateSince = r.state, r.since
		if st.StateSince.IsZero() {
			st.StateSince = s.ObservedAt
		}
	case r.Status.Fresh:
		st.StateSince = s.ObservedAt
	case s != nil:
		if stale := s.ObservedAt.Add(r.Config.StaleAfter.D()); stale.After(rec.UpdatedAt) {
			st.StateSince = stale
		}
	}
	return st, nil
}

// Actions lists observation, reboot and phase controls with their current
// availability.
func (t routerType) Actions(ctx context.Context, rec resource.Record) []resource.Action {
	m := t.m
	var unresolved, owned int
	var readErr error
	db := m.db.Reader()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id = ? AND unresolved = 1`, rec.ID).Scan(&unresolved); err != nil {
		readErr = err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_ownership WHERE kind = ? AND name = ?`, string(model.KindResource), rec.Name).Scan(&owned); err != nil {
		readErr = err
	}
	rebootReason := "router is not available"
	if r, err := m.view(ctx, rec); err == nil {
		if reason, err := m.rebootBlocked(ctx, r); err == nil {
			rebootReason = reason
		}
	}
	managed := rec.Phase == resource.PhaseManaged
	reasons := map[string]string{"reboot": rebootReason}
	if !managed {
		reasons["observe"] = "router is not managed"
		reasons["retire"] = "router is not managed"
	} else if unresolved > 0 {
		reasons["retire"] = "resolve the outstanding operation first"
	}
	if rec.Phase != resource.PhaseRetired {
		reasons["resume"] = "router is not retired"
		reasons["unmanage"] = "retire the router first"
	} else if unresolved > 0 {
		reasons["unmanage"] = "resolve the outstanding operation first"
	} else if owned > 0 {
		reasons["unmanage"] = "release Git ownership first"
	}
	out := make([]resource.Action, 0, len(routerActions))
	for _, a := range routerActions {
		a.Reason = reasons[a.Name]
		if readErr != nil {
			a.Reason = "router state could not be read"
		}
		a.Available = a.Reason == ""
		out = append(out, a)
	}
	return out
}

// ChangePhase moves a router to another phase. Observation stops with the
// managed phase (the loops follow the store's change notification), so the
// monitor forgets the native health of a router that leaves it and learns
// the unknown health of a resumed one.
func (t routerType) ChangePhase(ctx context.Context, rec resource.Record, to string, expected int, reason string) error {
	m := t.m
	// The observe lock keeps an observation from reporting health between
	// the phase change and Forget, which would bring a retired router back
	// into the monitor.
	locks := m.locksFor(rec.ID)
	locks.observe.Lock()
	defer locks.observe.Unlock()
	if err := m.resources.SetPhase(ctx, "@"+rec.ID, to, expected, reason); err != nil {
		return err
	}
	if m.native == nil {
		return nil
	}
	if to != resource.PhaseManaged {
		m.native.Forget(rec.Name)
		return nil
	}
	r, err := m.Get(ctx, "@"+rec.ID)
	if err != nil {
		return err
	}
	m.feedNative(r)
	return nil
}
