package calm

/*
RecoveryManager — the soft/hard recovery state machine and Omega^stable
bookkeeping.

The violation history is a sliding window of the last W slots. Let rate be the
fraction of violated slots in that window; the transitions are

	NORMAL: rate > eta                      -> SOFT (soft counter reset)
	SOFT:   rate <= eta                     -> NORMAL
	        else soft counter >= Wr         -> HARD
	HARD:   rate <= eta                     -> NORMAL

Omega^stable = (mode, replica targets) is the last configuration that verified
clean. It is refreshed only while the state is NORMAL and the slot recorded no
violation, so a rollback can never restore a configuration that was already
failing.

Which action a mode takes once recovery is active is a per-mode strategy chosen
by PolicyParams.RecoveryBehavior; the thresholds eta and Wr stay global.

The simulator keys this per cluster. v1 runs a single decision domain per
deployed policy, so there is one manager per LoopState.
*/

import "fmt"

// RecoveryState is the recovery phase of the decision domain. The string values
// are the wire form carried in the node payload and the trace.
type RecoveryState string

// The three recovery phases.
const (
	RecoveryNormal RecoveryState = "normal"
	RecoverySoft   RecoveryState = "soft"
	RecoveryHard   RecoveryState = "hard"
)

// RecoveryAction is the reconfiguration a mode requests while in recovery.
type RecoveryAction string

// The four recovery actions.
const (
	// RecoveryActionNone keeps the autoscaler's own output.
	RecoveryActionNone RecoveryAction = "none"
	// RecoveryActionEnsureRedundancy tops every service up to the mode's
	// redundancy target, augmenting the autoscaler's output.
	RecoveryActionEnsureRedundancy RecoveryAction = "ensure_redundancy"
	// RecoveryActionRestoreStable reconciles to Omega^stable, replacing the
	// autoscaler's output.
	RecoveryActionRestoreStable RecoveryAction = "restore_stable"
	// RecoveryActionReinstantiateStable tears down and rebuilds from
	// Omega^stable. It reaches the same target as RecoveryActionRestoreStable
	// but costs more transition energy.
	RecoveryActionReinstantiateStable RecoveryAction = "reinstantiate_stable"
)

// OmegaStable is the last configuration that verified clean.
type OmegaStable struct {
	Mode          Mode           `json:"mode"`
	ReplicaTarget map[string]int `json:"replica_target"`
}

// RecoveryManager owns the recovery state machine for one decision domain. It is
// not safe for concurrent use; LoopState owns the lock (see store.go).
type RecoveryManager struct {
	eta      float64
	softWr   int
	windowW  int
	state    RecoveryState
	softCnt  int
	history  []bool
	omega    *OmegaStable
	rollback bool
}

// NewRecoveryManager returns a manager using the recovery thresholds from cfg
// and the violation window width windowW (shared with the stability guard).
func NewRecoveryManager(cfg RecoveryConfig, windowW int) *RecoveryManager {
	return &RecoveryManager{
		eta:      cfg.Eta,
		softWr:   cfg.SoftWindowWr,
		windowW:  windowW,
		state:    RecoveryNormal,
		rollback: true,
	}
}

// SetRollbackEnabled turns Omega^stable rollback on or off. Disabling it keeps
// the state machine running (so the recovery cost still enters the reward) while
// suppressing the restore, which is how the simulator's no-recovery ablation is
// expressed.
func (r *RecoveryManager) SetRollbackEnabled(enabled bool) { r.rollback = enabled }

// RecordViolation appends one slot's violation flag and advances the state
// machine. It must be called exactly once per slot: the window advances by call,
// not by slot index.
func (r *RecoveryManager) RecordViolation(violated bool) {
	r.history = append(r.history, violated)
	if len(r.history) > r.windowW {
		r.history = append(r.history[:0], r.history[len(r.history)-r.windowW:]...)
	}
	rate := r.ViolationRate()

	switch r.state {
	case RecoveryNormal:
		if rate > r.eta {
			r.state = RecoverySoft
			r.softCnt = 0
		}
	case RecoverySoft:
		r.softCnt++
		if rate <= r.eta {
			r.state = RecoveryNormal
			r.softCnt = 0
		} else if r.softCnt >= r.softWr {
			r.state = RecoveryHard
		}
	case RecoveryHard:
		if rate <= r.eta {
			r.state = RecoveryNormal
			r.softCnt = 0
		}
	}
}

// UpdateStable refreshes Omega^stable. The caller must only invoke it for a slot
// that both sits in NORMAL state and recorded no violation; the NORMAL check is
// repeated here so a caller mistake cannot install a failing configuration as the
// rollback target.
func (r *RecoveryManager) UpdateStable(mode Mode, replicaTarget map[string]int) {
	if r.state != RecoveryNormal {
		return
	}
	copied := make(map[string]int, len(replicaTarget))
	for svc, n := range replicaTarget {
		copied[svc] = n
	}
	r.omega = &OmegaStable{Mode: mode, ReplicaTarget: copied}
}

// State returns the current recovery phase.
func (r *RecoveryManager) State() RecoveryState { return r.state }

// SoftCounter returns how many slots the domain has spent in soft recovery.
func (r *RecoveryManager) SoftCounter() int { return r.softCnt }

// ViolationRate returns the fraction of violated slots in the window, or 0 when
// no slot has been recorded yet.
func (r *RecoveryManager) ViolationRate() float64 {
	if len(r.history) == 0 {
		return 0
	}
	violated := 0
	for _, v := range r.history {
		if v {
			violated++
		}
	}
	return float64(violated) / float64(len(r.history))
}

// StableConfig returns Omega^stable and whether one is available. It reports no
// configuration while rollback is disabled, so callers cannot restore it.
func (r *RecoveryManager) StableConfig() (OmegaStable, bool) {
	if !r.rollback || r.omega == nil {
		return OmegaStable{}, false
	}
	copied := make(map[string]int, len(r.omega.ReplicaTarget))
	for svc, n := range r.omega.ReplicaTarget {
		copied[svc] = n
	}
	return OmegaStable{Mode: r.omega.Mode, ReplicaTarget: copied}, true
}

// RecoveryActionFor maps a mode's recovery behaviour and the current recovery
// state to the action to take.
//
//	persistent_only (latency_priority, energy_saving): act only on HARD
//	normal          (balanced):  redundancy on SOFT, restore on HARD
//	fallback        (resilient): redundancy on SOFT, reinstantiate on HARD
func RecoveryActionFor(behavior string, state RecoveryState) (RecoveryAction, error) {
	switch behavior {
	case RecoveryBehaviorPersistentOnly:
		if state == RecoveryHard {
			return RecoveryActionRestoreStable, nil
		}
		return RecoveryActionNone, nil
	case RecoveryBehaviorNormal:
		switch state {
		case RecoveryHard:
			return RecoveryActionRestoreStable, nil
		case RecoverySoft:
			return RecoveryActionEnsureRedundancy, nil
		default:
			return RecoveryActionNone, nil
		}
	case RecoveryBehaviorFallback:
		switch state {
		case RecoveryHard:
			return RecoveryActionReinstantiateStable, nil
		case RecoverySoft:
			return RecoveryActionEnsureRedundancy, nil
		default:
			return RecoveryActionNone, nil
		}
	default:
		return RecoveryActionNone, fmt.Errorf("calm: unknown recovery_behavior %q", behavior)
	}
}

// ApplyRecoveryAction adjusts plan in place for the given recovery action and
// reports whether the plan changed.
//
// current is the live replica count per service, used by the no-op guard: when
// the deployment already equals Omega^stable a restore would emit a churn of
// identical writes every slot for no change, so it is skipped.
//
// Both restore actions produce the same absolute target here. The simulator
// distinguishes them by emitting a teardown followed by a rebuild, which costs
// extra transition energy; v1 applies replica counts absolutely through PATCH,
// so the distinction survives only in the plan reason.
func ApplyRecoveryAction(plan *PlacementPlan, action RecoveryAction, params PolicyParams, current map[string]int, omega *OmegaStable) bool {
	if plan == nil || action == RecoveryActionNone {
		return false
	}
	if plan.ReplicaTarget == nil {
		plan.ReplicaTarget = map[string]int{}
	}

	switch action {
	case RecoveryActionEnsureRedundancy:
		changed := false
		for _, svc := range sortedKeys(plan.ReplicaTarget) {
			if plan.ReplicaTarget[svc] < params.RedundancyTarget {
				plan.ReplicaTarget[svc] = params.RedundancyTarget
				changed = true
			}
		}
		if changed {
			plan.Reason = appendReason(plan.Reason, fmt.Sprintf("recovery=%s target=%d", action, params.RedundancyTarget))
		}
		return changed

	case RecoveryActionRestoreStable, RecoveryActionReinstantiateStable:
		if omega == nil {
			// No verified stable configuration yet, or rollback disabled: the
			// restore is a no-op and the autoscaler's output stands.
			plan.Reason = appendReason(plan.Reason, fmt.Sprintf("recovery=%s skipped (no stable config)", action))
			return false
		}
		if sameReplicaMap(current, omega.ReplicaTarget) {
			plan.Reason = appendReason(plan.Reason, fmt.Sprintf("recovery=%s skipped (already stable)", action))
			return false
		}
		restored := make(map[string]int, len(omega.ReplicaTarget))
		for svc, n := range omega.ReplicaTarget {
			restored[svc] = n
		}
		plan.ReplicaTarget = restored
		plan.Migrations = nil
		plan.Offloads = nil
		plan.Reason = appendReason(plan.Reason, fmt.Sprintf("recovery=%s mode=%s", action, omega.Mode))
		return true
	}
	return false
}

// sameReplicaMap compares two replica maps ignoring entries that are zero or
// below, which represent "service not deployed" in both forms.
func sameReplicaMap(a, b map[string]int) bool {
	nonZero := func(m map[string]int) map[string]int {
		out := make(map[string]int, len(m))
		for k, v := range m {
			if v > 0 {
				out[k] = v
			}
		}
		return out
	}
	left, right := nonZero(a), nonZero(b)
	if len(left) != len(right) {
		return false
	}
	for k, v := range left {
		if right[k] != v {
			return false
		}
	}
	return true
}

func appendReason(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

// RecoverySnapshot is the serialisable form of a RecoveryManager.
type RecoverySnapshot struct {
	State            RecoveryState `json:"state"`
	SoftCounter      int           `json:"soft_counter"`
	ViolationHistory []bool        `json:"violation_history"`
	Omega            *OmegaStable  `json:"omega_stable,omitempty"`
}

// Snapshot returns the manager's mutable state. Thresholds are excluded so a
// config change takes effect on restore.
func (r *RecoveryManager) Snapshot() RecoverySnapshot {
	snap := RecoverySnapshot{
		State:            r.state,
		SoftCounter:      r.softCnt,
		ViolationHistory: append([]bool(nil), r.history...),
	}
	if r.omega != nil {
		copied := make(map[string]int, len(r.omega.ReplicaTarget))
		for svc, n := range r.omega.ReplicaTarget {
			copied[svc] = n
		}
		snap.Omega = &OmegaStable{Mode: r.omega.Mode, ReplicaTarget: copied}
	}
	return snap
}

// Restore replaces the manager's mutable state. An unrecognised state falls back
// to NORMAL rather than leaving the machine in a phase it can never leave.
func (r *RecoveryManager) Restore(s RecoverySnapshot) {
	switch s.State {
	case RecoveryNormal, RecoverySoft, RecoveryHard:
		r.state = s.State
	default:
		r.state = RecoveryNormal
	}
	r.softCnt = s.SoftCounter
	r.history = append([]bool(nil), s.ViolationHistory...)
	if len(r.history) > r.windowW {
		r.history = r.history[len(r.history)-r.windowW:]
	}
	r.omega = nil
	if s.Omega != nil {
		copied := make(map[string]int, len(s.Omega.ReplicaTarget))
		for svc, n := range s.Omega.ReplicaTarget {
			copied[svc] = n
		}
		r.omega = &OmegaStable{Mode: s.Omega.Mode, ReplicaTarget: copied}
	}
}
