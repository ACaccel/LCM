package machine

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/resource/operation"
)

// identityAccept makes one sample accept the device identity it observes
// as the expected one; see RebindIdentity.
type identityAccept struct {
	name    string
	version int
	reason  string
	// resolved lists the operations the rebind closed first, for the audit
	// entry.
	resolved []string
}

// RebindIdentity records the device identity the machine reports now as
// the expected one of a machine whose identity reads "changed", typically
// after its database was restored on new hardware. The resource keeps its
// ID, history, documents and maintenance; observations, health and the
// helper's controller ID continue unchanged.
//
// It is a direct administrator request that needs the machine's exact name
// and a reason, and it audits the previous and the accepted identity
// hashes. It never trusts a stored observation: the identity is taken from
// a new sample committed together with the change, which refuses unless
// that sample is complete and still differs from the expected identity.
//
// Operations still unresolved are bound to the old identity's history, so
// none may survive: a pending or submitting one is closed after a helper
// receipt query, as at recovery, and the rest are resolved without
// verification when they can no longer act (see checkQuiescent), which
// includes every request of another boot than the current one. Otherwise
// the rebind is refused and nothing but those closures has changed. The
// power helper's receipt ledger is neither read nor altered: the helper of
// a new host must be provisioned for the machine's ID as for any new
// installation.
func (m *Manager) RebindIdentity(ctx context.Context, name string, expected int, reason, confirm string) (Record, error) {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	a, err := operation.RequireDirect(ctx, ErrForbidden)
	if err != nil {
		return Record{}, err
	}
	if !operation.ValidReason(reason) {
		return Record{}, precondition("a reason is required")
	}
	r, err := m.Get(ctx, name)
	if err != nil {
		return Record{}, err
	}
	if confirm != r.Name {
		return Record{}, precondition("confirm the request with the exact resource name")
	}
	if r.Version != expected {
		return Record{}, ErrConflict
	}
	if r.Phase != resource.PhaseManaged {
		return Record{}, precondition(resource.MachineRetired)
	}
	if m.observer == nil {
		return Record{}, ErrUnsupported
	}
	if !r.Status.Fresh {
		return Record{}, precondition(resource.NeedsObservation)
	}
	if r.Status.Identity != "changed" {
		return Record{}, precondition(resource.IdentityNotChanged)
	}
	resolved, err := m.settleForRebind(ctx, a, r, reason)
	if err != nil {
		return Record{}, err
	}
	m.pollMu.Lock()
	defer m.pollMu.Unlock()
	return m.observeLocked(ctx, name, &identityAccept{name: r.Name, version: expected, reason: reason, resolved: resolved})
}

// settleForRebind closes every unresolved operation of r or returns why one
// cannot be closed, deciding all before it resolves any.
func (m *Manager) settleForRebind(ctx context.Context, a auth.Actor, r Record, reason string) ([]string, error) {
	ops, err := m.ops.Unresolved(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	var open []*Operation
	for i := range ops {
		op := &ops[i]
		// With the control lock held no Submit is in flight, so a pending
		// or submitting operation was abandoned.
		if err := m.closeAbandoned(context.WithoutCancel(ctx), op); err != nil {
			return nil, err
		}
		if !op.Unresolved {
			continue
		}
		if err := m.checkQuiescent(ctx, *op, r); err != nil {
			return nil, err
		}
		open = append(open, op)
	}
	ids := make([]string, 0, len(open))
	for _, op := range open {
		if op.State == operation.Observing {
			op.State, op.Reason = operation.Indeterminate, resource.NoCompletionEvidence
		}
		op.MarkResolved(actorInfo(a), "device identity rebound: "+reason, m.clk.Now().UTC())
		err := m.ops.Save(ctx, op, operation.Change{Audit: &operation.Audit{
			Action: "operation.resolve", Target: r.Name,
			Details: map[string]any{"operationId": op.ID, "reason": reason, "unverified": true, "identityRebind": true},
		}})
		if errors.Is(err, operation.ErrStale) {
			return nil, precondition("operation changed; reload it")
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, op.ID)
	}
	return ids, nil
}

// acceptIdentityTx makes the sample being committed in tx accept its device
// identity: it checks that the sample is complete, that the stored status
// and the sample still read "changed" and that nothing is unresolved, sets
// the expected identity to the sampled one and records the audit entry and
// control event. It returns the event for publication after the commit.
func (m *Manager) acceptIdentityTx(ctx context.Context, tx *sql.Tx, r Record, cur currentRow, accept *identityAccept, next *Status, snap Snapshot, identity *string) (*Event, error) {
	if cur.version != accept.version {
		return nil, ErrConflict
	}
	if cur.status.Identity != "changed" || next.Identity != "changed" {
		return nil, precondition(resource.IdentityNotChanged)
	}
	if snap.Coverage["probe"].State == "unknown" || snap.Coverage["identity"].State != "available" || snap.DeviceHash == "" || snap.BootID == "" {
		return nil, precondition(resource.NeedsObservation)
	}
	if n, err := m.ops.UnresolvedCount(ctx, tx, r.ID); err != nil || n > 0 {
		if err == nil {
			err = precondition(resource.OperationUnresolved)
		}
		return nil, err
	}
	e, err := m.recordTx(ctx, tx, "identity.rebind", accept.name, r.ID, map[string]any{
		"reason": accept.reason, "previousIdentity": *identity, "acceptedIdentity": snap.DeviceHash, "resolvedOperations": accept.resolved,
	})
	if err != nil {
		return nil, err
	}
	*identity = snap.DeviceHash
	next.Identity = "verified"
	return &e, nil
}
