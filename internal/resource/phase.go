package resource

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/model"
)

// transitions lists the allowed phase changes. Unmanaging goes through
// retired so that observation and control have stopped before a resource
// is archived.
var transitions = map[string][]string{
	PhaseManaged: {PhaseRetired},
	PhaseRetired: {PhaseManaged, PhaseUnmanaged},
}

// SetPhase moves a resource to another management phase. expected must
// equal the current document version. Unmanaging is refused while a config
// source owns the resource. Type-specific conditions are checked by the
// type's hooks in the same transaction; work outside the database (such as
// stopping a service) belongs to the type before it calls SetPhase.
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
		if phase == PhaseUnmanaged {
			var owned int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_ownership WHERE kind = ? AND name = ?`,
				string(model.KindResource), rec.Name).Scan(&owned); err != nil {
				return err
			}
			if owned > 0 {
				return precondition("%s is owned by a config source; release Git ownership before unmanaging it", rec.Name)
			}
		}
		if h := s.hooksFor(rec.Type); h != nil {
			if err := h.BeforePhase(ctx, tx, PhaseChange{Record: &rec, To: phase, Reason: reason, Actor: actor}); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE resources SET phase = ?, updated_at = ? WHERE id = ?`, phase, s.clk.Now().UnixMilli(), rec.ID)
		return err
	})
	if err != nil {
		return err
	}
	change := ChangePhase
	if phase == PhaseUnmanaged {
		change = ChangeUnmanaged
	}
	s.changed(Change{ID: rec.ID, Name: rec.Name, Type: rec.Type, Change: change})
	details := map[string]any{"id": rec.ID, "type": rec.Type, "from": rec.Phase, "to": phase}
	if reason != "" {
		details["reason"] = reason
	}
	return s.audit.Record(ctx, "resource.phase", rec.Name, audit.OutcomeOK, details)
}

func allowed(from, to string) bool {
	for _, p := range transitions[from] {
		if p == to {
			return true
		}
	}
	return false
}
