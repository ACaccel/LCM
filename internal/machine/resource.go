package machine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
)

// Hooks returns the resource store hooks of the machine type.
func (m *Manager) Hooks() resource.TypeHooks { return hooks{m} }

// Type returns the machine implementation of resource.Type.
func (m *Manager) Type() resource.Type { return machineType{m} }

// Changed publishes control events written by the hooks of a committed
// change; connect it to the resource store's OnChange. It only reads the
// database and never calls back into the store.
func (m *Manager) Changed(c resource.Change) {
	if c.Type == model.ResourceMachine {
		m.flushStaged(context.Background())
	}
}

type hooks struct{ m *Manager }

// hookPrecondition matches both resource.ErrPrecondition, which the API maps
// for every type, and ErrPrecondition of this package.
func hookPrecondition(reason string) error {
	return fmt.Errorf("%w: %w", resource.ErrPrecondition, precondition(reason))
}

// BeforeSave creates the status row of a new machine and forgets journal
// checkpoints when the unit selection changes, so that the next poll reads
// the journal of the new selection from its start window.
func (h hooks) BeforeSave(ctx context.Context, tx *sql.Tx, s resource.Save) error {
	m := h.m
	now := m.clk.Now()
	if s.Old == nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_status (resource_id, identity_hash, status, updated_at) VALUES (?, '', '{}', ?)`,
			s.Next.ID, now.UnixMilli()); err != nil {
			return err
		}
	} else if journalSelection(s.Old.Document) != journalSelection(s.Next.Document) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM machine_cursors WHERE resource_id = ?`, s.Next.ID); err != nil {
			return err
		}
	}
	return m.stageControl(ctx, tx, s.Next.ID, "save")
}

// BeforePhase refuses to stop managing a machine with an operation whose
// outcome is unknown and clears the observed status, which belongs to the
// managed period only.
func (h hooks) BeforePhase(ctx context.Context, tx *sql.Tx, p resource.PhaseChange) error {
	m := h.m
	id := p.Record.ID
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id = ? AND unresolved = 1`, id).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved > 0 {
		return hookPrecondition("unresolved operation")
	}
	var maint int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance WHERE resource_id = ?`, id).Scan(&maint); err != nil {
		return err
	}
	if maint > 0 {
		return hookPrecondition("exit maintenance first")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE resource_status SET status = '{}', updated_at = ? WHERE resource_id = ?`, m.clk.Now().UnixMilli(), id); err != nil {
		return err
	}
	return m.stageControl(ctx, tx, id, p.To)
}

func journalSelection(doc *model.Resource) string {
	cfg, _ := doc.Machine()
	if cfg == nil {
		return ""
	}
	s, _ := encode(struct {
		Units   []string
		Journal bool
	}{cfg.Units, cfg.Journal})
	return s
}

// stageControl stores a control event inside a store transaction and keeps
// it for publication after the commit.
func (m *Manager) stageControl(ctx context.Context, tx *sql.Tx, id, action string) error {
	now := m.clk.Now().UTC()
	e := Event{ResourceID: id, Source: "control", Key: newID(), Kind: "machine." + action, Severity: "info", OccurredAt: now, IngestedAt: now}
	if err := insertEvent(ctx, tx, &e); err != nil {
		return err
	}
	m.stagedMu.Lock()
	m.staged = append(m.staged, stagedEvent{event: e, stagedAt: now})
	m.stagedMu.Unlock()
	return nil
}

// stagedRetention keeps a staged event that is not visible yet, because its
// transaction may still be committing.
const stagedRetention = time.Minute

// flushStaged publishes staged events that were committed. Events of
// aborted transactions are never found and are dropped after
// stagedRetention.
func (m *Manager) flushStaged(ctx context.Context) {
	m.stagedMu.Lock()
	pending := m.staged
	m.staged = nil
	m.stagedMu.Unlock()
	var publish []Event
	var keep []stagedEvent
	for _, s := range pending {
		var id int64
		err := m.db.Reader().QueryRowContext(ctx, `SELECT id FROM resource_events WHERE resource_id = ? AND source = 'control' AND source_key = ?`,
			s.event.ResourceID, s.event.Key).Scan(&id)
		switch {
		case err == nil:
			s.event.ID = id
			publish = append(publish, s.event)
		case errors.Is(err, sql.ErrNoRows) && m.clk.Now().Sub(s.stagedAt) < stagedRetention:
			keep = append(keep, s)
		case !errors.Is(err, sql.ErrNoRows):
			m.log.Warn("read staged machine event failed", "error", err)
		}
	}
	if len(keep) > 0 {
		m.stagedMu.Lock()
		m.staged = append(keep, m.staged...)
		m.stagedMu.Unlock()
	}
	m.publish(publish)
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
	case "healthy":
		h = monitor.HealthHealthy
	case "degraded":
		h = monitor.HealthDegraded
	case "down":
		h = monitor.HealthDown
	}
	m.native.SetNative(r.Name, model.ResourceMachine, r.Document.Metadata.Labels, h, slices.Clone(r.Status.Reasons))
}

// online reports whether the latest observation is fresh and the probe
// succeeded.
func online(r Record) bool {
	return r.Status.Fresh && r.Status.Snapshot != nil && r.Status.Snapshot.Coverage["probe"].State != "unknown"
}

type machineType struct{ m *Manager }

// machineActions are the machine's controls in display order. None of them
// may be run by policies: power and maintenance are direct administrator
// decisions. Entering maintenance is reversible: it stops nothing that runs,
// it only holds local writes until maintenance is exited, so it needs no
// impact acknowledgement.
var machineActions = []resource.Action{
	{Name: "observe", Risk: resource.RiskRead, Kind: resource.KindAction},
	{Name: "maintenance-enter", Risk: resource.RiskReversible, Kind: resource.KindAction},
	{Name: "maintenance-exit", Risk: resource.RiskReversible, Kind: resource.KindAction},
	{Name: "reboot", Risk: resource.RiskDisruptive, Kind: resource.KindOperation},
	{Name: "graceful-shutdown", Risk: resource.RiskDestructive, Kind: resource.KindOperation},
	{Name: "power-on", Risk: resource.RiskReversible, Kind: resource.KindOperation},
	{Name: "retire", Risk: resource.RiskReversible, Kind: resource.KindPhase},
	{Name: "resume", Risk: resource.RiskReversible, Kind: resource.KindPhase},
	{Name: "unmanage", Risk: resource.RiskDestructive, Kind: resource.KindPhase},
}

// ActionCatalog lists the machine's controls without availability, for
// clients that describe the type rather than one machine.
func (machineType) ActionCatalog() []resource.Action { return slices.Clone(machineActions) }

func (machineType) Name() string { return model.ResourceMachine }

// Status reports online while observations are fresh and successful and
// unknown otherwise: a missing observation never proves the machine is off.
func (t machineType) Status(ctx context.Context, rec resource.Record) (resource.Status, error) {
	r, err := t.m.view(ctx, rec)
	if err != nil {
		return resource.Status{}, err
	}
	st := resource.Status{State: resource.StateUnknown, StateSince: rec.UpdatedAt, Detail: newStatusView(r.Status)}
	switch {
	case online(r) && !r.onlineSince.IsZero():
		st.State, st.StateSince = resource.StateOnline, r.onlineSince
	case online(r):
		st.State, st.StateSince = resource.StateOnline, r.Status.Snapshot.ObservedAt
	case r.Status.Snapshot != nil:
		// Unknown since the last sample went stale, or since the failed
		// sample.
		since := r.Status.Snapshot.ObservedAt
		if r.Status.Fresh {
			st.StateSince = since
		} else if stale := since.Add(r.Config.StaleAfter.D()); stale.After(rec.UpdatedAt) {
			st.StateSince = stale
		}
	}
	return st, nil
}

// Actions lists the machine's observation, maintenance, power and phase
// controls. None of them may be run by policies: power and maintenance are
// direct administrator decisions.
func (t machineType) Actions(ctx context.Context, rec resource.Record) []resource.Action {
	m := t.m
	managed := rec.Phase == resource.PhaseManaged
	unavailable := func(cond bool, reason string) (bool, string) {
		if cond {
			return false, reason
		}
		return true, ""
	}
	var maint, unresolved, owned int
	var readErr error
	db := m.db.Reader()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance WHERE resource_id = ?`, rec.ID).Scan(&maint); err != nil {
		readErr = err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id = ? AND unresolved = 1`, rec.ID).Scan(&unresolved); err != nil {
		readErr = err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_ownership WHERE kind = ? AND name = ?`, string(model.KindResource), rec.Name).Scan(&owned); err != nil {
		readErr = err
	}
	action := func(name string, ok bool, reason string) resource.Action {
		if readErr != nil {
			ok, reason = false, "machine state could not be read"
		}
		i := slices.IndexFunc(machineActions, func(a resource.Action) bool { return a.Name == name })
		a := machineActions[i]
		a.Available, a.Reason = ok, reason
		return a
	}
	var out []resource.Action

	ok, reason := unavailable(!managed, "machine is not managed")
	if ok && m.observer == nil {
		ok, reason = false, "no observer is configured"
	}
	out = append(out, action("observe", ok, reason))

	ok, reason = unavailable(!managed, "machine is not managed")
	if ok && maint > 0 {
		ok, reason = false, "maintenance is already active"
	}
	out = append(out, action("maintenance-enter", ok, reason))
	ok, reason = unavailable(maint == 0, "maintenance is not active")
	out = append(out, action("maintenance-exit", ok, reason))

	power := map[string]Capability{}
	if caps, err := m.Capabilities(ctx, "@"+rec.ID); err == nil {
		for _, c := range caps {
			power[c.Action] = c
		}
	}
	for _, op := range []string{"reboot", "graceful-shutdown", "power-on"} {
		c, found := power[op]
		ok, reason := c.Available, c.Reason
		switch {
		case !found:
			ok, reason = false, "machine is not available"
		case ok && unresolved > 0:
			ok, reason = false, "an unresolved operation already exists"
		}
		out = append(out, action(op, ok, reason))
	}

	phaseBlock := ""
	switch {
	case unresolved > 0:
		phaseBlock = "resolve the outstanding operation first"
	case maint > 0:
		phaseBlock = "exit maintenance first"
	}
	ok, reason = unavailable(!managed, "machine is not managed")
	if ok && phaseBlock != "" {
		ok, reason = false, phaseBlock
	}
	out = append(out, action("retire", ok, reason))
	ok, reason = unavailable(rec.Phase != resource.PhaseRetired, "machine is not retired")
	out = append(out, action("resume", ok, reason))
	ok, reason = unavailable(rec.Phase != resource.PhaseRetired, "retire the machine first")
	switch {
	case ok && phaseBlock != "":
		ok, reason = false, phaseBlock
	case ok && owned > 0:
		ok, reason = false, "release Git ownership first"
	}
	out = append(out, action("unmanage", ok, reason))
	return out
}

// ChangePhase moves a machine to another phase. Observation stops with the
// managed phase, so the monitor forgets the native health of a machine
// that leaves it and is told the (unknown) health of a resumed one.
func (t machineType) ChangePhase(ctx context.Context, rec resource.Record, to string, expected int, reason string) error {
	m := t.m
	err := m.resources.SetPhase(ctx, "@"+rec.ID, to, expected, reason)
	m.flushStaged(ctx)
	if err != nil {
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
