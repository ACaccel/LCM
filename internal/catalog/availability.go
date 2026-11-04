package catalog

import (
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
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
	// ReasonNotManaged: the service is retired.
	ReasonNotManaged = "notManaged"
	// ReasonBusy: another action or phase change is running.
	ReasonBusy = "busy"
)

// Actions lists every action of the manager.
var Actions = []string{
	ActionStart, ActionStop, ActionRestart, ActionDeploy, ActionRollback, ActionBackup, ActionRunJob,
}

// Availability tells whether an action can start on a service now.
type Availability struct {
	Action  string
	Allowed bool
	// Reason is one of the Reason constants; empty when allowed.
	Reason string
}

// AvailableActions reports for every action whether it can start on rec,
// from the phase, the document, the runtime's capabilities and the state. Deployment
// history is not consulted, so an available rollback may still find no
// target; Plan gives the definitive answer.
func AvailableActions(rec *Record) []Availability {
	out := make([]Availability, 0, len(Actions))
	for _, a := range Actions {
		reason := ""
		switch {
		case rec.Phase != resource.PhaseManaged:
			reason = ReasonNotManaged
		case !configured(a, rec.Config):
			reason = ReasonNotConfigured
		case checkRuntime(a, rec.Runtime()) != nil:
			reason = ReasonUnsupported
		case !CanAct(a, rec.State):
			reason = ReasonInvalidState
		}
		out = append(out, Availability{Action: a, Allowed: reason == "", Reason: reason})
	}
	return out
}

func configured(action string, cfg *model.ServiceConfig) bool {
	switch action {
	case ActionDeploy, ActionRollback:
		return cfg.Source != nil
	case ActionBackup:
		return cfg.Backup != nil
	case ActionRunJob:
		return len(cfg.Jobs) > 0
	}
	return true
}

// checkRuntime refuses process actions the runtime cannot perform, so that
// LCM never records a state change it did not make. Deploy skips the
// restart when unsupported, so it always passes; so does retiring, which
// is a phase change that stops only what the runtime can stop.
func checkRuntime(action string, r model.Runtime) error {
	caps := rt.CapabilitiesOf(r)
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
		return fmt.Errorf("%w: %s on a %s runtime", ErrUnsupported, action, r.Type)
	}
	return nil
}
