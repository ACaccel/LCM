package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/resource/operation"
)

// DirectRequest marks an authenticated console/CLI/API boundary, never a policy run.
func DirectRequest(ctx context.Context) context.Context { return auth.WithDirect(ctx) }

func requireDirect(ctx context.Context) error {
	_, err := operation.RequireDirect(ctx, ErrForbidden)
	return err
}

// PowerRequest is fixed before the helper can perform a side effect.
type PowerRequest struct {
	ControllerID string    `json:"controllerId"`
	OperationID  string    `json:"operationId"`
	RequestHash  string    `json:"requestHash"`
	Action       string    `json:"action"`
	BootID       string    `json:"bootId"`
	Deadline     time.Time `json:"deadline"`
	IssuedAt     time.Time `json:"issuedAt"`
	ValidUntil   time.Time `json:"validUntil"`
}

// Receipt is helper evidence, not evidence that the machine is powered off.
type Receipt struct {
	State      string    `json:"state"`
	AcceptedAt time.Time `json:"acceptedAt"`
	BootID     string    `json:"bootId"`
}

// PowerBackend fails closed if the restricted helper or autostart is unavailable.
type PowerBackend interface {
	Preflight(context.Context) error
	Prepare(context.Context, PowerRequest) (Receipt, error)
	Submit(context.Context, PowerRequest) (Receipt, error)
	Query(context.Context, PowerRequest) (Receipt, error)
}

// ErrReceiptMissing is the helper's answer to a query for a request it has
// no receipt of: it never prepared, and so never submitted, the request.
var ErrReceiptMissing = errors.New("machine: helper has no receipt of the request")

// Operation survives controller restarts and retains the original unverified result.
type Operation = operation.Operation[Status, PowerRequest, Receipt]

// Maintenance keeps the durable write gate until an explicit verified exit.
// Acknowledged is always true: the confirmed request that entered it
// acknowledges partial workload coverage and manual recovery.
type Maintenance struct {
	Preview      MaintenancePreview `json:"preview"`
	ResourceID   string             `json:"resourceId"`
	State        string             `json:"state"`
	EnteredAt    time.Time          `json:"enteredAt"`
	Actor        Actor              `json:"actor"`
	Reason       string             `json:"reason"`
	Acknowledged bool               `json:"acknowledged"`
	// Action is the power action whose request entered maintenance; it is
	// empty for a manual entry.
	Action string `json:"action,omitempty"`
}

// Capability explains why a control is disabled instead of offering a no-op.
type Capability struct {
	Action    string `json:"action"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

func (m *Manager) Capabilities(ctx context.Context, name string) ([]Capability, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	reason := ""
	switch {
	case m.power == nil:
		reason = resource.NoPowerHelper
	case r.Phase != resource.PhaseManaged:
		reason = resource.MachineRetired
	case r.Status.Snapshot == nil || r.Status.Snapshot.Init != "systemd":
		reason = resource.PowerNeedsSystemd
	case !r.Status.Fresh || r.Status.Identity != "verified":
		reason = resource.PowerNeedsFresh
	case r.Status.Health != "healthy":
		reason = resource.PowerNeedsHealthy
	case !powerEvidenceReady(r.Status):
		reason = resource.PowerNeedsEvidence
	}
	if reason == "" {
		if err := m.power.Preflight(ctx); err != nil {
			reason = resource.PowerPreflightFailed
		}
	}
	return []Capability{{Action: "reboot", Available: reason == "", Reason: reason}, {Action: "graceful-shutdown", Available: reason == "", Reason: reason}, {Action: "power-on", Available: false, Reason: resource.NoPowerOn}}, nil
}

func (m *Manager) Maintenance(ctx context.Context, name string) (*Maintenance, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	var body string
	err = m.db.Reader().QueryRowContext(ctx, `SELECT body FROM machine_maintenance WHERE resource_id=?`, r.ID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v Maintenance
	err = json.Unmarshal([]byte(body), &v)
	return &v, err
}

// EnterMaintenance holds local writes without a power request. The
// request acknowledges partial workload coverage and manual recovery. A
// nonempty previewHash must match the workload inventory at entry, so that
// what the operator reviewed is what is recorded.
func (m *Manager) EnterMaintenance(ctx context.Context, name, reason string, expected int, previewHash string) (Maintenance, error) {
	if err := requireDirect(ctx); err != nil {
		return Maintenance{}, err
	}
	if !operation.ValidReason(reason) {
		return Maintenance{}, precondition("a reason is required")
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return Maintenance{}, err
	}
	if r.Version != expected {
		return Maintenance{}, ErrConflict
	}
	if r.Phase != resource.PhaseManaged {
		return Maintenance{}, precondition(resource.MachineRetired)
	}
	var v Maintenance
	var event Event
	err = m.gate.Exclusive(ctx, func(tx *sql.Tx) error {
		var err error
		if v, err = m.enterTx(ctx, tx, r, reason, "", previewHash, expected); err != nil {
			return err
		}
		event, err = m.recordTx(ctx, tx, "maintenance.enter", name, r.ID, map[string]any{"reason": reason, "partialCoverageAcknowledged": true})
		return err
	})
	if err != nil {
		return Maintenance{}, err
	}
	m.publish([]Event{event})
	return v, nil
}

// enterTx stores maintenance of r, with the workload inventory at entry,
// within tx, which must hold the exclusive write gate. action names the
// power request entering it, if any.
func (m *Manager) enterTx(ctx context.Context, tx *sql.Tx, r Record, reason, action, previewHash string, expected int) (Maintenance, error) {
	a, _ := actor(ctx)
	v := Maintenance{ResourceID: r.ID, State: "active", EnteredAt: m.clk.Now().UTC(), Actor: actorInfo(a), Reason: reason, Acknowledged: true, Action: action}
	p, err := preview(ctx, tx, r)
	if err != nil {
		return v, err
	}
	if previewHash != "" && previewHash != p.Hash {
		return v, precondition("maintenance impact changed; review a new preview")
	}
	v.Preview = p
	body, err := encode(v)
	if err != nil {
		return v, err
	}
	if m.maintenancePreflight != nil {
		if err := m.maintenancePreflight(ctx); err != nil {
			return v, precondition("controller update or another maintenance dependency is pending")
		}
	}
	cur, err := current(ctx, tx, r.ID)
	if err != nil {
		return v, err
	}
	if cur.phase != resource.PhaseManaged {
		return v, precondition(resource.MachineRetired)
	}
	if cur.version != expected {
		return v, ErrConflict
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance`).Scan(&count); err != nil {
		return v, err
	}
	if count > 0 {
		return v, precondition(resource.MaintenanceActive)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO machine_maintenance(host,resource_id,state,entered_at,body) VALUES('local',?,?,?,?)`, r.ID, v.State, v.EnteredAt.UnixMilli(), body)
	return v, err
}

func (m *Manager) ExitMaintenance(ctx context.Context, name, reason string, expected int) error {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	if err := requireDirect(ctx); err != nil {
		return err
	}
	if !operation.ValidReason(reason) {
		return precondition("a resolution reason is required")
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return err
	}
	if !r.Status.Fresh || r.Status.Health != "healthy" || r.Status.Identity != "verified" {
		return precondition("fresh verified healthy observations are required before exit")
	}
	if r.Version != expected {
		return ErrConflict
	}
	err = m.gate.Exclusive(ctx, func(tx *sql.Tx) error {
		if err := m.verifyFreshTx(ctx, tx, r, true); err != nil {
			return err
		}
		anchor, err := m.recoveryAnchor(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		cur, err := current(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if cur.status.Snapshot == nil || !cur.status.Snapshot.ObservedAt.After(anchor) {
			return precondition("observe fresh health after maintenance or manual resolution before exit")
		}
		unresolved, err := m.ops.UnresolvedCount(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if unresolved > 0 {
			return precondition(resource.ResolveOperationFirst)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM machine_maintenance WHERE resource_id=?`, r.ID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	return m.record(ctx, "maintenance.exit", name, map[string]any{"reason": reason})
}

func (m *Manager) recoveryAnchor(ctx context.Context, tx *sql.Tx, id string) (time.Time, error) {
	var body string
	if err := tx.QueryRowContext(ctx, `SELECT body FROM machine_maintenance WHERE resource_id=?`, id).Scan(&body); err != nil {
		return time.Time{}, err
	}
	var maintenance Maintenance
	if err := json.Unmarshal([]byte(body), &maintenance); err != nil {
		return time.Time{}, err
	}
	anchor := maintenance.EnteredAt
	rows, err := tx.QueryContext(ctx, `SELECT body FROM resource_operations WHERE resource_id=?`, id)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return time.Time{}, err
		}
		op, err := m.ops.Decode(raw)
		if err != nil {
			return time.Time{}, err
		}
		if op.ResolvedAt != nil && op.ResolvedAt.After(anchor) {
			anchor = *op.ResolvedAt
		}
	}
	return anchor, rows.Err()
}

// Submit enters maintenance unless it is already active, records intent and
// hands the request to the helper. Maintenance entry, intent and their
// audit entries commit together, so a refused request leaves no
// maintenance behind. An ambiguous handoff is never retried automatically.
func (m *Manager) Submit(ctx context.Context, name string, req resource.OperationRequest) (Operation, error) {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	a, err := operation.RequireDirect(ctx, ErrForbidden)
	if err != nil {
		return Operation{}, err
	}
	action, key, expected := req.Action, req.Key, req.Version
	if action != "reboot" && action != "graceful-shutdown" {
		return Operation{}, ErrUnsupported
	}
	if !operation.ValidKey(key) {
		return Operation{}, precondition("a bounded idempotency key is required")
	}
	if !operation.ValidReason(req.Reason) {
		return Operation{}, precondition("a reason is required")
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	if req.Confirm != r.Name {
		return Operation{}, precondition("confirm the request with the exact resource name")
	}
	hash := operation.RequestHash(r.ID, action, expected)
	if prior, ok, err := m.ops.Replay(ctx, r.ID, key, hash); err != nil || ok {
		if err == nil {
			// The control lock is held, so no Submit is working on a prior
			// request still pending or submitting: it was abandoned.
			err = m.closeAbandoned(context.WithoutCancel(ctx), &prior)
		}
		return prior, err
	}
	if r.Version != expected {
		return Operation{}, ErrConflict
	}
	caps, err := m.Capabilities(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	if !caps[0].Available {
		return Operation{}, precondition(caps[0].Reason)
	}
	active, err := m.Maintenance(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	op := m.ops.Start(r.ID, action, expected, actorInfo(a), req.Reason)
	now := op.CreatedAt
	validUntil := r.Status.Snapshot.ObservedAt.Add(r.Config.StaleAfter.D())
	if capTime := now.Add(5 * time.Minute); validUntil.After(capTime) {
		validUntil = capTime
	}
	op.Request = PowerRequest{ControllerID: r.ID, OperationID: op.ID, RequestHash: hash, Action: action, BootID: r.Status.Snapshot.BootID, IssuedAt: now, ValidUntil: validUntil, Deadline: now.Add(10 * time.Minute)}
	// Entering maintenance needs the exclusive gate, which refuses while
	// other local writes are in flight. Active maintenance already excludes
	// them and is reused as it is.
	write := m.db.Write
	if active == nil {
		write = m.gate.Exclusive
	}
	var events []Event
	err = write(ctx, func(tx *sql.Tx) error {
		events = events[:0]
		var count int
		if err := m.verifyFreshTx(ctx, tx, r, true); err != nil {
			return err
		}
		cur, err := current(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if cur.version != expected {
			return ErrConflict
		}
		if n, err := m.ops.UnresolvedCount(ctx, tx, r.ID); err != nil || n > 0 {
			if err == nil {
				err = precondition(resource.OperationUnresolved)
			}
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance WHERE resource_id=? AND state='active'`, r.ID).Scan(&count); err != nil {
			return err
		}
		switch {
		case count == 0 && active != nil:
			return precondition("maintenance ended while the request was prepared; submit it again")
		case count == 0:
			if _, err := m.enterTx(ctx, tx, r, req.Reason, action, "", expected); err != nil {
				return err
			}
			e, err := m.recordTx(ctx, tx, "maintenance.enter", name, r.ID, map[string]any{"reason": req.Reason, "partialCoverageAcknowledged": true, "action": action, "operationId": op.ID})
			if err != nil {
				return err
			}
			events = append(events, e)
		}
		if err := m.ops.InsertTx(ctx, tx, &op, key, hash); err != nil {
			return err
		}
		e, err := m.recordTx(ctx, tx, "operation.submit", name, r.ID, map[string]any{"operationId": op.ID, "action": action, "reason": req.Reason})
		events = append(events, e)
		return err
	})
	if err != nil {
		if prior, ok, e := m.ops.Replay(ctx, r.ID, key, hash); e == nil && ok {
			return prior, nil
		}
		return Operation{}, err
	}
	m.publish(events)
	// From here on the outcome must be recorded even if the caller goes
	// away: an operation left pending blocks every power action until it is
	// closed. The helper calls are bounded by their own timeouts.
	ctx = context.WithoutCancel(ctx)
	receipt, err := m.power.Prepare(ctx, op.Request)
	if err != nil {
		op.State, op.Unresolved, op.Reason = operation.Failed, false, resource.HelperPrepareFailed
		return op, m.ops.Save(ctx, &op, operation.Change{})
	}
	op.Receipt = &receipt
	if receipt.State != "prepared" {
		op.State, op.Reason = operation.Indeterminate, resource.HelperNoPrepared
		return op, m.ops.Save(ctx, &op, operation.Change{})
	}
	// Submitting is the durable fence: the helper is asked to submit only
	// once it is stored, so an operation found still pending was never
	// submitted.
	op.State = operation.Submitting
	if err = m.ops.Save(ctx, &op, operation.Change{Check: func(tx *sql.Tx) error { return m.submittableTx(ctx, tx, r, op.Request) }}); err != nil {
		op.State, op.Unresolved, op.Reason = operation.Failed, false, resource.PreconditionsChanged
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return op, m.ops.Save(fctx, &op, operation.Change{})
	}
	// A new variable: op.Receipt still points at the prepare receipt, which
	// a failed submission must not overwrite.
	submitted, err := m.power.Submit(ctx, op.Request)
	if err != nil {
		op.State, op.Reason = operation.Indeterminate, resource.HelperUnverified
	} else {
		op.Receipt = &submitted
		op.State = operation.Observing
		if submitted.State != "accepted" {
			op.State, op.Reason = operation.Indeterminate, resource.HelperNoAcceptance
		}
		if action == "graceful-shutdown" {
			op.State, op.Reason = operation.Indeterminate, resource.NoPowerOffEvidence
		}
	}
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return op, m.ops.Save(fctx, &op, operation.Change{})
}

func powerEvidenceReady(s Status) bool {
	if s.Snapshot == nil {
		return false
	}
	for _, source := range []string{"identity", "boot", "init", "uptime", "cpu", "memory"} {
		if s.Snapshot.Coverage[source].State != "available" {
			return false
		}
	}
	return !math.IsNaN(s.Snapshot.UptimeSeconds) && !math.IsInf(s.Snapshot.UptimeSeconds, 0) && s.Snapshot.UptimeSeconds >= 0 && s.Snapshot.UptimeSeconds < float64((100*365*24*time.Hour)/time.Second)
}

// submittableTx rechecks within tx, before the submitting fence is stored,
// that the observations and maintenance authorizing req still hold.
func (m *Manager) submittableTx(ctx context.Context, tx *sql.Tx, r Record, req PowerRequest) error {
	if err := m.verifyFreshTx(ctx, tx, r, true); err != nil {
		return err
	}
	cur, err := current(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	latest := cur.status
	if !powerEvidenceReady(latest.Status) || latest.Snapshot.Init != "systemd" {
		return precondition("required OS evidence changed during preparation")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance WHERE resource_id=? AND state='active'`, r.ID).Scan(&active); err != nil {
		return err
	}
	if active != 1 || !m.clk.Now().Before(req.ValidUntil) {
		return precondition("maintenance or health authorization expired")
	}
	return nil
}

func (m *Manager) verifyFreshTx(ctx context.Context, tx *sql.Tx, r Record, healthy bool) error {
	cur, err := current(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	version, status := cur.version, cur.status
	if version != r.Version {
		return ErrConflict
	}
	s := status.Snapshot
	if cur.phase != resource.PhaseManaged || status.DocumentVersion != version || s == nil || r.Status.Snapshot == nil || s.BootID != r.Status.Snapshot.BootID || status.Identity != "verified" || s.ObservedAt.After(m.clk.Now()) || m.clk.Now().Sub(s.ObservedAt) > r.Config.StaleAfter.D() || (healthy && status.Health != "healthy") {
		return precondition("required observations changed or expired during preparation")
	}
	return nil
}

// Operation returns one operation of a machine.
func (m *Manager) Operation(ctx context.Context, name, id string) (Operation, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	return m.ops.Get(ctx, r.ID, id)
}

// Operations returns the newest operations of a machine first.
func (m *Manager) Operations(ctx context.Context, name string) ([]Operation, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	return m.ops.List(ctx, r.ID)
}

// Reconcile decides unresolved operations. It runs at startup and every
// minute and never resubmits an interrupted operation. Abandoned
// operations are closed after a receipt query. Other operations are decided
// from the stored receipt and observations: the helper is queried only for
// a reboot whose stored receipt is not an acceptance while fresh
// observations within its deadline show a new boot, since only then can a
// receipt change the outcome. An operation whose outcome did not change is
// not written.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	ops, err := m.ops.Unresolved(ctx, "")
	if err != nil {
		return err
	}
	for i := range ops {
		op := &ops[i]
		if op.State == operation.Pending || op.State == operation.Submitting {
			err = m.closeAbandoned(ctx, op)
		} else {
			m.decide(ctx, op)
			err = m.ops.Save(ctx, op, operation.Change{})
		}
		if err != nil && !errors.Is(err, operation.ErrStale) {
			return err
		}
	}
	return nil
}

// decide settles an observing or indeterminate operation from its stored
// receipt and the latest observations: succeeded on verified reboot
// evidence, indeterminate once an observing reboot passed its deadline.
// A shutdown is never verified.
func (m *Manager) decide(ctx context.Context, op *Operation) {
	if op.Action != "reboot" {
		if op.State == operation.Observing {
			op.State, op.Reason = operation.Indeterminate, resource.NoCompletionEvidence
		}
		return
	}
	if r, err := m.Get(ctx, "@"+op.ResourceID); err == nil && newBootObserved(*op, r) {
		if (op.Receipt == nil || op.Receipt.State != "accepted") && m.power != nil {
			if receipt, err := m.power.Query(ctx, op.Request); err == nil {
				op.Receipt = &receipt
			}
		}
		if op.Receipt != nil && rebootVerified(*op, *op.Receipt, r) {
			ev := r.Status
			op.State, op.Unresolved, op.Reason, op.Evidence = operation.Succeeded, false, resource.BootVerified, &ev
			return
		}
	}
	if op.State == operation.Observing && !m.clk.Now().Before(op.Request.Deadline) {
		op.State, op.Reason = operation.Indeterminate, resource.NoCompletionEvidence
	}
}

// closeAbandoned closes op when it is still pending or submitting although
// no Submit is working on it; the caller holds the control lock. The
// helper's independent receipt is queried first, so that a controller
// database restored from before the handoff cannot hide it. A pending
// request failed only when the helper has no receipt of it or one showing
// that nothing was requested from the OS: the helper submits nothing it
// did not prepare, and LCM asks it to submit only after storing the
// submitting fence. A submitting request may have reached the helper, so
// it is indeterminate unless a reboot is verified. Neither is ever
// submitted again.
func (m *Manager) closeAbandoned(ctx context.Context, op *Operation) error {
	if op.State != operation.Pending && op.State != operation.Submitting {
		return nil
	}
	pending := op.State == operation.Pending
	if m.power == nil {
		op.CloseAbandoned(false)
		op.Reason = resource.HelperDownOnRecovery
	} else if receipt, err := m.power.Query(ctx, op.Request); errors.Is(err, ErrReceiptMissing) {
		op.CloseAbandoned(pending)
	} else if err != nil {
		op.CloseAbandoned(false)
		op.Reason = resource.HelperNoReceipt
	} else {
		op.Receipt = &receipt
		if r, err := m.Get(ctx, "@"+op.ResourceID); err == nil && rebootVerified(*op, receipt, r) {
			ev := r.Status
			op.State, op.Unresolved, op.Reason, op.Evidence = operation.Succeeded, false, resource.BootVerified, &ev
		} else {
			op.CloseAbandoned(pending && (receipt.State == "prepared" || receipt.State == "failed"))
		}
	}
	err := m.ops.Save(ctx, op, operation.Change{})
	if errors.Is(err, operation.ErrStale) {
		cur, gerr := m.ops.Get(ctx, op.ResourceID, op.ID)
		if gerr != nil {
			return gerr
		}
		*op = cur
		return nil
	}
	return err
}

// newBootObserved reports whether fresh verified healthy observations,
// taken after op was created and by its deadline, show a boot other than
// the one the request was bound to.
func newBootObserved(op Operation, r Record) bool {
	s := r.Status.Snapshot
	return op.Action == "reboot" && r.Status.Fresh && r.Status.Identity == "verified" && r.Status.Health == "healthy" && powerEvidenceReady(r.Status) &&
		s != nil && s.BootID != op.Request.BootID && s.ObservedAt.After(op.CreatedAt) && !s.ObservedAt.After(op.Request.Deadline)
}

func rebootVerified(op Operation, receipt Receipt, r Record) bool {
	if !newBootObserved(op, r) || receipt.State != "accepted" || receipt.BootID != op.Request.BootID {
		return false
	}
	s := r.Status.Snapshot
	bootAt := s.ObservedAt.Add(-time.Duration(s.UptimeSeconds * float64(time.Second)))
	return !receipt.AcceptedAt.Before(op.CreatedAt) && !receipt.AcceptedAt.After(op.Request.ValidUntil) && !bootAt.Before(receipt.AcceptedAt.Add(-time.Minute)) && !bootAt.After(op.Request.Deadline.Add(time.Minute))
}

// checkQuiescent returns why op may still cause an OS action on the boot r
// observes, or nil. A request is bound to one boot, which the helper
// checks, so an operation of another boot can no longer act. On the same
// boot its validity must have ended and the helper must show that nothing
// was requested from the OS: expiry alone does not revoke an accepted
// action. A helper without a receipt of the request never prepared it and
// so cannot submit it.
func (m *Manager) checkQuiescent(ctx context.Context, op Operation, r Record) error {
	if r.Status.Snapshot == nil || r.Status.Snapshot.BootID != op.Request.BootID {
		return nil
	}
	if m.clk.Now().Before(op.Request.ValidUntil) || m.power == nil {
		return precondition("an accepted or uncertain request requires a verified new boot before manual resolution")
	}
	receipt, err := m.power.Query(ctx, op.Request)
	if errors.Is(err, ErrReceiptMissing) {
		return nil
	}
	if err != nil || (receipt.State != "prepared" && receipt.State != "failed") {
		return precondition("request quiescence is unverified; expiry does not revoke an accepted OS action")
	}
	return nil
}

// Resolve closes review without rewriting an unverified operation as success.
func (m *Manager) Resolve(ctx context.Context, name, id, reason string, expected int) (Operation, error) {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	a, err := operation.RequireDirect(ctx, ErrForbidden)
	if err != nil {
		return Operation{}, err
	}
	if !operation.ValidReason(reason) {
		return Operation{}, precondition("a resolution reason is required")
	}
	op, err := m.Operation(ctx, name, id)
	if err != nil {
		return Operation{}, err
	}
	// With the control lock held no Submit is in flight, so a pending or
	// submitting operation was abandoned; close it before judging it.
	if err := m.closeAbandoned(context.WithoutCancel(ctx), &op); err != nil {
		return Operation{}, err
	}
	if err := m.ops.CheckResolvable(op); err != nil {
		return Operation{}, err
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	if !r.Status.Fresh || r.Status.Identity != "verified" {
		return Operation{}, precondition(resource.PowerNeedsFresh)
	}
	if r.Version != expected {
		return Operation{}, ErrConflict
	}
	if err := m.checkQuiescent(ctx, op, r); err != nil {
		return Operation{}, err
	}
	op.MarkResolved(actorInfo(a), reason, m.clk.Now().UTC())
	err = m.ops.Save(ctx, &op, operation.Change{Audit: &operation.Audit{
		Action: "operation.resolve", Target: name, Details: map[string]any{"operationId": id, "reason": reason, "unverified": true},
	}})
	if errors.Is(err, operation.ErrStale) {
		return Operation{}, precondition("operation changed; reload it")
	}
	if err != nil {
		return Operation{}, err
	}
	return op, nil
}
