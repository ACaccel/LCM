/*
Package CALM_PolicySelector is stage 2 of the CALM loop: it turns one slot of
telemetry into a policy mode and routes the run down that mode's output port.

The four output ports are the four modes in canonical order, so the port index is
the bandit's arm index: latency_priority 0, balanced 1, energy_saving 2,
resilient 3. Downstream each port feeds one CALM_PolicyMode instance.

Slot semantics: one Execute that reaches a decision is one slot. A run that ends
early -- telemetry unavailable, undecodable telemetry, or a failed selection --
holds the slot instead, so the stability guard's dwell and switch budget count
decisions rather than trigger events.

Everything the node reads and writes about the service lives in the shared calm
loop state, not on this struct: post-verification writes the same record from a
later node in the same slot, and the node instance is replaced on redeploy while
the learner must survive it.
*/
package CALM_PolicySelector

import (
	"fmt"
	"os"
	"sync"

	lcm "1935/efo/LCM_Enabler/interfaces"
	i "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/nodes/Functions/calm"
	u "1935/efo/Rule_Framework/nodes/utils"
)

// KeyTelemetryOk is the input field CALM_Telemetry clears when it could not read
// the control plane or the trace backend. An absent key counts as usable
// telemetry, so a policy driven by something other than CALM_Telemetry still
// runs; only an explicit false holds the slot.
const KeyTelemetryOk = "telemetry_ok"

// Reasons reported in the output's reason field. They explain which mode came
// out, which is not the same question as the guard verdict: a mode can be
// retained because the guard blocked a switch, or emitted unchanged because the
// slot never reached a decision.
const (
	reasonSelected             = "selected by bandit"
	reasonRetained             = "retained previous mode: stability guard blocked the switch"
	reasonTelemetryUnavailable = "telemetry unavailable"
)

// guardReasonHeld is the guard verdict of a slot that never got as far as
// asking the guard.
const guardReasonHeld = "not evaluated: slot held"

// Property is the node's design-time configuration.
type Property struct {
	// Alpha is the LinUCB exploration coefficient. The learner is shared by
	// every CALM node of the service and cannot change its exploration
	// coefficient once it has started learning, so this value takes effect only
	// when this call is the one that creates the service's loop state; later it
	// is ignored in favour of the live learner. A value of zero or less means
	// "unset" and leaves the configured selector_alpha (default 1.0) in place.
	Alpha float64 `yaml:"alpha"`
	// StateFile is an optional path for a JSON snapshot of the loop, written
	// after every committed decision and read back once when this node instance
	// first runs. Empty disables it. It is a convenience for a demo that
	// restarts the EFO, not a durability guarantee: every failure is logged and
	// the loop carries on with what it has in memory.
	StateFile string `yaml:"state_file"`
	// FallbackMode is the mode emitted when no decision has ever been made and
	// none can be made now. Empty means balanced.
	FallbackMode string `yaml:"fallback_mode"`
}

// OutputFormat is the decision readout. Every field is a scalar so the
// execution trace and the GUI run panel can show the whole basis of the
// decision, including the scores of the arms that were not taken.
type OutputFormat struct {
	Mode                  string  `mapstructure:"mode"`
	Slot                  int     `mapstructure:"slot"`
	ScoresLatencyPriority float64 `mapstructure:"scores_latency_priority"`
	ScoresBalanced        float64 `mapstructure:"scores_balanced"`
	ScoresEnergySaving    float64 `mapstructure:"scores_energy_saving"`
	ScoresResilient       float64 `mapstructure:"scores_resilient"`
	Allowed               bool    `mapstructure:"allowed"`
	Switched              bool    `mapstructure:"switched"`
	GuardReason           string  `mapstructure:"guard_reason"`
	Reason                string  `mapstructure:"reason"`
	ViolationRate         float64 `mapstructure:"violation_rate"`
	RecoveryState         string  `mapstructure:"recovery_state"`
}

var Description = i.NodeDescription{
	NodeType:      "CALM_PolicySelector",
	NumInputPort:  1,
	NumOutputPort: calm.NumArms,
}

// CALM_PolicySelector holds no decision state of its own; see the package
// comment.
type CALM_PolicySelector struct {
	i.FunctionNode
	// restoreOnce reads the state file at most once per node instance. Doing it
	// on every run would undo, every slot, everything learned since the last
	// save.
	restoreOnce sync.Once
}

func (n *CALM_PolicySelector) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	fallback := fallbackMode(property.FallbackMode)
	payload := u.ContentOf(u.FirstInputPacket(input))

	state, err := loopState(info.Service_Name, property.Alpha)
	if err != nil {
		// Without loop state there is no bandit, no guard and no memory of the
		// previous mode; the run still has to leave by one port.
		fmt.Println(Description.NodeType+": cannot obtain loop state,", err)
		return result(OutputFormat{
			Mode:        string(fallback),
			GuardReason: guardReasonHeld,
			Reason:      "loop state unavailable: " + err.Error(),
		})
	}
	n.restoreOnce.Do(func() { restoreState(state, property.StateFile) })

	var out OutputFormat
	var committed bool
	state.Do(func(l *calm.Loop) { out, committed = decide(l, payload, fallback) })

	// The snapshot takes the same lock as Do, so it can only be taken after the
	// callback has returned.
	if committed {
		saveState(state, property.StateFile)
	}
	return result(out)
}

// decide runs one slot against the loop. It reports whether the slot was
// committed, which is what advances the slot counter and is the only case worth
// snapshotting.
func decide(l *calm.Loop, payload lcm.DataPacketContent, fallback calm.Mode) (OutputFormat, bool) {
	out := OutputFormat{
		Slot:          l.Slot,
		ViolationRate: l.Recovery.ViolationRate(),
		RecoveryState: string(l.Recovery.State()),
		GuardReason:   guardReasonHeld,
	}
	hold := func(reason string) (OutputFormat, bool) {
		out.Mode = string(heldMode(l.PrevMode, fallback))
		out.Reason = reason
		return out, false
	}

	// 1. No telemetry means no basis for a decision. The slot is not consumed
	// and the pending reward is left for the next one, so a blind slot costs
	// the loop nothing but a repeat of the current mode.
	if !telemetryOK(payload) {
		return hold(reasonTelemetryUnavailable)
	}

	// 2. Fold in the previous slot's reward. The simulator updates at the end of
	// a slot; post-verification runs after this node, so the update lands at the
	// start of the next slot instead. The two are equivalent: nothing between
	// the two points reads the learner.
	//
	// The pending record is cleared either way. A rejected update cannot be
	// repaired by retrying it next slot, and a decision whose reward never
	// arrived is dropped rather than paired with a later slot's outcome.
	if l.Pending != nil && l.Pending.RewardReady {
		if err := l.LinUCB.Update(l.Pending.Context, l.Pending.Arm, l.Pending.Reward); err != nil {
			fmt.Println(Description.NodeType+": discarding the pending reward,", err)
		}
		l.Pending = nil
	}

	// 3. Rebuild the context from this slot's telemetry.
	var state calm.StateVector
	if err := state.FromMap(payload); err != nil {
		fmt.Println(Description.NodeType+": cannot read the telemetry state,", err)
		return hold("invalid telemetry: " + err.Error())
	}
	context := l.Context.Build(state, l.PrevMode, l.Recovery.ViolationRate())

	// 4. Ask the guard, then the bandit. Select is read-only, so it is called
	// even when the guard blocks a switch: the scores then show what the bandit
	// would have chosen, which is what makes a blocked slot legible in the
	// trace. A blocked slot still emits the previous mode.
	slot := l.Slot
	allowed := l.Guard.SwitchAllowed(slot)
	bypass := l.PrevSevere
	prevArm, hasPrev := calm.ModeIndex(l.PrevMode)
	// Slot 0 with no previous mode has nothing to retain, so a blocked guard
	// cannot stop the first decision. This reproduces the simulator, whose
	// guard check only guards a change away from an existing mode.
	selecting := allowed || bypass || !hasPrev
	out.Allowed = allowed
	out.GuardReason = guardReason(l, slot, allowed, bypass, hasPrev)

	arm, scores, err := l.LinUCB.Select(context)
	if err != nil {
		fmt.Println(Description.NodeType+": bandit selection failed,", err)
		if selecting {
			return hold("selection failed: " + err.Error())
		}
		// The scores were only wanted for the readout; the retained mode below
		// does not depend on them.
		arm = prevArm
	}
	out.ScoresLatencyPriority = scores[0]
	out.ScoresBalanced = scores[1]
	out.ScoresEnergySaving = scores[2]
	out.ScoresResilient = scores[3]

	reason := reasonSelected
	if !selecting {
		arm = prevArm
		reason = reasonRetained
	}
	mode, ok := calm.ModeAt(arm)
	if !ok {
		// Unreachable: Select and ModeIndex both return canonical arm indices.
		fmt.Println(Description.NodeType+": arm out of range,", arm)
		return hold(fmt.Sprintf("arm %d out of range", arm))
	}

	// 5. Commit the decision.
	switched := hasPrev && arm != prevArm
	l.Guard.Record(slot, switched)
	l.PrevMode = mode
	// The severe flag is not cleared here: post-verification owns it and
	// rewrites it every slot with that slot's verdict.
	l.Pending = &calm.PendingDecision{
		Context:  context,
		Arm:      arm,
		Mode:     mode,
		Switched: switched,
		Slot:     slot,
	}
	l.Slot = slot + 1

	out.Mode = string(mode)
	out.Switched = switched
	out.Reason = reason
	return out, true
}

// guardReason explains the verdict in the terms the guard was configured with,
// so a demo can read why a switch was refused without knowing the thresholds.
func guardReason(l *calm.Loop, slot int, allowed, bypass, hasPrev bool) string {
	dwell := slot - l.Guard.LastSwitchSlot()
	switches := l.Guard.SwitchesInWindow(slot)
	if bypass {
		return fmt.Sprintf("bypassed: severe violation in the previous slot (dwell %d, %d switches in window)", dwell, switches)
	}
	if allowed {
		return fmt.Sprintf("allowed: dwell %d, %d of %d switches used in the window",
			dwell, switches, l.Config.Guard.MaxSwitchesSW)
	}
	blocked := fmt.Sprintf("blocked: %d switches in the window reached max_switches_SW %d",
		switches, l.Config.Guard.MaxSwitchesSW)
	if dwell < l.Config.Guard.MinDwellT {
		blocked = fmt.Sprintf("blocked: dwell %d is below min_dwell_T %d", dwell, l.Config.Guard.MinDwellT)
	}
	if !hasPrev {
		return blocked + ", overridden: no mode to retain"
	}
	return blocked
}

// heldMode is what a slot emits when it reaches no decision: the mode already in
// force, or the fallback before there is one.
func heldMode(prev, fallback calm.Mode) calm.Mode {
	if _, ok := calm.ModeIndex(prev); ok {
		return prev
	}
	return fallback
}

// loopState resolves the shared loop for the service.
//
// The alpha property is passed through the configuration rather than applied to
// the learner, which gives it exactly the semantics StateFor documents: the
// first caller's configuration builds the loop and every later caller gets the
// existing one. The copy is shallow on purpose -- the maps it shares are read
// only from here on.
func loopState(service string, alpha float64) (*calm.LoopState, error) {
	cfg, err := calm.Shared()
	if err != nil {
		return nil, err
	}
	if alpha > 0 {
		tuned := *cfg
		tuned.SelectorAlpha = alpha
		cfg = &tuned
	}
	return calm.StateFor(service, cfg)
}

// telemetryOK reads the telemetry health flag from the flat payload.
func telemetryOK(payload lcm.DataPacketContent) bool {
	raw, found := payload[KeyTelemetryOk]
	if !found {
		return true
	}
	ok, isBool := raw.(bool)
	return !isBool || ok
}

// fallbackMode resolves the fallback property, defaulting to balanced. An
// unknown name is reported rather than silently accepted: the GUI offers an
// enum, so a bad value is a hand-edited policy file.
func fallbackMode(name string) calm.Mode {
	if name == "" {
		return calm.ModeBalanced
	}
	mode := calm.Mode(name)
	if _, ok := calm.ModeIndex(mode); !ok {
		fmt.Println(Description.NodeType+": unknown fallback_mode", name+", using", calm.ModeBalanced)
		return calm.ModeBalanced
	}
	return mode
}

// restoreState loads the optional state file. A missing file is the normal first
// run, not a failure.
func restoreState(state *calm.LoopState, path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Println(Description.NodeType+": cannot read state file "+path+",", err)
		}
		return
	}
	if err := state.RestoreJSON(data); err != nil {
		fmt.Println(Description.NodeType+": cannot restore state file "+path+",", err)
		return
	}
	fmt.Println(Description.NodeType + ": restored loop state from " + path)
}

// saveState writes the state file, best effort.
//
// The snapshot goes to a temporary file next to the target and is renamed over
// it, so a crash mid-write leaves the previous snapshot intact rather than a
// truncated one that would fail to load.
func saveState(state *calm.LoopState, path string) {
	if path == "" {
		return
	}
	data, err := state.SnapshotJSON()
	if err != nil {
		fmt.Println(Description.NodeType+": cannot snapshot the loop state,", err)
		return
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		fmt.Println(Description.NodeType+": cannot write state file "+temp+",", err)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		fmt.Println(Description.NodeType+": cannot replace state file "+path+",", err)
		os.Remove(temp)
	}
}

// result encodes the readout and routes the run down the chosen mode's port.
func result(out OutputFormat) i.NodeExecResult {
	arm, ok := calm.ModeIndex(calm.Mode(out.Mode))
	if !ok {
		// Unreachable: every path sets Mode to one of the four modes.
		fmt.Println(Description.NodeType+": no port for mode", out.Mode)
		arm, _ = calm.ModeIndex(calm.ModeBalanced)
	}
	encoded, err := u.EncodeOutput(out, Description)
	if err != nil {
		fmt.Println(Description.NodeType+": Encode output error,", err)
		encoded = u.EmptyOutput(Description.NumOutputPort)
	}
	return i.NodeExecResult{Output: encoded, Ports: []int{arm}}
}
