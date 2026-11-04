package resource

import (
	"context"
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/audit"
)

// ErrImpactUnacknowledged is wrapped by ImpactError.
var ErrImpactUnacknowledged = errors.New("resource: impact not acknowledged")

// ImpactError refuses a disruptive action on a resource other managed
// resources depend on or are controlled through, because the caller did not
// acknowledge that impact.
type ImpactError struct {
	Action string
	Impact Impact
}

func (e *ImpactError) Error() string {
	return fmt.Sprintf("resource: %s on %s affects %d dependent(s) and %d control dependent(s); acknowledge the impact to proceed",
		e.Action, e.Impact.Resource, len(e.Impact.Dependents), len(e.Impact.ControlDependents))
}

func (e *ImpactError) Unwrap() error { return ErrImpactUnacknowledged }

// phaseRetire is the phase change that can act on a resource: retiring a
// running service stops it.
const phaseRetire = "retire"

// Guarded reports whether an action needs an acknowledged impact: it is
// disruptive or destructive and acts on the resource. The only phase change
// that can act is retire; unmanaging archives a resource that is already
// retired and touches nothing that runs.
func Guarded(a Action) bool {
	if a.Risk != RiskDisruptive && a.Risk != RiskDestructive {
		return false
	}
	return a.Kind != KindPhase || a.Name == phaseRetire
}

// CheckImpact computes the impact of action a on the resource name and
// refuses it with an *ImpactError when the action is guarded, the impact is
// not empty and the caller did not acknowledge it. The impact is returned
// in every case, so dry runs can show it.
func (s *Store) CheckImpact(ctx context.Context, name string, a Action, acknowledged bool) (Impact, error) {
	imp, err := s.Impact(ctx, name)
	if err != nil {
		return Impact{}, err
	}
	if Guarded(a) && !imp.Empty() && !acknowledged {
		return imp, &ImpactError{Action: a.Name, Impact: imp}
	}
	return imp, nil
}

// RequiresAcknowledgement reports whether action a with this impact needs
// acknowledgeImpact.
func RequiresAcknowledgement(a Action, imp Impact) bool { return Guarded(a) && !imp.Empty() }

// WithAcknowledgedImpact records an acknowledged, non-empty impact on every
// audit entry written with the returned context, by resource names only.
func WithAcknowledgedImpact(ctx context.Context, imp Impact) context.Context {
	if imp.Empty() {
		return ctx
	}
	return audit.WithDetail(ctx, "acknowledgedImpact", imp.Names())
}
