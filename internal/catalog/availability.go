package catalog

import (
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Reasons why an action is unavailable, in the order Availability checks
// them.
const (
	// ReasonNotConfigured: the document lacks what the action needs (a
	// source, a backup command, jobs).
	ReasonNotConfigured = "notConfigured"
	// ReasonUnsupported: the runtime cannot perform the action.
	ReasonUnsupported = "unsupported"
	// ReasonInvalidState: the action may not start in the current state.
	ReasonInvalidState = "invalidState"
)

// Actions lists every action of the manager.
var Actions = []string{
	ActionStart, ActionStop, ActionRestart, ActionDeploy, ActionRollback, ActionBackup, ActionRunJob, ActionRetire,
}

// Availability tells whether an action can start on a service now.
type Availability struct {
	Action  string
	Allowed bool
	// Reason is one of the Reason constants; empty when allowed.
	Reason string
}

// AvailableActions reports for every action whether it can start on rec,
// from the document, the runtime's capabilities and the state. Deployment
// history is not consulted, so an available rollback may still find no
// target; Plan gives the definitive answer.
func AvailableActions(rec *Record) []Availability {
	out := make([]Availability, 0, len(Actions))
	for _, a := range Actions {
		reason := ""
		switch {
		case !configured(a, rec.Spec):
			reason = ReasonNotConfigured
		case checkRuntime(a, rec.Spec) != nil:
			reason = ReasonUnsupported
		case !CanAct(a, rec.State):
			reason = ReasonInvalidState
		}
		out = append(out, Availability{Action: a, Allowed: reason == "", Reason: reason})
	}
	return out
}

func configured(action string, svc *model.Service) bool {
	switch action {
	case ActionDeploy, ActionRollback:
		return svc.Spec.Source != nil
	case ActionBackup:
		return svc.Spec.Backup != nil
	case ActionRunJob:
		return len(svc.Spec.Jobs) > 0
	}
	return true
}

// checkRuntime refuses process actions the runtime cannot perform, so that
// LCM never records a state change it did not make. Retire skips the stop
// and deploy skips the restart when unsupported, so both always pass.
func checkRuntime(action string, svc *model.Service) error {
	caps := rt.CapabilitiesOf(svc.Spec.Runtime)
	ok := true
	switch action {
	case ActionStart:
		ok = caps.Start
	case ActionStop:
		ok = caps.Stop
	case ActionRestart:
		ok = caps.Restart
	}
	if !ok {
		return fmt.Errorf("%w: %s on a %s runtime", ErrUnsupported, action, svc.Spec.Runtime.Type)
	}
	return nil
}
