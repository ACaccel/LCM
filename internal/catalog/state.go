package catalog

import (
	"errors"
	"fmt"
)

// State is the lifecycle state of a service. Health is a separate concern
// owned by the monitor.
type State string

// Lifecycle states.
const (
	StateRegistered  State = "registered"
	StateDeploying   State = "deploying"
	StateRunning     State = "running"
	StateUpdating    State = "updating"
	StateRollingBack State = "rolling_back"
	StateStopped     State = "stopped"
	StateFailed      State = "failed"
	StateRetired     State = "retired"
)

// States lists every state in display order.
var States = []State{
	StateRegistered, StateDeploying, StateRunning, StateUpdating,
	StateRollingBack, StateStopped, StateFailed, StateRetired,
}

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Transient reports whether s is held only while an action is running. A
// transient state found without a running action means the process stopped
// mid-action.
func (s State) Transient() bool {
	return s == StateDeploying || s == StateUpdating || s == StateRollingBack
}

// transitions is the complete table of allowed state changes. An action
// that fails ends in failed unless the runtime reports the service still
// running, so every non-terminal state can reach failed.
var transitions = map[State]map[State]bool{
	StateRegistered:  set(StateDeploying, StateRunning, StateStopped, StateFailed, StateRetired),
	StateDeploying:   set(StateRunning, StateFailed),
	StateUpdating:    set(StateRunning, StateFailed),
	StateRollingBack: set(StateRunning, StateFailed),
	StateRunning:     set(StateUpdating, StateRollingBack, StateStopped, StateFailed, StateRetired),
	StateStopped:     set(StateDeploying, StateRunning, StateRollingBack, StateFailed, StateRetired),
	StateFailed:      set(StateDeploying, StateRollingBack, StateRunning, StateStopped, StateRetired),
	StateRetired:     set(),
}

func set(states ...State) map[State]bool {
	m := make(map[State]bool, len(states))
	for _, s := range states {
		m[s] = true
	}
	return m
}

// CanTransition reports whether the lifecycle allows moving from one state
// to another. Staying in the same state is not a transition.
func CanTransition(from, to State) bool { return transitions[from][to] }

// Errors of the lifecycle.
var (
	// ErrInvalidTransition is returned when the state machine forbids a change.
	ErrInvalidTransition = errors.New("catalog: transition not allowed")
	// ErrStateChanged is returned when the state is no longer the expected one.
	ErrStateChanged = errors.New("catalog: state changed concurrently")
)

// StateError reports an action that the current state forbids.
type StateError struct {
	Service string
	Action  string
	State   State
}

func (e *StateError) Error() string {
	return fmt.Sprintf("catalog: cannot %s service %q in state %s", e.Action, e.Service, e.State)
}

// Is makes errors.Is(err, ErrInvalidTransition) true for StateError.
func (e *StateError) Is(target error) bool { return target == ErrInvalidTransition }

// actionStates lists the states each action may start from. Deploy,
// rollback and retire also move through the transitions table.
var actionStates = map[string][]State{
	ActionStart:    {StateRegistered, StateStopped, StateFailed},
	ActionStop:     {StateRegistered, StateRunning, StateFailed},
	ActionRestart:  {StateRunning, StateStopped, StateFailed},
	ActionDeploy:   {StateRegistered, StateRunning, StateStopped, StateFailed},
	ActionRollback: {StateRunning, StateStopped, StateFailed},
	ActionBackup:   {StateRegistered, StateRunning, StateStopped, StateFailed},
	ActionRunJob:   {StateRegistered, StateRunning, StateStopped, StateFailed},
	ActionRetire:   {StateRegistered, StateRunning, StateStopped, StateFailed},
}

// Actions of the manager.
const (
	ActionDeploy   = "deploy"
	ActionRollback = "rollback"
	ActionRestart  = "restart"
	ActionStart    = "start"
	ActionStop     = "stop"
	ActionBackup   = "backup"
	ActionRunJob   = "run-job"
	ActionRetire   = "retire"
)

// CanAct reports whether action may start in state.
func CanAct(action string, state State) bool {
	for _, s := range actionStates[action] {
		if s == state {
			return true
		}
	}
	return false
}
