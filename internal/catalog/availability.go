package catalog

import (
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/message"
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
	// ReasonHelperUnavailable: the service is managed and the host's deploy
	// helper, which performs the action, is not installed.
	ReasonHelperUnavailable = "helperUnavailable"
	// ReasonInvalidState: the action may not start in the current state.
	ReasonInvalidState = "invalidState"
	// ReasonNotManaged: the service is retired.
	ReasonNotManaged = "notManaged"
	// ReasonBusy: another action or phase change is running.
	ReasonBusy = "busy"
)

// Actions lists every action of the manager.
var Actions = []string{
	ActionStart, ActionStop, ActionRestart, ActionDeploy, ActionRollback, ActionBackup, ActionRestore, ActionRunJob, ActionApplyRoute,
}

// Availability tells whether an action can start on a service now.
type Availability struct {
	Action  string
	Allowed bool
	// Reason is one of the Reason constants; empty when allowed.
	Reason string
	// Message says more about Reason when its cause varies (why the deploy
	// helper is unavailable); nil otherwise.
	Message *message.Message
}

// AvailableActions reports for every action whether it can start on rec,
// from the phase, the document, the runtime's capabilities, the deploy
// helper (helper is nil when it can be called; only managed services use
// it) and the state. Deployment history is not consulted, so an available
// rollback may still find no target; Plan gives the definitive answer.
func AvailableActions(rec *Record, helper error) []Availability {
	out := make([]Availability, 0, len(Actions))
	for _, a := range Actions {
		reason := ""
		var msg *message.Message
		switch {
		case rec.Phase != resource.PhaseManaged:
			reason = ReasonNotManaged
		case !configured(a, rec.Config):
			reason = ReasonNotConfigured
		case checkRuntime(a, rec.Runtime()) != nil:
			reason = ReasonUnsupported
		case checkHelper(a, rec.Runtime(), helper) != nil:
			reason, msg = ReasonHelperUnavailable, helperMessage(helper)
		case !CanAct(a, rec.State):
			reason = ReasonInvalidState
		}
		out = append(out, Availability{Action: a, Allowed: reason == "", Reason: reason, Message: msg})
	}
	return out
}

// helperMessage is the message naming why the deploy helper cannot be
// called.
func helperMessage(ready error) *message.Message {
	var ue *rt.UnavailableError
	if !errors.As(ready, &ue) {
		m := message.New("action.helperNotInstalled", ready.Error())
		return &m
	}
	code := "action.helperNotInstalled"
	switch ue.Cause {
	case rt.CauseUntrusted:
		code = "action.helperUntrusted"
	case rt.CauseWorkDir:
		code = "action.helperWorkDir"
	}
	m := message.New(code, ue.Error())
	for k, v := range ue.Params {
		m.Params[k] = v
	}
	return &m
}

func configured(action string, cfg *model.ServiceConfig) bool {
	switch action {
	case ActionDeploy, ActionRollback:
		return cfg.Source != nil
	case ActionBackup:
		return cfg.Backup != nil
	case ActionRestore:
		return cfg.Backup != nil && cfg.Backup.Typed() && cfg.Backup.Restorable()
	case ActionRunJob:
		return len(cfg.Jobs) > 0
	case ActionApplyRoute:
		return cfg.Route != nil
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

// checkHelper refuses the actions the deploy helper performs for a managed
// service while the helper cannot be called (ready says why), before
// anything is recorded.
func checkHelper(action string, r model.Runtime, ready error) error {
	if ready == nil {
		return nil
	}
	// Presenting a service is the helper's work for every runtime.
	if action == ActionApplyRoute {
		return fmt.Errorf("%w: %w", ErrHelperUnavailable, ready)
	}
	if r.Type != model.RuntimeManaged {
		return nil
	}
	switch action {
	case ActionStart, ActionStop, ActionRestart, ActionDeploy, ActionRollback, ActionBackup, ActionRestore:
		return fmt.Errorf("%w: %w", ErrHelperUnavailable, ready)
	}
	return nil
}
