package calm

/*
StabilityGuard — dwell time and switch-rate enforcement for policy-mode changes.

A switch is allowed only when both hold:

	slot - lastSwitchSlot >= minDwell            (minimum dwell between switches)
	switches in [slot-window+1, slot] < maxSwitches   (switch budget per window)

lastSwitchSlot starts at -(minDwell+1) so the very first slot is always allowed;
the simulator uses the same seed value and the selector depends on it, otherwise
the loop could never make its first decision.

The simulator keys this state per cluster. v1 runs a single decision domain per
deployed policy, so there is one guard per LoopState and no cluster map.
*/

// StabilityGuard tracks mode switches and enforces the dwell and budget limits.
// It is not safe for concurrent use; LoopState owns the lock (see store.go).
type StabilityGuard struct {
	windowW     int
	maxSwitches int
	minDwell    int

	lastSwitchSlot int
	// switchHistory holds the slots at which a switch occurred, oldest first,
	// pruned to the window on every Record.
	switchHistory []int
}

// NewStabilityGuard returns a guard configured from cfg.
func NewStabilityGuard(cfg GuardConfig) *StabilityGuard {
	return &StabilityGuard{
		windowW:        cfg.WindowW,
		maxSwitches:    cfg.MaxSwitchesSW,
		minDwell:       cfg.MinDwellT,
		lastSwitchSlot: -(cfg.MinDwellT + 1),
	}
}

// SwitchAllowed reports whether a mode switch may be made at slot. It does not
// record anything; call Record once the decision is committed.
//
// A severe violation bypasses this guard entirely; that decision belongs to the
// selector node, which knows the previous slot's severity.
func (g *StabilityGuard) SwitchAllowed(slot int) bool {
	if slot-g.lastSwitchSlot < g.minDwell {
		return false
	}
	return g.SwitchesInWindow(slot) < g.maxSwitches
}

// Record commits the outcome of one decision slot. It must be called every slot,
// switched or not, because it also prunes the window.
func (g *StabilityGuard) Record(slot int, switched bool) {
	if switched {
		g.lastSwitchSlot = slot
		g.switchHistory = append(g.switchHistory, slot)
	}
	windowStart := slot - g.windowW + 1
	drop := 0
	for drop < len(g.switchHistory) && g.switchHistory[drop] < windowStart {
		drop++
	}
	if drop > 0 {
		g.switchHistory = append(g.switchHistory[:0], g.switchHistory[drop:]...)
	}
}

// SwitchesInWindow returns how many switches fall in [slot-window+1, slot].
// Exposed so the node output can explain why a switch was blocked.
func (g *StabilityGuard) SwitchesInWindow(slot int) int {
	windowStart := slot - g.windowW + 1
	count := 0
	for _, s := range g.switchHistory {
		if s >= windowStart {
			count++
		}
	}
	return count
}

// LastSwitchSlot returns the slot of the most recent switch, or the seed value
// -(minDwell+1) when no switch has happened yet.
func (g *StabilityGuard) LastSwitchSlot() int { return g.lastSwitchSlot }

// GuardSnapshot is the serialisable form of a StabilityGuard.
type GuardSnapshot struct {
	LastSwitchSlot int   `json:"last_switch_slot"`
	SwitchHistory  []int `json:"switch_history"`
}

// Snapshot returns the guard's mutable state. The configured limits are not
// included: they come from config on restore, so a config change takes effect
// rather than being overridden by a stale state file.
func (g *StabilityGuard) Snapshot() GuardSnapshot {
	return GuardSnapshot{
		LastSwitchSlot: g.lastSwitchSlot,
		SwitchHistory:  append([]int(nil), g.switchHistory...),
	}
}

// Restore replaces the guard's mutable state.
func (g *StabilityGuard) Restore(s GuardSnapshot) {
	g.lastSwitchSlot = s.LastSwitchSlot
	g.switchHistory = append([]int(nil), s.SwitchHistory...)
}
