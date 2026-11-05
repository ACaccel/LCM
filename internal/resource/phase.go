package resource

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ACaccel/lcm/internal/audit"
)

// transitions lists the allowed phase changes. Unmanaging goes through
// retired so that observation and control have stopped before a resource
// is archived.
var transitions = map[string][]string{
	PhaseManaged: {PhaseRetired},
	PhaseRetired: {PhaseManaged, PhaseUnmanaged},
}

// PhaseGuard takes part in every phase change of any resource type. It runs
// inside the write transaction before the type's hooks; an error, usually
// wrapping ErrPrecondition, aborts the change.
type PhaseGuard interface {
	CheckPhase(ctx context.Context, tx *sql.Tx, rec Record, to string) error
}

// SetPhase moves a resource to another management phase. expected must
// equal the current document version. The configured PhaseGuard can refuse
// the change, such as unmanaging a resource a config source owns.
// Type-specific conditions are checked by the type's hooks in the same
// transaction; work outside the database (such as stopping a service)
// belongs to the type before it calls SetPhase.
func (s *Store) SetPhase(ctx context.Context, ref, phase string, expected int, reason string) error {
	actor, err := requireActor(ctx)
	if err != nil {
		return err
	}
	switch phase {
	case PhaseManaged, PhaseRetired, PhaseUnmanaged:
	default:
		return precondition("unknown phase %q", phase)
	}
	ctx, release, err := s.gate.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	var rec Record
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		cur, err := lookup(ctx, tx, ref)
		if err != nil {
			return err
		}
		rec = cur.Record
		if rec.Version != expected {
			return fmt.Errorf("%w: %s is at version %d, expected %d", ErrConflict, rec.Name, rec.Version, expected)
		}
		if !allowed(rec.Phase, phase) {
			return precondition("%s is %s and cannot become %s", rec.Name, rec.Phase, phase)
		}
		if s.phases != nil {
			if err := s.phases.CheckPhase(ctx, tx, rec, phase); err != nil {
				return err
			}
		}
		if h := s.hooksFor(rec.Type); h != nil {
			if err := h.BeforePhase(ctx, tx, PhaseChange{Record: &rec, To: phase, Reason: reason, Actor: actor}); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE resources SET phase = ?, updated_at = ? WHERE id = ?`, phase, s.clk.Now().UnixMilli(), rec.ID); err != nil {
			return err
		}
		details := map[string]any{"id": rec.ID, "type": rec.Type, "from": rec.Phase, "to": phase}
		if reason != "" {
			details["reason"] = reason
		}
		return s.audit.RecordTx(ctx, tx, "resource.phase", rec.Name, audit.OutcomeOK, details)
	})
	if err != nil {
		return err
	}
	change := ChangePhase
	if phase == PhaseUnmanaged {
		change = ChangeUnmanaged
	}
	s.changed(Change{ID: rec.ID, Name: rec.Name, Type: rec.Type, Change: change})
	return nil
}

func allowed(from, to string) bool {
	for _, p := range transitions[from] {
		if p == to {
			return true
		}
	}
	return false
}
