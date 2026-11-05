package machine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/resource"
)

// DirectRequest marks an authenticated console/CLI/API boundary, never a policy run.
func DirectRequest(ctx context.Context) context.Context { return auth.WithDirect(ctx) }

func requireDirect(ctx context.Context) error {
	a, err := actor(ctx)
	if err != nil {
		return err
	}
	if !auth.IsDirect(ctx) || (a.Kind != auth.KindUser && a.Kind != auth.KindToken && a != auth.CLI) {
		return ErrForbidden
	}
	return nil
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

// Operation survives controller restarts and retains the original unverified result.
type Operation struct {
	Evidence   *Status      `json:"evidence,omitempty"`
	ID         string       `json:"id"`
	ResourceID string       `json:"resourceId"`
	Action     string       `json:"action"`
	State      string       `json:"state"`
	Unresolved bool         `json:"unresolved"`
	Version    int          `json:"version"`
	CreatedAt  time.Time    `json:"createdAt"`
	UpdatedAt  time.Time    `json:"updatedAt"`
	Actor      Actor        `json:"actor"`
	Request    PowerRequest `json:"request"`
	Receipt    *Receipt     `json:"receipt"`
	Reason     string       `json:"reason"`
	Resolution string       `json:"resolution,omitempty"`
	ResolvedBy *Actor       `json:"resolvedBy,omitempty"`
	ResolvedAt *time.Time   `json:"resolvedAt,omitempty"`
}

// Maintenance keeps the durable write gate until an explicit verified exit.
type Maintenance struct {
	Preview      MaintenancePreview `json:"preview"`
	ResourceID   string             `json:"resourceId"`
	State        string             `json:"state"`
	EnteredAt    time.Time          `json:"enteredAt"`
	Actor        Actor              `json:"actor"`
	Reason       string             `json:"reason"`
	Acknowledged bool               `json:"acknowledged"`
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
		reason = "restricted helper is not configured"
	case r.Phase != resource.PhaseManaged:
		reason = "machine is retired"
	case r.Status.Snapshot == nil || r.Status.Snapshot.Init != "systemd":
		reason = "power control requires a verified Linux/systemd host"
	case !r.Status.Fresh || r.Status.Identity != "verified":
		reason = "fresh verified observations are required"
	case r.Status.Health != "healthy":
		reason = "required health checks must be healthy"
	case !powerEvidenceReady(r.Status):
		reason = "required OS evidence sources are unavailable"
	}
	if reason == "" {
		if err := m.power.Preflight(ctx); err != nil {
			reason = "power control is disabled or its helper/autostart preflight failed"
		}
	}
	return []Capability{{Action: "reboot", Available: reason == "", Reason: reason}, {Action: "graceful-shutdown", Available: reason == "", Reason: reason}, {Action: "power-on", Available: false, Reason: "no independent power-on channel; manual power-on is required"}}, nil
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

// EnterMaintenance requires the operator to prepare unknown workloads explicitly.
func (m *Manager) EnterMaintenance(ctx context.Context, name, reason string, acknowledged bool, expected int, previewHash ...string) (Maintenance, error) {
	if err := requireDirect(ctx); err != nil {
		return Maintenance{}, err
	}
	if !validReason(reason) || !acknowledged {
		return Maintenance{}, precondition("a reason and workload/recovery acknowledgement are required")
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return Maintenance{}, err
	}
	if r.Version != expected {
		return Maintenance{}, ErrConflict
	}
	if r.Phase != resource.PhaseManaged {
		return Maintenance{}, precondition("machine is retired")
	}
	a, _ := actor(ctx)
	v := Maintenance{ResourceID: r.ID, State: "active", EnteredAt: m.clk.Now().UTC(), Actor: actorInfo(a), Reason: reason, Acknowledged: true}
	err = m.gate.Exclusive(ctx, func(tx *sql.Tx) error {
		p, err := preview(ctx, tx, r)
		if err != nil {
			return err
		}
		if len(previewHash) > 0 && (len(previewHash) != 1 || previewHash[0] != p.Hash) {
			return precondition("maintenance impact changed; review a new preview")
		}
		v.Preview = p
		body, err := encode(v)
		if err != nil {
			return err
		}
		if m.maintenancePreflight != nil {
			if err := m.maintenancePreflight(ctx); err != nil {
				return precondition("controller update or another maintenance dependency is pending")
			}
		}
		var count int
		cur, err := current(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if cur.phase != resource.PhaseManaged {
			return precondition("machine is retired")
		}
		if cur.version != expected {
			return ErrConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance`).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return precondition("maintenance is already active")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO machine_maintenance(host,resource_id,state,entered_at,body) VALUES('local',?,?,?,?)`, r.ID, v.State, v.EnteredAt.UnixMilli(), body)
		return err
	})
	if err == nil {
		err = m.record(ctx, "maintenance.enter", name, map[string]any{"reason": reason, "partialCoverageAcknowledged": true})
	}
	return v, err
}

func validReason(v string) bool {
	return strings.TrimSpace(v) != "" && len(v) <= 1000 && !strings.ContainsAny(v, "\x00\r")
}

func (m *Manager) ExitMaintenance(ctx context.Context, name, reason string, expected int) error {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	if err := requireDirect(ctx); err != nil {
		return err
	}
	if !validReason(reason) {
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
		var count int
		if err := m.verifyFreshTx(ctx, tx, r, true); err != nil {
			return err
		}
		anchor, err := recoveryAnchor(ctx, tx, r.ID)
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
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id=? AND unresolved=1`, r.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return precondition("resolve the outstanding operation first")
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

var idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func recoveryAnchor(ctx context.Context, tx *sql.Tx, id string) (time.Time, error) {
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
		op, err := decodeOperation(raw, nil)
		if err != nil {
			return time.Time{}, err
		}
		if op.ResolvedAt != nil && op.ResolvedAt.After(anchor) {
			anchor = *op.ResolvedAt
		}
	}
	return anchor, rows.Err()
}

// Submit records intent before handoff; an ambiguous handoff is never retried automatically.
func (m *Manager) Submit(ctx context.Context, name, action, key string, expected int) (Operation, error) {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	if err := requireDirect(ctx); err != nil {
		return Operation{}, err
	}
	if action != "reboot" && action != "graceful-shutdown" {
		return Operation{}, ErrUnsupported
	}
	if !idempotencyPattern.MatchString(key) {
		return Operation{}, precondition("a bounded idempotency key is required")
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	hashBytes := sha256.Sum256([]byte(r.ID + "\x00" + action + "\x00" + fmtVersion(expected)))
	hash := hex.EncodeToString(hashBytes[:])
	if prior, err := m.operationByKey(ctx, r.ID, key); err == nil {
		if prior.Request.RequestHash != hash {
			return Operation{}, ErrConflict
		}
		return prior, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Operation{}, err
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
	v, err := m.Maintenance(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	if v == nil || !v.Acknowledged {
		return Operation{}, precondition("enter acknowledged maintenance first")
	}
	a, _ := actor(ctx)
	now := m.clk.Now().UTC()
	op := Operation{ID: newID(), ResourceID: r.ID, Action: action, State: "pending", Unresolved: true, Version: expected, CreatedAt: now, UpdatedAt: now, Actor: actorInfo(a)}
	validUntil := r.Status.Snapshot.ObservedAt.Add(r.Config.StaleAfter.D())
	if capTime := now.Add(5 * time.Minute); validUntil.After(capTime) {
		validUntil = capTime
	}
	op.Request = PowerRequest{ControllerID: r.ID, OperationID: op.ID, RequestHash: hash, Action: action, BootID: r.Status.Snapshot.BootID, IssuedAt: now, ValidUntil: validUntil, Deadline: now.Add(10 * time.Minute)}
	body, err := encode(op)
	if err != nil {
		return Operation{}, err
	}
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
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
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_maintenance WHERE resource_id=? AND state='active'`, r.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return precondition("maintenance is not ready")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id=? AND unresolved=1`, r.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return precondition("an unresolved operation already exists")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO resource_operations(id,resource_id,idempotency_key,request_hash,action,state,created_at,updated_at,body) VALUES(?,?,?,?,?,?,?,?,?)`, op.ID, r.ID, key, hash, action, op.State, now.UnixMilli(), now.UnixMilli(), body)
		return err
	})
	if err != nil {
		if prior, e := m.operationByKey(ctx, r.ID, key); e == nil && prior.Request.RequestHash == hash {
			return prior, nil
		}
		return Operation{}, err
	}
	// From here on the outcome must be recorded even if the caller goes
	// away: an operation left pending blocks every power action until an
	// operator resolves it. The helper calls are bounded by their own
	// timeouts.
	ctx = context.WithoutCancel(ctx)
	if err = m.record(ctx, "operation.submit", name, map[string]any{"operationId": op.ID, "action": action}); err != nil {
		op.State, op.Unresolved, op.Reason = "failed", false, "request was not sent: its audit entry could not be stored"
		return op, errors.Join(err, m.saveOperation(ctx, &op))
	}
	receipt, err := m.power.Prepare(ctx, op.Request)
	if err != nil {
		op.State = "failed"
		op.Unresolved = false
		op.Reason = "helper preparation failed"
		return op, m.saveOperation(ctx, &op)
	}
	op.Receipt = &receipt
	if receipt.State != "prepared" {
		op.State = "indeterminate"
		op.Reason = "helper preparation did not establish a new prepared receipt"
		return op, m.saveOperation(ctx, &op)
	}
	op.State = "submitting"
	if err = m.saveSubmitting(ctx, &op, r); err != nil {
		op.State = "failed"
		op.Unresolved = false
		op.Reason = "required preconditions changed before handoff"
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if saveErr := m.saveOperation(fctx, &op); saveErr != nil {
			return op, saveErr
		}
		return op, nil
	}
	receipt, err = m.power.Submit(ctx, op.Request)
	if err != nil {
		op.State = "indeterminate"
		op.Reason = "helper submission could not be verified"
	} else {
		op.Receipt = &receipt
		op.State = "observing"
		if receipt.State != "accepted" {
			op.State = "indeterminate"
			op.Reason = "helper did not verify OS request acceptance"
		}
		if action == "graceful-shutdown" {
			op.State = "indeterminate"
			op.Reason = "accepted request has no independent power-off evidence"
		}
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if saveErr := m.saveOperation(fctx, &op); saveErr != nil {
		return op, saveErr
	}
	return op, nil
}

func fmtVersion(v int) string { return strconv.Itoa(v) }

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

func (m *Manager) saveSubmitting(ctx context.Context, op *Operation, r Record) error {
	op.UpdatedAt = m.clk.Now().UTC()
	body, err := encode(op)
	if err != nil {
		return err
	}
	return m.db.Write(ctx, func(tx *sql.Tx) error {
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
		if active != 1 || !m.clk.Now().Before(op.Request.ValidUntil) {
			return precondition("maintenance or health authorization expired")
		}
		_, err = tx.ExecContext(ctx, `UPDATE resource_operations SET state=?,updated_at=?,body=? WHERE id=? AND unresolved=1`, op.State, op.UpdatedAt.UnixMilli(), body, op.ID)
		return err
	})
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

func (m *Manager) saveOperation(ctx context.Context, op *Operation) error {
	op.UpdatedAt = m.clk.Now().UTC()
	body, err := encode(op)
	if err != nil {
		return err
	}
	var event *Event
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		var priorBody string
		if err := tx.QueryRowContext(ctx, `SELECT body FROM resource_operations WHERE id=?`, op.ID).Scan(&priorBody); err != nil {
			return err
		}
		prior, err := decodeOperation(priorBody, nil)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE resource_operations SET state=?,unresolved=?,updated_at=?,body=? WHERE id=?`, op.State, op.Unresolved, op.UpdatedAt.UnixMilli(), body, op.ID)
		if err != nil {
			return err
		}
		if prior.State != op.State || prior.Unresolved != op.Unresolved || prior.Reason != op.Reason {
			e := Event{ResourceID: op.ResourceID, OperationID: op.ID, Source: "control", Key: newID(), Kind: "machine.operation.changed", Severity: severity(op.State), OccurredAt: op.UpdatedAt, IngestedAt: op.UpdatedAt, Reason: op.State}
			if err := insertEvent(ctx, tx, &e); err != nil {
				return err
			}
			event = &e
		}
		return nil
	})
	if err == nil && event != nil {
		m.publish([]Event{*event})
	}
	return err
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

func (m *Manager) Operation(ctx context.Context, name, id string) (Operation, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	var body string
	err = m.db.Reader().QueryRowContext(ctx, `SELECT body FROM resource_operations WHERE resource_id=? AND id=?`, r.ID, id).Scan(&body)
	return decodeOperation(body, err)
}

func (m *Manager) Operations(ctx context.Context, name string) ([]Operation, error) {
	r, err := m.Get(ctx, name)
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
		if err = rows.Scan(&body); err != nil {
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

// Reconcile only queries receipts. It never resubmits an interrupted operation.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	rows, err := m.db.Reader().QueryContext(ctx, `SELECT o.body FROM resource_operations o JOIN resources r ON r.id = o.resource_id WHERE o.unresolved=1 AND r.type='machine'`)
	if err != nil {
		return err
	}
	var ops []Operation
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			_ = rows.Close()
			return err
		}
		op, err := decodeOperation(body, nil)
		if err != nil {
			_ = rows.Close()
			return err
		}
		ops = append(ops, op)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for i := range ops {
		op := &ops[i]
		if m.power == nil {
			op.State = "indeterminate"
			op.Reason = "helper unavailable during recovery"
		} else {
			receipt, queryErr := m.power.Query(ctx, op.Request)
			if queryErr != nil {
				op.State = "indeterminate"
				op.Reason = "helper receipt unavailable"
			} else {
				op.Receipt = &receipt
				r, readErr := m.Get(ctx, "@"+op.ResourceID)
				if readErr == nil && rebootVerified(*op, receipt, r) {
					op.State = "succeeded"
					op.Unresolved = false
					op.Reason = "new boot and fresh required health verified"
					op.Evidence = &r.Status
				} else if !m.clk.Now().Before(op.Request.Deadline) || op.Action == "graceful-shutdown" || op.State == "pending" || op.State == "submitting" {
					op.State = "indeterminate"
					op.Reason = "operation lacks verified completion evidence"
				}
			}
		}
		if err = m.saveOperation(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

func rebootVerified(op Operation, receipt Receipt, r Record) bool {
	s := r.Status.Snapshot
	if op.Action != "reboot" || receipt.State != "accepted" || receipt.BootID != op.Request.BootID || !r.Status.Fresh || r.Status.Identity != "verified" || r.Status.Health != "healthy" || !powerEvidenceReady(r.Status) || s == nil || s.BootID == op.Request.BootID || !s.ObservedAt.After(op.CreatedAt) {
		return false
	}
	bootAt := s.ObservedAt.Add(-time.Duration(s.UptimeSeconds * float64(time.Second)))
	return !s.ObservedAt.After(op.Request.Deadline) && !receipt.AcceptedAt.Before(op.CreatedAt) && !receipt.AcceptedAt.After(op.Request.ValidUntil) && !bootAt.Before(receipt.AcceptedAt.Add(-time.Minute)) && !bootAt.After(op.Request.Deadline.Add(time.Minute))
}

// Resolve closes review without rewriting an unverified operation as success.
func (m *Manager) Resolve(ctx context.Context, name, id, reason string, expected int) (Operation, error) {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	if err := requireDirect(ctx); err != nil {
		return Operation{}, err
	}
	if !validReason(reason) {
		return Operation{}, precondition("a resolution reason is required")
	}
	op, err := m.Operation(ctx, name, id)
	if err != nil {
		return Operation{}, err
	}
	if !op.Unresolved {
		return Operation{}, precondition("operation is already resolved")
	}
	if op.State != "indeterminate" {
		return Operation{}, precondition("only indeterminate operations can be manually resolved")
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return Operation{}, err
	}
	if !r.Status.Fresh || r.Status.Identity != "verified" {
		return Operation{}, precondition("fresh verified observations are required")
	}
	if r.Version != expected {
		return Operation{}, ErrConflict
	}
	if r.Status.Snapshot.BootID == op.Request.BootID {
		if m.clk.Now().Before(op.Request.ValidUntil) || m.power == nil {
			return Operation{}, precondition("an accepted or uncertain request requires a verified new boot before manual resolution")
		}
		receipt, err := m.power.Query(ctx, op.Request)
		if err != nil || (receipt.State != "prepared" && receipt.State != "failed") {
			return Operation{}, precondition("request quiescence is unverified; expiry does not revoke an accepted OS action")
		}
	}
	a, _ := actor(ctx)
	now := m.clk.Now().UTC()
	op.Unresolved = false
	op.Resolution = reason
	info := actorInfo(a)
	op.ResolvedBy = &info
	op.ResolvedAt = &now
	if err = m.saveOperation(ctx, &op); err != nil {
		return Operation{}, err
	}
	err = m.record(ctx, "operation.resolve", name, map[string]any{"operationId": id, "reason": reason, "unverified": true})
	return op, err
}
