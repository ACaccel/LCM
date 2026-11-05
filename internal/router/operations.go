package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/connection"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/router/xiaomi"
)

// Operation states.
const (
	statePending       = "pending"
	stateSubmitting    = "submitting"
	stateObserving     = "observing"
	stateSucceeded     = "succeeded"
	stateFailed        = "failed"
	stateIndeterminate = "indeterminate"
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
type Operation struct {
	Evidence   *Status        `json:"evidence,omitempty"`
	ID         string         `json:"id"`
	ResourceID string         `json:"resourceId"`
	Action     string         `json:"action"`
	State      string         `json:"state"`
	Unresolved bool           `json:"unresolved"`
	Version    int            `json:"version"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	Actor      resource.Actor `json:"actor"`
	Request    Request        `json:"request"`
	Receipt    *Receipt       `json:"receipt"`
	Reason     string         `json:"reason"`
	// RequestReason is the operator's reason for the request; Reason
	// explains the current state.
	RequestReason string          `json:"requestReason,omitempty"`
	Resolution    string          `json:"resolution,omitempty"`
	ResolvedBy    *resource.Actor `json:"resolvedBy,omitempty"`
	ResolvedAt    *time.Time      `json:"resolvedAt,omitempty"`
}

// Capability explains whether an operation can be submitted now.
type Capability struct {
	Action    string
	Available bool
	Reason    string
}

var idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func validReason(v string) bool {
	return strings.TrimSpace(v) != "" && len(v) <= 1000 && !strings.ContainsAny(v, "\x00\r")
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
		return "router is not managed", nil
	}
	conn, ok := m.conns.Get(r.Connection)
	switch {
	case !ok:
		return "connection is not configured on this host", nil
	case conn.Type != r.Document.Spec.Adapter:
		return "connection type does not match the adapter", nil
	case !conn.AllowsResource(r.Name):
		return "connection does not allow this resource", nil
	case !conn.Allows(connection.ActionReboot):
		return "connection does not allow reboot", nil
	case !online(r):
		return "a fresh successful observation is required", nil
	}
	if _, ok := bootAt(r.Status.Snapshot); !ok {
		return "the router does not report its uptime", nil
	}
	db := m.db.Reader()
	var unresolved, maint int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id = ? AND unresolved = 1`, r.ID).Scan(&unresolved); err != nil {
		return "", err
	}
	if unresolved > 0 {
		return "an unresolved operation already exists", nil
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance`).Scan(&maint); err != nil {
		return "", err
	}
	if maint > 0 {
		return "local machine maintenance is active", nil
	}
	return "", nil
}

// Submit stores a reboot request and sends it once. It returns the stored
// operation even when sending failed: the state tells what is known. The
// same idempotency key with the same version returns the first operation
// instead of sending again.
func (m *Manager) Submit(ctx context.Context, ref string, req resource.OperationRequest) (Operation, error) {
	if err := requireDirect(ctx); err != nil {
		return Operation{}, err
	}
	action, key, expected := req.Action, req.Key, req.Version
	if action != connection.ActionReboot {
		return Operation{}, fmt.Errorf("%w: only reboot is supported", ErrUnsupported)
	}
	if !idempotencyPattern.MatchString(key) {
		return Operation{}, precondition("a bounded idempotency key is required")
	}
	if !validReason(req.Reason) {
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
	sum := sha256.Sum256([]byte(r.ID + "\x00" + action + "\x00" + strconv.Itoa(expected)))
	hash := hex.EncodeToString(sum[:])
	if prior, err := m.operationByKey(ctx, r.ID, key); err == nil {
		if prior.Request.RequestHash != hash {
			return Operation{}, ErrConflict
		}
		// The control lock is held, so no Submit is in flight: a prior
		// request still pending or submitting was abandoned and must not
		// be reported as if it were progressing.
		if err := m.closeAbandoned(context.WithoutCancel(ctx), &prior); err != nil {
			return Operation{}, err
		}
		return prior, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Operation{}, err
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

	a, _ := actor(ctx)
	now := m.clk.Now().UTC()
	baseline, _ := bootAt(r.Status.Snapshot)
	op := Operation{
		ID: newID(), ResourceID: r.ID, Action: action, State: statePending, Unresolved: true, Version: expected,
		CreatedAt: now, UpdatedAt: now, Actor: resource.ActorOf(a), RequestReason: req.Reason,
		Request: Request{
			Action: action, Connection: r.Connection, RequestHash: hash, IssuedAt: now, Deadline: now.Add(rebootDeadline),
			BaselineObservedAt: r.Status.Snapshot.ObservedAt, BaselineBootAt: baseline,
		},
	}
	body, err := encode(op)
	if err != nil {
		return Operation{}, err
	}
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		cur, err := readCurrent(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if cur.version != expected {
			return ErrConflict
		}
		if cur.phase != resource.PhaseManaged {
			return precondition("router is not managed")
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id = ? AND unresolved = 1`, r.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return precondition("an unresolved operation already exists")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO resource_operations(id,resource_id,idempotency_key,request_hash,action,state,created_at,updated_at,body) VALUES(?,?,?,?,?,?,?,?,?)`,
			op.ID, r.ID, key, hash, action, op.State, now.UnixMilli(), now.UnixMilli(), body)
		return err
	})
	if err != nil {
		if prior, e := m.operationByKey(ctx, r.ID, key); e == nil && prior.Request.RequestHash == hash {
			return prior, nil
		}
		return Operation{}, err
	}
	// From here on the outcome must be recorded even if the caller goes
	// away: a request that may have reached the router is never repeated,
	// and one that was not sent must not stay pending.
	ctx = context.WithoutCancel(ctx)
	if err := m.record(ctx, "operation.submit", r, op.ID, map[string]any{"operationId": op.ID, "action": action, "connection": r.Connection, "reason": req.Reason}); err != nil {
		m.failUnsent(ctx, &op, "request was not sent: its audit entry could not be stored")
		return op, err
	}
	op.State = stateSubmitting
	if err := m.saveOperation(ctx, &op, statePending); err != nil {
		op.State = statePending
		m.failUnsent(ctx, &op, "request was not sent: its state could not be stored")
		return op, err
	}
	outcome, sendErr := c.Reboot(ctx)
	submittedAt := m.clk.Now().UTC()
	switch outcome {
	case xiaomi.RebootAccepted:
		op.State, op.Receipt = stateObserving, &Receipt{Outcome: receiptAccepted, SubmittedAt: submittedAt}
		op.Reason = "router accepted the request; waiting for reboot evidence"
	case xiaomi.RebootUnknown:
		op.State, op.Receipt = stateObserving, &Receipt{Outcome: receiptUnknown, SubmittedAt: submittedAt}
		op.Reason = "submission outcome unknown; waiting for reboot evidence"
	case xiaomi.RebootRejected:
		op.State, op.Unresolved, op.Receipt = stateFailed, false, &Receipt{Outcome: receiptRejected, SubmittedAt: submittedAt}
		op.Reason = "router rejected the request"
	default:
		op.State, op.Unresolved, op.Receipt = stateFailed, false, &Receipt{Outcome: receiptNotSent, SubmittedAt: submittedAt}
		op.Reason = "request was not delivered"
		if errors.Is(sendErr, xiaomi.ErrAuth) {
			op.Reason = "request was not delivered: authentication rejected"
			m.dropClient(r.Connection)
		}
	}
	if sendErr != nil {
		m.log.Warn("router reboot submission", "router", r.Name, "operation", op.ID, "outcome", outcome.String(), "error", sendErr)
	}
	if err := m.saveOperation(ctx, &op, stateSubmitting); err != nil {
		return op, err
	}
	if op.State == stateObserving {
		m.wake(r.ID)
	}
	return op, nil
}

// failUnsent closes an operation that was stored but certainly not sent.
// A failure to store that is only logged: the operation stays pending
// until closeAbandoned or Reconcile closes it the same way.
func (m *Manager) failUnsent(ctx context.Context, op *Operation, reason string) {
	from := op.State
	op.State, op.Unresolved, op.Reason = stateFailed, false, reason
	op.Receipt = &Receipt{Outcome: receiptNotSent, SubmittedAt: m.clk.Now().UTC()}
	if err := m.saveOperation(ctx, op, from); err != nil {
		m.log.Warn("router operation left pending", "operation", op.ID, "error", err)
	}
}

// closeAbandoned closes op when it is still pending or submitting although
// no Submit is working on it; the caller makes sure of that (control lock,
// startup or a passed deadline). A pending request was never sent, so it
// failed; a submitting one may have reached the router, so its outcome is
// indeterminate. Neither is ever sent again. Other states are left alone.
func (m *Manager) closeAbandoned(ctx context.Context, op *Operation) error {
	from := op.State
	switch from {
	case statePending:
		op.State, op.Unresolved = stateFailed, false
		op.Receipt = &Receipt{Outcome: receiptNotSent, SubmittedAt: m.clk.Now().UTC()}
		op.Reason = "request was never sent: submission stopped before sending"
	case stateSubmitting:
		op.State = stateIndeterminate
		op.Reason = "interrupted before the submission outcome was recorded; never resubmitted"
	default:
		return nil
	}
	if err := m.saveOperation(ctx, op, from); err != nil {
		if errors.Is(err, errStale) {
			cur, gerr := m.operationByID(ctx, op.ID)
			if gerr != nil {
				return gerr
			}
			*op = cur
			return nil
		}
		return err
	}
	return nil
}

// observing reports whether a router has an operation waiting for evidence.
func (m *Manager) observing(ctx context.Context, id string) bool {
	var n int
	if err := m.db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id = ? AND unresolved = 1 AND state = ?`,
		id, stateObserving).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// settle decides observing operations of a router from its latest status:
// succeeded on reboot evidence, indeterminate once the deadline passed
// without it. Operations in other states are left alone.
func (m *Manager) settle(ctx context.Context, id string) {
	m.closeStale(ctx, id)
	ops, err := m.unresolved(ctx, `o.resource_id = ? AND o.state = ?`, id, stateObserving)
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
			op.State, op.Unresolved, op.Evidence = stateSucceeded, false, &ev
			op.Reason = "router booted after the request and answers again"
		case !now.Before(op.Request.Deadline):
			op.State = stateIndeterminate
			op.Reason = "no reboot evidence before the deadline"
		default:
			continue
		}
		if err := m.saveOperation(ctx, op, stateObserving); err != nil && !errors.Is(err, errStale) {
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
	ops, err := m.unresolved(ctx, `o.resource_id = ? AND o.state IN (?, ?)`, id, statePending, stateSubmitting)
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
	ops, err := m.unresolved(ctx, `o.state IN (?, ?)`, statePending, stateSubmitting)
	if err != nil {
		return err
	}
	for i := range ops {
		if err := m.closeAbandoned(ctx, &ops[i]); err != nil {
			return err
		}
	}
	observing, err := m.unresolved(ctx, `o.state = ?`, stateObserving)
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
	if err := requireDirect(ctx); err != nil {
		return Operation{}, err
	}
	if !validReason(reason) {
		return Operation{}, precondition("a resolution reason is required")
	}
	r, err := m.Get(ctx, ref)
	if err != nil {
		return Operation{}, err
	}
	locks := m.locksFor(r.ID)
	locks.control.Lock()
	defer locks.control.Unlock()
	op, err := m.Operation(ctx, "@"+r.ID, id)
	if err != nil {
		return Operation{}, err
	}
	// With the control lock held no Submit is in flight, so a pending or
	// submitting operation was abandoned; close it before judging it.
	if err := m.closeAbandoned(context.WithoutCancel(ctx), &op); err != nil {
		return Operation{}, err
	}
	switch {
	case !op.Unresolved:
		return Operation{}, precondition("operation is already resolved")
	case op.State != stateIndeterminate:
		return Operation{}, precondition("only indeterminate operations can be manually resolved")
	case r.Version != expected:
		return Operation{}, ErrConflict
	case m.clk.Now().Before(op.Request.Deadline):
		return Operation{}, precondition("a request that may still take effect cannot be resolved before its deadline")
	}
	a, _ := actor(ctx)
	now := m.clk.Now().UTC()
	by := resource.ActorOf(a)
	op.Unresolved, op.Resolution, op.ResolvedBy, op.ResolvedAt = false, reason, &by, &now
	if err := m.saveOperation(ctx, &op, stateIndeterminate); err != nil {
		if errors.Is(err, errStale) {
			return Operation{}, precondition("operation changed; reload it")
		}
		return Operation{}, err
	}
	return op, m.record(ctx, "operation.resolve", r, op.ID, map[string]any{"operationId": op.ID, "reason": reason, "unverified": true})
}

// errStale means an operation changed since it was read.
var errStale = errors.New("router: operation changed concurrently")

// saveOperation stores op if it is still unresolved in state from, and
// records an event when its state, resolution or reason changed.
func (m *Manager) saveOperation(ctx context.Context, op *Operation, from string) error {
	op.UpdatedAt = m.clk.Now().UTC()
	body, err := encode(op)
	if err != nil {
		return err
	}
	var event *resource.Event
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		event = nil
		res, err := tx.ExecContext(ctx, `UPDATE resource_operations SET state=?,unresolved=?,updated_at=?,body=? WHERE id=? AND state=? AND unresolved=1`,
			op.State, op.Unresolved, op.UpdatedAt.UnixMilli(), body, op.ID, from)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errStale
		}
		e := resource.Event{
			ResourceID: op.ResourceID, OperationID: op.ID, Source: "control", Key: newID(), Kind: "router.operation.changed",
			Severity: severity(op.State), OccurredAt: op.UpdatedAt, IngestedAt: op.UpdatedAt, Reason: op.State,
		}
		if !op.Unresolved && op.State == stateIndeterminate {
			e.Reason = "resolved"
		}
		if err := resource.InsertEvent(ctx, tx, &e); err != nil {
			return err
		}
		event = &e
		return nil
	})
	if err == nil && event != nil {
		m.publish([]resource.Event{*event})
	}
	return err
}

// record audits a control action and stores its event.
func (m *Manager) record(ctx context.Context, action string, r Record, operationID string, details map[string]any) error {
	if err := m.audit.Record(ctx, "router."+action, r.Name, audit.OutcomeOK, details); err != nil {
		return err
	}
	now := m.clk.Now().UTC()
	e := resource.Event{ResourceID: r.ID, OperationID: operationID, Source: "control", Key: newID(), Kind: "router." + action, Severity: "info", OccurredAt: now, IngestedAt: now}
	if err := m.db.Write(ctx, func(tx *sql.Tx) error { return resource.InsertEvent(ctx, tx, &e) }); err != nil {
		return err
	}
	m.publish([]resource.Event{e})
	return nil
}

// unresolved reads the unresolved router operations matching where (over
// alias o).
func (m *Manager) unresolved(ctx context.Context, where string, args ...any) ([]Operation, error) {
	rows, err := m.db.Reader().QueryContext(ctx, `SELECT o.body FROM resource_operations o JOIN resources r ON r.id = o.resource_id
		WHERE o.unresolved = 1 AND r.type = 'router' AND `+where+` ORDER BY o.created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Operation
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		op, err := decodeOperation(body, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (m *Manager) operationByID(ctx context.Context, id string) (Operation, error) {
	var body string
	err := m.db.Reader().QueryRowContext(ctx, `SELECT body FROM resource_operations WHERE id=?`, id).Scan(&body)
	return decodeOperation(body, err)
}

func (m *Manager) operationByKey(ctx context.Context, id, key string) (Operation, error) {
	var body string
	err := m.db.Reader().QueryRowContext(ctx, `SELECT body FROM resource_operations WHERE resource_id=? AND idempotency_key=?`, id, key).Scan(&body)
	return decodeOperation(body, err)
}

func decodeOperation(body string, err error) (Operation, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	err = json.Unmarshal([]byte(body), &op)
	return op, err
}

// Operation returns one operation of a router.
func (m *Manager) Operation(ctx context.Context, ref, id string) (Operation, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return Operation{}, err
	}
	var body string
	err = m.db.Reader().QueryRowContext(ctx, `SELECT body FROM resource_operations WHERE resource_id=? AND id=?`, r.ID, id).Scan(&body)
	return decodeOperation(body, err)
}

// Operations returns the newest operations of a router first.
func (m *Manager) Operations(ctx context.Context, ref string) ([]Operation, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	rows, err := m.db.Reader().QueryContext(ctx, `SELECT body FROM resource_operations WHERE resource_id=? ORDER BY created_at DESC LIMIT 200`, r.ID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Operation{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		op, err := decodeOperation(body, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
