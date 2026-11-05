package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/connection"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/resource/operation"
	"github.com/ACaccel/lcm/internal/router/xiaomi"
)

// Receipt outcomes, from xiaomi.RebootOutcome.
const (
	receiptAccepted = "accepted"
	receiptUnknown  = "unknown"
	receiptRejected = "rejected"
	receiptNotSent  = "not-sent"
)

// Request is fixed when the operation is stored, before anything is sent.
type Request struct {
	Action      string    `json:"action"`
	Connection  string    `json:"connection"`
	RequestHash string    `json:"requestHash"`
	IssuedAt    time.Time `json:"issuedAt"`
	// Deadline ends the wait for reboot evidence.
	Deadline time.Time `json:"deadline"`
	// BaselineObservedAt and BaselineBootAt come from the fresh successful
	// observation that authorized the request; a reboot must start after
	// that boot.
	BaselineObservedAt time.Time `json:"baselineObservedAt"`
	BaselineBootAt     time.Time `json:"baselineBootAt"`
}

// Receipt records how the router answered the request. Acceptance is not
// completion.
type Receipt struct {
	Outcome     string    `json:"outcome"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// Operation is a durable reboot request; its JSON form is the API's
// ResourceOperation.
type Operation = operation.Operation[Status, Request, Receipt]

// Capability explains whether an operation can be submitted now.
type Capability struct {
	Action    string
	Available bool
	Reason    string
}

// Capabilities reports whether a reboot can be submitted now and why not.
func (m *Manager) Capabilities(ctx context.Context, ref string) ([]Capability, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	reason, err := m.rebootBlocked(ctx, r)
	if err != nil {
		return nil, err
	}
	return []Capability{{Action: connection.ActionReboot, Available: reason == "", Reason: reason}}, nil
}

// rebootBlocked returns why a reboot of r cannot be submitted now, or "".
func (m *Manager) rebootBlocked(ctx context.Context, r Record) (string, error) {
	if r.Phase != resource.PhaseManaged {
		return resource.RouterNotManaged, nil
	}
	conn, ok := m.conns.Get(r.Connection)
	switch {
	case !ok:
		return resource.ReasonNoConnection, nil
	case conn.Type != r.Document.Spec.Adapter:
		return resource.ReasonConnectionType, nil
	case !conn.AllowsResource(r.Name):
		return resource.ReasonConnectionDenied, nil
	case !conn.Allows(connection.ActionReboot):
		return resource.ConnectionNoReboot, nil
	case !online(r):
		return resource.NeedsObservation, nil
	}
	if _, ok := bootAt(r.Status.Snapshot); !ok {
		return resource.NoUptime, nil
	}
	db := m.db.Reader()
	unresolved, err := m.ops.UnresolvedCount(ctx, db, r.ID)
	if err != nil {
		return "", err
	}
	if unresolved > 0 {
		return resource.OperationUnresolved, nil
	}
	var maint int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance`).Scan(&maint); err != nil {
		return "", err
	}
	if maint > 0 {
		return resource.LocalMaintenance, nil
	}
	return "", nil
}

// Submit stores a reboot request and sends it once. It returns the stored
// operation even when sending failed: the state tells what is known. The
// same idempotency key with the same version returns the first operation
// instead of sending again.
func (m *Manager) Submit(ctx context.Context, ref string, req resource.OperationRequest) (Operation, error) {
	a, err := operation.RequireDirect(ctx, ErrForbidden)
	if err != nil {
		return Operation{}, err
	}
	action, key, expected := req.Action, req.Key, req.Version
	if action != connection.ActionReboot {
		return Operation{}, fmt.Errorf("%w: only reboot is supported", ErrUnsupported)
	}
	if !operation.ValidKey(key) {
		return Operation{}, precondition("a bounded idempotency key is required")
	}
	if !operation.ValidReason(req.Reason) {
		return Operation{}, precondition("a reason is required")
	}
	r, err := m.Get(ctx, ref)
	if err != nil {
		return Operation{}, err
	}
	if req.Confirm != r.Name {
		return Operation{}, precondition("confirm the request with the exact resource name")
	}
	locks := m.locksFor(r.ID)
	locks.control.Lock()
	defer locks.control.Unlock()
	hash := operation.RequestHash(r.ID, action, expected)
	if prior, ok, err := m.ops.Replay(ctx, r.ID, key, hash); err != nil || ok {
		if err == nil {
			// The control lock is held, so no Submit is in flight: a prior
			// request still pending or submitting was abandoned and must
			// not be reported as if it were progressing.
			err = m.closeAbandoned(context.WithoutCancel(ctx), &prior)
		}
		return prior, err
	}
	if r.Version != expected {
		return Operation{}, ErrConflict
	}
	reason, err := m.rebootBlocked(ctx, r)
	if err != nil {
		return Operation{}, err
	}
	if reason != "" {
		return Operation{}, precondition(reason)
	}
	c, err := m.client(r)
	if err != nil {
		return Operation{}, precondition(err.Error())
	}
	// The lease is held until the request has been sent, so that machine
	// maintenance cannot start in between.
	ctx, release, err := m.gate.Acquire(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer release()

	op := m.ops.Start(r.ID, action, expected, resource.ActorOf(a), req.Reason)
	baseline, _ := bootAt(r.Status.Snapshot)
	op.Request = Request{
		Action: action, Connection: r.Connection, RequestHash: hash, IssuedAt: op.CreatedAt, Deadline: op.CreatedAt.Add(rebootDeadline),
		BaselineObservedAt: r.Status.Snapshot.ObservedAt, BaselineBootAt: baseline,
	}
	var event resource.Event
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		cur, err := readCurrent(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if cur.version != expected {
			return ErrConflict
		}
		if cur.phase != resource.PhaseManaged {
			return precondition(resource.RouterNotManaged)
		}
		if err := m.ops.InsertTx(ctx, tx, &op, key, hash); err != nil {
			return err
		}
		event, err = m.ops.RecordTx(ctx, tx, "operation.submit", r.Name, r.ID, op.ID, map[string]any{"operationId": op.ID, "action": action, "connection": r.Connection, "reason": req.Reason})
		return err
	})
	if err != nil {
		if prior, ok, e := m.ops.Replay(ctx, r.ID, key, hash); e == nil && ok {
			return prior, nil
		}
		return Operation{}, err
	}
	m.publish([]resource.Event{event})
	// From here on the outcome must be recorded even if the caller goes
	// away: a request that may have reached the router is never repeated,
	// and one that was not sent must not stay pending.
	ctx = context.WithoutCancel(ctx)
	op.State = operation.Submitting
	if err := m.ops.Save(ctx, &op, operation.Change{}); err != nil {
		op.State = operation.Pending
		m.failUnsent(ctx, &op, resource.NotSentState)
		return op, err
	}
	outcome, sendErr := c.Reboot(ctx)
	submittedAt := m.clk.Now().UTC()
	switch outcome {
	case xiaomi.RebootAccepted:
		op.State, op.Receipt = operation.Observing, &Receipt{Outcome: receiptAccepted, SubmittedAt: submittedAt}
		op.Reason = resource.RouterAccepted
	case xiaomi.RebootUnknown:
		op.State, op.Receipt = operation.Observing, &Receipt{Outcome: receiptUnknown, SubmittedAt: submittedAt}
		op.Reason = resource.OutcomeUnknown
	case xiaomi.RebootRejected:
		op.State, op.Unresolved, op.Receipt = operation.Failed, false, &Receipt{Outcome: receiptRejected, SubmittedAt: submittedAt}
		op.Reason = resource.RouterRejected
	default:
		op.State, op.Unresolved, op.Receipt = operation.Failed, false, &Receipt{Outcome: receiptNotSent, SubmittedAt: submittedAt}
		op.Reason = resource.NotDelivered
		if errors.Is(sendErr, xiaomi.ErrAuth) {
			op.Reason = resource.NotDeliveredAuth
			m.dropClient(r.Connection)
		}
	}
	if sendErr != nil {
		m.log.Warn("router reboot submission", "router", r.Name, "operation", op.ID, "outcome", outcome.String(), "error", sendErr)
	}
	if err := m.ops.Save(ctx, &op, operation.Change{}); err != nil {
		return op, err
	}
	if op.State == operation.Observing {
		m.wake(r.ID)
	}
	return op, nil
}

// failUnsent closes an operation that was stored but certainly not sent.
// A failure to store that is only logged: the operation stays pending
// until closeAbandoned closes it the same way.
func (m *Manager) failUnsent(ctx context.Context, op *Operation, reason string) {
	op.State, op.Unresolved, op.Reason = operation.Failed, false, reason
	op.Receipt = &Receipt{Outcome: receiptNotSent, SubmittedAt: m.clk.Now().UTC()}
	if err := m.ops.Save(ctx, op, operation.Change{}); err != nil {
		m.log.Warn("router operation left pending", "operation", op.ID, "error", err)
	}
}

// closeAbandoned closes op when it is still pending or submitting although
// no Submit is working on it; the caller makes sure of that (control lock,
// startup or a passed deadline). The router is asked to reboot only after
// the submitting state is stored, so a pending request was never sent and
// failed; a submitting one may have reached the router, so its outcome is
// indeterminate. Neither is ever sent again. Other states are left alone.
func (m *Manager) closeAbandoned(ctx context.Context, op *Operation) error {
	pending := op.State == operation.Pending
	if !op.CloseAbandoned(pending) {
		return nil
	}
	if pending {
		op.Receipt = &Receipt{Outcome: receiptNotSent, SubmittedAt: m.clk.Now().UTC()}
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

// observing reports whether a router has an operation waiting for evidence.
func (m *Manager) observing(ctx context.Context, id string) bool {
	ops, err := m.ops.Unresolved(ctx, id, operation.Observing)
	return err == nil && len(ops) > 0
}

// settle decides observing operations of a router from its latest status:
// succeeded on reboot evidence, indeterminate once the deadline passed
// without it. Operations in other states are left alone.
func (m *Manager) settle(ctx context.Context, id string) {
	m.closeStale(ctx, id)
	ops, err := m.ops.Unresolved(ctx, id, operation.Observing)
	if err != nil {
		m.log.Warn("read router operations failed", "error", err)
		return
	}
	if len(ops) == 0 {
		return
	}
	r, err := m.Get(ctx, "@"+id)
	if err != nil {
		m.log.Warn("read router failed", "error", err)
		return
	}
	now := m.clk.Now()
	for i := range ops {
		op := &ops[i]
		switch {
		case rebootVerified(*op, r.Status):
			ev := r.Status
			op.State, op.Unresolved, op.Evidence = operation.Succeeded, false, &ev
			op.Reason = resource.RouterBooted
		case !now.Before(op.Request.Deadline):
			op.State = operation.Indeterminate
			op.Reason = resource.NoRebootEvidence
		default:
			continue
		}
		if err := m.ops.Save(ctx, op, operation.Change{}); err != nil && !errors.Is(err, operation.ErrStale) {
			m.log.Warn("save router operation failed", "operation", op.ID, "error", err)
		}
	}
}

// closeStale closes operations of a router left pending or submitting past
// their deadline, which no Submit can still be working on. The control lock
// is only tried: a held lock means a Submit is in flight, and the next
// settle looks again.
func (m *Manager) closeStale(ctx context.Context, id string) {
	locks := m.locksFor(id)
	if !locks.control.TryLock() {
		return
	}
	defer locks.control.Unlock()
	ops, err := m.ops.Unresolved(ctx, id, operation.Pending, operation.Submitting)
	if err != nil {
		m.log.Warn("read router operations failed", "error", err)
		return
	}
	now := m.clk.Now()
	for i := range ops {
		if now.Before(ops[i].Request.Deadline) {
			continue
		}
		if err := m.closeAbandoned(ctx, &ops[i]); err != nil {
			m.log.Warn("save router operation failed", "operation", ops[i].ID, "error", err)
		}
	}
}

// rebootVerified reports whether st proves the reboot of op: a fresh
// successful observation, taken after the submission and before the
// deadline, of a router that booted after the submission (minus
// bootTolerance) and after the baseline boot.
func rebootVerified(op Operation, st Status) bool {
	s := st.Snapshot
	if op.Receipt == nil || !st.Fresh || s == nil || !s.ObservedAt.After(op.Receipt.SubmittedAt) || s.ObservedAt.After(op.Request.Deadline) {
		return false
	}
	at, ok := bootAt(s)
	return ok && !at.Before(op.Receipt.SubmittedAt.Add(-bootTolerance)) && at.After(op.Request.BaselineBootAt.Add(bootTolerance))
}

// Reconcile runs at startup. A request interrupted before it was sent
// failed; one interrupted after that, before its outcome was recorded,
// becomes indeterminate. Neither is sent again. Observing requests are
// decided from the stored status.
func (m *Manager) Reconcile(ctx context.Context) error {
	ops, err := m.ops.Unresolved(ctx, "", operation.Pending, operation.Submitting)
	if err != nil {
		return err
	}
	for i := range ops {
		if err := m.closeAbandoned(ctx, &ops[i]); err != nil {
			return err
		}
	}
	observing, err := m.ops.Unresolved(ctx, "", operation.Observing)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, op := range observing {
		if !seen[op.ResourceID] {
			seen[op.ResourceID] = true
			m.settle(ctx, op.ResourceID)
		}
	}
	return nil
}

// Resolve closes an indeterminate operation after review. It never turns
// the operation into a success; it only records who closed it and why.
func (m *Manager) Resolve(ctx context.Context, ref, id, reason string, expected int) (Operation, error) {
	a, err := operation.RequireDirect(ctx, ErrForbidden)
	if err != nil {
		return Operation{}, err
	}
	if !operation.ValidReason(reason) {
		return Operation{}, precondition("a resolution reason is required")
	}
	r, err := m.Get(ctx, ref)
	if err != nil {
		return Operation{}, err
	}
	locks := m.locksFor(r.ID)
	locks.control.Lock()
	defer locks.control.Unlock()
	op, err := m.ops.Get(ctx, r.ID, id)
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
	switch {
	case r.Version != expected:
		return Operation{}, ErrConflict
	case m.clk.Now().Before(op.Request.Deadline):
		return Operation{}, precondition("a request that may still take effect cannot be resolved before its deadline")
	}
	op.MarkResolved(resource.ActorOf(a), reason, m.clk.Now().UTC())
	err = m.ops.Save(ctx, &op, operation.Change{Audit: &operation.Audit{
		Action: "operation.resolve", Target: r.Name, Details: map[string]any{"operationId": op.ID, "reason": reason, "unverified": true},
	}})
	if errors.Is(err, operation.ErrStale) {
		return Operation{}, precondition("operation changed; reload it")
	}
	if err != nil {
		return Operation{}, err
	}
	return op, nil
}

// Operation returns one operation of a router.
func (m *Manager) Operation(ctx context.Context, ref, id string) (Operation, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return Operation{}, err
	}
	return m.ops.Get(ctx, r.ID, id)
}

// Operations returns the newest operations of a router first.
func (m *Manager) Operations(ctx context.Context, ref string) ([]Operation, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	return m.ops.List(ctx, r.ID)
}
