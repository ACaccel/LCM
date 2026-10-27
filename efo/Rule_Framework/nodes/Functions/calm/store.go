package calm

/*
Cross-slot state for the CALM loop.

Four feedback edges close the loop across slots: the severe-violation bypass, the
violation rate, the previous mode, and the reward that trains the bandit. None of
them can be a graph edge, because one policy run walks forward only and can never
return to a node it already executed. They live here instead, and the selector
applies the previous slot's reward at the start of the next one -- mathematically
the same as the simulator's end-of-slot update.

State is keyed by service name and never evicted; one LoopState is a few
kilobytes and a deployment has a handful of services. It is memory only, matching
the existing LCM_Enabler semantics: an EFO restart resets every learner. The
selector's optional state file (SnapshotJSON) is the escape hatch.

Each LoopState carries its own mutex. Callers take it through Do and must not
retain any pointer reached through the Loop past the callback.
*/

import (
	"encoding/json"
	"fmt"
	"sync"
)

// PendingDecision is the decision awaiting its reward. The selector records it
// when it picks a mode; post-verification fills in the reward, and the next
// slot's selector folds it into the bandit.
type PendingDecision struct {
	Context     []float64 `json:"context"`
	Arm         int       `json:"arm"`
	Mode        Mode      `json:"mode"`
	Switched    bool      `json:"switched"`
	Slot        int       `json:"slot"`
	Reward      float64   `json:"reward"`
	RewardReady bool      `json:"reward_ready"`
}

// Loop is the mutable state of one service's adaptive loop, plus the stateless
// helpers derived from configuration. It is only reachable inside LoopState.Do,
// which holds the lock for the duration of the callback.
type Loop struct {
	// Config is the resolved configuration this loop was built from.
	Config *Config
	// Context, Reward and Autoscaler are stateless helpers built once from
	// Config so every node in the loop uses the same settings.
	Context    *ContextBuilder
	Reward     *RewardCalculator
	Autoscaler *Autoscaler

	// LinUCB, Guard and Recovery carry the learner and the two state machines.
	LinUCB   *LinUCB
	Guard    *StabilityGuard
	Recovery *RecoveryManager

	// Slot counts adaptive cycles. The selector increments it once per run and
	// both the guard and the pending decision are stamped with it.
	Slot int
	// PrevMode is the mode chosen in the previous slot; ModeUnset before the
	// first decision.
	PrevMode Mode
	// PrevSevere carries the previous slot's severe-violation flag, which lets
	// the current slot bypass the stability guard.
	PrevSevere bool
	// Pending is the decision awaiting its reward, if any.
	Pending *PendingDecision
}

// LoopState guards one Loop.
type LoopState struct {
	mu   sync.Mutex
	loop Loop
}

// NewLoopState builds a fresh loop from cfg.
func NewLoopState(cfg *Config) (*LoopState, error) {
	if cfg == nil {
		return nil, fmt.Errorf("calm: NewLoopState requires a configuration")
	}
	learner, err := NewLinUCB(FeatureDim, cfg.SelectorAlpha)
	if err != nil {
		return nil, err
	}
	return &LoopState{loop: Loop{
		Config:     cfg,
		Context:    NewContextBuilder(cfg),
		Reward:     NewRewardCalculator(cfg.Reward, cfg.ReplicaCapacity),
		Autoscaler: NewAutoscaler(cfg),
		LinUCB:     learner,
		Guard:      NewStabilityGuard(cfg.Guard),
		Recovery:   NewRecoveryManager(cfg.Recovery, cfg.Guard.WindowW),
	}}, nil
}

// Do runs fn with exclusive access to the loop. Everything a node needs to read
// and write in one slot happens inside a single call, so a slot's decision is
// atomic with respect to another policy touching the same service.
func (s *LoopState) Do(fn func(l *Loop)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.loop)
}

var (
	registryMu sync.Mutex
	registry   = map[string]*LoopState{}
)

// StateFor returns the loop state for a service, creating it on first use. The
// first caller's configuration wins: later callers with a different config get
// the existing loop, because a learner cannot change its feature layout or its
// thresholds mid-run without invalidating what it has learned.
func StateFor(service string, cfg *Config) (*LoopState, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if st, ok := registry[service]; ok {
		return st, nil
	}
	st, err := NewLoopState(cfg)
	if err != nil {
		return nil, err
	}
	registry[service] = st
	return st, nil
}

// ResetRegistry drops every loop state. It exists so tests can start from a
// clean learner; production code has no reason to call it.
func ResetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]*LoopState{}
}

// loopSnapshotVersion guards the state file against a layout change. A snapshot
// from another version is rejected rather than partially applied.
const loopSnapshotVersion = 1

// LoopSnapshot is the serialisable form of a loop.
type LoopSnapshot struct {
	Version    int              `json:"version"`
	FeatureDim int              `json:"feature_dim"`
	Slot       int              `json:"slot"`
	PrevMode   Mode             `json:"prev_mode"`
	PrevSevere bool             `json:"prev_severe"`
	Pending    *PendingDecision `json:"pending,omitempty"`
	LinUCB     json.RawMessage  `json:"linucb"`
	Guard      GuardSnapshot    `json:"guard"`
	Recovery   RecoverySnapshot `json:"recovery"`
}

// SnapshotJSON serialises the loop for the selector's optional state file.
func (s *LoopState) SnapshotJSON() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	learner, err := s.loop.LinUCB.SnapshotJSON()
	if err != nil {
		return nil, err
	}
	snap := LoopSnapshot{
		Version:    loopSnapshotVersion,
		FeatureDim: FeatureDim,
		Slot:       s.loop.Slot,
		PrevMode:   s.loop.PrevMode,
		PrevSevere: s.loop.PrevSevere,
		LinUCB:     learner,
		Guard:      s.loop.Guard.Snapshot(),
		Recovery:   s.loop.Recovery.Snapshot(),
	}
	if s.loop.Pending != nil {
		pending := *s.loop.Pending
		pending.Context = append([]float64(nil), s.loop.Pending.Context...)
		snap.Pending = &pending
	}
	return json.Marshal(snap)
}

// RestoreJSON replaces the loop's learned and machine state from a snapshot.
// Configuration is not restored: the file carries what was learned, the config
// carries how the loop is tuned, so a retuned deployment keeps its new settings.
func (s *LoopState) RestoreJSON(data []byte) error {
	var snap LoopSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("calm: cannot decode loop snapshot: %w", err)
	}
	if snap.Version != loopSnapshotVersion {
		return fmt.Errorf("calm: loop snapshot version %d is not supported (want %d)", snap.Version, loopSnapshotVersion)
	}
	if snap.FeatureDim != FeatureDim {
		return fmt.Errorf("calm: loop snapshot feature dimension %d does not match %d", snap.FeatureDim, FeatureDim)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(snap.LinUCB) > 0 {
		if err := s.loop.LinUCB.RestoreJSON(snap.LinUCB); err != nil {
			return err
		}
	}
	s.loop.Slot = snap.Slot
	s.loop.PrevMode = snap.PrevMode
	s.loop.PrevSevere = snap.PrevSevere
	s.loop.Pending = nil
	if snap.Pending != nil {
		pending := *snap.Pending
		pending.Context = append([]float64(nil), snap.Pending.Context...)
		s.loop.Pending = &pending
	}
	s.loop.Guard.Restore(snap.Guard)
	s.loop.Recovery.Restore(snap.Recovery)
	return nil
}
